package service

// 长上下文缺 credits 的 429 只限「这个号 × 这个模型 × 长请求」，不封整个号。
//
// ---- 为什么需要它 ----
//
// 订阅号跑超过 200K 的请求时，多数号会回 429 `Usage credits are required for long context
// requests.`（2026-09-16 实测只有 sonnet-4-6 这样；opus / sonnet-5 的长请求照常成功）。
// 上游只给 Fable 做了模型级处理，其余模型落进通用 429 分支，按
// `anthropic-ratelimit-unified-reset`（月末）**封整个号**。于是一个长会话每发一次，
// 就轮 10 个号、封 10 个号到月底 —— 这些号对短请求、对别的模型本来完全可用。
// 池子缩水后，调度落到 7d 已用到 0.99 的号上，别的用户开始收到
// `would exceed your account's rate limit`：真正被伤到的是无辜的请求。
//
// ---- 为什么限流 scope 要带「长请求」这一维 ----
//
// 只按模型限流也不行：长请求失败的号，短 sonnet-4-6 照样跑得通；按模型封，
// 会把 sonnet-4-6 的短请求一起赶到那几个有资格的号上。所以 scope 是
// `<模型>#long_context`，调度时**只有被判为长请求**才看这个 scope。
//
// 长不长有两个来源：
//   - 进门时估：官方 count_tokens 要多一次往返，不值。估算刻意偏低（ASCII 4 字符 / token，
//     Claude 实际更密），因为误判的代价更大 —— 短请求被当成长请求，会跳过被标记的号，
//     号全被标记时直接无号可用；漏判只是多轮几次号。
//   - 上游说了算：漏判的请求一旦吃到这个 429，就地把本请求改判为长，同一请求的
//     failover 从此跳过已标记的号。漏判的代价因此是 1 次上游往返，而不是整个池子。
//     这就是 ctx 里放可变指针、而不是不可变 bool 的原因。
//
// 挂载点：handler 里一行 XiuWithLongContextRequest、ratelimit_service 里一处入口、
// model_rate_limit 里一行 key 追加。只接了 /v1/messages —— new-api 走的就是这条；
// 别的入口不查 scope，最坏退化成轮号，不会封号。

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/tidwall/gjson"
)

const (
	// Anthropic 长上下文计价 / 资格的分界线：输入超过 200K token。
	xiuLongContextTokenThreshold = 200_000

	// 按下面的估算口径，1 个 token 至少对应 2 字节（非 ASCII 字符 UTF-8 至少 2 字节、
	// 记 1 token；ASCII 4 字节记 1 token），不足这么多字节的 body 不可能超过阈值，
	// 直接跳过 JSON 遍历 —— 绝大多数请求走这条快路径。
	xiuLongContextMinBodyBytes = 2 * xiuLongContextTokenThreshold

	xiuLongContextScopeSuffix           = "#long_context"
	xiuLongContextCreditsRequiredReason = "xiu_anthropic_long_context_credits_required"

	// 上游给的重置点是月末，但资格并不跟着月度走：同一个号先成功上百次、随后开始拒
	// （22 号，2026-09-16）。封顶 5 小时 = 与 5h 窗口同尺度，让号有机会被重新试到；
	// 代价是每个号每 5 小时至多为长请求白试一次。
	xiuLongContextScopeMaxCooldown = 5 * time.Hour
)

type xiuLongContextKey struct{}

// XiuWithLongContextRequest 在调度前把「这是不是长请求」记进 ctx。
func XiuWithLongContextRequest(ctx context.Context, body []byte) context.Context {
	return xiuWithLongContext(ctx, xiuIsLongContextBody(body))
}

func xiuWithLongContext(ctx context.Context, long bool) context.Context {
	flag := new(atomic.Bool)
	flag.Store(long)
	return context.WithValue(ctx, xiuLongContextKey{}, flag)
}

func xiuIsLongContext(ctx context.Context) bool {
	flag, _ := ctx.Value(xiuLongContextKey{}).(*atomic.Bool)
	return flag != nil && flag.Load()
}

// xiuMarkLongContext 把本请求改判为长（见文件头「上游说了算」）。
func xiuMarkLongContext(ctx context.Context) {
	if flag, _ := ctx.Value(xiuLongContextKey{}).(*atomic.Bool); flag != nil {
		flag.Store(true)
	}
}

// xiuIsLongContextBody 对 body 里所有字符串值估 token，超过阈值即提前停。
// data（图片 / 文档 base64、redacted_thinking）与 signature（thinking 签名）是大块字节，
// 按文本算会把一张截图估成几万 token，必须跳过。
//
// 大 body 是 MB 级，所以全程零拷贝：unsafe 视图避开 ParseBytes 的整份拷贝，
// 直接在 Raw 上数字符，避开反转义副本与 []rune 分配。body 在调用期间不被改写。
func xiuIsLongContextBody(body []byte) bool {
	if len(body) < xiuLongContextMinBodyBytes {
		return false
	}
	return xiuEstimateJSONTokens(gjson.Parse(unsafe.String(unsafe.SliceData(body), len(body)))) > xiuLongContextTokenThreshold
}

func xiuEstimateJSONTokens(value gjson.Result) int {
	if value.Type == gjson.String {
		return xiuEstimateRawStringTokens(value.Raw)
	}
	total := 0
	if value.IsObject() || value.IsArray() {
		value.ForEach(func(key, child gjson.Result) bool {
			// 数组元素的 key 是空串，天然过得了这道过滤
			if name := key.String(); name != "data" && name != "signature" {
				total += xiuEstimateJSONTokens(child)
			}
			return total <= xiuLongContextTokenThreshold
		})
	}
	return total
}

// xiuEstimateRawStringTokens 口径：ASCII 4 字符 / token，非 ASCII 1 字符 / token。
// 转义按它代表的那一个字符算：`\n` 是 1 个 ASCII，`\uXXXX` 是 1 个非 ASCII ——
// Python 的 json.dumps 默认把中文全写成 \uXXXX，按字节数会估高 5 倍。
func xiuEstimateRawStringTokens(raw string) int {
	if len(raw) >= 2 {
		raw = raw[1 : len(raw)-1]
	}
	ascii, other := 0, 0
	for i := 0; i < len(raw); {
		switch c := raw[i]; {
		case c == '\\' && i+1 < len(raw) && raw[i+1] == 'u':
			other++
			i += 6
		case c == '\\':
			ascii++
			i += 2
		case c < utf8.RuneSelf:
			ascii++
			i++
		default:
			_, size := utf8.DecodeRuneInString(raw[i:])
			other++
			i += size
		}
	}
	return (ascii+3)/4 + other
}

// xiuLongContextScope 与调度侧必须落到同一个 key：写入侧拿到的是转发用的模型名，
// 调度侧拿到的是请求里的模型名，[1m] 后缀的去留两边不一定一致。
func xiuLongContextScope(model string) string {
	model = strings.ToLower(normalizeClaudeCodeLongContextModel(strings.TrimSpace(model)))
	if model == "" {
		return ""
	}
	return model + xiuLongContextScopeSuffix
}

// xiuLongContextRateLimitKeys 供 modelRateLimitKeysForRequest 的 Anthropic 分支追加：
// 短请求不带这个 key。
func xiuLongContextRateLimitKeys(ctx context.Context, modelKey string) []string {
	if ctx == nil || !xiuIsLongContext(ctx) {
		return nil
	}
	if scope := xiuLongContextScope(modelKey); scope != "" {
		return []string{scope}
	}
	return nil
}

// 线上 sonnet 的这种 429 不带 error.details（error_code 有时有、有时没有），
// 只能认文案。文案变了会静默退回封整号，所以命中日志里带上 error_code 以便对照。
func xiuIsLongContextCreditsRequired(body []byte) bool {
	return strings.Contains(strings.ToLower(extractUpstreamErrorMessage(body)), "long context")
}

// xiuPersistLongContextCreditsRequired 只在 Anthropic 的 429 分支里调用。
// 返回 true 表示已处理，调用方必须直接返回，不能再落进任何整号限流分支 ——
// 哪怕写库失败也一样，失败不许放大成封号。
func (s *RateLimitService) xiuPersistLongContextCreditsRequired(ctx context.Context, account *Account, headers http.Header, body []byte, model string) bool {
	if !xiuIsLongContextCreditsRequired(body) {
		return false
	}
	xiuMarkLongContext(ctx)

	scope := xiuLongContextScope(model)
	if scope == "" || s.accountRepo == nil {
		slog.Warn("xiu_long_context_credits_required_unscoped", "account_id", account.ID, "model", model)
		return true
	}

	now := time.Now()
	resetAt := now.Add(xiuLongContextScopeMaxCooldown)
	if headerReset, ok := parseAnthropicResetTimestamp(headers.Get("anthropic-ratelimit-unified-reset"), now, 366*24*time.Hour); ok && headerReset.Before(resetAt) {
		resetAt = headerReset
	}

	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scope, resetAt, xiuLongContextCreditsRequiredReason); err != nil {
		slog.Warn("xiu_long_context_credits_required_set_failed",
			"account_id", account.ID, "scope", scope, "reset_at", resetAt, "error", err)
		return true
	}
	slog.Info("xiu_long_context_credits_required_scoped",
		"account_id", account.ID, "scope", scope, "reset_at", resetAt,
		"error_code", gjson.GetBytes(body, "error.details.error_code").String())
	return true
}
