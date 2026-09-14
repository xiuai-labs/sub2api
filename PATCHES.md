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
第二条是 xiu 自己要的一块面（上游没有理由长出它），**没有撤销日期**。

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

**没有上游 PR，也不打算提。** 上游的用量接口按窗口设计是对的；要「这号一共烧了多少」的是
xiu-pool 的 sub2api 页，每 30 秒对账一次、一次一整个池子。上游能答这句话的只有
`/admin/usage/stats?account_id=`（或 `/:id/stats?days=90`）—— **一次一个号**，
而且那条 SQL 还要按 endpoint 做四组 GROUPING SETS，缓存又只有 30 秒，
等于每轮每个号全表扫一遍。这条补丁买的是**合批 + 长缓存**。

**「历史」跨得过 usage_logs 的保留期**（`dashboard_aggregation.retention.usage_logs_days`，默认 90 天）：
`usage_logs` 上挂一个 AFTER DELETE 触发器，被删的行在同一事务里按号累加进归档表，只增不减；
读数 = 归档 + 现存行。保留期清理、管理员手动清、删号级联一律接住，上游 Go 代码一行不动。
⚠️ 分区表的清理走 DROP 分区、绕过触发器 —— 迁移发现 `usage_logs` 是分区表就直接报错。

面全在 `xiu_` 基名的新文件里，缓存、并发、口径等决定的「为什么」
写在代码注释里，不在这里复述：

- `backend/migrations/xiu_001_account_cost_totals.sql` —— 归档表与触发器。不带数字编号，
  排在所有上游迁移之后，runner 跳过已应用的，上游以后的迁移照常执行。
- `backend/internal/repository/xiu_account_cost_repo.go` —— 读数，一条语句。聚合列逐行照抄
  上游 `GetAccountWindowStatsBatch`，与触发器同口径；三处对不上时同名 `_test.go` 拦下。
- `backend/internal/service/xiu_account_cost.go` —— `GetXiuTotalCostBatch`。
- `backend/internal/handler/admin/xiu_account_cost_handler.go` ——
  `POST /api/v1/admin/accounts/xiu-total-cost/batch`。

**这条补丁什么时候能撤**：上游哪天出了按号批量、跨保留期的累计用量，`xiu_` 文件整份删掉、
调用方改指过去；触发器、函数与 `xiu_account_cost_totals` 表另写一条迁移删掉（已应用的迁移文件不能改）。
