//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 会话锚点：第二轮起同一会话的哈希不再随消息增长而变（PATCHES.md「会话锚点」）。
func TestXiuAnchoredSessionHash_StableAcrossRounds(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "cline/1.0", APIKeyID: 7}
	round2 := mustParseSessionHashRequest(t, anthropicSessionBody("System", []any{msg("user", "m1"), msg("assistant", "r1"), msg("user", "m2")}, ""), ctx)
	round3 := mustParseSessionHashRequest(t, anthropicSessionBody("System", []any{msg("user", "m1"), msg("assistant", "r1"), msg("user", "m2"), msg("assistant", "r2"), msg("user", "m3")}, ""), ctx)
	rollback := mustParseSessionHashRequest(t, anthropicSessionBody("System", []any{msg("user", "m1"), msg("assistant", "r1"), msg("user", "m2"), msg("assistant", "r2"), msg("user", "different m3")}, ""), ctx)

	h2 := svc.GenerateSessionHash(round2)
	require.NotEmpty(t, h2)
	require.Equal(t, h2, svc.GenerateSessionHash(round3), "第三轮应沿用第二轮的会话")
	require.Equal(t, h2, svc.GenerateSessionHash(rollback), "回滚改写最后一条也还是同一个会话")
}

// 第一轮沿用上游算法：模板化开场白不能把所有新会话撞到一个号上。
func TestXiuAnchoredSessionHash_FirstRoundLeftToUpstream(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "cline/1.0", APIKeyID: 7}
	round1 := mustParseSessionHashRequest(t, anthropicSessionBody("System", []any{msg("user", "m1")}, ""), ctx)
	require.Empty(t, svc.xiuAnchoredSessionHash(round1))
	require.NotEmpty(t, svc.GenerateSessionHash(round1))
}

// 第一条 assistant 回复不同 = 不同会话；上下文（IP / UA / key）不同 = 不同会话。
func TestXiuAnchoredSessionHash_DistinguishesSessions(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "cline/1.0", APIKeyID: 7}
	a := mustParseSessionHashRequest(t, anthropicSessionBody("System", []any{msg("user", "hi"), msg("assistant", "reply A"), msg("user", "m2")}, ""), ctx)
	b := mustParseSessionHashRequest(t, anthropicSessionBody("System", []any{msg("user", "hi"), msg("assistant", "reply B"), msg("user", "m2")}, ""), ctx)
	other := mustParseSessionHashRequest(t, anthropicSessionBody("System", []any{msg("user", "hi"), msg("assistant", "reply A"), msg("user", "m2")}, ""), &SessionContext{ClientIP: "9.9.9.9", UserAgent: "cline/1.0", APIKeyID: 7})
	require.NotEqual(t, svc.GenerateSessionHash(a), svc.GenerateSessionHash(b))
	require.NotEqual(t, svc.GenerateSessionHash(a), svc.GenerateSessionHash(other))
}

// Claude Code 的 metadata session_id 仍是最高优先级，锚点不碰它。
func TestXiuAnchoredSessionHash_MetadataStillWins(t *testing.T) {
	svc := &GatewayService{}
	metadata := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("System", []any{msg("user", "m1"), msg("assistant", "r1"), msg("user", "m2")}, metadata), nil)
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", svc.GenerateSessionHash(parsed))
}

// 第一条 assistant 只有 tool_use：用块的原始 JSON 当锚点，tool_use id 每会话唯一。
func TestXiuAnchoredSessionHash_ToolUseOnlyFirstReply(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "cline/1.0", APIKeyID: 7}
	toolUse := func(id string) any {
		return map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "read", "input": map[string]any{"path": "a"}}}}
	}
	toolResult := map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"}}}
	round2 := mustParseSessionHashRequest(t, anthropicSessionBody("S", []any{msg("user", "go"), toolUse("toolu_1"), toolResult}, ""), ctx)
	round3 := mustParseSessionHashRequest(t, anthropicSessionBody("S", []any{msg("user", "go"), toolUse("toolu_1"), toolResult, msg("assistant", "done"), msg("user", "next")}, ""), ctx)
	other := mustParseSessionHashRequest(t, anthropicSessionBody("S", []any{msg("user", "go"), toolUse("toolu_2"), toolResult}, ""), ctx)
	h := svc.GenerateSessionHash(round2)
	require.NotEmpty(t, svc.xiuAnchoredSessionHash(round2))
	require.Equal(t, h, svc.GenerateSessionHash(round3))
	require.NotEqual(t, h, svc.GenerateSessionHash(other))
}

// chat-completions 形态：system 在 messages 里（role=system），也要进锚点，否则不同 system 的会话会撞在一起。
func TestXiuAnchoredSessionHash_SystemRoleMessageCounts(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "x", APIKeyID: 7}
	a := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("system", "be A"), msg("user", "hi"), msg("assistant", "hello"), msg("user", "m2")}, ""), ctx)
	b := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("system", "be B"), msg("user", "hi"), msg("assistant", "hello"), msg("user", "m2")}, ""), ctx)
	require.NotEqual(t, svc.GenerateSessionHash(a), svc.GenerateSessionHash(b))
}
