package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// xiuNormalizeClaudeCompatRequest 把 OpenAI 协议（Chat Completions / Responses）进来的 Claude 请求
// 改成上游收的形态。四个 OpenAI→Anthropic 入口（Anthropic 平台两个、OpenAI 平台 Anthropic 原生两个）
// 都在 validateClaude55Request 之前调用。纯函数：返回新值，不改入参。
//
//   - 新一代型号（claude.XiuIsStrictSamplingModel）剥掉采样参数。OpenAI 客户端几乎都带 temperature，
//     原样转发就是 400；Anthropic 的建议本来就是「省略」。
//     body 只喂给 validateClaude55Request，而它只对 Sonnet 5.5 查采样参数 ——
//     所以只在 Sonnet 5.5 时改写 body，别的型号省掉几次整包拷贝（body 可达数 MB）。
//   - reasoning effort 的 none / minimal 上游只收 low..max，直译即 400。
//     Sonnet 5.5 → none（上游转换会译成 between_tools，真正的「不预先思考」）；
//     其余新一代 → low（最低档，简单请求会整轮跳过 thinking）；老模型默认就不思考 → 去掉 reasoning。
func xiuNormalizeClaudeCompatRequest(body []byte, req apicompat.ResponsesRequest, model string) ([]byte, apicompat.ResponsesRequest) {
	strict := claude.XiuIsStrictSamplingModel(model)
	isSonnet55 := claude.IsSonnet55(model)
	hadSampling := req.Temperature != nil || req.TopP != nil

	if strict {
		req.Temperature, req.TopP = nil, nil
	}
	if isSonnet55 {
		for _, field := range []string{"temperature", "top_p", "top_k"} {
			body, _ = deleteJSONPathBytes(body, field)
		}
	}

	effortFrom := ""
	if req.Reasoning != nil && (req.Reasoning.Effort == "none" || req.Reasoning.Effort == "minimal") {
		effortFrom = req.Reasoning.Effort
		effortTo := ""
		switch {
		case isSonnet55:
			effortTo = "none"
		case strict:
			effortTo = "low"
		}
		if effortTo == "" {
			req.Reasoning = nil
		} else {
			reasoning := *req.Reasoning
			reasoning.Effort = effortTo
			req.Reasoning = &reasoning
		}
	}

	if (strict && hadSampling) || effortFrom != "" {
		// 每个带 temperature 的 OpenAI 格式请求都会走到这里，量大，用 Debug。
		logger.L().Debug("xiu.claude_compat_request_normalized",
			zap.String("model", model),
			zap.Bool("sampling_stripped", strict && hadSampling),
			zap.String("effort_from", effortFrom),
			zap.Any("reasoning_to", req.Reasoning),
		)
	}
	return body, req
}
