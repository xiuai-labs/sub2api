# Patches

This is a fork of [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) for the xiu
deployment. It carries **one** patch, and it exists only until upstream merges it.

## The rule

> **Edit an upstream file only where upstream offers no other attachment point.
> Anything that can live outside sub2api stays outside sub2api.**

Every upstream file we touch is a conflict we pay for on every rebase. Files we add under
`xiu/` (or with a `xiu_` basename) cost nothing, forever. The gate is
`xiu/check-touchpoints.sh`, run by `release.sh` before any image is built — it reads
`xiu/allowed-touchpoints.txt` in both directions, so an undeclared edit and a
declared-but-unmodified entry both fail.

This fork ran at **zero patches** from 2026-09-06 to 2026-09-10, tracking the official
image. The moment that stopped being true, the build chain had to be wired up
(`release.sh`, `image-pins.env`, this file). That cost is the reason to keep the list at
one entry and to drop it as soon as upstream lands the fix.

## Patch set

| Files | Why it must live in an upstream file |
|---|---|
| `backend/internal/service/gateway_claude_oauth_body.go`<br>`backend/internal/service/gateway_forward.go`<br>`backend/internal/service/gateway_count_tokens.go`<br>`backend/internal/service/gateway_system_cache_control_test.go` | 网关不再剥离客户端打在 `system[*].cache_control` 上的缓存断点。删除动作与它的三处调用点都是 `GatewayService` 的方法，Go 的包布局不允许把它们搬进 `xiu/`。 |

### 不再剥离客户端 system 上的 cache_control

**上游 PR：[Wei-Shaw/sub2api#6943](https://github.com/Wei-Shaw/sub2api/pull/6943)。合并后撤掉这条补丁、退回官方镜像。**

OAuth mimic 路径无条件删除 `system[*].cache_control`，不打日志也不报错。把稳定前缀锚在
system 上的客户端因此拿到 `cache_creation` 与 `cache_read` **双 0** —— 不是没命中，
是连写都没写。

这个动作是上游 46e5ac9 引进时「system 必然被整个重写」的配套：客户端的 system 内容都
搬进 messages 了，残留断点指着空气。后来 system 注入变成可配置，就长出了原设计里不存在
的组合 —— system 没被重写，断点照删。剥离失去了前提，代码却还在。

补丁是净删除（移除 `stripSystemCacheControl` 选项及其三处调用点），外加一处补齐：
`ForwardCountTokens` 是五条出口里唯一没调过 `enforceCacheControlLimit` 的，原先那句
strip 就是它唯一的断点数压制，拿掉后实测顶到 5 块，而上游对超限是 400。

同类问题在 messages 层是上游 [#2369](https://github.com/Wei-Shaw/sub2api/issues/2369)，
已收进 `rewrite_message_cache_control` 开关；那次只落地了 messages 一半，system 与 tools
两条留在原地。
