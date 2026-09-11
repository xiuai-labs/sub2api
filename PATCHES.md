# Patches

This is a fork of [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) for the xiu
deployment. It carries **two** patches.

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
(`release.sh`, `image-pins.env`, this file). That cost is the reason to keep the list
short and to drop each entry as soon as its reason is gone.

**两条补丁的性质不同**，别按同一条标准审：第一条是上游的 bug，挂着 PR，合并即撤；
第二条是 xiu 自己要的一块面（上游没有理由长出它），**没有撤销日期** —— 它缩到一行，
是因为一行就是 gin 路由注册的全部代价。

## Patch set

| Files | Why it must live in an upstream file |
|---|---|
| `backend/internal/service/gateway_claude_oauth_body.go`<br>`backend/internal/service/gateway_forward.go`<br>`backend/internal/service/gateway_count_tokens.go`<br>`backend/internal/service/gateway_system_cache_control_test.go` | 网关不再剥离客户端打在 `system[*].cache_control` 上的缓存断点。删除动作与它的三处调用点都是 `GatewayService` 的方法，Go 的包布局不允许把它们搬进 `xiu/`。 |
| `backend/internal/server/routes/admin.go`（**一行**） | 账号历史总消耗的批量接口。聚合与 HTTP 处理都在 `xiu_` 基名的新文件里，只有 gin 的路由注册没有第二个挂载点。 |

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

### 账号历史总消耗的批量接口

**没有上游 PR，也不打算提。** 这不是上游的缺陷 —— 它的用量接口按窗口设计是对的
（`today-stats/batch` 钉在今天、`/:id/stats` 夹在 90 天内）。要「从头到现在一共烧了多少」
的是 xiu-pool 的 sub2api 页：那一页每一行第一眼要答的就是「这号值不值钱」，
而那是个不带窗口的问题。

面全在两个新文件里，零冲突：

- `backend/internal/service/xiu_account_cost.go` —— `GetXiuTotalCostBatch`。
  **不新写 SQL**，复用上游的 `GetAccountWindowStatsBatch`，只把窗口起点放到 1970。
  金额口径因此与「今日消耗」逐字相同（账号口径 = `SUM(COALESCE(account_stats_cost,
  total_cost) * COALESCE(account_rate_multiplier, 1))`），两个数才比得起来。
- `backend/internal/handler/admin/xiu_account_cost_handler.go` ——
  `POST /api/v1/admin/accounts/xiu-total-cost/batch`。

两个决定值得记下来：

- 🔴 **缓存 TTL 五分钟，比上游那几张（30 秒）长一个量级。** 判据是代价与变速不匹配：
  全历史 `SUM` 要顺着 `idx(account_id, created_at)` 扫到底，比「今日」贵得多；
  而「这号一共烧了多少」半小时不变也改变不了任何决定。调用方每 30 秒对账一次，
  没有这一层等于每 30 秒全表扫一遍。`GetOrLoad` 的 singleflight 顺手挡掉并发穿透。
- 🔴 **`computed_at` 跟着 payload 一起进缓存**，所以缓存命中时回的是当初算出来那一刻，
  不是这次请求的时刻。读它的人拿它当「看到于」画在屏幕上 —— 回 HTTP 往返时刻的话，
  等于让一个五分钟前的数拿此刻的新鲜度背书。

**路径与文件名都带 `xiu` 前缀**：撞不上上游将来的任何路由，rebase 时一眼认得出是谁的。

**这条补丁什么时候能撤**：上游哪天把「起点可给」抽成公开 API，
`xiu_account_cost.go` 整份删掉、路由改指过去即可。
