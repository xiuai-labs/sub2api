package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var xiuCompatInput = json.RawMessage(`"hi"`)

// 新一代 Claude 对非默认 temperature / top_p / top_k 一律 400（Sonnet 5.5 在本地入口就被拒）。
// OpenAI 客户端几乎都带 temperature，剥掉后整条转换链必须走得通。
func TestXiuClaudeCompatStripsSamplingForNewModels(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m","temperature":0.3,"top_p":0.9,"top_k":5,"messages":[]}`)
	for _, model := range []string{
		"claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-5-5", "claude-opus-5",
		"claude-sonnet-5", "claude-fable-5-1", "claude-opus-4-8", "claude-opus-4-7",
		// 别的写法同样要认出来。
		"us.anthropic.claude-opus-5", "claude-sonnet-5.5", "claude-haiku-5-5-thinking",
	} {
		req := apicompat.ResponsesRequest{Model: model, Input: xiuCompatInput, Temperature: new(0.3), TopP: new(0.9)}

		outBody, outReq := xiuNormalizeClaudeCompatRequest(body, req, model)

		require.NoError(t, validateClaude55Request(outBody, model), model)
		converted, err := apicompat.ResponsesToAnthropicRequest(&outReq)
		require.NoError(t, err, model)
		assert.Nil(t, converted.Temperature, model)
		assert.Nil(t, converted.TopP, model)
	}

	// 本地校验读原始 body 的只有 Sonnet 5.5，body 只为它改写。
	outBody, _ := xiuNormalizeClaudeCompatRequest(body, apicompat.ResponsesRequest{}, "claude-sonnet-5-5")
	for _, field := range []string{"temperature", "top_p", "top_k"} {
		assert.False(t, gjson.GetBytes(outBody, field).Exists(), field)
	}
}

// 老模型仍收采样参数，原样放行。
func TestXiuClaudeCompatKeepsSamplingForOlderModels(t *testing.T) {
	t.Parallel()

	body := []byte(`{"temperature":0.3}`)
	for _, model := range []string{"claude-haiku-4-5-20251001", "claude-sonnet-4-6", "claude-opus-4-6"} {
		req := apicompat.ResponsesRequest{Model: model, Temperature: new(0.3)}
		outBody, outReq := xiuNormalizeClaudeCompatRequest(body, req, model)
		assert.Equal(t, body, outBody, model)
		require.NotNil(t, outReq.Temperature, model)
		assert.InDelta(t, 0.3, *outReq.Temperature, 0, model)
	}
}

// OpenAI 的 none / minimal 原样译成 output_config.effort 会被上游 400（只收 low..max）；正常档位不碰。
func TestXiuClaudeCompatMapsReasoningEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model, effort string
		wantEffort    string // 转换后的 output_config.effort；空表示不带
		wantThinking  string // 转换后的 thinking.type；空表示不带
	}{
		// 新一代：最低档 low，thinking 可能整轮跳过。
		{"claude-opus-5", "none", "low", ""},
		{"claude-haiku-5-5", "minimal", "low", ""},
		{"claude-fable-5-1", "none", "low", ""},
		{"claude-opus-5-5", "none", "low", "adaptive"},
		// Sonnet 5.5 有真正的「不预先思考」档，上游已把 none 译成 between_tools。
		{"claude-sonnet-5-5", "none", "low", "between_tools"},
		{"claude-sonnet-5-5", "minimal", "low", "between_tools"},
		// 老模型默认就不思考：去掉 reasoning 即可。
		{"claude-haiku-4-5", "none", "", ""},
		{"claude-sonnet-4-6", "minimal", "", ""},
		// 正常档位原样。
		{"claude-opus-5", "high", "high", "enabled"},
	}
	for _, tc := range cases {
		reasoning := &apicompat.ResponsesReasoning{Effort: tc.effort}
		req := apicompat.ResponsesRequest{Model: tc.model, Input: xiuCompatInput, Reasoning: reasoning}

		_, outReq := xiuNormalizeClaudeCompatRequest([]byte(`{}`), req, tc.model)
		converted, err := apicompat.ResponsesToAnthropicRequest(&outReq)
		require.NoError(t, err, "%s %s", tc.model, tc.effort)

		gotEffort, gotThinking := "", ""
		if converted.OutputConfig != nil {
			gotEffort = converted.OutputConfig.Effort
		}
		if converted.Thinking != nil {
			gotThinking = converted.Thinking.Type
		}
		assert.Equal(t, tc.wantEffort, gotEffort, "%s %s", tc.model, tc.effort)
		assert.Equal(t, tc.wantThinking, gotThinking, "%s %s", tc.model, tc.effort)
		assert.Equal(t, tc.effort, reasoning.Effort, "入参共享的 Reasoning 不被改动")
	}
}
