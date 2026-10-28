# Cocogem 长任务执行技术方案

- 状态：P0/P1 已实现并完成本轮审阅及验收；P2 原生旧 session resume 未实现。实现细节、成功/失败证据与边界见 [实现与验收报告](long-task-implementation.md)。
- 日期：2026-10-27（Asia/Shanghai）。
- 范围：Web 原生 CLI 路径的长任务、运行限制、输出存储、上下文取回、检查点与恢复；不改变原生 `internal/cli/` 行为。
- 本地基线：`feat/ux-overhaul` 当前工作树；HEAD `1218b5c915cd34af4f344be13e9abd4395c6a4e6`，存在大量既有未提交修改，不能把 HEAD 当作完整代码快照。
- 审阅方式：Codex 与本机 Claude Code 2.1.311 实际往返讨论三轮：独立审查、交叉质询、闭环确认，再对最终补充条款做短文确认。Claude Code 复用既有 `deepseek-flash[1m]` 模型及连接配置；CLI 载体不代表后端是 Anthropic Claude。未修改凭据，未操作既有 Claude Code 会话。
- 本文替代原来的 B→C→E 预算草案。它是本次实施依据；产品规格已同步至 `docs/product-v0.1.md`。本轮只完成本地实现与验收，未提交、推送或替换常驻服务。

## 1. 先确定产品定位

Cocogem 是面向本地单用户、使用已有 Claude/Codex/Gemini CLI 的多 Agent 协作工作台。首版场景是资料研究、独立复核、讨论、只读代码分析和结构化产物交付。研究—质疑—汇总、嵌套 handoff 和证据复核本来就可能运行很久。

主要产品目标是：用户能持续观察、取消和恢复合法工作，最终拿到来源可核对、分歧与限制保留的产物。降低平均工具数、运行分钟数或账单不是默认完成标准。Cocogem 目前也不提供任意 shell、修改代码、构建或测试工具，不能把 Multica 的 build/install/test 超时经验直接当成本项目阈值。

三层完成含义必须分开：

| 层次 | 判定 | 边界 |
| --- | --- | --- |
| 执行完成 | CLI 有经过验证的终止信号、进程收敛、持久提交成功、必需步骤完成 | 不能仅凭 EOF/退出码声称成功 |
| 产物契约通过 | 必需 artifact 提交，schema、来源归属、版本和角色规则通过 | 来源存在不等于证据充分，schema 通过不等于结论正确 |
| 内容质量与用户接受 | 核对事实、支持关系、反证、日期/数值/条件及有用性 | 依靠质量评测和人工判断，不能由 `succeeded` 自动推出 |

**默认不因累计运行多久或调用多少工具停止健康任务。** 保留权限、单次 I/O、协议、实际资源故障、可选运营期限和用户取消。长期运行是受支持场景，不是“无限费用、无限存储或保证正确答案”的承诺。

## 2. 主要参考：Clowder 与 Multica

2026-10-27 获取并核对以下固定提交中的关键文件，避免仅引用会变化的 `main`：

- Clowder：`5941b402ed273eb9f3e6e1c0d5ee4cf3839c71dd`。
- Multica：`c11b3375f50682ce497ea0851aeeb10a5bf997a0`。

| 设计事实 | 源码证据 | 对 Cocogem 的决定 |
| --- | --- | --- |
| Clowder `CLI_TIMEOUT_MS=0` 默认关闭无输出超时；开启后真实文本、工具调用/结果、thinking/rich block 重置计时；heartbeat/status/diagnostics 不算；CPU busy 最多给 2 倍窗口 | [环境配置][clowder-env]、[输出超时][clowder-timeout] | 默认不加总时长硬停；区分真正输出与协议噪声，不构建“新 source ID 才算进展”的规则 |
| Clowder 历史输入份额为有效输入容量的 85%，容量先扣除输出预留 | [容量计算][clowder-capacity] | 不把 85% 解读为 85KB 压缩触发点；不声称宿主知道所有 native 模型窗口 |
| Clowder continuation 协调器区分 resume/reborn，并在失败/取消时恢复未消费完的 capsule | [continuation 协调器][clowder-continuation] | 学习持久交接和失败窗口处理，避免另建预算 continuation 状态机 |
| Multica `DefaultAgentTimeout=0`，server 不因运行时间长而结束健康 Run；idle/tool watchdog 默认 2h | [daemon 配置][multica-config]、[任务语义][multica-tasks] | 不用固定总时长作为完成标准；2h 与其 shell/build 场景相关，不直接移植 |
| Multica 正常配置的 Codex semantic inactivity 从 `max(idle, tool)` 派生，默认 2h；10m 是整个 watchdog 套件关闭时的后备值 | [配置派生][multica-config] | 纠正旧稿“Codex 默认静默 10m”的不完整说法 |
| Claude adapter 仅在 `opts.MaxTurns > 0` 时传 `--max-turns` | [Claude adapter][multica-claude] | CLI/provider 可有自身 iteration limit，但不等于平台必须再加 60 次宿主工具预算 |
| 工具次数在已核查路径中用于 replay 安全；context overflow 不在自动 retry 原因中，失败并退役坏 session，下一次触发用 fresh session | [poisoned session][multica-poisoned] | 不将“工具数”当任务质量预算；不声称所有溢出都能自动救回 |

未在这些已核查调度/适配路径发现平台级累计工具调用硬配额。这不是对两个仓库所有功能、插件和部署配置都不存在任何限制的证明。

它们的代码是架构先例与运行经验，不是 Cocogem 的效果实验。论文降为补充材料：More with Less 在 TRAE Agent、三个模型、100 个 SWE-bench 任务上测试了提醒和轮次分配；BATS 支持在显式预算下持续展示预算。二者都没有证明多 Agent research 的 p75 截止能保留证据质量。因此不据此建设 p75/p90 硬预算或动态奖励系统。

## 3. 当前实现与实际瓶颈

| 当前事实 | 代码落点 | 本次处理 |
| --- | --- | --- |
| 正式 server 使用 native runner/fallback；Go Loop 的 12 轮、累积 100KB、4096 output tokens 不治理 native 内部循环 | `cmd/cocogem-server/main.go`；`internal/workflow/engine.go` | 方案以正式 native 路径为主，保留 Go Loop 测试/兼容边界 |
| native 初始 `system + messages` 最多 100000 bytes，工具返回不再累加到这个宿主请求 | `internal/nativecli/provider.go` | 保留宿主初始输入边界，引用化减少重复内联，不伪装成模型窗口 |
| 60 次后拒绝所有宿主工具，61–70 次可返回拒绝；第 71 次强停；`publish_artifact` 也会受阻 | `internal/nativecli/bridge.go` | 删除累计次数拒绝和强停，调用序号继续用于身份关联 |
| `/system` 的默认 `tool_calls` 仍为 30，与 bridge 的 60 不一致 | `internal/httpapi/server.go` | 删除这项限制字段及前端次数承诺，计数变成执行统计 |
| 三个 native parser 有累计 32MiB、65536 events、256KiB visible text 硬停 | `process.go`、`codex.go`、`gemini.go` | 同批处理累计计数与存储增长，不能只删 1h/6h |
| `message.Content` 不断拼接并全量更新，前端累加消息与事件 | `internal/workflow/engine.go`、`internal/store/store.go`、`web/src/app/App.tsx`、`web/src/api/client.ts` | 使用不可变正文块、有界预览和按需分页 |
| 每次捕获都创建新 source UUID；artifact 在步骤成功后才正式发布 | `engine.go` | ID/revision 不用于自动判断进展，不覆盖既有捕获 |
| usage 通常在 native 成功返回后写入 | `provider.go`、`bridge.go` | 缺失保持未知，区分部分已知；不承诺实时金额预算 |
| 单个 FIFO Run worker；Run 内 ready Steps 可并行 | `engine.go` | 保留调度结构，明确排队风险，不用超时模拟公平 |

## 4. 决策与默认值

### 4.1 默认行为

| 项目 | 决定 | 原因与代价 |
| --- | --- | --- |
| Step/Run 总时长 | 默认 0，无绝对期限；保留原环境变量配置正期限 | 避免打断合法长任务；健康或无意义但持续输出的工作可能长期运行 |
| 累计宿主工具数 | 删除 native 聚合上限，不新增 `ToolSafetyLimit` 配置 | 没有本地依据支持某个数字；费用与工具数可能增长 |
| 首个有效响应 | 保留 10m；允许显式 0 关闭 | 启动保护，独立于任务总长，不视为最佳实测阈值 |
| 静默 | 保留 15m、默认 warn；显式 stop 才停止；0 关闭 | 不凭空新增 30m/2h 强停；真挂死可能需要用户操作 |
| 重复工具调用 | 仅诊断提示，不拒绝，不触发自动续时/停止 | 重读、复核与重试读取可能合法；无法自动制止有输出的无意义循环 |
| 单工具与协议边界 | 保留 20s 宿主 I/O、64KiB 观察、4MiB 单帧等 | 限制一次操作及不可信载荷，不机械对齐 CLI MCP 等待值 |
| 上下文 | native 自管窗口；宿主使用确切引用和按需读取 | 避免双重摘要和关键证据静默丢失；需新增受限取回契约 |
| 失败恢复 | 没有自动重试；显式检查点 Resume 创建新 Attempt | 保持费用与副作用可解释，不制造第二套恢复系统 |
| 真 native resume | 后续按 CLI 能力门控 | 不能仅删除 ephemeral 参数就上线 |
| handoff/step/depth | 保留 12/32/4 等委派图保护 | 防止委派扩散；与正常资料工具调用次数是不同边界 |

### 4.2 配置解析

| 配置 | unset / 空字符串 | 显式 `0` / `0s` | 正数 | 负数 / 格式错误 |
| --- | --- | --- | --- | --- |
| `COCOGEM_RUN_TIMEOUT` | 0，无绝对上限 | 同左 | 首次 dispatch 起的 Run 绝对墙钟期限 | 启动失败 |
| `COCOGEM_STEP_TIMEOUT` | 0，无绝对上限 | 同左 | 逻辑 Step 首次 dispatch 起的绝对期限 | 启动失败 |
| `COCOGEM_FIRST_RESPONSE_TIMEOUT` | 10m | 关闭首响应检测 | 每个 Attempt 自真正 dispatch 起的首响应期限 | 启动失败 |
| `COCOGEM_STALL_TIMEOUT` | 15m | 关闭静默检测 | 距最近有效输出/受控工具活动的间隔 | 启动失败 |
| `COCOGEM_STALL_ACTION` | `warn` | 非法字符串 | 仅接受 `warn` 或 `stop` | 启动失败 |

`STALL_ACTION` 只影响 stall，首响应超时是独立停止原因。`stall=0` 时 action 不触发，也不得循环发告警。

默认值只在配置边界应用。first/stall 必须保留“未设置”和“显式 0”的区别，可用局部指针或显式 presence 字段，不能继续在 engine/liveness 内把 0 填回默认值。内部已解析的 0 就表示关闭；直接构造 engine 的测试需明确给出所需策略。

期限为 0 时仍创建独立 `WithCancel` 子 context；不把父 context 加 no-op cancel 当作 Run/Attempt 自己的取消句柄。所有执行继续继承用户取消与服务停机，正期限才加 `WithDeadline`。

### 4.3 正期限跨恢复的语义

1. 入队时冻结 Run execution policy，环境变量变化只影响新 Run。
2. Run 和逻辑 Step 在首次真正 dispatch 时分别持久化 `started_at` / `deadline_at`，写入一次后跨 Attempt、重启和 Resume 不重置。
3. Run 排队不计时；Run dispatch 后的依赖等待、恢复和停机计入其期限。Step 首次 dispatch 前的依赖等待不计，dispatch 后的恢复/停机计入该 Step 期限。
4. 已到期不再启动 CLI；现有 `run_timeout` / `step_timeout` 表达运营期限失败。重新执行需新 Run，不用 Resume 延长。
5. legacy queued Run 没有策略时，首次 dispatch 冻结一次。已启动的旧 Run 若无策略/期限，恢复前记录一次 `legacy_policy_adopted` 与 `adopted_at`，从 adoption 起应用当前配置，明确“旧策略未知、首次采用”，不能假称已继承原期限。之后恢复不再重新领时间。
6. 不改写历史身份、旧 Attempt、source 或 artifact；策略字段和迁移只追加。若配置本来明确为 0，不能因重启后环境改变而突然加限。

## 5. Liveness 只判断活动，不判断研究价值

只需区分 `last_protocol_event` 和 `last_output/activity`，不新建语义进展评分或多层奖励系统。

| 信号 | 首响应 / 静默判断 |
| --- | --- |
| 已校验的新增可见文本、实际新增的受支持 reasoning 元数据、受控 MCP 工具开始/结束 | 可确认有效响应并刷新活动时间 |
| init、thread/turn.started、heartbeat、status、diagnostics、usage、error、空 delta、同一协议 item 的重复内容 | 只作诊断，不刷新有效活动 |
| 合法重复读取同一 source、同参数的独立新工具调用 | 仍是工具活动；可以提示重复，但不冒充“新证据”，也不能直接拒绝 |
| 工具在途 | 按该工具自己的 deadline 管理；不能凭 `active=true` 永久豁免 watchdog |
| 新 source UUID、新 artifact revision、模型声称“正在进展” | 不用于自动授予时间或判断任务语义完成 |

各 adapter 在协议验证和权限验证后才更新活动；不能先看到顶层 `stream_event` / `item.updated` 就算进展。重复帧去重按协议 item/调用身份进行，不能把两个合法的新工具调用当成一帧回放。未有该 CLI fixture 证明的 thinking 增量不当作已支持；不记录或展示模型内部推理内容。

适配器中经过允许列表验证的 tool-call 开始，或宿主实际接受的工具开始，均可置 `firstSeen`；它证明模型已响应。仅 MCP inventory/receipt 不算。`firstSeen` 一旦成立就不再受首次响应硬计时，随后的工具由单工具 deadline 管理；不能让首个合法工具因模型没有先说一句话而被误杀。

工具超时返回明确工具错误供模型处理；工具活跃不能保护无限等待。宿主工具调用，包括来源/交接/取回路径，都要有有限单次执行边界。`warn` 仅显示警告；`stop` 和首响应超时要清理进程组、保存可恢复检查点。旧计时器不得取消后继 Attempt。

10m/15m 是沿用的产品默认，尚无本地分布证明最优；本次不再引入一个没有数据的新默认强停值。

## 6. 长输出、流与持久化：必须同批交付

### 6.1 移除累计限制，保留单次边界

native 的累计 bytes/events/text 改成统计，不再因为超过 32MiB、65536 events、256KiB visible text 杀任务。保留 4MiB 单 NDJSON 帧、工具参数/观察和网页/文件单次捕获上限。未知工具动作、会话身份变化、未确认终止仍失败，CLI 在本机不等于协议内容可信。

有界缓冲和数据库写入提供背压；不丢已确认业务记录，也不把 backpressure 的等待算成新的模型进展。纯噪声通知沿用限量合并/计数，不为每个心跳写一行。

### 6.2 正文存储

新增简单的 `message_chunks(message_id, seq, content)`：

- 对新 chunked 消息，正文按不超过 8KiB 原文、UTF-8 边界切块；按 8KiB 或 100ms flush，终态/中断前 flush 可保存部分。
- `messages.content` 只保留最多 64KiB 原文前缀，新增 `content_format`、`content_bytes`、`content_truncated`；完整性由这些字段和 Message.Status 共同表达，不把预览当全文。
- chunk、预览元数据和对应 `message_delta` 事件同事务提交，确认后才通知 SSE。重放以既有 `(run_id, seq)` 去重，chunk 以 `(message_id, seq)` 唯一，不能重复拼接。
- 旧 inline 消息原样保存，读取端按 inline/chunked 分支兼容；不通过重放旧 events 再制造一份正文。
- 8KiB/64KiB 是分页/缓冲尺度，不限制累计正文大小，也不证明恰好适合所有机器；调整这些尺度不得变成任务总量硬停。

`content_format` 仅取旧记录的缺省/`inline` 与新记录的 `chunks_v1`，不另存含义重复的 chunked 布尔值；`content_truncated` 表示预览未含全文，不表示原正文丢失。正文主记录是 inline Content 或 chunks 二者之一；事件清理不得删除 chunks、sources 或仍为旧 inline 格式的正文。

新增 `GET /api/v1/runs/{run_id}/messages/{message_id}/content?cursor=&limit=`。默认每页 8 块，最大 32 块；归属校验、稳定 cursor、重试不重不漏、旧 inline fallback 都有行为测试。API/OpenAPI、生成类型、fixtures 与 UI 一并更新。

前端默认展示有界预览和已用字节数，按需打开分页全文；不自动把所有页面重新拼成一个无限字符串。SSE handler、ToolTimeline、MessageCard 和 reducer 不能继续永久累加所有正文及事件；完整历史留在服务端，客户端保留有界活动窗口并按 cursor 加载旧记录。更新不重置用户阅读位置和 disclosure 状态。

后端 history/handoff/source 处理也要改成元数据、确切引用和有界分页；不能把完整 source/body 列表全部读入内存后再截前几条。已有来源权限快照和并行隔离规则不变，按 store 范围查询验证引用。

### 6.3 工具结果不是任意 JSON 字符串截断

- `read_file` / `fetch_url` 先保存确切捕获片段，再给模型合法 JSON 的预览、source ID、truncated 与可取回 cursor；展示截短与原始捕获本身已截断分别标记。
- `read_source` 增加可选 cursor / max_bytes，对原捕获版本分页，旧参数仍可用；不传 cursor 返回默认第一页及 `has_more` / `next_cursor`，不能静默装成全文。与 `read_context` 一样，每页原文最多 8KiB，另校验序列化后观察不超过 64KiB；前者是原文字节预算，后者是含 JSON 转义及元数据的协议预算。
- `get_task` 大结果返回结构化的预览和确切 message/artifact refs，完整值通过下一节的 `read_context` 读取。
- `publish_artifact` 接收回执、handoff 任务收据、错误结构等控制信息必须完整，不能截断。仍越过单次协议边界的返回是显式协议/实现错误，不返回损坏 JSON。
- 不删除 source 捕获中的 `Truncated`、文件 SHA/行区间、最终 URL/捕获时间，也不把片段称作完整网页。

磁盘、事务或存储实际失败时停止所属进程并尽力保存可提交的诊断；不能确认未落库正文、宣布产物成功或自动删除旧证据来腾空间。取消无限计数不承诺磁盘无限。

## 7. 上下文和证据取回闭环

### 7.1 保留 100000-byte 宿主初始输入边界

native 初始输入超限返回 `host_input_limit`，与 provider `context_overflow` 分开。它是宿主契约，不是模型 token window；不据此实现 85% LLM 摘要或把宿主校验外包给 provider。

装配保留：系统/当前职责、用户原任务、当前 Step 指令、权限和运行名单、来源与产物的确切版本、依赖状态、必要限制/分歧索引。长上游正文、被引用产物、工具观察用引用按需取回。必要索引本身仍超限时明确拒绝，不静默丢弃用户选择的输入。

native CLI 仍负责其运行中的上下文窗口与原生 compaction；宿主不假装能替换 CLI 内部会话历史。`internal/agent/loop.go` 的现有 12/60/100KB 测试与兼容限制不在本批放开。

### 7.2 同批新增受限 `read_context`

参数为 `kind=message|artifact`、稳定 ID、artifact 的确切 version、可选 cursor / max_bytes。每页原文最多 8KiB，编码后不超过 64KiB。消息返回 preview 或对应原文块；产物返回确切版本的正文/结构化字段，不解析网页或读取当前磁盘文件。artifact 未传 version 时只允许使用 dispatch 白名单中唯一确定的版本；同一 ID 存在多个已选版本时要求显式指定，不能动态取 latest。

这是 workflow-owned 的上下文取回工具，复用受控 MCP 验证方式：

1. 只读取本 Step 原本有权获得的、已提交上游结果和显式引用；白名单在该 Step/Attempt dispatch 时冻结为 ID + 确切版本 + 内容摘要值，不读取时动态推断，不接受模型自报授权。实际调用仍重验撤权，快照不能延长已撤销权限。
2. 不读取并行成员半条输出，不跨空间、会话、Run 或成员扩大范围。此前显式引用能获得的旧版本继续以确切版本读取。
3. 从中断 Attempt 续跑时，自己的历史未提交输出只作为有界恢复预览，返回 `committed=false` 和原 Attempt 身份；不能当作已交付结果或来源证据。完整原始消息仍可由用户从正文接口读取。
4. HTTP、chunked/旧 inline 及工具 cursor 均使用绑定不可变引用/版本、范围和内容位置的不透明 token，不能跨对象复用；是否可读最终仍由权限白名单决定。文件/网页取回继续走 `read_source`。
5. 工具清单、required capability、连接/隔离探测和每次调用的权限重验必须覆盖该工具；若必需的取回能力不被 CLI 暴露，明确 blocked，不能悄悄仅给预览。

**handoff 开始只传预览/引用之前，模型侧完整取回必须可用。** HTTP 全文接口存在不能代替模型工具。存储、UI 和模型取回可以分开发提交，但作为完整 P0 验收后一起启用。

### 7.3 证据保真验收

引用能打开只是第一项，还必须检查：

- 精确 source/artifact/message 版本与正文可逐页还原；源本身截断状态保留。
- 日期、数值、单位、否定条件、适用范围、争议和反证在最终结论中没有被预览误导或遗漏。
- critic/coordinator 能取回预览外关键证据，不因分页阻断正式产物提交。
- 权限撤销和并行快照隔离仍然生效。

不默认生成自由文本后台摘要；后续若引入摘要，摘要是辅助索引，不能替代原捕获或作为来源事实。

## 8. 状态、失败与恢复

### 8.1 状态矩阵

| 情况 | Attempt | 逻辑 Step | Run 与恢复 |
| --- | --- | --- | --- |
| 正常协议终止、必需产物和提交完成 | succeeded | succeeded | 全部必需步骤成功才 succeeded |
| stall action=warn | running | running | 继续，显示诊断 |
| stall action=stop / 首响应超时 | interrupted | interrupted | 无真失败时 interrupted；允许显式 Resume |
| 服务退出/崩溃后确认旧进程已收敛 | interrupted | interrupted / 待恢复 | 保留既有 running 恢复检查点；下次启动按持久认领恢复，归属/结果不确定则 interrupted |
| 已配置 Run/Step 绝对期限到期 | failed，明确 timeout kind | failed | failed，不能用 Resume 扩大额度 |
| P1 可识别 context_overflow | failed(kind=context_overflow) | interrupted | 无真失败时 interrupted；显式引用化 fresh Attempt |
| 鉴权/配额/配置/协议/产物契约/host_input_limit 最终错误 | failed | failed | failed；修复后新 Run，不静默换模型或凭据 |
| 用户取消先提交 | canceled | canceled | canceled；永不自动复活 |
| 旧进程归属/外部结果不确定 | uncertain / blocked | interrupted | interrupted；核实之前禁止重启外部执行 |

Run 使用已有终态 CAS 收敛；不能覆盖已经先提交的终态。仍未终态时，真正失败优先于可恢复中断；没有真失败才因 interrupted 保留检查点。这里“真正失败”按逻辑 Step 的最终 `failed` 状态判断，不能扫描到任意 failed Attempt 就失败整个 Run；`context_overflow` 的 failed Attempt 对应的是 interrupted Step。

请求检查点中断后先停止新 dispatch，取消尚在运行的兄弟 Attempt 并以中断原因记账，等待所属进程组和持久写入收敛，再提交 Run 状态；已成功结果保留。不能先写 Run interrupted 再用后来返回的 true failure 覆盖。进程归属仍不确定时保留 uncertain 诊断、阻止新的外部执行，不假称所有进程已停止；真正失败详情也不能丢。

普通依赖遇到 interrupted 保持 pending，不应立刻写成 skipped；真正失败的依赖仍按既有规则 skipped。free collaboration 已接受的失败回传与 handoff continuation 规则保持，不重新规划内置 workflow。

Attempt failed 与 Run failed 是不同层：例如一次上下文耗尽记录失败事实，但逻辑工作保留 interrupted 检查点。为此新增可机器判断的 `failure_kind`，与中文展示和人类错误描述分开；未知失败不靠宽泛字符串匹配自动当作可恢复。

聚合错误确定性展示：Run 级已配置期限到期优先报告 `run_timeout`；否则只有一个 failed Step 时用其 failure_kind，多个时报告 `multiple_step_failures`，错误摘要按计划 position/Step ID 排序。RunView 中保留全部 Step/Attempt 原因与可恢复性，不能用一个随机先返回的错误覆盖其他失败或 uncertain 诊断。

### 8.2 不做自动重试

本轮明确不实现“transient 一律自动重试一次”。只读模型工具仍会捕获新 source、创建 handoff、暂存产物或产生模型费用；进程退出也不能证明外部调用未发生。现有恢复已经有 Attempt、认领和 handoff 去重，不另建一套 retry/extension。

liveness/上下文中断只由现有显式 Resume 重新校验后开启新的 Attempt。服务重启的既有恢复路径继续处理停机留下的 running 检查点，不扩展为 provider 错误的自动重试；已经持久化为 interrupted 的手动检查点在服务重启后仍为 interrupted，不能被 `recoverActive` 无条件重新排队。必要的旧进程归属清理可以执行，但不因此启动模型。Resume 请求幂等、旧进程组已收敛、权限/技能/模型/连接/程序身份一致是前提。

P1 对 context overflow 先退役该失败 session，基于更小的引用化输入计划起 fresh session；不向相同坏会话重复发相同上下文。不设“整个长任务只准恢复一次”的新累计次数上限，因为没有自动续跑循环；每次均由用户显式发起。若同一检查点在没有新输出/捕获的情况下立即重现同一溢出，阻止机械重播，要求实际修改输入计划或新 Run。

机械重播按以下规则验收：

1. 每个 overflow 恢复 Attempt 保存 `input_plan_digest`：规范化后的系统/职责/原任务/Step 参数、引用 ID + 确切 version + 内容 hash/捕获范围、实际模型和连接身份；排除 Attempt/session ID、时间戳、cursor 和 timeout policy，改时间不能冒充改输入。
2. 另记录该 Attempt 起止的 checkpoint digest，基于实际持久化的非空正文块、确切来源内容/范围和已提交或独立标记的 draft 产物内容；去除协议 token delta、通知、UUID 和纯计数，不从“收到事件”推断进展。该摘要仅防重复恢复，不证明语义质量。
3. 首次显式 fresh 恢复可以使用与原始宿主计划相同的 digest，因为旧 CLI 累积历史已被丢弃。若一次标记为 `context_fresh` 的 Attempt 再溢出，且新计划 digest 不变、其间 checkpoint 内容无增量，拒绝下一次相同恢复为 blocked，保留 interrupted；没有 override 开关，需改变实际输入计划或新 Run。
4. 记录旧 session 退役与新 session 身份；若 CLI 不提供可核实的新身份，不能声称 fresh 恢复成功。事务防止并发 Resume 消费同一检查点；不自动循环，不从 failed Run 开启恢复。

产物成功提交保持 Step 级事务。中断前模型已提交但尚未正式发布的 payload 如要恢复，须以原 Attempt 的 draft 保存，不能从普通文本猜回。已成功的 Step/正式版本不重跑、不覆写；handoff 继续使用 request_id / FromAttemptID，避免恢复后重复委派。

### 8.3 用量与观测

记录已用工具数、累计时长、最近有效活动、来源/产物、失败类型、队列等待和恢复次数。tokens 只记录 CLI 实际报告的值；每个 Attempt 区分 known / unknown，Run 汇总出现缺口时标为 partial。尽力在失败清理路径保留已知 usage，不用 0 填未知。

不保证所有 native CLI 都能报告逐模型轮次，不保证实时 token/美元成本，不引入 Budget Ledger 框架。p75/p90 可在离线报表中观测，不能驱动默认硬停或“新来源换额度”。

## 9. 单 worker 和运营取舍

保留当前 FIFO，一个 Run 内仍可并行 ready Steps。一个持续输出的健康长 Run 可能长期占用唯一 worker；只有 status/heartbeat 的进程在 warn 默认下也可能一直等待用户处理。用户可以取消正在运行或排队的工作，界面必须清楚说明占用和排队。

本方案不承诺排队时延上限，不增加 worker、抢占、Redis、调度 profile 或成本模式。需要并发时作为独立产品特性设计，不靠缩短合法任务期限掩盖问题。已经需要固定运营期限的使用者可设置 Run/Step 正值，UI 显示真实冻结策略。

## 10. 分阶段实施与完整交付边界

| 阶段 | 必须完成的内容 | 完成门槛 |
| --- | --- | --- |
| P0：健康长任务可持续执行 | 默认无总时长/工具数、配置快照与逻辑期限、输出型 liveness 和最小 interrupted 收敛、删除累计流硬停、chunks/分页/模型 read_context 与 read_source 分页、初始引用化、UI/契约/文档同步、最小统计 | 全部读写/取回链同批可用；不能只删限额后宣称完成 |
| P1：可解释的检查点恢复 | 完整 failure_kind、context overflow 的显式 fresh Attempt、draft/交接去重、用量完整性、恢复质量样例 | 不重复正式结果、不复活取消、明确失败与中断；无自动 retry |
| P2：原生 session resume | 按各 CLI 的持久会话、workdir、配置/令牌重注入、坏 session 退役与权限回收能力实现 | 各 adapter 分别有真实隔离与 resume 证据；未证实就不支持 |

实施第一步同步产品规格及 README/.env.example 中相关默认值，再实现相应行为；不得提前把验证项勾选完成。P0 内按依赖分提交，不要求一次巨型提交，但只有完整范围通过检查后才合并启用。

真 native resume 至少要求：平台独占的持久目录与清理策略、会话稳定归属、CLI 程序/解析模型/非秘密连接身份一致、新 MCP token/端口注入、旧能力失效、授权来源/工具变化重新校验、坏历史不再继承。不能通过删除 `--no-session-persistence` / `--ephemeral` 绕过这些条件。继续复用原有本机 key/endpoint/provider/model，不要求重新登录。

## 11. 代码与契约落点

| 文件/模块 | 变更 |
| --- | --- |
| `cmd/cocogem-server/main.go`、`main_test.go` | unset/0/非法值解析、默认值入口、limits 展示 |
| `internal/workflow/engine.go`、`recovery.go`、`context.go` | policy/deadline 快照、独立 cancel、typed 中断收敛、chunk 提交、引用化上下文和显式恢复 |
| `internal/workflow/collaboration.go`、`plan.go` | `read_context`、get_task refs、必需取回能力、draft/幂等边界；不改已接受 workflow 和图安全上限 |
| `internal/nativecli/bridge.go`、`provider.go` | 删除 60/+10；保留工具身份计数、规范观察、初始输入错误、usage 完整性 |
| `internal/nativecli/liveness.go`、`process.go`、`codex.go`、`gemini.go` | 0 语义、活动映射、累计流限额移除、逐帧界限、错误类型、取消和进程组清理 |
| `internal/tools/registry.go`、文件/网页工具 | 结构化预览、来源捕获完整性、分页返回与单次执行边界 |
| `internal/workspace/types.go`、`internal/store` | 追加策略/期限/failure/message 元数据，chunks 迁移与分页查询，兼容旧记录，不重写历史 |
| `internal/httpapi/server.go`、`contracts.go`、对应测试 | 移除 `tool_calls:30`，全文分页、0 值与失败展示契约 |
| `cmd/contract`、`api/openapi.yaml`、`web/src/api/schema.ts` | 通过生成器更新字段/接口与事件说明，不手改生成产物 |
| `web/src/api/client.ts`、`web/src/app/App.tsx`、MessageCard/ToolTimeline/ContextPanel/SettingsDialog、presentation | 有界消息/事件窗口、分页全文、策略与中断原因、未知/部分用量；不重置滚动/展开状态 |
| `docs/product-v0.1.md`、README、`.env.example`、development-progress、fixtures/E2E | 同步真实行为和验收证据 |

本批不放开 `internal/agent/loop.go`，不修改 `internal/cli/` 会话格式，不建设新 scheduler、provider SDK、预算/profile 框架或后台 LLM 摘要服务。

## 12. 行为验收与验证边界

### 12.1 确定性自动化

1. 配置覆盖 unset/空/0/正数/负数/非法 action；first/stall 的显式 0 不被默认覆盖。
2. fake clock 缩时验证超过旧 1h/6h 的有效任务继续；0 下仍可取消全部所属并行步骤和进程。
3. 100 次以上工具调用后仍能 `publish_artifact`；同参数复核只提示；保留 handoff/深度负例。
4. 三个 parser 分别覆盖超过 70000 事件、32MiB 流和 256KiB 可见正文；单帧超限仍明确失败。
5. chunk/分页逐字还原正文，中文 UTF-8 边界正确；终态 flush、断线重放、旧 inline fallback、重复投递和并发 cursor 不重不漏。
6. 浏览器活动状态、事件窗口和单页正文缓冲有界；完整正文可按需读取；用户阅读位置、展开状态不被 SSE 重置。
7. >64KiB 业务观察产生合法结构化分页，控制回执保持完整；100000-byte 初始输入边界和必要索引超限有明确错误。
8. `read_context/read_source` 禁止外部引用、越权、未提交并行结果、旧错误版本和非法 cursor；合法上游能读出预览外反证。
9. init/status/error/usage/空和重复帧不刷新活动；真实新增文本/工具与支持的 reasoning 元数据可以刷新；在途工具受自身期限；首响应不被 init 延长；旧 timer 不取消 successor。
10. warn 不杀；显式 stop/首响应保存 interrupted；failed+interrupted 不被伪装成全成功；兄弟 Attempt/进程和事务收敛前不提交 Run 终态；failed Attempt 不误等于 failed Step；普通 interrupted 依赖保持 pending；free handoff 失败回传回归。
11. 正 Run/Step deadline 跨 Attempt、重启、Resume 不延长；legacy 只 adopt 一次；环境修改不改变已冻结 policy。
12. Resume 请求幂等、不重复 handoff/正式产物、不复活取消、不在未确认的旧进程旁启动；重启只恢复既有 running 检查点，不自动消费手动 interrupted；首次 fresh overflow 恢复允许、相同计划且无内容增量的重复恢复 blocked、改 timeout 不改变 input_plan_digest；已知/未知/partial 用量不混成 0。
13. 成对证据样例包含原文后部的否定条件、日期/价格单位、矛盾来源和证据不足结论；最终报告必须正确使用这些内容，不能只检查引用 ID。

实施检查沿用 `make test`、`make e2e` 和契约生成核对。当前工作树有既有生成文件修改，契约检查要比较本次前后漂移，不能把他人未提交修改当作本次错误或覆盖掉。

### 12.2 真实 CLI / provider 门禁

- 按 Claude/Codex/Gemini 分别记录版本、适配方式、实际模型/连接身份、输出事件、required tool 菜单、终止/溢出字段和隔离/取消结果；已有上游不兼容时如实记录，不要求换凭据或声称成功。
- 在原生循环真实执行 >60 次工具、跨越旧输出阈值、读取预览外来源并完成研究—复核—汇总；用公开/合成资料，捕获引用/反证完整性。
- 真实活动/静默分布用于后续调整 10m/15m；缺少证据时保留 warn，不新增默认强停。
- fake clock/CLI 只证明宿主机制；不证明真实长任务质量、思考期间事件频率或 native resume 可用。
- 先做少量配对场景发现问题，再按质量目标扩大样本；不把 10–20 个样例当稳定 p95 或普遍成功率证据。

## 13. 讨论决议与未验证事项

Claude Code 在最终条款核对中确认：并行收敛、Step/Attempt 失败区分、同计划机械重播防护、首响应计时、分页授权和重启不自动消费手动检查点，已经解决其前轮提出的阻断项。双方同意的是这份架构与实施决议，不是实现完成或真实 provider 已通过。

| 争议 | 最终采用的处理 |
| --- | --- |
| p75/p90 预算和动态延长是否是目标架构 | 不采用；仅允许离线观测，参考项目的执行模型优先 |
| Codex 曾提出默认 2h stop，Claude 第一轮提出 30m stop | 均无本地依据，撤回；保留 15m warn，stop 显式开启 |
| Claude 第一轮提议删 100KB 和放开 Go Loop | 撤回；保留宿主边界，聚焦正式 native 路径 |
| 去掉 256KB 后增长治理可否延后 | 不能；chunks、预览、分页、UI 和模型取回必须同批闭环 |
| 同一工具输入是否自动阻断 | 不阻断；连续重复提示只是诊断，不是质量阈值 |
| transient 自动重试 | 本方案不做；复用已有显式检查点恢复，不新增 retry 系统 |
| 每次 context overflow 恢复是否最多一次 | 不设长任务累计恢复次数硬限；每次由用户显式操作，禁止没有输入变化的机械重播 |
| 原生 session 持久化是否立刻实现 | P2 按能力验证；P0/P1 的 fresh Attempt 不冒充 native resume |

剩余的是验证门禁与部署取舍：真实三 CLI 的活动/溢出协议、长输出和取回工具、证据质量、资源增长、原生 session 安全性尚需实测。单 FIFO 可被健康长任务长期占用、warn 不能自动收敛真挂死、费用和磁盘可能增加，均是明确接受并展示的代价，不用任意次数上限掩盖。

[clowder-env]: https://github.com/zts212653/clowder-ai/blob/5941b402ed273eb9f3e6e1c0d5ee4cf3839c71dd/docs/configuration/environment.md
[clowder-timeout]: https://github.com/zts212653/clowder-ai/blob/5941b402ed273eb9f3e6e1c0d5ee4cf3839c71dd/packages/api/src/domains/cats/services/agents/invocation/member-output-timeout.ts
[clowder-capacity]: https://github.com/zts212653/clowder-ai/blob/5941b402ed273eb9f3e6e1c0d5ee4cf3839c71dd/packages/api/src/config/context-capacity.ts
[clowder-continuation]: https://github.com/zts212653/clowder-ai/blob/5941b402ed273eb9f3e6e1c0d5ee4cf3839c71dd/packages/api/src/domains/cats/services/agents/invocation/SessionContinuationCoordinator.ts
[multica-config]: https://github.com/multica-ai/multica/blob/c11b3375f50682ce497ea0851aeeb10a5bf997a0/server/internal/daemon/config.go
[multica-tasks]: https://github.com/multica-ai/multica/blob/c11b3375f50682ce497ea0851aeeb10a5bf997a0/apps/docs/content/docs/tasks.mdx
[multica-claude]: https://github.com/multica-ai/multica/blob/c11b3375f50682ce497ea0851aeeb10a5bf997a0/server/pkg/agent/claude.go
[multica-poisoned]: https://github.com/multica-ai/multica/blob/c11b3375f50682ce497ea0851aeeb10a5bf997a0/server/internal/daemon/poisoned.go
