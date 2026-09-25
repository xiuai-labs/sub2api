package service

import (
	"log/slog"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// xiuAnchoredSessionHash 给没有 Claude Code 元数据的 /v1/messages 请求一个**随对话不变**的粘性会话 key。
//
// 上游的两条退路（带 cache_control 的内容哈希、全部消息哈希）每轮都变：第三方客户端把断点打在
// 最后一条消息上，消息也每轮在长。哈希一变粘性会话就重新挑号，缓存留在上一个号上 ——
// 线上同一个 13 万 token 的会话三分钟内在三个号上各整段写了一遍（见 PATCHES.md「会话锚点」）。
//
// 锚点 = 请求上下文（IP / UA / key，与上游第三条退路同一口径）+ system + 第一条 user +
// 第一条 assistant。第一条 assistant 是模型自己的回复，同一客户端模板化的开场白靠它分开会话。
//
// 🔴 只在**有过 assistant 回复**（第二轮起）才接管：第一轮没有 assistant，锚点只剩 system + u1，
// 模板化开场白会把所有新会话撞到一个号上；第一轮沿用上游的算法。代价是第一轮到第二轮之间换一次号、
// 第一轮的缓存写白费 —— 刻意**不**把第一轮的绑定继承过来：粘住的号并发满时请求会排队等
// （`StickySessionWaitTimeout`），继承会把模板化开场白撞出的第一轮共号带进每个会话，
// 一个客户端的几百个会话就能把一个号排成长队。宁可多写一轮小前缀。
//
// 回空 = 不接管，交给上游。
func (s *GatewayService) xiuAnchoredSessionHash(parsed *ParsedRequest) string {
	if s == nil || parsed == nil {
		return ""
	}
	first := xiuFirstTurn(parsed.MessagesRaw())
	if first.user == "" || first.assistant == "" {
		return ""
	}
	// chat-completions 形态的 body 把 system 放在 messages 里（role=system），SystemRaw 为空；两处都收
	system := extractTextFromSystemRaw(parsed.SystemRaw()) + first.system
	anchor := s.hashContent(xiuSessionContextPrefix(parsed.SessionContext) + system + "\x00" + first.user + "\x00" + first.assistant)
	slog.Info("sticky.hash_source", "source", "xiu_anchor", "hash", anchor)
	return anchor
}

// xiuSessionContextPrefix 与上游第三条退路同一口径的上下文区分因子（无上下文时为空）。
func xiuSessionContextPrefix(sc *SessionContext) string {
	if sc == nil {
		return ""
	}
	return sc.ClientIP + ":" + NormalizeSessionUserAgent(sc.UserAgent) + ":" + strconv.FormatInt(sc.APIKeyID, 10) + "|"
}

// xiuFirstTurn_ 第一条 user、第一条 assistant 的内容摘要，以及 messages 里 role=system 的首条正文。
type xiuFirstTurn_ struct{ user, assistant, system string }

// xiuFirstTurn 取第一轮。user / assistant 有文字取文字；没有文字（第一条 assistant 只有 tool_use、
// 第一条 user 只有图片）退回内容块的规范化 JSON —— tool_use 的 id 每个会话唯一，正好当锚点，
// 历史又是客户端原样重放的。任一条不存在回空串。
func xiuFirstTurn(messagesRaw []byte) xiuFirstTurn_ {
	var first xiuFirstTurn_
	messages := parseRawJSONView(messagesRaw)
	if !messages.IsArray() {
		return first
	}
	messages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		content := msg.Get("content")
		digest := extractTextFromContentRaw(content)
		if digest == "" && content.Exists() {
			digest = string(canonicalAnthropicDigestJSON([]byte(content.Raw)))
		}
		switch role {
		case "user":
			if first.user == "" {
				first.user = digest
			}
		case "assistant":
			if first.assistant == "" {
				first.assistant = digest
			}
		case "system":
			if first.system == "" {
				first.system = strings.TrimSpace(digest)
			}
		}
		// user 与 assistant 都到手就停：长会话不必扫到底（system 只会在最前面）
		return first.user == "" || first.assistant == ""
	})
	return first
}
