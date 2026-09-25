package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// xiuNormalizeOpus55Thinking 把 Opus 5.5 请求里 adaptive 以外的 thinking 改成上游收的形态，
// 必须在 validateClaudeOpus55Request 之前调用：
//
//   - enabled  → adaptive，删 budget_tokens。v0.2.7 时它原样发给 Anthropic 是成功的，
//     上游 v0.2.8 的校验却把它 400 了。
//   - disabled → 删掉整个 thinking（Anthropic 的报错原文就是 "omit thinking"）；
//     客户端没给 output_config.effort 时补 low —— 省略 thinking 等于默认 adaptive + medium，
//     客户端本意是不想思考，low 是离它最近的档位。
//
// 两者都是 Claude Code 实打实发过来的（见 PATCHES.md），拒掉只会让用户撞 400。
func xiuNormalizeOpus55Thinking(parsed *ParsedRequest, model string) {
	if parsed == nil || parsed.Body == nil || !claude.IsOpus55(model) {
		return
	}
	body := parsed.Body.Bytes()
	var out []byte
	var err error
	switch gjson.GetBytes(body, "thinking.type").String() {
	case "enabled":
		if out, err = sjson.SetBytes(body, "thinking.type", "adaptive"); err != nil {
			return
		}
		if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
			if trimmed, err := sjson.DeleteBytes(out, "thinking.budget_tokens"); err == nil {
				out = trimmed
			}
		}
	case "disabled":
		if out, err = sjson.DeleteBytes(body, "thinking"); err != nil {
			return
		}
		if !gjson.GetBytes(out, "output_config.effort").Exists() {
			if withEffort, err := sjson.SetBytes(out, "output_config.effort", "low"); err == nil {
				out = withEffort
			}
		}
	default:
		return
	}
	_ = parsed.ReplaceBody(out)
}
