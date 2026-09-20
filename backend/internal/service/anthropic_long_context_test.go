//go:build unit

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

// anthropicLongContextRepo implements only the writes this path may reach. Every other
// method hits the nil embedded interface and panics, so a regression that wanders into
// an account-level rate limit or a temp-unschedulable write fails loudly.
type anthropicLongContextRepo struct {
	AccountRepository
	rateLimitCalls  int
	modelScope      string
	modelResetAt    time.Time
	modelReason     string
	modelLimitCalls int
}

func (r *anthropicLongContextRepo) SetRateLimited(context.Context, int64, time.Time) error {
	r.rateLimitCalls++
	return nil
}

func (r *anthropicLongContextRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, resetAt time.Time, reason ...string) error {
	r.modelLimitCalls++
	r.modelScope = scope
	r.modelResetAt = resetAt
	if len(reason) > 0 {
		r.modelReason = reason[0]
	}
	return nil
}

const anthropicLongContextErrorBody = `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."},"request_id":"req_x"}`

func anthropicLongContextTestAccount() *Account {
	return &Account{ID: 7, Type: AccountTypeOAuth, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}
}

func TestAnthropicLongContextCreditsRequired_ScopesToModelNotAccount(t *testing.T) {
	monthEnd := time.Now().Add(15 * 24 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(monthEnd.Unix(), 10))
	repo := &anthropicLongContextRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)

	startedAt := time.Now()
	shouldDisable := svc.HandleUpstreamError(context.Background(), anthropicLongContextTestAccount(), http.StatusTooManyRequests, headers, []byte(anthropicLongContextErrorBody), "claude-sonnet-4-6")

	require.False(t, shouldDisable)
	require.Zero(t, repo.rateLimitCalls, "long-context credits_required must not rate-limit the whole account")
	require.Equal(t, 1, repo.modelLimitCalls)
	require.Equal(t, "claude-sonnet-4-6"+anthropicLongContextScopeSuffix, repo.modelScope)
	require.Equal(t, anthropicLongContextCreditsRequiredReason, repo.modelReason)
	// The end-of-month reset is capped: the entitlement changes over time, so the
	// account must get a chance to be retried.
	require.WithinDuration(t, startedAt.Add(anthropicLongContextScopeMaxCooldown), repo.modelResetAt, 5*time.Second)
}

func TestAnthropicLongContextCreditsRequired_UsesEarlierHeaderReset(t *testing.T) {
	soon := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(soon.Unix(), 10))
	repo := &anthropicLongContextRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)

	svc.HandleUpstreamError(context.Background(), anthropicLongContextTestAccount(), http.StatusTooManyRequests, headers, []byte(anthropicLongContextErrorBody), "claude-sonnet-4-6")

	require.Equal(t, soon, repo.modelResetAt)
}

func TestAnthropicLongContextCreditsRequired_FableScopedToLongContextOnly(t *testing.T) {
	repo := &anthropicLongContextRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	body := `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests.","details":{"error_code":"credits_required"}}}`

	svc.HandleUpstreamError(context.Background(), anthropicLongContextTestAccount(), http.StatusTooManyRequests, http.Header{}, []byte(body), "claude-fable-5[1m]")

	require.Zero(t, repo.rateLimitCalls)
	require.Equal(t, "claude-fable-5"+anthropicLongContextScopeSuffix, repo.modelScope, "must not be widened into a Fable-family limit by the Fable branch")
}

func TestAnthropicLongContextCreditsRequired_IgnoresOtherRateLimits(t *testing.T) {
	ctx := withAnthropicLongContext(context.Background(), false)
	body := []byte(`{"error":{"message":"This request would exceed your account's rate limit."}}`)

	require.False(t, NewRateLimitService(&anthropicLongContextRepo{}, nil, nil, nil, nil).persistAnthropicLongContextCreditsRequired(ctx, anthropicLongContextTestAccount(), http.Header{}, body, "claude-sonnet-4-6"))
	require.False(t, isAnthropicLongContextRequest(ctx))
}

func TestAnthropicLongContextCreditsRequired_MarksRequestLongForFailover(t *testing.T) {
	// The entrance estimate missed; the upstream 429 corrects it in place so the rest of
	// this request's failover skips accounts that are already marked.
	ctx := withAnthropicLongContext(context.Background(), false)
	account := anthropicLongContextTestAccount()
	setAccountModelRateLimitSnapshot(account, anthropicLongContextScope("claude-sonnet-4-6"), time.Now().Add(time.Hour), "", time.Now())
	require.True(t, account.IsSchedulableForModelWithContext(ctx, "claude-sonnet-4-6"))

	svc := NewRateLimitService(&anthropicLongContextRepo{}, nil, nil, nil, nil)
	svc.HandleUpstreamError(ctx, anthropicLongContextTestAccount(), http.StatusTooManyRequests, http.Header{}, []byte(anthropicLongContextErrorBody), "claude-sonnet-4-6")

	require.False(t, account.IsSchedulableForModelWithContext(ctx, "claude-sonnet-4-6"))
}

func TestAnthropicLongContextScope_OnlyBlocksLongRequests(t *testing.T) {
	account := anthropicLongContextTestAccount()
	setAccountModelRateLimitSnapshot(account, anthropicLongContextScope("claude-sonnet-4-6"), time.Now().Add(time.Hour), anthropicLongContextCreditsRequiredReason, time.Now())

	shortCtx := context.Background()
	longCtx := withAnthropicLongContext(context.Background(), true)

	require.True(t, account.IsSchedulableForModelWithContext(shortCtx, "claude-sonnet-4-6"), "short requests stay schedulable")
	require.False(t, account.IsSchedulableForModelWithContext(longCtx, "claude-sonnet-4-6"))
	require.False(t, account.IsSchedulableForModelWithContext(longCtx, "claude-sonnet-4-6[1m]"), "the [1m] suffix shares the bare model's scope")
	require.True(t, account.IsSchedulableForModelWithContext(longCtx, "claude-opus-5"), "other models are unaffected")
}

func TestEstimateAnthropicJSONTokens(t *testing.T) {
	english := strings.Repeat("a", 4*1000)
	cjk := strings.Repeat("中", 1000)
	// Base64 images and thinking signatures are large byte blobs, not text tokens.
	body := `{"model":"m","system":"` + english + `","tools":[{"name":"t","description":"` + english + `"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"` + cjk + `"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("A", 100000) + `"}},` +
		`{"type":"thinking","thinking":"` + english + `","signature":"` + strings.Repeat("B", 100000) + `"}]}]}`

	require.InDelta(t, 4000, estimateAnthropicJSONTokens(gjson.Parse(body)), 50)
}

func TestEstimateAnthropicRawStringTokens(t *testing.T) {
	require.Equal(t, 0, estimateAnthropicRawStringTokens(`""`))
	require.Equal(t, 2, estimateAnthropicRawStringTokens(`"abcd\n\"x"`), "an escape counts as one char: abcd + newline + quote + x = 7 ASCII")
	require.Equal(t, 2, estimateAnthropicRawStringTokens(`"中文"`))
	require.Equal(t, 2, estimateAnthropicRawStringTokens(`"中文"`), "Python-escaped CJK costs the same as the literal text")
}

func TestWithAnthropicLongContextRequest(t *testing.T) {
	isLong := func(body string) bool {
		return isAnthropicLongContextRequest(WithAnthropicLongContextRequest(context.Background(), []byte(body)))
	}

	require.False(t, isAnthropicLongContextRequest(context.Background()))
	require.False(t, isLong(`{"messages":[{"role":"user","content":"`+strings.Repeat("中", 1000)+`"}]}`), "fast path")
	require.True(t, isLong(`{"messages":[{"role":"user","content":"`+strings.Repeat("abcd", anthropicLongContextTokenThreshold+10)+`"}]}`))
	// Large in bytes but all image data: not a long-context request.
	require.False(t, isLong(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"`+strings.Repeat("A", 4*anthropicLongContextTokenThreshold)+`"}}]}]}`))
}
