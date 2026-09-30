//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const xiuUpdatesBody = `{"model":"claude-opus-5-5","max_tokens":1024,"thinking":{"type":"adaptive","display":"updates"},"messages":[{"role":"user","content":"hi"}]}`

// OAuth mimic 用固定 beta 集合覆盖客户端 beta：body 要 display=updates 时必须补上它的 beta，
// 否则上游 400 "thinking.adaptive.display: Input should be 'summarized', 'omitted', 'highlights'"。
func TestXiuThinkingDisplayUpdatesBetaOAuthMimic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		body   string
		drop   map[string]struct{}
		want   bool
	}{
		{"client sent beta", xiuBetaThinkingDisplayUpdates + ",custom-beta", xiuUpdatesBody, nil, true},
		// body 字段离了 beta 没有意义，字段本身就是意图。
		{"client omitted beta", "custom-beta", xiuUpdatesBody, nil, true},
		{"policy filtered", xiuBetaThinkingDisplayUpdates, xiuUpdatesBody, map[string]struct{}{xiuBetaThinkingDisplayUpdates: {}}, false},
		// 其余请求的伪装指纹一点不变。
		{"summarized", xiuBetaThinkingDisplayUpdates, `{"thinking":{"type":"adaptive","display":"summarized"}}`, nil, false},
		{"no thinking", xiuBetaThinkingDisplayUpdates, `{}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestGatewayServiceForBeta(false)
			header := http.Header{}
			header.Set("anthropic-beta", tc.header)
			got, set := s.computeFinalAnthropicBeta("oauth", true, "claude-opus-5-5", header, []byte(tc.body), tc.drop)
			require.True(t, set)
			require.Equal(t, tc.want, containsBetaToken(got, xiuBetaThinkingDisplayUpdates))
			require.False(t, containsBetaToken(got, "custom-beta"))
		})
	}
}

// 兜底：最终 beta 缺 token（策略过滤 / 账号覆写 / 透传客户端没带）时降成 omitted，宁可不给进度也不 400。
func TestXiuThinkingDisplayUpdatesSanitize(t *testing.T) {
	out, changed := sanitizeAnthropicBodyForBetaTokens([]byte(xiuUpdatesBody), "claude-code-20250219")
	require.True(t, changed)
	require.Equal(t, "omitted", gjson.GetBytes(out, "thinking.display").String())
	require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String())

	out, changed = sanitizeAnthropicBodyForBetaTokens([]byte(xiuUpdatesBody), "claude-code-20250219, "+xiuBetaThinkingDisplayUpdates)
	require.False(t, changed)
	require.Equal(t, "updates", gjson.GetBytes(out, "thinking.display").String())

	for _, body := range []string{`{"thinking":{"type":"adaptive","display":"summarized"}}`, `{"thinking":{"type":"adaptive"}}`, `{}`} {
		out, changed = sanitizeAnthropicBodyForBetaTokens([]byte(body), "")
		require.False(t, changed, body)
		require.Equal(t, body, string(out))
	}
}

// 整条出站链：mimic 补 beta 时 body 原样；策略过滤掉 beta 时 body 降级，两侧始终对称。
func TestXiuThinkingDisplayUpdatesBuildUpstreamRequest(t *testing.T) {
	for _, tc := range []struct {
		name        string
		filtered    bool
		wantDisplay string
	}{{"kept", false, "updates"}, {"downgraded", true, "omitted"}} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("anthropic-beta", xiuBetaThinkingDisplayUpdates)
			if tc.filtered {
				c.Set(betaPolicyFilterSetKey, map[string]struct{}{xiuBetaThinkingDisplayUpdates: {}})
			}
			account := &Account{ID: 702, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "test-token"}, Status: StatusActive, Schedulable: true}
			svc := &GatewayService{cfg: &config.Config{}}
			req, _, err := svc.buildUpstreamRequest(context.Background(), c, account, []byte(xiuUpdatesBody),
				"test-token", "oauth", "claude-opus-5-5", false, true)
			require.NoError(t, err)
			out := readUpstreamBodyForTest(t, req)
			header := getHeaderRaw(req.Header, "anthropic-beta")
			require.Equal(t, !tc.filtered, containsBetaToken(header, xiuBetaThinkingDisplayUpdates))
			require.Equal(t, tc.wantDisplay, gjson.GetBytes(out, "thinking.display").String())
		})
	}
}
