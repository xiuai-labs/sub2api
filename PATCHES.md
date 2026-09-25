# Patches

This is a fork of [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) for the xiu
deployment. It carries **seven** patches.

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
| `backend/internal/service/gateway_forward.go`（**一行**）<br>`backend/internal/service/gateway_count_tokens.go`（**一行**）<br>`backend/internal/service/gateway_anthropic_apikey_passthrough_test.go`（**删两个用例**） | Opus 5.5 的 `thinking.type=enabled/disabled` 改写成上游收的形态，不再被上游 v0.2.8 的入口校验 400。校验就在这两个转发函数体开头，上游没有钩子；上游测试断言 enabled / disabled 必拒，与本补丁相反，只能删那两个用例。逻辑在 `xiu_opus55_thinking.go`。 |
| `backend/internal/service/gateway_service.go`（**一处 if**）<br>`backend/internal/service/generate_session_hash_test.go`（**删四个用例**） | 会话锚点：没有 Claude Code 元数据的 `/v1/messages` 请求第二轮起粘性会话 key 随对话不变，不再每轮换号。挂在 `GenerateSessionHash` 两条退路之前，上游没有钩子；上游四个用例断言「每轮哈希必须不同」，与本补丁相反，只能删。逻辑在 `xiu_session_anchor{,_test}.go`。 |
| `backend/internal/service/gateway_forward.go`（**再加一行**，与 Opus 5.5 那行同文件） | 补断点：一个 `cache_control` 都没有的请求在伪装分支补 Parrot 式四个断点（system 末块 / tools[-1] / 最后一条 / 倒数第二个 user）。位置在上游的 message 断点重写之后、工具名改写之前，上游没有钩子。逻辑在 `xiu_cache_breakpoints{,_test}.go`。 |
| `backend/internal/service/gateway_messages_cache.go`（**一个函数 + 一处回退**）<br>`backend/internal/service/gateway_forward_as_responses.go`（**一行**）<br>`backend/internal/service/gateway_forward_as_chat_completions.go`（**一行**）<br>`backend/internal/service/openai_gateway_responses_anthropic_native.go`（**一行**）<br>`backend/internal/service/openai_gateway_chat_completions_anthropic_native.go`（**一行**）<br>`backend/internal/service/gateway_forward_as_chat_completions_test.go`（**加一个用例**）<br>`backend/internal/service/gateway_responses_cache_breakpoints_test.go`（**新文件，沿用上游文件名**） | 🟢 **上游 PR 原样提前合入。** Responses / Chat Completions → Anthropic 的四条出口给 Claude 模型补「随对话前进」的缓存断点，否则 API Key 号每轮只有 cache_read、新增内容永远不写缓存。是上游 [#7594](https://github.com/Wei-Shaw/sub2api/pull/7594) 两个 commit 的原样 cherry-pick，刻意不改名、不加 `xiu_` 前缀：上游收编后下次 rebase 三方合并会自动消掉。 |
| `backend/internal/service/upstream_response_model.go`（**一个字段 + 两行**）<br>`backend/internal/service/openai_gateway_usage.go`（**一行**）<br>`backend/internal/server/routes/admin.go`（**一行**）<br>`frontend/src/components/admin/usage/UsageTable.vue`（**三行**） | 🟡 **临时补丁，打算丢弃。** `/admin/usage` 的「已降级 / 疑似降智」标记。信号要在上游的流事件观察器与记用量处取，路由与表格也没有扩展点。逻辑在 `xiu_usage_downgrade{,_test}.go`、repository / handler 各一个 `xiu_` 文件、前端 `xiu_DowngradeBadge.vue` 与 `xiu_useUsageDowngrades.ts`。标记存 Redis（TTL 3 天），没有迁移。 |

### 用量记录的降智标记（临时）

**2026-09-24**：照 [codex-state-kit 的降智识别](https://github.com/DouDOU-start/codex-state-kit/blob/master/docs/usage-records.md#降智识别)
（它照的是官方 Codex 客户端 codex-rs）给 OpenAI 路径的用量记录打标，只看上游自己给的信号：

| 信号 | 结论 |
|---|---|
| 响应头 / 事件 `headers` 里的 `openai-model` / `x-openai-model` 与发出去的模型不一致 | 🔴 **已降级**（被改路由到备用模型） |
| `x-codex-safety-buffering-enabled: true`，或事件带 `safety_buffering` 对象 / `response.metadata` 的 safety_buffering | 🟠 疑似降智 |
| `response.metadata` 里的 `openai_verification_recommendation` | 🟠 疑似降智 |
| 只有响应体模型名不一致（带日期快照不算） | 🟠 疑似降智 |

与官方的一处差异：`openai-model` 比较放宽到允许带日期快照，因为池子里有公开 API 的号。
`x-codex-turn-state` 长度、主额度已用百分比只作参考写进依据，不参与判定。

- **只存被标记的请求，存在 Redis，TTL 3 天**：key 为 `xiu:usage_downgrade:{api_key_id}:{request_id}`
  （usage_logs 的幂等键）。刻意不用 PG 表 —— 这是临时功能，表一旦建了就得再写一条迁移去删，永远留在 fork 里。
  Redis 客户端借 `gatewayCache` / `apiKeyCache` 的（repository 的 xiu 文件给它们各加一个 `XiuRedis()`，
  service 侧类型断言取用）。🔴 超过 3 天的用量记录不再有标记。
- 响应头在记用量时从 `OpenAIForwardResult` 读（HTTP 响应头 / WS 握手头）；流事件信号由上游的
  `upstreamResponseModelObserver` 顺手看，按本机 request_id 暂存、记用量时取走。
  🔴 **WS 路径判不出「已降级」**：WS 的观察器不经过 begin，拿不到 request_id，事件里的信号看不到；
  握手头里的 `openai-model` 也不用 —— 连接是池化复用的、客户端还可能中途换模型，握手时的模型不一定是这一轮的。
  WS 上只剩握手头的安全缓冲与响应体模型不一致两条（都只到「疑似」）。
- 前端在表格拿到一页数据后带每行的 `(id, api_key_id, request_id)` 批量查 `POST /admin/usage/xiu-downgrades`，模型列下显示红 / 橙标记，
  点开是判定依据。用户侧「使用记录」页复用同一个表格，那里不查。没做「只看降智」筛选。

**撤的时候**：删 `xiu_usage_downgrade*`（service / repository / handler）、`xiu_DowngradeBadge.vue`、
`xiu_useUsageDowngrades.ts`，四个上游文件里带 `// xiu: 降智标记` 的行删掉，台账与 allowed-touchpoints 同步删。
Redis 里剩下的 key 3 天内自己过期，不用管。

### 会话锚点：没有元数据的会话第二轮起不换号

**2026-09-25 线上**：经 new-api 进来的非 Claude Code 客户端（UA `Go-http-client`，一天 2.4 万请求）缓存读占比 23~34%，
未缓存输入一天 2.1 亿 token、整段重写 4000 次 / 3.1 亿 token。用量序列里同一个 13.5 万 token 的会话三分钟内在
三个号上各整段写了一遍（`in=2 w=13 万 r=0` 连着出现）。机制在 `GenerateSessionHash`：有 Claude Code 元数据的走
session_id（稳定）；没有的退到「带 cache_control 的内容哈希」再退到「全部消息哈希」，第三方客户端把断点打在最后
一条消息上，两种哈希每轮都变 → 粘性会话每轮重新挑号 → 缓存留在上一个号上。上游写了按摘要链前缀匹配的
`FindAnthropicSession` / `SaveAnthropicSession`，但**没有任何调用方**（只有 Gemini handler 接了线）。

补丁：锚点 = 请求上下文（IP / UA / key，与上游第三条退路同口径）+ system + 第一条 user + 第一条 assistant（有文字取文字，只有 tool_use / 图片时取块的规范化 JSON，tool_use id 每会话唯一），
🔴 **只在第二轮起接管**（有 assistant 回复才算）：第一轮锚点只剩 system + u1，模板化开场白会把所有新会话撞到一个号上。
代价：第一轮到第二轮之间换一次号（第一轮的缓存写白费，那一轮本来就小）；客户端压缩历史改了第一轮内容时会换一次号。
🔴 **刻意不把第一轮的绑定继承到锚点 key 上**（2026-09-25 第二轮 review 定的）：粘住的号并发满时请求会排队等
（`StickySessionWaitTimeout`）而不是换号，继承会把模板化开场白撞出的第一轮共号带进每个会话，一个客户端的几百个
并行会话就能把一个号排成长队。宁可每个会话多写一轮小前缀。
日志 `sticky.hash_source source=xiu_anchor`，与上游三条路同一事件名。
Claude Code 的 metadata session_id 仍是最高优先级，不受影响。上游四个用例（`ContinuousConversation_HashChangesWithMessages` /
`MessageRollback` / `SameUserGrowingConversation` / `LongConversation`）断言每轮哈希必须不同，删掉。

**可以提上游**：更对的做法是把上游自己那条摘要链接进 `/v1/messages` handler；本补丁是不动 handler 的最小改法。
**撤的时候**：删 `xiu_session_anchor{,_test}.go`，`GenerateSessionHash` 里那三行删掉，四个上游用例还原。

### 补断点：一个断点都没有的请求

**2026-09-25 线上**：上面那批流量里 47% 的请求一条缓存读写都没有（非流式的 73%），平均输入 3683 token，高于最低可缓存长度。
这类客户端自己不发 `cache_control`，而两个能补断点的开关都关着（`enable_claude_oauth_system_prompt_injection` /
`rewrite_message_cache_control`），请求原样出去，Anthropic 一个字不缓存。#7594 只管 Responses / Chat Completions 入口，管不到这条。

补丁：伪装分支（OAuth 号 + 非 Claude Code 客户端）里，**第二轮起**（messages 里有过 assistant）、整个请求一个 `cache_control` 都没有时补断点：system 最后一个非空
text 块（字符串 system 不碰：messages 上的断点已把它一起缓存，升成数组会撞上游「原样透传」的用例）、messages 最后一条与倒数第二个 user
（tools[-1] 不在补丁里补：挂载点后面上游两个分支都会无条件打，合计四个正好到上限）
（上游 `addMessageCacheBreakpoints`）。🔴 「客户端自己管断点」只看 messages / tools 上有没有：system 上的可能是上游 system 注入带来的（那个开关一开，注入块自带 cache_control），按它判补丁会整个失效；只标了 system 的请求照补对话断点，system 不再动。总数 ≤ 4 由后面已有的 `enforceCacheControlLimit` 兜底。
🔴 **必须在「会话锚点」之后上**：不修换号只补断点，每轮换号写得更多，账单反而更贵。
🔴 **单发请求不补**（2026-09-25 第二轮 review 定的）：缓存写按 1.25 倍计费，分类 / 抽取一类的单发请求写了永远没人读，
补断点等于白多付 25%，而线上这批流量里非流式的大头正是它们；多轮会话第二轮写、第三轮起读，只损失第一轮那一段的复用。
代价：断点带 `ttl:"5m"`，第三方 Anthropic 兼容中转若拒收 `ttl` 会从「不缓存」变成 400 —— 我们的号全是官方 OAuth，不受影响。

**可以提上游**（与 #7594 同类：「客户端不带断点时网关补」）。
**撤的时候**：删 `xiu_cache_breakpoints{,_test}.go`，`gateway_forward.go` 里那三行删掉。

### Responses / Chat Completions 出口的缓存断点（上游 #7594 提前合入）

**2026-09-25**：Codex（`/v1/responses`）与 Chat Completions 客户端本身不带 `cache_control`，
Responses→Anthropic 转换链此前只在 OAuth mimicry 分支打 message 断点，API Key 的 Anthropic 号
只剩上游中转打在 tools 上的固定断点：首轮写一次固定前缀，之后每轮只有 cache_read，新增长的对话
永远不写缓存（PR 作者线上 15 个请求：第 1 个 `cache_creation=45891`，后 14 个全是 `cache_read=45891`、零写入）。

上游 [#7594](https://github.com/Wei-Shaw/sub2api/pull/7594)（`ee0c1d40b` + `119d544ab`，截至合入时 open、CI 全绿、`mergeable: clean`）
的做法：复用上游自己的 `stripMessageCacheControl` + `addMessageCacheBreakpoints`，每个请求在最后一条 message
与倒数第二个 user message 上重打断点（与 Parrot 一致），再走已有的 `enforceCacheControlLimit` 限 4 个；
只对模型名含 `claude` 的注入（DeepSeek / Kimi / GLM 的 Anthropic 兼容端点不动）；thinking / redacted_thinking
不能带 `cache_control`，向前回退到上一个块。四条出口：Anthropic 平台的 `ForwardAsResponses` / `ForwardAsChatCompletions`，
OpenAI 平台的 `forwardResponsesViaNativeAnthropic` / `forwardChatCompletionsViaNativeAnthropic`。

审查时看到的三点，都不阻塞，记在这里备查：

- Anthropic 平台的两条出口在打断点前**没有**调 `StripEmptyTextBlocks`（OpenAI 平台那两条有）。转换器对 user 侧会跳过空 text，
  只有「assistant 消息无内容」会造出 `{"type":"text","text":""}` 占位块；断点落在最后一条与倒数第二个 user 上，
  几乎碰不到 assistant 占位块。OAuth mimicry 分支早就用同一个 helper 跑了几个月，同样的形状。
- OAuth 号走这两条协议时，message 断点现在**不再受**「重写 message cache_control」开关约束（mimicry 之后无条件重打）。
  这两类客户端本来就不带断点，开关的本意是「要不要覆盖 Claude Code 自己打的断点」，所以无条件是对的。
- 注入的断点带 `ttl:"5m"`（`claude.DefaultCacheControlTTL`）。API Key 号此前在这两条路径上一个断点都没有，
  第三方中转若拒收 `ttl` 字段会从「不缓存」变成 400。🟡 上线后看一眼 Anthropic API Key 号在 responses / chat 路径的错误率。

上线后怎么核（目前唯一的「监控」，人肉）：usage_logs 没有 endpoint 列，用 `user_agent` 认 Codex；
按账号 × 天看「有缓存写入的请求数 / 请求数」。修复前的形态是每天每号只有 1 个请求有写入、其余全 0；
修复后每轮都应写一点（read 逐轮增长，write 是本轮新增的那几千）。在 DEPLOY_HOST 上进 `sub2api-postgres` 容器的 psql
（命令形态见 xiu-router `deploy/scripts/backup.sh` 顶部注释）：

```sql
SELECT a.id, a.name, a.type, date_trunc('day', u.created_at) AS day,
       count(*)                                              AS reqs,
       count(*) FILTER (WHERE u.cache_creation_tokens > 0)   AS reqs_with_write,
       sum(u.cache_creation_tokens)                          AS cache_write,
       sum(u.cache_read_tokens)                              AS cache_read,
       sum(u.input_tokens)                                   AS input_uncached
FROM usage_logs u JOIN accounts a ON a.id = u.account_id
WHERE u.created_at > now() - interval '3 days'
  AND a.platform = 'anthropic' AND u.user_agent ILIKE 'codex%'
GROUP BY 1, 2, 3, 4 ORDER BY day DESC, reqs DESC;
```

**撤的时候**：上游收编后 rebase 到新 tag，三方合并两边内容一致会自动消掉；若上游合入前改了内容而冲突，
七个文件全部 `git checkout <新 tag> -- <file>`，台账这一行/这一节与 allowed-touchpoints 的七行同步删。

### Opus 5.5 thinking=enabled/disabled 改写

**2026-09-23 升 v0.2.8 当晚**：上游 `c4c2e6607` 在 `/v1/messages` 与 count_tokens 入口加了
`validateClaudeOpus55Request`，`thinking.type` 为 `enabled` 或 `disabled` 一律 400
`claude-opus-5-5 requires adaptive thinking…`。v0.2.7 时 enabled 原样发给 Anthropic 是成功的
（升级前日志里 Anthropic 只拒 disabled，没有一条拒 enabled）；切换后 crs-max（oidc_46）的
Opus 5.5 请求约四分之一被网关拒掉。

补丁在校验之前改写，两种都放行：

- **enabled** → adaptive，删 `budget_tokens`。
- **disabled** → 删掉整个 `thinking`（Anthropic 自己的报错原文就是 "omit thinking"），
  客户端没给 `output_config.effort` 时补 `low`。同晚 20 点起 oidc_112 的 Claude Code 会话里
  部分请求带 disabled，一小时 100 多次 400；升级前这类请求同样被 Anthropic 400
  （`"thinking.type.disabled" is not supported for this model`），所以这一半不是回归，是顺手治。
  🔴 代价：省略 thinking 等于默认 adaptive，客户端本想不思考，现在按 low 档思考，输出 token 会多一点。

走 OpenAI 协议转换的四条路径不用管：apicompat 转换时已经给 Opus 5.5 写死 adaptive。
回归测试在 `xiu_opus55_thinking_test.go`（含「挂载点在校验之前」的入口测试）。

**enabled 那半可以提上游**（与 bedrock_request.go 对 Opus 4.7+ 的 enabled→adaptive 一致），合了就撤那半。
**撤的时候**：删 `xiu_opus55_thinking{,_test}.go`，两处挂载行删掉，上游测试那两个用例还原。disabled 那半上游多半不会收（它是有意拒的），真要撤就先确认客户端不再发 disabled。

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

**应该提上游**（纯 bug，上游截至 v0.2.8 仍未修），合了就撤。
**撤的时候**：删测试文件，两处 err 分支还原为上游版本，台账与 allowed-touchpoints 各删两行。

## 已撤

### Opus 5.5（2026-09-23，基线 v0.2.8）

上游 v0.2.8 收编：`c4c2e6607` 加了模型条目与 $4/$20 计价（`claude.IsOpus55`，排在 opus-5 前面），
`ca0882593` 把 CLI 伪装版本号改成运行期值 —— 每小时从 anthropics/claude-code 的 GitHub Releases
同步最新稳定版（只进不退），指纹下限也跟着运行期值走。补丁的四处改动与 `xiu_opus55{,_test}.go` 全删。

🔴 **内置基线仍是 2.1.258**，低于 Opus 5.5 要求的 2.1.280。同步拉不到 GitHub 时会退回基线、
Opus 5.5 回 400。兜底不用打补丁：管理后台「设置」里手动固定 `claude_code_client_version`
（优先级最高），或设 `SUB2API_CLAUDE_CLI_VERSION`。

### 账号历史总消耗的批量接口（2026-09-16）

`POST /admin/accounts/xiu-total-cost/batch` 与它的 `xiu_account_cost*` 文件、`admin.go` 那一行路由已删。
已应用的迁移文件不能改，所以 `xiu_001_account_cost_totals.sql` 留着，由
`xiu_002_drop_account_cost_totals.sql` 删掉触发器、函数与归档表。
🔴 **归档表里那份「超过保留期的累计」随之消失**：xiu-pool 那边只算拉得到的日账，这是定过的。
