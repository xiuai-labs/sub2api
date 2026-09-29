package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 用例照抄 codex-state-kit src/downgrade.rs 的测试。

func xiuHeaders(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestXiuDowngrade_CleanTurnIsNotFlagged(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeHeaders(xiuHeaders("openai-model", "gpt-6-astra", "x-codex-turn-state", strings.Repeat("a", 292), "x-codex-primary-used-percent", "12"))
	require.Nil(t, s.report("gpt-6-astra", "gpt-6-astra"))
}

func TestXiuDowngrade_SafetyBufferingHeadersAreOnlySuspected(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeHeaders(xiuHeaders(
		"x-codex-safety-buffering-enabled", "true",
		"x-codex-safety-buffering-faster-model", "gpt-5.6-luna",
		"x-codex-primary-used-percent", "60",
		"x-codex-turn-state", strings.Repeat("b", 780),
	))
	r := s.report("gpt-6-astra", "gpt-6-astra")
	require.NotNil(t, r)
	require.Equal(t, xiuDowngradeSuspected, r.Verdict)
	require.True(t, r.SafetyBuffering)
	require.Empty(t, r.EffectiveModel)
	require.Equal(t, "gpt-5.6-luna", r.FasterModel)
	require.Equal(t, 780, r.TurnStateLen)
	require.Equal(t, 60.0, *r.PrimaryUsedPercent)
	require.Contains(t, r.Signals[0], "gpt-5.6-luna")
}

func TestXiuDowngrade_ReroutedServingModelConfirms(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeHeaders(xiuHeaders(
		"openai-model", "gpt-5.6-luna",
		"x-codex-safety-buffering-enabled", "true",
		"x-codex-safety-buffering-faster-model", "gpt-5.6-luna",
	))
	r := s.report("gpt-6-astra", "gpt-6-astra")
	require.NotNil(t, r)
	require.Equal(t, xiuDowngradeConfirmed, r.Verdict)
	require.Equal(t, "gpt-5.6-luna", r.EffectiveModel)
	require.Contains(t, r.Signals[0], "openai-model 为 gpt-5.6-luna")
}

func TestXiuDowngrade_AdvertisedButDisabledTreatmentIsNotFlagged(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeHeaders(xiuHeaders(
		"openai-model", "GPT-6-Astra",
		"x-codex-safety-buffering-enabled", "false",
		"x-codex-safety-buffering-faster-model", "gpt-5.6-luna",
	))
	require.Nil(t, s.report("gpt-6-astra", ""))
}

func TestXiuDowngrade_ServedSnapshotIsNotFlagged(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeHeaders(xiuHeaders("openai-model", "gpt-6-astra-2026-05-01"))
	require.Nil(t, s.report("gpt-6-astra", ""))
}

func TestXiuDowngrade_VerificationRecommendationIsSuspected(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeEvent([]byte(`{"type":"response.metadata","metadata":{"openai_verification_recommendation":["trusted_access_for_cyber"]}}`))
	r := s.report("gpt-6-astra", "")
	require.NotNil(t, r)
	require.Equal(t, xiuDowngradeSuspected, r.Verdict)
	require.Equal(t, []string{"trusted_access_for_cyber"}, r.Verifications)
}

func TestXiuDowngrade_StreamEventsFollowRetryModelPrecedence(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeHeaders(xiuHeaders("x-codex-safety-buffering-faster-model", "gpt-fast-header"))
	s.observeEvent([]byte(`{"type":"response.created","safety_buffering":false}`))
	s.observeEvent([]byte(`{"type":"response.output_text.delta","safety_buffering":{"use_cases":["cyber"],"reasons":["user_risk"],"retry_model":"gpt-fast-wire"}}`))
	r := s.report("gpt-6-astra", "")
	require.NotNil(t, r)
	require.Equal(t, "gpt-fast-wire", r.FasterModel)
	require.Equal(t, []string{"cyber"}, r.UseCases)
	require.Equal(t, []string{"user_risk"}, r.Reasons)

	// 显式 null 的 retry_model 表示没有备用模型。
	n := &xiuDowngradeSignals{}
	n.observeHeaders(xiuHeaders("x-codex-safety-buffering-faster-model", "gpt-fast-header"))
	n.observeEvent([]byte(`{"type":"x","safety_buffering":{"reasons":[],"use_cases":[],"retry_model":null}}`))
	r = n.report("gpt-6-astra", "")
	require.NotNil(t, r)
	require.Empty(t, r.FasterModel)
}

func TestXiuDowngrade_MetadataEventsCarryHeadersAndBuffering(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeEvent([]byte(`{"type":"response.metadata","headers":{"openai-model":"gpt-5.6-luna","x-codex-turn-state":"ccc"},"metadata":{"type":"safety_buffering","use_cases":["bio"],"reasons":["policy"]}}`))
	r := s.report("gpt-6-astra", "")
	require.NotNil(t, r)
	require.Equal(t, xiuDowngradeConfirmed, r.Verdict)
	require.Equal(t, []string{"bio"}, r.UseCases)
}

func TestXiuDowngrade_TurnStateLengthAloneDoesNotFlag(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeHeaders(xiuHeaders("x-codex-turn-state", strings.Repeat("d", 780)))
	require.Nil(t, s.report("gpt-6-astra", "gpt-6-astra"))
}

func TestXiuDowngrade_DifferentBodyModelAloneIsOnlySuspected(t *testing.T) {
	s := &xiuDowngradeSignals{}
	r := s.report("gpt-6-astra", "gpt-5.6-luna")
	require.NotNil(t, r)
	require.Equal(t, xiuDowngradeSuspected, r.Verdict)
	require.Equal(t, "gpt-5.6-luna", r.EffectiveModel)
	require.Nil(t, s.report("gpt-6-astra", "gpt-6-astra-2026-05-01"))
}

func TestXiuDowngrade_GeneratedTextMentioningKeysIsIgnored(t *testing.T) {
	s := &xiuDowngradeSignals{}
	s.observeEvent([]byte(`{"type":"response.output_text.delta","delta":"{\"safety_buffering\":{},\"headers\":{\"openai-model\":\"x\"}}"}`))
	require.Nil(t, s.report("gpt-6-astra", ""))
}

// 挂载点：begin 绑定本机 request_id → ObserveOpenAI 收事件 → 记用量时按同一 request_id 取走。
func TestXiuDowngrade_ObserverHandsEventSignalsToRecordUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = req.WithContext(context.WithValue(req.Context(), ctxkey.RequestID, "xiu-req-1"))

	// 上一次失败尝试留下的信号，在新一次 begin 时被清掉。
	stale := beginUpstreamResponseModelObservation(c)
	stale.ObserveOpenAI([]byte(`{"type":"response.metadata","metadata":{"openai_verification_recommendation":["stale"]}}`), "response.metadata")

	observer := beginUpstreamResponseModelObservation(c)
	observer.ObserveOpenAI([]byte(`{"type":"response.output_text.delta","delta":"hi"}`), "response.output_text.delta")
	observer.ObserveOpenAI([]byte(`{"type":"response.metadata","metadata":{"openai_verification_recommendation":["trusted_access_for_cyber"]}}`), "response.metadata")
	observer.ObserveOpenAI([]byte(`{"type":"response.completed","response":{"model":"gpt-6-astra"}}`), "response.completed")
	require.Equal(t, "gpt-6-astra", observer.Model())

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "xiu-req-1")
	got := xiuTakeDowngradeEvents(ctx)
	require.NotNil(t, got)
	require.Equal(t, []string{"trusted_access_for_cyber"}, got.verifications)
	require.Nil(t, xiuTakeDowngradeEvents(ctx), "取走之后不再留")
}

type xiuFakeGatewayCache struct {
	GatewayCache
	rdb *redis.Client
}

func (f *xiuFakeGatewayCache) XiuRedis() *redis.Client { return f.rdb }

type xiuFakeAPIKeyCache struct {
	APIKeyCache
	rdb *redis.Client
}

func (f *xiuFakeAPIKeyCache) XiuRedis() *redis.Client { return f.rdb }

func TestXiuDowngrade_RecordUsageSavesOnlyFlaggedAndListReadsBack(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := &OpenAIGatewayService{cache: &xiuFakeGatewayCache{rdb: rdb}}
	ctx := context.Background()

	openai := &Account{Platform: PlatformOpenAI}
	s.xiuRecordUsageDowngrade(ctx, &Account{Platform: PlatformGrok}, &UsageLog{RequestID: "grok", APIKeyID: 1},
		&OpenAIForwardResult{UpstreamResponseModel: "grok-build-x"}, "grok-4.9")
	s.xiuRecordUsageDowngrade(ctx, openai, &UsageLog{RequestID: "clean", APIKeyID: 1},
		&OpenAIForwardResult{UpstreamHeaders: xiuHeaders("openai-model", "gpt-6-astra"), UpstreamResponseModel: "gpt-6-astra"}, "gpt-6-astra")
	// WS 握手头里的 openai-model 不作数：连接是池化复用的。
	s.xiuRecordUsageDowngrade(ctx, openai, &UsageLog{RequestID: "ws-handshake", APIKeyID: 7},
		&OpenAIForwardResult{ResponseHeaders: xiuHeaders("openai-model", "gpt-5.6-luna"), OpenAIWSMode: true}, "gpt-6-astra")
	// 但握手头里的安全缓冲照算。
	s.xiuRecordUsageDowngrade(ctx, openai, &UsageLog{RequestID: "ws-buffered", APIKeyID: 7},
		&OpenAIForwardResult{ResponseHeaders: xiuHeaders("x-codex-safety-buffering-enabled", "true"), OpenAIWSMode: true}, "gpt-6-astra")
	s.xiuRecordUsageDowngrade(ctx, openai, &UsageLog{RequestID: "client:http", APIKeyID: 7},
		&OpenAIForwardResult{UpstreamHeaders: xiuHeaders("openai-model", "gpt-5.6-luna")}, "gpt-6-astra")

	require.ElementsMatch(t, []string{"xiu:usage_downgrade:7:client:http", "xiu:usage_downgrade:7:ws-buffered"}, mr.Keys())
	require.Equal(t, xiuDowngradeTTL, mr.TTL("xiu:usage_downgrade:7:client:http"))

	keys := &APIKeyService{cache: &xiuFakeAPIKeyCache{rdb: rdb}}
	got, err := keys.XiuListUsageDowngrades(ctx, []XiuUsageDowngradeQuery{
		{ID: 10, APIKeyID: 1, RequestID: "clean"},
		{ID: 11, APIKeyID: 7, RequestID: "client:http"},
		{ID: 12, APIKeyID: 8, RequestID: "client:http"}, // 同 request_id 不同 key 不串
		{ID: 13, APIKeyID: 7, RequestID: ""},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, xiuDowngradeConfirmed, got[11].Verdict)
	require.Equal(t, "gpt-5.6-luna", got[11].EffectiveModel)
}
