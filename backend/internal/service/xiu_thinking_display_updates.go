package service

import (
	"bytes"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// thinking.display=updates（只回进度更新）是 beta 值，上游只在 anthropic-beta 带这个 token 时收；
// 缺 token 报的是与未知值同一个 400："thinking.adaptive.display: Input should be 'summarized', 'omitted', 'highlights'"。
// 新版 Claude Code / Agent SDK 默认就发它。
const xiuBetaThinkingDisplayUpdates = "thinking-display-updates-2026-08-18"

// 每个请求都过这里、body 可达数 MB：先用字节扫描挡掉绝大多数请求，gjson 只做精确判定。
func xiuWantsThinkingDisplayUpdates(body []byte) bool {
	if !bytes.Contains(body, []byte(`"updates"`)) {
		return false
	}
	return gjson.GetBytes(body, "thinking.display").String() == "updates"
}

// xiuMimicThinkingDisplayBeta 给 OAuth mimic 的 incomingBeta 补上 updates 的 token。
// mimic 用固定 beta 集合覆盖客户端 beta（只放行 structured-outputs），经 new-api 转来的请求 UA 不是
// claude-cli，全走这条路，token 被丢、body 原样 → 2026-10-02 起持续 400。
// 只在 body 真要 updates 时补：其余请求的伪装指纹保持不变。策略过滤仍由调用方的 drop set 生效。
func xiuMimicThinkingDisplayBeta(incoming string, body []byte) string {
	if !xiuWantsThinkingDisplayUpdates(body) {
		return incoming
	}
	return mergeAnthropicBeta([]string{xiuBetaThinkingDisplayUpdates}, incoming)
}

// xiuDowngradeThinkingDisplayUnlessBeta 是兜底：最终 beta 缺 token（策略过滤 / 账号覆写 / 透传客户端没带）
// 时把 updates 降成 omitted。updates 下推理块本来就是空的，omitted 只少了进度更新，比整条 400 好。
func xiuDowngradeThinkingDisplayUnlessBeta(body []byte, anthropicBetaHeader string) ([]byte, bool) {
	if anthropicBetaTokensContains(anthropicBetaHeader, xiuBetaThinkingDisplayUpdates) || !xiuWantsThinkingDisplayUpdates(body) {
		return body, false
	}
	b, err := sjson.SetBytes(body, "thinking.display", "omitted")
	if err != nil {
		logger.L().Warn("xiu.thinking_display_downgrade_failed", zap.Error(err), zap.Int("body_len", len(body)))
		return body, false
	}
	// 按理修复后很少触发；一旦变多说明又有路径在丢 beta。
	logger.L().Info("xiu.thinking_display_downgraded",
		zap.String("model", gjson.GetBytes(body, "model").String()),
		zap.String("anthropic_beta", anthropicBetaHeader),
	)
	return b, true
}
