package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
)

// xiuNormalizeClaude55Thinking 把 5.5 代请求里上游不收的 thinking 改成它收的形态，
// 必须在 validateClaude55Request 之前调用。enabled / disabled 都是 Claude Code 实打实发过来的
// （见 PATCHES.md），拒掉只会让用户撞 400。
//
// Opus 5.5（thinking 关不掉）：
//   - enabled  → adaptive，删 budget_tokens。v0.2.7 时它原样发给 Anthropic 是成功的，
//     上游 v0.2.8 的校验却把它 400 了。
//   - disabled → 删掉整个 thinking（Anthropic 的报错原文就是 "omit thinking"）；
//     客户端没给 output_config.effort 时补 low —— 省略 thinking 等于默认 adaptive + medium，
//     客户端本意是不想思考，low 是离它最近的档位。
//
// Sonnet 5.5（最低档是 between_tools，即「不预先思考」）：
//   - enabled  → adaptive，删 budget_tokens。
//   - disabled → between_tools，与上游 Bedrock 兼容路径（sanitizeBedrockThinking）同一映射。
//     between_tools 在 xhigh / max 下 400，此时退回删掉 thinking：客户端既要高 effort，
//     就按 effort 走 adaptive。
func xiuNormalizeClaude55Thinking(parsed *ParsedRequest, model string) {
	if parsed == nil || parsed.Body == nil {
		return
	}
	isOpus55, isSonnet55 := claude.IsOpus55(model), claude.IsSonnet55(model)
	if !isOpus55 && !isSonnet55 {
		return
	}
	body := parsed.Body.Bytes()
	var out []byte
	var ok bool
	switch gjson.GetBytes(body, "thinking.type").String() {
	case "enabled":
		if out, ok = setJSONValueBytes(body, "thinking.type", "adaptive"); !ok {
			return
		}
		out, _ = deleteJSONPathBytes(out, "thinking.budget_tokens")
	case "disabled":
		// OutputEffort 由解析阶段填好，不必再扫一遍 body。
		effort := parsed.OutputEffort
		if isSonnet55 && effort != "xhigh" && effort != "max" {
			// disabled 本身不带别的字段；整块替换，免得残留字段撞 between_tools 的「不收其他字段」。
			if out, ok = setJSONRawBytes(body, "thinking", []byte(`{"type":"between_tools"}`)); !ok {
				return
			}
			break
		}
		if out, ok = deleteJSONPathBytes(body, "thinking"); !ok {
			return
		}
		if isOpus55 && effort == "" {
			out, _ = setJSONValueBytes(out, "output_config.effort", "low")
		}
	default:
		return
	}
	_ = parsed.ReplaceBody(out)
}
