package service

// xiu fork：用量记录的「已降级 / 疑似降智」标记（见仓库根 PATCHES.md）。
//
// 判定照抄 codex-state-kit 的 src/downgrade.rs，而它照的是官方 Codex 客户端（codex-rs），
// 只看上游自己给出的信号：
//
//   - openai-model / x-openai-model 与发出去的模型不一致（不区分大小写）→ 已降级。
//     官方客户端据此提示「请求被改路由到备用模型」，是唯一一条确证。
//   - 安全缓冲：x-codex-safety-buffering-enabled: true，或流事件带 safety_buffering 对象，
//     或 response.metadata 的 metadata.type = safety_buffering → 疑似降智。本轮没被改路由。
//   - response.metadata 里的 openai_verification_recommendation → 疑似降智。
//   - 只有响应体里的模型名不一致（带日期快照不算）→ 疑似降智。
//
// 信号有两处来源：响应头（HTTP 响应头 / WS 握手头）在记用量时从 OpenAIForwardResult 上直接读；
// 流事件里的信号由 upstreamResponseModelObserver 顺手看一眼，按本机 request_id 暂存，
// 记用量时取走。WS 路径的观察器不经过 begin，拿不到 request_id，所以 WS 只有握手头这一路，
// 而握手头里的 openai-model 不用（见 xiuRecordUsageDowngrade），WS 上「已降级」判不出来。
//
// 只存被标记的请求，存在 Redis、TTL 3 天（这是个临时功能，不留迁移）；没有 key 就是干净的。
// key 用 usage_logs 的幂等键 (api_key_id, request_id)：写标记时 usage_logs 的行可能还在
// 批量队列里，拿不到自增 id；前端表格每行本来就带这两个字段。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	xiuDowngradeConfirmed = "confirmed"
	xiuDowngradeSuspected = "suspected"

	xiuHeaderSafetyEnabled     = "x-codex-safety-buffering-enabled"
	xiuHeaderSafetyFasterModel = "x-codex-safety-buffering-faster-model"
	xiuHeaderTurnState         = "x-codex-turn-state"
	xiuHeaderPrimaryUsed       = "x-codex-primary-used-percent"

	xiuDowngradeKeyPrefix = "xiu:usage_downgrade:"
	xiuDowngradeTTL       = 72 * time.Hour

	// 流事件信号暂存的寿命：请求结束到记用量之间只隔一个 worker 排队，
	// 过了这个时间还没人取，就是失败请求留下的，扫掉。
	xiuDowngradeEventTTL = 10 * time.Minute
)

// XiuUsageDowngradeReport 是一条被标记请求的判定与依据，原样存进 Redis、原样吐给前端。
type XiuUsageDowngradeReport struct {
	Verdict            string   `json:"verdict"`
	RequestedModel     string   `json:"requested_model,omitempty"`
	EffectiveModel     string   `json:"effective_model,omitempty"`
	SafetyBuffering    bool     `json:"safety_buffering,omitempty"`
	Reasons            []string `json:"reasons,omitempty"`
	UseCases           []string `json:"use_cases,omitempty"`
	FasterModel        string   `json:"faster_model,omitempty"`
	Verifications      []string `json:"verifications,omitempty"`
	TurnStateLen       int      `json:"turn_state_len,omitempty"`
	PrimaryUsedPercent *float64 `json:"primary_used_percent,omitempty"`
	Signals            []string `json:"signals"`
}

// xiuRedisProvider 由 repository 的 gatewayCache / apiKeyCache 在 xiu 文件里实现，
// 这里只做类型断言，免得动上游的接口与装配。
type xiuRedisProvider interface {
	XiuRedis() *redis.Client
}

func xiuDowngradeKey(apiKeyID int64, requestID string) string {
	return xiuDowngradeKeyPrefix + strconv.FormatInt(apiKeyID, 10) + ":" + requestID
}

// ── 信号 ────────────────────────────────────────────────────────────────

type xiuDowngradeSignals struct {
	safetyEnabled *bool
	fasterModel   string
	servedModel   string

	buffered         bool // 见过 safety_buffering 对象
	reasons          []string
	useCases         []string
	retryModelSet    bool // 事件显式给了 retry_model（含 null），优先于头
	retryModel       string
	verifications    []string
	turnStateLen     int
	primaryUsed      *float64
	lastObservedUnix int64
}

func (s *xiuDowngradeSignals) observeHeader(name, value string) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(name)) {
	case xiuHeaderSafetyEnabled:
		enabled := strings.EqualFold(value, "true")
		s.safetyEnabled = &enabled
	case xiuHeaderSafetyFasterModel:
		if value != "" {
			s.fasterModel = value
		}
	case "openai-model", "x-openai-model":
		if value != "" {
			s.servedModel = value
		}
	case xiuHeaderTurnState:
		if value != "" {
			s.turnStateLen = len(value)
		}
	case xiuHeaderPrimaryUsed:
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			s.primaryUsed = &f
		}
	}
}

func (s *xiuDowngradeSignals) observeHeaders(h http.Header) {
	for name, values := range h {
		if len(values) > 0 {
			s.observeHeader(name, values[0])
		}
	}
}

func xiuJSONStrings(v gjson.Result) []string {
	var out []string
	if !v.IsArray() {
		return out
	}
	for _, item := range v.Array() {
		if text := strings.TrimSpace(item.String()); text != "" && item.Type != gjson.Null {
			out = append(out, text)
		}
	}
	return out
}

func xiuAppendUnique(list []string, items ...string) []string {
	for _, item := range items {
		found := false
		for _, existing := range list {
			if existing == item {
				found = true
				break
			}
		}
		if !found {
			list = append(list, item)
		}
	}
	return list
}

func (s *xiuDowngradeSignals) observeBuffering(v gjson.Result) {
	if !v.IsObject() {
		return
	}
	s.buffered = true
	s.reasons = xiuAppendUnique(s.reasons, xiuJSONStrings(v.Get("reasons"))...)
	s.useCases = xiuAppendUnique(s.useCases, xiuJSONStrings(v.Get("use_cases"))...)
	if retry := v.Get("retry_model"); retry.Exists() {
		s.retryModelSet = true
		s.retryModel = ""
		if retry.Type == gjson.String {
			s.retryModel = strings.TrimSpace(retry.String())
		}
	}
}

func (s *xiuDowngradeSignals) observeHeaderJSON(v gjson.Result) {
	if !v.IsObject() {
		return
	}
	v.ForEach(func(key, value gjson.Result) bool {
		if value.IsArray() {
			value = value.Get("0")
		}
		if value.Type != gjson.Null {
			s.observeHeader(key.String(), value.String())
		}
		return true
	})
}

var (
	xiuKeySafetyBuffering = []byte(`"safety_buffering"`)
	xiuKeyVerification    = []byte(`"openai_verification_recommendation"`)
	xiuKeyHeaders         = []byte(`"headers"`)
)

// xiuDowngradeEventHasSignal 是每帧都要走的快路径：绝大多数帧三次子串扫描就结束。
func xiuDowngradeEventHasSignal(payload []byte) bool {
	return bytes.Contains(payload, xiuKeySafetyBuffering) ||
		bytes.Contains(payload, xiuKeyVerification) ||
		bytes.Contains(payload, xiuKeyHeaders)
}

// observeEvent 只认顶层 / response 层的键，生成文本里出现同名字符串不会误判。
func (s *xiuDowngradeSignals) observeEvent(payload []byte) {
	if !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	if b := root.Get("safety_buffering"); b.IsObject() {
		s.observeBuffering(b)
	}
	if root.Get("type").String() == "response.metadata" {
		meta := root.Get("metadata")
		if meta.Get("type").String() == "safety_buffering" {
			s.observeBuffering(meta)
		}
		s.verifications = xiuAppendUnique(s.verifications, xiuJSONStrings(meta.Get("openai_verification_recommendation"))...)
	}
	s.observeHeaderJSON(root.Get("headers"))
	s.observeHeaderJSON(root.Get("response.headers"))
}

func (s *xiuDowngradeSignals) merge(o *xiuDowngradeSignals) {
	if o == nil {
		return
	}
	if o.safetyEnabled != nil {
		s.safetyEnabled = o.safetyEnabled
	}
	if o.fasterModel != "" {
		s.fasterModel = o.fasterModel
	}
	if o.servedModel != "" {
		s.servedModel = o.servedModel
	}
	if o.buffered {
		s.buffered = true
	}
	s.reasons = xiuAppendUnique(s.reasons, o.reasons...)
	s.useCases = xiuAppendUnique(s.useCases, o.useCases...)
	if o.retryModelSet {
		s.retryModelSet, s.retryModel = true, o.retryModel
	}
	s.verifications = xiuAppendUnique(s.verifications, o.verifications...)
	if o.turnStateLen > 0 {
		s.turnStateLen = o.turnStateLen
	}
	if o.primaryUsed != nil {
		s.primaryUsed = o.primaryUsed
	}
}

// xiuSameModel：同名，或同名的带日期快照（gpt-6-astra-2026-05-01）。
func xiuSameModel(requested, actual string) bool {
	requested = strings.ToLower(strings.TrimSpace(requested))
	actual = strings.ToLower(strings.TrimSpace(actual))
	return requested == actual || strings.HasPrefix(actual, requested+"-")
}

// report 判定。sentModel 是发往上游的模型，responseModel 是响应体里声明的模型。
func (s *xiuDowngradeSignals) report(sentModel, responseModel string) *XiuUsageDowngradeReport {
	sent := strings.TrimSpace(sentModel)
	responseModel = strings.TrimSpace(responseModel)
	var signals []string

	// 官方客户端这里是严格的不区分大小写相等；我们放宽到允许带日期快照，
	// 因为池子里还有公开 API 的号，那边的 openai-model 头给的是快照名。
	servedMismatch := sent != "" && s.servedModel != "" && !xiuSameModel(sent, s.servedModel)
	if servedMismatch {
		signals = append(signals, fmt.Sprintf("上游响应头 openai-model 为 %s，与请求的 %s 不一致（官方客户端据此提示请求被改路由到备用模型）", s.servedModel, sent))
	}

	// 只有 enabled: false 的头只是宣告有这项处理，不算。
	buffered := s.buffered || (s.safetyEnabled != nil && *s.safetyEnabled)
	fasterModel := s.fasterModel
	if s.retryModelSet {
		fasterModel = s.retryModel
	}
	if buffered {
		text := "上游对本次请求启用了安全缓冲（safety buffering），响应被额外审查"
		var parts []string
		if len(s.useCases) > 0 {
			parts = append(parts, "场景 "+strings.Join(s.useCases, "/"))
		}
		if len(s.reasons) > 0 {
			parts = append(parts, "原因 "+strings.Join(s.reasons, "/"))
		}
		if len(parts) > 0 {
			text += "（" + strings.Join(parts, "，") + "）"
		}
		if fasterModel != "" {
			text += "，官方客户端会提示改用更快的 " + fasterModel + " 重试"
		}
		signals = append(signals, text)
	}

	if len(s.verifications) > 0 {
		signals = append(signals, "上游建议账号完成验证："+strings.Join(s.verifications, "/")+"（被改路由的账号会收到该提示）")
	}

	bodyMismatch := sent != "" && responseModel != "" && !xiuSameModel(sent, responseModel)
	if bodyMismatch {
		signals = append(signals, fmt.Sprintf("响应体返回的模型为 %s，与请求的 %s 不一致", responseModel, sent))
	}

	if !servedMismatch && !buffered && !bodyMismatch && len(s.verifications) == 0 {
		return nil
	}
	if s.turnStateLen > 0 {
		signals = append(signals, fmt.Sprintf("x-codex-turn-state 长度 %d（仅供参考）", s.turnStateLen))
	}
	if s.primaryUsed != nil {
		signals = append(signals, fmt.Sprintf("主额度已用 %s%%（仅供参考）", strconv.FormatFloat(*s.primaryUsed, 'f', -1, 64)))
	}

	r := &XiuUsageDowngradeReport{
		Verdict:            xiuDowngradeSuspected,
		RequestedModel:     sent,
		SafetyBuffering:    buffered,
		Reasons:            s.reasons,
		UseCases:           s.useCases,
		Verifications:      s.verifications,
		TurnStateLen:       s.turnStateLen,
		PrimaryUsedPercent: s.primaryUsed,
		Signals:            signals,
	}
	if servedMismatch {
		r.Verdict = xiuDowngradeConfirmed
		r.EffectiveModel = s.servedModel
	} else if bodyMismatch {
		r.EffectiveModel = responseModel
	}
	if buffered {
		r.FasterModel = fasterModel
	}
	return r
}

// ── 流事件暂存：observer → RecordUsage ─────────────────────────────────

var xiuDowngradeEvents = struct {
	sync.Mutex
	m         map[string]*xiuDowngradeSignals
	lastSweep time.Time
}{m: map[string]*xiuDowngradeSignals{}}

func xiuRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxkey.RequestID).(string)
	return strings.TrimSpace(id)
}

// xiuBindDowngradeObserver 在 beginUpstreamResponseModelObservation 里挂一行。
// 每次 begin 是一次新的转发尝试：清掉上一次尝试留下的信号，只算最终成功的那次。
func xiuBindDowngradeObserver(c *gin.Context, o *upstreamResponseModelObserver) {
	if c == nil || c.Request == nil || o == nil {
		return
	}
	o.xiuRequestID = xiuRequestIDFromContext(c.Request.Context())
	if o.xiuRequestID == "" {
		return
	}
	xiuDowngradeEvents.Lock()
	delete(xiuDowngradeEvents.m, o.xiuRequestID)
	xiuDowngradeEvents.Unlock()
}

// xiuObserveDowngradeEvent 在 ObserveOpenAI 里挂一行，每帧都走，快路径必须便宜。
func (o *upstreamResponseModelObserver) xiuObserveDowngradeEvent(payload []byte) {
	if o == nil || o.xiuRequestID == "" || !xiuDowngradeEventHasSignal(payload) {
		return
	}
	var probe xiuDowngradeSignals
	probe.observeEvent(payload)
	if !probe.buffered && len(probe.verifications) == 0 && probe.servedModel == "" &&
		probe.safetyEnabled == nil && probe.fasterModel == "" {
		return
	}
	now := time.Now()
	xiuDowngradeEvents.Lock()
	defer xiuDowngradeEvents.Unlock()
	entry := xiuDowngradeEvents.m[o.xiuRequestID]
	if entry == nil {
		entry = &xiuDowngradeSignals{}
		xiuDowngradeEvents.m[o.xiuRequestID] = entry
	}
	entry.merge(&probe)
	entry.lastObservedUnix = now.Unix()
	if now.Sub(xiuDowngradeEvents.lastSweep) > xiuDowngradeEventTTL {
		xiuDowngradeEvents.lastSweep = now
		cutoff := now.Add(-xiuDowngradeEventTTL).Unix()
		for k, v := range xiuDowngradeEvents.m {
			if v.lastObservedUnix < cutoff {
				delete(xiuDowngradeEvents.m, k)
			}
		}
	}
}

func xiuTakeDowngradeEvents(ctx context.Context) *xiuDowngradeSignals {
	id := xiuRequestIDFromContext(ctx)
	if id == "" {
		return nil
	}
	xiuDowngradeEvents.Lock()
	defer xiuDowngradeEvents.Unlock()
	entry := xiuDowngradeEvents.m[id]
	delete(xiuDowngradeEvents.m, id)
	return entry
}

// ── 记用量时判定并落库 ─────────────────────────────────────────────────

// xiuRecordUsageDowngrade 在 OpenAIGatewayService.RecordUsage 里、usageLog 建好之后挂一行。
// 只记被标记的请求；落库失败只打日志，绝不影响计费。
// 只看 OpenAI 平台的号：这个服务也转 grok / 国产供应商，它们的模型名本来就对不上，不是降智。
func (s *OpenAIGatewayService) xiuRecordUsageDowngrade(ctx context.Context, account *Account, usageLog *UsageLog, result *OpenAIForwardResult, sentModel string) {
	if s == nil || account == nil || !account.IsOpenAI() || usageLog == nil || result == nil {
		return
	}
	signals := &xiuDowngradeSignals{}
	if result.ResponseHeaders != nil {
		signals.observeHeaders(result.ResponseHeaders)
		// WS 的 ResponseHeaders 是握手头：连接来自池子、按模型亲和复用，客户端也可能中途换模型，
		// 握手时的 openai-model 不一定是这一轮的，拿它判「已降级」会误报。只留安全缓冲这类账号级信号。
		if result.OpenAIWSMode {
			signals.servedModel = ""
		}
	}
	if result.UpstreamHeaders != nil {
		signals.observeHeaders(result.UpstreamHeaders)
	}
	signals.merge(xiuTakeDowngradeEvents(ctx))

	report := signals.report(sentModel, result.UpstreamResponseModel)
	if report == nil || strings.TrimSpace(usageLog.RequestID) == "" {
		return
	}
	provider, ok := s.cache.(xiuRedisProvider)
	if !ok || provider.XiuRedis() == nil {
		return
	}
	payload, err := json.Marshal(report)
	if err == nil {
		err = provider.XiuRedis().Set(ctx, xiuDowngradeKey(usageLog.APIKeyID, usageLog.RequestID), payload, xiuDowngradeTTL).Err()
	}
	if err != nil {
		logger.L().Warn("xiu.usage_downgrade_save_failed",
			zap.String("request_id", usageLog.RequestID),
			zap.Int64("api_key_id", usageLog.APIKeyID),
			zap.Error(err))
		return
	}
	logger.L().Info("xiu.usage_downgrade",
		zap.String("verdict", report.Verdict),
		zap.Int64("account_id", usageLog.AccountID),
		zap.String("request_id", usageLog.RequestID),
		zap.String("sent_model", report.RequestedModel),
		zap.String("effective_model", report.EffectiveModel))
}

// XiuUsageDowngradeQuery 是用量列表里的一行：id 用来回填结果，其余两个拼 key。
type XiuUsageDowngradeQuery struct {
	ID        int64  `json:"id"`
	APIKeyID  int64  `json:"api_key_id"`
	RequestID string `json:"request_id"`
}

// XiuListUsageDowngrades 给管理端用量列表批量查标记，返回按 usage_logs.id 索引。
// 挂在 APIKeyService 上只是因为管理端用量 handler 手里有它、它手里有 Redis。
func (s *APIKeyService) XiuListUsageDowngrades(ctx context.Context, rows []XiuUsageDowngradeQuery) (map[int64]*XiuUsageDowngradeReport, error) {
	out := map[int64]*XiuUsageDowngradeReport{}
	if s == nil || len(rows) == 0 {
		return out, nil
	}
	provider, ok := s.cache.(xiuRedisProvider)
	if !ok || provider.XiuRedis() == nil {
		return out, nil
	}
	keys := make([]string, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if requestID := strings.TrimSpace(row.RequestID); requestID != "" {
			keys = append(keys, xiuDowngradeKey(row.APIKeyID, requestID))
			ids = append(ids, row.ID)
		}
	}
	if len(keys) == 0 {
		return out, nil
	}
	values, err := provider.XiuRedis().MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		text, ok := value.(string)
		if !ok {
			continue
		}
		var report XiuUsageDowngradeReport
		if json.Unmarshal([]byte(text), &report) == nil {
			out[ids[i]] = &report
		}
	}
	return out, nil
}
