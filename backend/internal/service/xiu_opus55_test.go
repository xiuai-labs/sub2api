package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"
)

// Claude Opus 5.5 官方定价：$4 输入 / $20 输出 per MTok，缓存读 $0.20，5m 写 $5，1h 写 $8。
// 比 Opus 5 便宜 20%，而 "claude-opus-5-5" 字面上包含 "opus-5"，
// 任何按子串归系列的兜底都会把它算成 Opus 5 的 $5/$25（多收 25%，无报错）。
const (
	opus55InputPricePerToken     = 4e-6
	opus55OutputPricePerToken    = 20e-6
	opus55CacheReadPricePerToken = 0.2e-6
)

// 价格表缺 claude-opus-5-5 时（远端滞后），不能落到 opus-5 条目。
func TestOpus55_FamilyFallbackDoesNotUseOpus5Rates(t *testing.T) {
	svc := NewBillingService(&config.Config{}, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-5":   {InputCostPerToken: 5e-6, OutputCostPerToken: 25e-6},
			"claude-opus-4-8": {InputCostPerToken: 5e-6, OutputCostPerToken: 25e-6},
		},
	})

	for _, model := range []string{"claude-opus-5-5", "claude-opus-5.5", "claude-opus-5-5-20260920"} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			assert.InDelta(t, opus55InputPricePerToken, pricing.InputPricePerToken, 1e-12)
			assert.InDelta(t, opus55OutputPricePerToken, pricing.OutputPricePerToken, 1e-12)
			assert.InDelta(t, opus55CacheReadPricePerToken, pricing.CacheReadPricePerToken, 1e-12)
		})
	}
}

// 动态价格服务完全不可用时的硬编码兜底；同时锁住 opus-5 不被 5.5 反向误伤。
func TestOpus55_HardcodedFallbackPricing(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)

	tests := []struct {
		model  string
		input  float64
		output float64
	}{
		{"claude-opus-5-5", opus55InputPricePerToken, opus55OutputPricePerToken},
		{"claude-opus-5.5", opus55InputPricePerToken, opus55OutputPricePerToken},
		{"claude-opus-5", 5e-6, 25e-6},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			assert.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-12)
			assert.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-12)
		})
	}
}

// 上游对 claude-opus-5-5 设了客户端版本闸门：
// "Claude Code 2.1.258 does not support this model; version 2.1.280 or newer is required."
// 伪装基线低于它，OAuth 号上的 opus-5-5 请求全部 400。
func TestOpus55_CLIVersionPassesUpstreamGate(t *testing.T) {
	assert.GreaterOrEqual(t, semver.Compare("v"+claude.CLICurrentVersion, "v2.1.280"), 0)
}

// 能力随 Opus 5 继承是有意为之（5.5 同样支持 fast 与 xhigh/max），这里锁住它。
func TestOpus55_InDefaultModels(t *testing.T) {
	assert.Contains(t, claude.DefaultModelIDs(), "claude-opus-5-5")
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, claude.EffortLevelsForModel("claude-opus-5-5"))
	assert.True(t, modelSupportsAnthropicFastMode("claude-opus-5-5"))
}
