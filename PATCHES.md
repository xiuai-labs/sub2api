# Patches

This is a fork of [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) for the xiu
deployment. It carries **three** patches.

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
| `backend/internal/service/openai_images.go`（**一处 err 分支**）<br>`backend/internal/service/openai_images_responses.go`（**一处 err 分支**） | 生图的传输层错误（死代理 / DNS / TCP / TLS）走 failover 换号。错误就产生在这两个转发函数体内，上游没有钩子；改动是把手写的 `fmt.Errorf` 换成上游自己的 `handleOpenAIUpstreamTransportError`。 |
| `backend/internal/pkg/claude/constants.go`（**一个常量 + 一条模型**）<br>`backend/internal/pkg/claude/cli_version_test.go`（**测试夹具版本号**）<br>`backend/internal/service/billing_service.go`（**一条兜底价 + 一处 if**）<br>`backend/internal/service/pricing_service.go`（**一条系列 + 一处 case**） | Opus 5.5。版本号是常量本身；模型清单与两张计价表都是上游的有序表 / 分支，没有注册钩子。匹配函数在 `xiu_opus55.go`。 |

### Opus 5.5

**2026-09-23 实测**：渠道 33 打 `claude-opus-5-5` 回 400
`Claude Code 2.1.258 does not support this model; version 2.1.280 or newer is required.`
—— 上游按指纹 UA 设了客户端版本闸门。`CLICurrentVersion` 升到 2.1.280；
存量账号指纹靠上游已有的 `floorClaudeCLIUserAgentVersion` 自动抬到下限。
`SUB2API_CLAUDE_CLI_VERSION` 环境变量治不了：抬指纹用的是常量，不是覆盖值。

计价：`claude-opus-5-5` 字面含 `opus-5`，价格表缺条目时会按 Opus 5（$5/$25）算，
多收 25%、无报错。系列兜底与硬编码兜底各加一条 $4/$20，排在 opus-5 前面。
价格 JSON 与前端清单**刻意没动**：远端价表已有正确条目，前端只是显示。

**撤的时候**：上游收编 opus-5-5 后删 `xiu_opus55{,_test}.go` 与四处改动；
常量若上游已 ≥ 2.1.280 就直接用上游的。

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

### 生图传输层错误走 failover

**2026-09-20 线上事故**：账号 303 绑的 socks 代理（154.53.73.10:45001）约 12:15 挂了。
文本路径（responses / chat / messages 等 20 多处）在 `doOpenAIUpstream` 出错时统一调
`handleOpenAIUpstreamTransportError`，返回 `*UpstreamFailoverError`，handler 静默换号 ——
24h 内 99 次，用户无感。生图的两个转发点（API Key 与 OAuth）返回的却是普通
`fmt.Errorf("upstream request failed: …")`，而生图 handler 的换号循环只认
`*UpstreamFailoverError`，于是 99 次 `socks connect … i/o timeout` 原样回给了用户。

补丁把这两处 err 分支换成同一个 helper 调用，行为与文本路径对齐（含持久故障时临时停调该号）。
代价：Ops 错误事件里少了 `UpstreamURL` 字段（helper 不带）。
回归测试在 `xiu_openai_images_transport_failover_test.go`。

**应该提上游**（纯 bug，上游 main 截至 bbdcfbac0 仍未修），合了就撤。
**撤的时候**：删测试文件，两处 err 分支还原为上游版本，台账与 allowed-touchpoints 各删两行。

## 已撤

### 账号历史总消耗的批量接口（2026-09-16）

`POST /admin/accounts/xiu-total-cost/batch` 与它的 `xiu_account_cost*` 文件、`admin.go` 那一行路由已删。
已应用的迁移文件不能改，所以 `xiu_001_account_cost_totals.sql` 留着，由
`xiu_002_drop_account_cost_totals.sql` 删掉触发器、函数与归档表。
🔴 **归档表里那份「超过保留期的累计」随之消失**：xiu-pool 那边只算拉得到的日账，这是定过的。
