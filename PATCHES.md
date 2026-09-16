# Patches

This is a fork of [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) for the xiu
deployment. It carries **one** patch.

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

**「历史总消耗」那条 2026-09-16 撤了**：xiu-pool 改拉官方 `GET /admin/accounts/:id/stats` 的日账、
在自己那边按天存（xiu-pool ARCHITECTURE「sub2api 任期」）—— 接进来的第三方服务跑的是官方镜像，
那一口本来就只有我们的镜像有。撤的方式见下面「已撤」一节。

## Patch set

| Files | Why it must live in an upstream file |
|---|---|
| `backend/internal/service/ratelimit_service.go`（**一处 if**）<br>`backend/internal/service/model_rate_limit.go`（**一行**）<br>`backend/internal/handler/gateway_handler.go`（**一行**） | 长上下文缺 credits 的 429 不封整号。逻辑全在 `xiu_long_context.go`；三处分别是 429 分类入口、调度的限流 key、请求进门时估长度，上游都没有钩子。 |

### 长上下文缺 credits 不封整号

**2026-09-16 线上事故**：一个 sonnet-4-6 长会话（>200K）每发一次，多数号回 429
`Usage credits are required for long context requests.`，上游按月末重置点**封整个号**、
轮 10 个号封 10 个。池子缩水后别的用户收到 `would exceed your account's rate limit`。
24h 内 86 个请求打了 891 次上游。

补丁把它改成 `<模型>#long_context` 这个模型级 scope，调度时**只有估算为长请求**才看它；
短请求、别的模型照常用这个号。估算为什么刻意偏低、scope 为什么封顶 5 小时，
写在 `backend/internal/service/xiu_long_context.go` 的注释里。

**可以提上游**（与 Fable 的 credits_required 处理同类，上游已有先例 #6484），合了就撤。
**撤的时候**：删 `xiu_long_context{,_test}.go`，三处挂载点各删掉本补丁那一两行。

## 已撤

### 账号历史总消耗的批量接口（2026-09-16）

`POST /admin/accounts/xiu-total-cost/batch` 与它的 `xiu_account_cost*` 文件、`admin.go` 那一行路由已删。
已应用的迁移文件不能改，所以 `xiu_001_account_cost_totals.sql` 留着，由
`xiu_002_drop_account_cost_totals.sql` 删掉触发器、函数与归档表。
🔴 **归档表里那份「超过保留期的累计」随之消失**：xiu-pool 那边只算拉得到的日账，这是定过的。
