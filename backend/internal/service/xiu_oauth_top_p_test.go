package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// OAuth 伪装补 temperature=1 时，客户端传的 top_p 要丢掉（二选一，留着就是上游 400）。
// 2026-10-12 客户反馈：sonnet-4-6 只传 top_p 不传 temperature，回 "cannot both be specified"。
func TestXiuOAuthMimicDropsTopPWhenFillingTemperature(t *testing.T) {
	t.Parallel()

	norm := func(body string) []byte {
		out, _ := normalizeClaudeOAuthRequestBody([]byte(body), "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})
		return out
	}

	// 只传 top_p：补 temperature=1，丢 top_p。
	out := norm(`{"model":"claude-sonnet-4-6","messages":[],"top_p":0.9}`)
	require.Equal(t, "1", gjson.GetBytes(out, "temperature").Raw)
	require.False(t, gjson.GetBytes(out, "top_p").Exists())

	// 都没传：照旧补 temperature=1。
	out = norm(`{"model":"claude-sonnet-4-6","messages":[]}`)
	require.Equal(t, "1", gjson.GetBytes(out, "temperature").Raw)

	// 客户端自己传了 temperature：不补，原样透传。
	out = norm(`{"model":"claude-sonnet-4-6","messages":[],"temperature":0.3}`)
	require.Equal(t, "0.3", gjson.GetBytes(out, "temperature").Raw)
}
