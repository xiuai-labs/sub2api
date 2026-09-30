package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestXiuSSEErrorStatus(t *testing.T) {
	// 映射表本身是照抄文档的数据，只抽查；要测的是 overloaded 与兜底
	cases := map[string]int{
		`{"type":"error","error":{"type":"invalid_request_error"}}`:           400,
		`{"type":"error","error":{"details":null,"type":"overloaded_error"}}`: 529,
		`{"type":"error","error":{"type":"something_new"}}`:                   502,
		`not json`: 502,
		``:         502,
	}
	for body, want := range cases {
		require.Equal(t, want, xiuSSEErrorStatus([]byte(body)), body)
	}
}

// 挂载点端到端：流已吐出内容后的非 overloaded 错误（overloaded 那条由上游用例覆盖）。
func TestXiuSSEErrorStatus_PostOutputContentFilterIs400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	const errorJSON = `{"type":"error","error":{"type":"invalid_request_error","message":"Output blocked by content filtering policy"}}`
	fixture := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
		"event: error\ndata: " + errorJSON + "\n\n"
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(fixture)),
	}}
	repo := &gatewayForwardErrorPolicyRepoStub{}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     NewRateLimitService(repo, nil, cfg, nil, nil),
		deferredService:      &DeferredService{},
	}

	_, err = svc.Forward(context.Background(), c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
	require.JSONEq(t, errorJSON, string(failoverErr.ResponseBody))
}
