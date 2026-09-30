//go:build unit

package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// xiuImagesDeadProxyUpstream 模拟 2026-09-20 线上：deadAccountID 绑的代理拨号超时，其余账号正常出图。
type xiuImagesDeadProxyUpstream struct {
	service.HTTPUpstream
	deadAccountID int64
	mu            sync.Mutex
	accountIDs    []int64
}

func (u *xiuImagesDeadProxyUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()
	if accountID == u.deadAccountID {
		return nil, errors.New(`Post "https://chatgpt.com/backend-api/codex/responses": socks connect tcp 154.53.73.10:45001->chatgpt.com:443: dial tcp 154.53.73.10:45001: i/o timeout`)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(bytes.NewBufferString(
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":11,\"output_tokens\":22},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2UtMQ==\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\",\"quality\":\"high\",\"size\":\"1024x1024\"}]}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}, nil
}

func (u *xiuImagesDeadProxyUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

// 第一个号的代理死了，用户应当无感：handler 换到第二个号，照常拿到图。
func TestXiuOpenAIGatewayHandlerImages_DeadProxyFailsOverToHealthyAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(3131)
	accounts := []service.Account{
		{
			ID: 1, Name: "image-account-dead-proxy", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Priority: 0,
			Credentials: map[string]any{"access_token": "token-1"},
		},
		{
			ID: 2, Name: "image-account-healthy", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{"access_token": "token-2"},
		},
	}
	upstream := &xiuImagesDeadProxyUpstream{deadAccountID: 1}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gatewayService := service.NewOpenAIGatewayService(
		openAIImagesFailoverAccountRepo{accounts: accounts},
		nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil,
		upstream,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		service.NewConcurrencyService(nil),
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)
	handler.maxAccountSwitches = 10

	body := []byte(`{"model":"gpt-image-1","prompt":"draw a cat","quality":"high","size":"1024x1024"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body)).WithContext(context.Background())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      99,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, AllowImageGeneration: true},
		User:    &service.User{ID: 100},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})

	handler.Images(c)

	require.Equal(t, []int64{1, 2}, upstream.calls(), "dead-proxy account must be tried once, then the healthy one")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "aW1hZ2UtMQ==", gjson.GetBytes(rec.Body.Bytes(), "data.0.b64_json").String())
	require.NotContains(t, rec.Body.String(), "154.53.73.10")
}
