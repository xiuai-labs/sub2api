package service

// Anthropic long-context credits_required handling.
//
// Subscription accounts that are not entitled to >200K-token requests answer them
// with 429 `Usage credits are required for long context requests.`. That is an
// entitlement failure for "this account x this model x long requests", not a shared
// window exhaustion: the same account keeps serving short requests for the same
// model, and other models, without any problem.
//
// Without dedicated handling the response falls into the generic Anthropic 429
// branch, which rate-limits the WHOLE account until
// `anthropic-ratelimit-unified-reset` (often the end of the month). A single long
// conversation then fails over through the pool and takes out one account per
// attempt, and unrelated users start seeing `would exceed your account's rate
// limit` because the pool has shrunk.
//
// The limit is therefore stored under a `<model>#long_context` model scope, and the
// scheduler only consults that scope for requests that are themselves long. Scoping
// by model alone would be too wide: it would push short requests for that model away
// from accounts that can serve them.
//
// Whether a request is long comes from two sources:
//   - An estimate at the gateway entrance. Calling count_tokens would cost an extra
//     upstream round trip. The estimate is deliberately low (4 ASCII chars per token,
//     Claude is denser in practice) because a false positive is the expensive error:
//     a short request treated as long skips every marked account and may find none.
//     A false negative only costs a failover.
//   - The upstream itself. When a request the estimate missed receives this 429, the
//     request is re-marked as long in place, so its remaining failover attempts skip
//     accounts that are already marked. That is why the context carries a mutable
//     flag rather than an immutable bool.
//
// Only /v1/messages records the flag. Other entrances never consult the scope, so the
// worst case there is an extra failover, never an account-level rate limit.

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
	// Anthropic's long-context boundary: more than 200K input tokens.
	anthropicLongContextTokenThreshold = 200_000

	// With the estimate below one token is at least 2 bytes (a non-ASCII rune is >= 2
	// UTF-8 bytes and counts as 1 token; 4 ASCII bytes count as 1 token), so a smaller
	// body cannot exceed the threshold. Almost every request takes this fast path and
	// skips the JSON walk entirely.
	anthropicLongContextMinBodyBytes = 2 * anthropicLongContextTokenThreshold

	anthropicLongContextScopeSuffix           = "#long_context"
	anthropicLongContextCreditsRequiredReason = "anthropic_long_context_credits_required"

	// The reset header points at the end of the billing period, but the entitlement
	// does not follow it: the same account has been observed to succeed on long
	// requests and start rejecting them later the same day. Capping the cooldown at the
	// 5h window scale lets the account be retried; the cost is at most one wasted long
	// request per account every 5 hours.
	anthropicLongContextScopeMaxCooldown = 5 * time.Hour
)

type anthropicLongContextKey struct{}

// WithAnthropicLongContextRequest records, before scheduling, whether the request
// body is estimated to be a long-context request.
func WithAnthropicLongContextRequest(ctx context.Context, body []byte) context.Context {
	return withAnthropicLongContext(ctx, isAnthropicLongContextBody(body))
}

func withAnthropicLongContext(ctx context.Context, long bool) context.Context {
	flag := new(atomic.Bool)
	flag.Store(long)
	return context.WithValue(ctx, anthropicLongContextKey{}, flag)
}

func isAnthropicLongContextRequest(ctx context.Context) bool {
	flag, _ := ctx.Value(anthropicLongContextKey{}).(*atomic.Bool)
	return flag != nil && flag.Load()
}

// markAnthropicLongContextRequest re-marks the current request as long once the
// upstream has said so (see the file header).
func markAnthropicLongContextRequest(ctx context.Context) {
	if flag, _ := ctx.Value(anthropicLongContextKey{}).(*atomic.Bool); flag != nil {
		flag.Store(true)
	}
}

// isAnthropicLongContextBody estimates tokens over every string value in the body
// and stops as soon as the threshold is exceeded. `data` (base64 images / documents,
// redacted_thinking) and `signature` (thinking signatures) are large byte blobs, not
// text; counting them would turn a single screenshot into tens of thousands of tokens.
//
// Long bodies are megabytes, so this stays zero-copy: the unsafe view avoids
// gjson.ParseBytes copying the whole body, and characters are counted on Raw to avoid
// unescaped copies and []rune allocations. The body is not mutated during the call.
func isAnthropicLongContextBody(body []byte) bool {
	if len(body) < anthropicLongContextMinBodyBytes {
		return false
	}
	return estimateAnthropicJSONTokens(gjson.Parse(unsafe.String(unsafe.SliceData(body), len(body)))) > anthropicLongContextTokenThreshold
}

func estimateAnthropicJSONTokens(value gjson.Result) int {
	if value.Type == gjson.String {
		return estimateAnthropicRawStringTokens(value.Raw)
	}
	total := 0
	if value.IsObject() || value.IsArray() {
		value.ForEach(func(key, child gjson.Result) bool {
			// Array elements have an empty key and pass this filter naturally.
			if name := key.String(); name != "data" && name != "signature" {
				total += estimateAnthropicJSONTokens(child)
			}
			return total <= anthropicLongContextTokenThreshold
		})
	}
	return total
}

// estimateAnthropicRawStringTokens counts 4 ASCII chars or 1 non-ASCII char per token.
// An escape counts as the single character it stands for: `\n` is one ASCII char and
// `\uXXXX` is one non-ASCII char. Python's json.dumps escapes all CJK text as \uXXXX
// by default, so counting bytes would overestimate such bodies about five times.
func estimateAnthropicRawStringTokens(raw string) int {
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

// anthropicLongContextScope must resolve to the same key on both sides: the writer
// sees the forwarded model name and the scheduler sees the requested one, and the two
// do not always agree on the [1m] suffix.
func anthropicLongContextScope(model string) string {
	model = strings.ToLower(normalizeClaudeCodeLongContextModel(strings.TrimSpace(model)))
	if model == "" {
		return ""
	}
	return model + anthropicLongContextScopeSuffix
}

// anthropicLongContextRateLimitKeys is appended by modelRateLimitKeysForRequest for
// Anthropic accounts. Short requests get no extra key.
func anthropicLongContextRateLimitKeys(ctx context.Context, modelKey string) []string {
	if ctx == nil || !isAnthropicLongContextRequest(ctx) {
		return nil
	}
	if scope := anthropicLongContextScope(modelKey); scope != "" {
		return []string{scope}
	}
	return nil
}

// This 429 does not reliably carry error.details (error_code is present on some
// responses and absent on others), so the message text is the only stable signal. If
// the wording changes this silently degrades to the account-level path, which is why
// the hit log includes error_code for comparison.
func isAnthropicLongContextCreditsRequired(body []byte) bool {
	return strings.Contains(strings.ToLower(extractUpstreamErrorMessage(body)), "long context")
}

// persistAnthropicLongContextCreditsRequired is called only from the Anthropic 429
// branch. A true result means the response has been handled and the caller must return
// without falling into any account-level rate limit branch — including when the write
// fails: a persistence failure must not be widened into an account-level limit.
func (s *RateLimitService) persistAnthropicLongContextCreditsRequired(ctx context.Context, account *Account, headers http.Header, body []byte, model string) bool {
	if s == nil || account == nil || !isAnthropicLongContextCreditsRequired(body) {
		return false
	}
	markAnthropicLongContextRequest(ctx)

	scope := anthropicLongContextScope(model)
	if scope == "" || s.accountRepo == nil {
		slog.Warn("anthropic_long_context_credits_required_unscoped", "account_id", account.ID, "model", model)
		return true
	}

	now := time.Now()
	resetAt := now.Add(anthropicLongContextScopeMaxCooldown)
	if headerReset, ok := parseAnthropicResetTimestamp(headers.Get("anthropic-ratelimit-unified-reset"), now, 366*24*time.Hour); ok && headerReset.Before(resetAt) {
		resetAt = headerReset
	}

	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scope, resetAt, anthropicLongContextCreditsRequiredReason); err != nil {
		slog.Warn("anthropic_long_context_credits_required_rate_limit_set_failed",
			"account_id", account.ID,
			"scope", scope,
			"reset_at", resetAt,
			"error", err)
		return true
	}
	slog.Info("anthropic_long_context_credits_required_model_rate_limited",
		"account_id", account.ID,
		"scope", scope,
		"reset_at", resetAt,
		"error_code", gjson.GetBytes(body, "error.details.error_code").String())
	return true
}
