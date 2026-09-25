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

func TestXiuNormalizeOpus55Thinking(t *testing.T) {
	cases := []struct {
		name, model, thinking, wantType string
		wantBudget                      bool
	}{
		{"enabled becomes adaptive", "claude-opus-5-5", `{"type":"enabled","budget_tokens":1024}`, "adaptive", false},
		{"adaptive untouched", "claude-opus-5-5", `{"type":"adaptive"}`, "adaptive", false},
		{"other models untouched", "claude-opus-5", `{"type":"enabled","budget_tokens":1024}`, "enabled", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.model + `","messages":[{"role":"user","content":"hi"}],"thinking":` + tc.thinking + `}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
			require.NoError(t, err)
			xiuNormalizeOpus55Thinking(parsed, tc.model)
			out := parsed.Body.Bytes()
			require.Equal(t, tc.wantType, gjson.GetBytes(out, "thinking.type").String())
			require.Equal(t, tc.wantBudget, gjson.GetBytes(out, "thinking.budget_tokens").Exists())
			require.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(out, "messages").Raw)
		})
	}
}

// 挂载点回归：enabled / disabled 都不再在 Forward / ForwardCountTokens 入口被 400 拒掉，且 API-key 映射后的名字也生效。
func TestXiuOpus55ThinkingPassesGatewayValidation(t *testing.T) {
	for _, typ := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		model := "claude-opus-5-5"
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: typ}
		if typ == AccountTypeAPIKey {
			model = "public-opus"
			account.Credentials = map[string]any{"model_mapping": map[string]any{model: "claude-opus-5-5"}}
		}
		body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello"}],"thinking":{"type":"enabled","budget_tokens":1024}}`)
		parsed := &ParsedRequest{Model: model, Body: NewRequestBodyRef(body)}
		validationModel := model
		if typ == AccountTypeAPIKey {
			validationModel = account.GetMappedModel(model)
		}
		xiuNormalizeOpus55Thinking(parsed, validationModel)
		require.NoError(t, validateClaudeOpus55Request(parsed.Body.Bytes(), validationModel))
	}

	// 真走一遍入口：只要不是 400 就说明挂载点在校验之前（之后的上游调用在空 service 上会另行出错）。
	for _, thinking := range []string{`{"type":"enabled","budget_tokens":1024}`, `{"type":"disabled"}`} {
		for _, count := range []bool{false, true} {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}],"thinking":` + thinking + `}`)
			parsed := &ParsedRequest{Model: "claude-opus-5-5", Body: NewRequestBodyRef(body)}
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
			func() {
				defer func() { _ = recover() }()
				if count {
					_ = (&GatewayService{}).ForwardCountTokens(context.Background(), c, account, parsed)
				} else {
					_, _ = (&GatewayService{}).Forward(context.Background(), c, account, parsed)
				}
			}()
			require.NotContains(t, rec.Body.String(), "requires adaptive thinking")
			require.NotContains(t, []string{"enabled", "disabled"}, gjson.GetBytes(parsed.Body.Bytes(), "thinking.type").String())
		}
	}
}

func TestXiuNormalizeOpus55DisabledThinking(t *testing.T) {
	cases := []struct {
		name, extra, wantEffort string
	}{
		{"no effort gets low", ``, "low"},
		{"client effort kept", `,"output_config":{"effort":"high"}`, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}` + tc.extra + `}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
			require.NoError(t, err)
			xiuNormalizeOpus55Thinking(parsed, "claude-opus-5-5")
			out := parsed.Body.Bytes()
			require.False(t, gjson.GetBytes(out, "thinking").Exists())
			require.Equal(t, tc.wantEffort, gjson.GetBytes(out, "output_config.effort").String())
			require.NoError(t, validateClaudeOpus55Request(out, "claude-opus-5-5"))
		})
	}

	// 别的模型的 disabled 不碰。
	body := []byte(`{"model":"claude-opus-5","messages":[],"thinking":{"type":"disabled"}}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "")
	require.NoError(t, err)
	xiuNormalizeOpus55Thinking(parsed, "claude-opus-5")
	require.Equal(t, "disabled", gjson.GetBytes(parsed.Body.Bytes(), "thinking.type").String())
}
