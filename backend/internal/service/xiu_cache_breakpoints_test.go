//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func xiuCountCacheControl(body []byte) int {
	_, m, tl, sy := collectCacheControlPaths(body)
	return len(m) + len(tl) + len(sy)
}

// 一个断点都没有：system 最后一块、最后一条 message、倒数第二个 user，共 3 个；tools[-1] 由挂载点后面的上游代码补，合计 4 个不超上限。
func TestXiuEnsureCacheBreakpoints_AddsThreeWhenNone(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"be brief"}],"tools":[{"name":"a","input_schema":{"type":"object"}},{"name":"b","input_schema":{"type":"object"}}],"messages":[
		{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},
		{"role":"user","content":[{"type":"text","text":"q2"}]},{"role":"assistant","content":"a2"},
		{"role":"user","content":"q3"}]}`)
	out := xiuEnsureCacheBreakpoints(body)
	require.Equal(t, 3, xiuCountCacheControl(out))
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "system.0.cache_control.type").String())
	require.Equal(t, "be brief", gjson.GetBytes(out, "system.0.text").String())
	require.False(t, gjson.GetBytes(out, "tools.1.cache_control").Exists())
	require.True(t, gjson.GetBytes(out, "messages.4.content.0.cache_control").Exists())
	require.True(t, gjson.GetBytes(out, "messages.2.content.0.cache_control").Exists())
	// 挂载点后面上游会补 tools[-1]，合计 4 个正好到上限
	withTools := applyToolsLastCacheBreakpoint(out)
	require.Equal(t, 4, xiuCountCacheControl(withTools))
	require.Equal(t, 4, xiuCountCacheControl(enforceCacheControlLimit(withTools)))
}

// 客户端在 messages / tools 上打了断点（哪怕只有一个）就一个字不碰。
func TestXiuEnsureCacheBreakpoints_LeavesClientBreakpointsAlone(t *testing.T) {
	for _, body := range []string{
		`{"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"q"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"q","cache_control":{"type":"ephemeral"}}]}]}`,
	} {
		require.JSONEq(t, body, string(xiuEnsureCacheBreakpoints([]byte(body))))
	}
}

// 只有 system 带断点（客户端只标 system，或上游 system 注入带来的）：system 不再动，对话断点照补。
func TestXiuEnsureCacheBreakpoints_SystemOnlyBreakpointStillGetsMessageOnes(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}},{"type":"text","text":"t"}],"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"a"},{"role":"user","content":"q2"}]}`)
	out := xiuEnsureCacheBreakpoints(body)
	require.False(t, gjson.GetBytes(out, "system.1.cache_control").Exists())
	require.True(t, gjson.GetBytes(out, "messages.2.content.0.cache_control").Exists())
}

// 单发请求（还没有 assistant 回复）一个字不碰：写了没人读的缓存只会多花钱。
func TestXiuEnsureCacheBreakpoints_SingleTurnUntouched(t *testing.T) {
	body := `{"system":[{"type":"text","text":"s"}],"tools":[{"name":"a","input_schema":{}}],"messages":[{"role":"user","content":"classify this"}]}`
	require.JSONEq(t, body, string(xiuEnsureCacheBreakpoints([]byte(body))))
}

// system 是数组时打在最后一个非空 text 块上；字符串 system 原样透传（上游有用例守着），没有 system 不造块。
func TestXiuMarkSystemLastBlock(t *testing.T) {
	out := xiuMarkSystemLastBlock([]byte(`{"system":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"text","text":""}]}`))
	require.True(t, gjson.GetBytes(out, "system.1.cache_control").Exists())
	require.False(t, gjson.GetBytes(out, "system.2.cache_control").Exists())
	require.JSONEq(t, `{"system":"plain"}`, string(xiuMarkSystemLastBlock([]byte(`{"system":"plain"}`))))
	require.JSONEq(t, `{"messages":[]}`, string(xiuMarkSystemLastBlock([]byte(`{"messages":[]}`))))
}

// 字符串 system 的请求：system 不动，其余三个断点照补。
func TestXiuEnsureCacheBreakpoints_StringSystemUntouched(t *testing.T) {
	body := []byte(`{"system":"be brief","tools":[{"name":"a","input_schema":{}}],"messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},{"role":"user","content":"q2"},{"role":"assistant","content":"a2"},{"role":"user","content":"q3"}]}`)
	out := xiuEnsureCacheBreakpoints(body)
	require.Equal(t, "be brief", gjson.GetBytes(out, "system").String())
	require.Equal(t, 2, xiuCountCacheControl(out))
}
