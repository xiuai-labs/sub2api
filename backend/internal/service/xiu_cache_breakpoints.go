package service

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// xiuEnsureCacheBreakpoints 给**一个 cache_control 都没有**的请求补 Parrot 式断点：
// system 最后一块、messages 最后一条与倒数第二个 user（上游自己的 helper）。tools[-1] 不在这里补：
// 挂载点后面的两个分支（工具名改写 / 否则）都会无条件调 applyToolsLastCacheBreakpoint。
//
// 只在伪装分支（OAuth 号 + 非 Claude Code 客户端）挂载：不发断点的第三方客户端在这里原样出去，
// Anthropic 就一个字不缓存 —— 线上这类请求占经 new-api 的非 Claude Code 流量的 47%（PATCHES.md「补断点」）。
// 带了任何断点的请求一律不碰：客户端自己管断点的（Claude Code、Cline）比我们清楚该打在哪。
// 总数 ≤ 4 由后面已有的 enforceCacheControlLimit 兜底。
func xiuEnsureCacheBreakpoints(body []byte) []byte {
	// 🔴 只从第二轮起补（messages 里有过 assistant）：缓存写按 1.25 倍计费，单发请求（分类、抽取一类，
	// 线上这批流量里非流式的大头）写了永远没人读，补断点等于白多付 25%；多轮会话第二轮写、第三轮起读，
	// 只损失第一轮那一段的复用
	if !xiuHasAssistantMessage(body) {
		return body
	}
	// 复用上游 enforceCacheControlLimit 的收集器，口径一致；thinking 块上的非法断点不算（上游会删）
	_, messagePaths, toolPaths, systemPaths := collectCacheControlPaths(body)
	// 🔴 「客户端自己管断点」只看 messages / tools：system 上的断点可能是上游 system 注入带来的
	// （那个开关一开，注入块自带 cache_control），按它判会让这条补丁整个失效；
	// 客户端只标 system 的，给它补上对话断点也只多不少
	if len(messagePaths)+len(toolPaths) > 0 {
		return body
	}
	if len(systemPaths) == 0 {
		body = xiuMarkSystemLastBlock(body)
	}
	return addMessageCacheBreakpoints(body)
}

// xiuMarkSystemLastBlock 把断点打在 system 最后一个非空 text 块上。
//
// 🔴 字符串形式的 system 不碰：messages 上的断点本来就把前面的 system 一起缓存了，system 级断点
// 只是让不同会话能共享同一段 system 前缀，锦上添花；而把字符串升成数组会改变透传形态
// （上游有用例守着「关掉注入时 system 原样透传」）。
func xiuMarkSystemLastBlock(body []byte) []byte {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return body
	}
	raw := fmt.Sprintf(`{"type":"ephemeral","ttl":%q}`, claude.DefaultCacheControlTTL)
	blocks := system.Array()
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Get("type").String() != "text" || blocks[i].Get("text").String() == "" {
			continue
		}
		if next, err := sjson.SetRawBytes(body, fmt.Sprintf("system.%d.cache_control", i), []byte(raw)); err == nil {
			return next
		}
		return body
	}
	return body
}

// xiuHasAssistantMessage messages 里有没有 assistant 回复 —— 有才算多轮会话。
func xiuHasAssistantMessage(body []byte) bool {
	found := false
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		found = msg.Get("role").String() == "assistant"
		return !found
	})
	return found
}
