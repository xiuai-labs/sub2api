package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 2026-09-20 线上：账号绑的 socks 代理挂了，文本请求静默换号，生图却把
// `socks connect tcp ...: i/o timeout` 直接回给用户 —— 生图两个转发点返回的是普通 error，
// handler 的换号循环只认 *UpstreamFailoverError。
func TestXiuForwardImages_TransportErrorReturnsFailoverError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dialErr := errors.New(`Post "https://chatgpt.com/backend-api/codex/images/generations": socks connect tcp 154.53.73.10:45001->chatgpt.com:443: dial tcp 154.53.73.10:45001: i/o timeout`)

	cases := []struct {
		name    string
		account *Account
	}{
		{
			name: "oauth",
			account: &Account{
				ID:       1,
				Name:     "openai-oauth",
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"access_token":       "token-123",
					"chatgpt_account_id": "acct-123",
				},
			},
		},
		{
			name: "apikey",
			account: &Account{
				ID:       2,
				Name:     "openai-apikey",
				Platform: PlatformOpenAI,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":  "test-api-key",
					"base_url": "https://image-upstream.example/v1",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			svc := &OpenAIGatewayService{
				cfg:          &config.Config{},
				httpUpstream: &httpUpstreamRecorder{err: dialErr},
			}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)

			_, err = svc.ForwardImages(context.Background(), c, tc.account, body, parsed, "")

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr, "transport error must become a failover so the handler switches account, got: %T", err)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.Empty(t, rec.Body.String(), "handler owns the response; forward must not write on transport error")

			rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, events, 1)
			require.Equal(t, "request_error", events[0].Kind)
		})
	}
}
