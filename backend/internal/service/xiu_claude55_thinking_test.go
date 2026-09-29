package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestXiuNormalizeClaude55Thinking(t *testing.T) {
	cases := []struct {
		name, model, thinking, extra string
		wantType                     string // 空串 = 整个 thinking 被删
		wantEffort                   string
	}{
		// Opus 5.5：关不掉，disabled → 省略 thinking，缺 effort 补 low。
		{"opus enabled becomes adaptive", "claude-opus-5-5", `{"type":"enabled","budget_tokens":1024}`, ``, "adaptive", ""},
		{"opus adaptive untouched", "claude-opus-5-5", `{"type":"adaptive"}`, ``, "adaptive", ""},
		{"opus disabled gets low", "claude-opus-5-5", `{"type":"disabled"}`, ``, "", "low"},
		{"opus disabled keeps client effort", "claude-opus-5-5", `{"type":"disabled"}`, `,"output_config":{"effort":"high"}`, "", "high"},
		// Sonnet 5.5：disabled → between_tools；它不收 xhigh / max，此时退回省略 thinking。
		{"sonnet enabled becomes adaptive", "claude-sonnet-5-5", `{"type":"enabled","budget_tokens":1024}`, ``, "adaptive", ""},
		{"sonnet disabled becomes between_tools", "claude-sonnet-5-5", `{"type":"disabled"}`, ``, "between_tools", ""},
		{"sonnet disabled with high effort", "claude-sonnet-5-5", `{"type":"disabled"}`, `,"output_config":{"effort":"high"}`, "between_tools", "high"},
		{"sonnet disabled with xhigh drops thinking", "claude-sonnet-5-5", `{"type":"disabled"}`, `,"output_config":{"effort":"xhigh"}`, "", "xhigh"},
		{"sonnet disabled with max drops thinking", "claude-sonnet-5-5", `{"type":"disabled"}`, `,"output_config":{"effort":"max"}`, "", "max"},
		{"sonnet between_tools untouched", "claude-sonnet-5-5", `{"type":"between_tools"}`, ``, "between_tools", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.model + `","messages":[{"role":"user","content":"hi"}],"thinking":` + tc.thinking + tc.extra + `}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
			require.NoError(t, err)
			xiuNormalizeClaude55Thinking(parsed, tc.model)
			out := parsed.Body.Bytes()
			require.Equal(t, tc.wantType, gjson.GetBytes(out, "thinking.type").String())
			require.Equal(t, tc.wantType != "", gjson.GetBytes(out, "thinking").Exists())
			require.False(t, gjson.GetBytes(out, "thinking.budget_tokens").Exists())
			require.Equal(t, tc.wantEffort, gjson.GetBytes(out, "output_config.effort").String())
			require.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(out, "messages").Raw)
			require.NoError(t, validateClaude55Request(out, tc.model))
		})
	}

	// 前代仍可 enabled / disabled，不碰。
	for _, model := range []string{"claude-opus-5", "claude-sonnet-5"} {
		for _, thinking := range []string{`{"type":"enabled","budget_tokens":1024}`, `{"type":"disabled"}`} {
			body := []byte(`{"model":"` + model + `","messages":[],"thinking":` + thinking + `}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
			require.NoError(t, err)
			xiuNormalizeClaude55Thinking(parsed, model)
			require.JSONEq(t, string(body), string(parsed.Body.Bytes()), model)
		}
	}
}

// 挂载点回归：真走 Forward / ForwardCountTokens 入口，四类账号、含 API-key 映射后的名字，
// enabled / disabled 都不再被入口校验 400（之后的上游调用在空 service 上会另行出错，不关心）。
func TestXiuClaude55ThinkingPassesGatewayValidation(t *testing.T) {
	for _, target := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		for _, thinking := range []string{`{"type":"enabled","budget_tokens":1024}`, `{"type":"disabled"}`} {
			for _, typ := range []string{AccountTypeOAuth, AccountTypeAPIKey, AccountTypeBedrock, AccountTypeServiceAccount} {
				for _, count := range []bool{false, true} {
					model := target
					account := &Account{ID: 1, Platform: PlatformAnthropic, Type: typ}
					switch typ {
					case AccountTypeAPIKey:
						model = "public-alias"
						account.Credentials = map[string]any{"model_mapping": map[string]any{model: target}}
					case AccountTypeBedrock:
						account.Credentials = map[string]any{"aws_region": "eu-west-1"}
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello"}],"thinking":` + thinking + `}`)
					parsed := &ParsedRequest{Model: model, Body: NewRequestBodyRef(body)}
					func() {
						defer func() { _ = recover() }()
						if count {
							_ = (&GatewayService{}).ForwardCountTokens(context.Background(), c, account, parsed)
						} else {
							_, _ = (&GatewayService{}).Forward(context.Background(), c, account, parsed)
						}
					}()
					name := target + "/" + typ + "/" + thinking
					require.NotContains(t, rec.Body.String(), "requires adaptive thinking", name)
					require.NotContains(t, []string{"enabled", "disabled"}, gjson.GetBytes(parsed.Body.Bytes(), "thinking.type").String(), name)
				}
			}
		}
	}
}
