package service

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// xiuLongContextRepo 只实现本补丁会走到的写方法；其余方法落到 nil 接口上直接 panic，
// 于是「意外走进了整号限流 / 临时停调」这类回归会当场炸出来。
type xiuLongContextRepo struct {
	AccountRepository
	rateLimitCalls  int
	modelScope      string
	modelResetAt    time.Time
	modelReason     string
	modelLimitCalls int
}

func (r *xiuLongContextRepo) SetRateLimited(context.Context, int64, time.Time) error {
	r.rateLimitCalls++
	return nil
}

func (r *xiuLongContextRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, resetAt time.Time, reason ...string) error {
	r.modelLimitCalls++
	r.modelScope = scope
	r.modelResetAt = resetAt
	if len(reason) > 0 {
		r.modelReason = reason[0]
	}
	return nil
}

const xiuLongContextBody = `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."},"request_id":"req_x"}`

func xiuAnthropicOAuthAccount() *Account {
	return &Account{ID: 7, Type: AccountTypeOAuth, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}
}

func TestXiuLongContextCreditsRequired_ScopesToModelNotAccount(t *testing.T) {
	monthEnd := time.Now().Add(15 * 24 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(monthEnd.Unix(), 10))
	repo := &xiuLongContextRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)

	startedAt := time.Now()
	shouldDisable := svc.HandleUpstreamError(context.Background(), xiuAnthropicOAuthAccount(), http.StatusTooManyRequests, headers, []byte(xiuLongContextBody), "claude-sonnet-4-6")

	require.False(t, shouldDisable)
	require.Zero(t, repo.rateLimitCalls, "长上下文缺 credits 不许封整个号")
	require.Equal(t, 1, repo.modelLimitCalls)
	require.Equal(t, "claude-sonnet-4-6"+xiuLongContextScopeSuffix, repo.modelScope)
	require.Equal(t, xiuLongContextCreditsRequiredReason, repo.modelReason)
	// 月末重置点被封顶：资格会变（同一个号先成后败），得留出重新学习的机会
	require.WithinDuration(t, startedAt.Add(xiuLongContextScopeMaxCooldown), repo.modelResetAt, 5*time.Second)
}

func TestXiuLongContextCreditsRequired_UsesEarlierHeaderReset(t *testing.T) {
	soon := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(soon.Unix(), 10))
	repo := &xiuLongContextRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)

	svc.HandleUpstreamError(context.Background(), xiuAnthropicOAuthAccount(), http.StatusTooManyRequests, headers, []byte(xiuLongContextBody), "claude-sonnet-4-6")

	require.Equal(t, soon, repo.modelResetAt)
}

func TestXiuLongContextCreditsRequired_FableAlsoScopedToLongContextOnly(t *testing.T) {
	repo := &xiuLongContextRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	body := `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests.","details":{"error_code":"credits_required"}}}`

	svc.HandleUpstreamError(context.Background(), xiuAnthropicOAuthAccount(), http.StatusTooManyRequests, http.Header{}, []byte(body), "claude-fable-5[1m]")

	require.Zero(t, repo.rateLimitCalls)
	require.Equal(t, "claude-fable-5"+xiuLongContextScopeSuffix, repo.modelScope, "不能被 Fable 分支吞成整个 Fable 家族限流")
}

func TestXiuLongContextCreditsRequired_IgnoresOtherRateLimits(t *testing.T) {
	ctx := xiuWithLongContext(context.Background(), false)
	body := []byte(`{"error":{"message":"This request would exceed your account's rate limit."}}`)

	require.False(t, NewRateLimitService(&xiuLongContextRepo{}, nil, nil, nil, nil).xiuPersistLongContextCreditsRequired(ctx, xiuAnthropicOAuthAccount(), http.Header{}, body, "claude-sonnet-4-6"))
	require.False(t, xiuIsLongContext(ctx))
}

func TestXiuLongContextCreditsRequired_MarksRequestLongForFailover(t *testing.T) {
	// 进门估漏了；上游的 429 就地纠正，同一请求后续选号要跳过已标记的号
	ctx := xiuWithLongContext(context.Background(), false)
	account := xiuAnthropicOAuthAccount()
	setAccountModelRateLimitSnapshot(account, xiuLongContextScope("claude-sonnet-4-6"), time.Now().Add(time.Hour), "", time.Now())
	require.True(t, account.IsSchedulableForModelWithContext(ctx, "claude-sonnet-4-6"))

	svc := NewRateLimitService(&xiuLongContextRepo{}, nil, nil, nil, nil)
	svc.HandleUpstreamError(ctx, xiuAnthropicOAuthAccount(), http.StatusTooManyRequests, http.Header{}, []byte(xiuLongContextBody), "claude-sonnet-4-6")

	require.False(t, account.IsSchedulableForModelWithContext(ctx, "claude-sonnet-4-6"))
}

func TestXiuLongContextScope_OnlyBlocksLongRequests(t *testing.T) {
	account := xiuAnthropicOAuthAccount()
	setAccountModelRateLimitSnapshot(account, xiuLongContextScope("claude-sonnet-4-6"), time.Now().Add(time.Hour), xiuLongContextCreditsRequiredReason, time.Now())

	shortCtx := context.Background()
	longCtx := xiuWithLongContext(context.Background(), true)

	require.True(t, account.IsSchedulableForModelWithContext(shortCtx, "claude-sonnet-4-6"), "短请求照常调度")
	require.False(t, account.IsSchedulableForModelWithContext(longCtx, "claude-sonnet-4-6"))
	require.False(t, account.IsSchedulableForModelWithContext(longCtx, "claude-sonnet-4-6[1m]"), "[1m] 后缀与裸名同一个 scope")
	require.True(t, account.IsSchedulableForModelWithContext(longCtx, "claude-opus-5"), "别的模型不受牵连")
}

func TestXiuEstimateJSONTokens(t *testing.T) {
	english := strings.Repeat("a", 4*1000)
	cjk := strings.Repeat("中", 1000)
	// 图片 base64 与 thinking 签名是大块字节，却不是文本 token
	body := `{"model":"m","system":"` + english + `","tools":[{"name":"t","description":"` + english + `"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"` + cjk + `"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("A", 100000) + `"}},` +
		`{"type":"thinking","thinking":"` + english + `","signature":"` + strings.Repeat("B", 100000) + `"}]}]}`

	require.InDelta(t, 4000, xiuEstimateJSONTokens(gjson.Parse(body)), 50)
}

func TestXiuEstimateRawStringTokens(t *testing.T) {
	require.Equal(t, 0, xiuEstimateRawStringTokens(`""`))
	require.Equal(t, 2, xiuEstimateRawStringTokens(`"abcd\n\"x"`), "转义按一个字符算：abcd + 换行 + 引号 + x = 7 个 ASCII")
	require.Equal(t, 2, xiuEstimateRawStringTokens(`"中文"`))
	require.Equal(t, 2, xiuEstimateRawStringTokens(`"\u4e2d\u6587"`), "Python 转义的中文与原文同价")
}

func TestXiuWithLongContextRequest(t *testing.T) {
	isLong := func(body string) bool {
		return xiuIsLongContext(XiuWithLongContextRequest(context.Background(), []byte(body)))
	}

	require.False(t, xiuIsLongContext(context.Background()))
	require.False(t, isLong(`{"messages":[{"role":"user","content":"`+strings.Repeat("中", 1000)+`"}]}`), "快路径")
	require.True(t, isLong(`{"messages":[{"role":"user","content":"`+strings.Repeat("abcd", xiuLongContextTokenThreshold+10)+`"}]}`))
	// 字节够大、但全是图片：不算长上下文
	require.False(t, isLong(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"`+strings.Repeat("A", 4*xiuLongContextTokenThreshold)+`"}}]}]}`))
}
