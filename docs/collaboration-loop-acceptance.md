# 协作循环验收（v0.1）

日期：2026-10-29。范围：Research 五步、只读工程分析四步、共享历史窗口、原生工具拒绝恢复。本文只使用合成 fixture、临时项目与临时 SQLite；不包含真实用户对话、run ID、数据库、来源正文或凭证。真实 provider 结果只记录脱敏统计与结论。

## 最终行为

### Research：五步

1. `researcher` 研究并记录发现、来源与不确定性。
2. `critic` 独立读取来源并逐项质疑。
3. `researcher` 读取初稿与质疑的确切上游消息，逐项回应、补证或承认证据不足。
4. `critic` 读取初稿、自己的质疑和回应，复查并保留未决项。
5. `coordinator` 读取前四步的确切上游消息，发布本 Run 唯一的 `research_report`。

后两步的 handoff 精确包含其依赖步骤：复查步骤读取前三步，最终步骤读取前四步；每一步都可通过 `read_context` 分页读取确切消息，不依赖内联预览。中间步骤没有发布义务。

### 只读工程分析：四步

1. `analyst` 读取工程文件并给出带文件证据的发现。
2. 复核成员以 `critic` 角色独立读取并逐项确认或质疑；该步骤不发布产物。
3. `analyst` 读取复核的确切回复，逐项只读答辩。
4. 同一复核成员以 `final_reviewer` 角色读取初稿、复核与答辩，逐项核对最初发现、独立重读文件，发布唯一的 `code_analysis`。

`final_reviewer` 的依赖包含最初的 `analyst` 步骤，而不仅是复核与答辩；读取范围随之包含初评。`critic` 是已有角色，成员仍来自 `assignments[reviewer]`；最终步骤仍是同一成员。`RequiredArtifact(reviewer)` 保留 `code_analysis` 仅用于旧两步计划和历史恢复，中间步骤不再使用 `reviewer` 角色，因此不会继承发布义务。分析步骤永远没有 `write_file`、`patch_file` 或 `run_test`。旧的 `code_analysis` 两步计划、历史 Run 与恢复路径保持兼容。

### 共享历史与精确引用

- 每次尝试在派发时冻结读取范围。Shared history 只输出该范围内的已提交消息，并显示完整 `message_id / agent_id / step_id / run_id / status`。
- 最近一轮已完成的记录即使超过历史条数或字节预算，也不会整轮消失：窗口保留真实消息 ID、作者和状态，正文按原预算做有界预览并标注 `preview truncated` 与 `read_context` 分页方式；原任务和最终回复优先保留。更早的整轮记录仍按预算丢弃，不扩大内容预算。
- 三处历史选择共用同一实现：请求组包、尝试读取范围、恢复时重新推导的输入摘要。超预算历史不会让恢复摘要与派发时不一致，也不会把机械重放误判为输入变化。
- 当前 Run 只内联冻结范围内的已完成消息。未提交、失败、其他兄弟步骤或计划根正文不会成为复核引用。
- `handoff_task.context_refs` 只接受完整已提交消息 ID、`产物ID@版本号` 或来源 ID。短 ID、步骤 ID、前缀、失败/处理中消息都会被拒绝；错误回显被拒引用并提示从 Shared history 或 `get_task` 取完整 ID，不猜测、不自动扩权。仅当入选历史窗口包含某轮成功 Run 的权威交付消息时，该轮交付中的确切 `产物ID@版本号` 与类型/标题才会以有界元数据进入请求上下文，可 `read_context field=artifact_json` 分页原文并在交接中传给非作者；未交付、草稿、处理中、旧历史无 delivery 或其他 thread 的版本不列出、不推断，也不存在通用跨 Run 产物目录。复核或质疑只引用被复核的工作，不得包含目标成员自己创作的消息或产物；拒绝错误回显冲突引用，便于改用独立成员或移除该引用。
- 同一 thread 最近最多三个失败、取消或中断 Run 的步骤状态会作为诊断元数据加入后续步骤上下文。字段固定上限、经过安全字符处理与截断，只说明运行状态与真实错误，不是证据；不包含 stderr、原始工具输入或未提交正文。

### 原生工具拒绝的窄恢复

- 系统提示只说明本步骤实际启用的工具，不再声称“未授权调用会终止该步骤”。提示不是权限边界；CLI 明确拒绝且证明未执行时，成员应改用可用工具继续。
- Claude 适配器在以下条件同时成立时，才把一次未开放工具的调用视为“未执行”并允许原 CLI 调用继续：
  1. `init` 库存没有暴露该工具；任何多暴露立即失败。
  2. assistant `tool_use` 不在本步骤 gate 的允许集合内。
  3. call identity 非空、未与已知 native/denied call 重复、未超过三次拒绝上限。
  4. 同一 call ID 随后收到 `is_error=true`，且正文精确匹配 CLI 固定的 `No such tool available: <name>`；Bash/Task 允许其固定 `is disabled for this session, in subagents as well as here.` 后缀。
  5. 不保存或输出原始参数；不发 `tool_started`，只记录一条不含输入的有界错误事件。
- 成功 result、非固定拒绝文本、身份冲突、缺失/重复 call ID、pending 无结果、更多越权库存、超过三次拒绝或重复拒绝结果仍然 fail closed。未解析的拒绝错误保留安全工具名与 call ID，便于诊断。宿主不重启或重放 CLI，也不启用任何新工具。
- Codex/Gemini 没有等价安装版证据，本轮只改进安全工具名诊断，不扩恢复面。

### 不变的流程

- 协作讨论保留 `3N+2` 步与既有草案、复查、最终交付语义。
- 受控开发保留四步闭环、普通复核消息与最终 `engineering_report` 发布。
- 自由协作保留显式多目标并行提交、结果回传与禁止假共识；多目标只受既有名册、交接、步骤和深度预算约束。

## 测试与证据

### 自动化

本轮实际执行并通过：

- `GOFLAGS=-p=1 go test ./... -count=1`（全部 Go 包）。
- `GOFLAGS=-p=1 go test -race ./internal/workflow ./internal/skills ./internal/nativecli -count=1`（本轮改动包）。
- `GOFLAGS=-p=1 go test -race -shuffle=on -tags=integration ./cmd/cocogem -count=1`（CLI 集成）。
- `gofmt` 检查与 `go vet ./...`。
- `GOFLAGS=-p=1 go test ./internal/workflow ./internal/nativecli ./internal/httpapi ./internal/store -count=1`。
- 独立审核的历史窗口反例（五个 4KB 回复的最新 Research 轮）通过 Go overlay 直接对当前代码重跑，结果由失败转为通过。
- 新增行为断言：超预算最新轮的完整 ID/作者/状态与最终回复保留、正文不超过原预算且带截断标记、`read_context` 能分页到原始正文、超预算历史下派发与恢复摘要一致、只读分析最终复核依赖包含初评、未解析拒绝错误保留安全工具名。

此前由独立验收执行并通过、本次修复未改动相关前端与契约文件：

- 完整 `GOFLAGS=-p=1 make test`（Go race、CLI 集成、179 个前端测试）。
- `make check-contract`。
- 7 个 Chrome E2E 场景。
- 真实 Claude + Gemini 短答与八步讨论（约 210 秒）。

### 安装版 CLI 边界证据（本地假模型）

`COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 go test ./internal/nativecli -run '^TestInstalledClaudeDeniedToolBoundary$' -count=1 -v`

实际 Claude 2.1.312，命令参数与 `claudeArgs` 一致（`--restricted --tools Read --allowedTools ... --permission-mode dontAsk --permission-prompts none --setting-sources '' --strict-mcp-config`），本地假模型分别强制请求 `Bash`（写临时 marker）、`Task` 和未知 MCP 工具。CLI 的 `init` 库存只含 `Read`，随后返回固定的 `No such tool available` 错误；解析器记录有界拒绝事件、没有 `tool_started`、没有写入 marker，并在模型随后正常回答时以退出码 0 结束。该证据只证明 CLI 协议与权限边界，不证明真实 provider 或模型质量。

### 真实 native 模型验收（opt-in）

`COCOGEM_CHECK_LIVE_LOOP=1 GOFLAGS=-p=1 go test ./internal/workflow -run 'TestLiveCollaborationLoopAcceptance/(research_five_steps|historical_result_reviewed_by_another_member)' -count=1 -v -timeout 25m`

使用本机既有 Claude CLI 默认模型与连接配置，不改认证、endpoint 或模型；临时合成项目只含 `evidence.md`，临时 SQLite。本轮只运行自然 Research 五步与紧随其后的自然历史复核，不运行只读分析、失败诊断和工具错误子用例。

独立 exact-artifact 验收可单独运行（不依赖新的 Research）：

`COCOGEM_CHECK_LIVE_LOOP=1 GOFLAGS=-p=1 go test ./internal/workflow -run '^TestLiveDeliveredHistoryArtifactReview$' -count=1 -v -timeout 25m`

- Research 题干只包含问题、范围与“保留合成 token”的要求，不写步骤、工具名、MCP 名、消息 ID 或分步指南。验收不再要求固定次数的 `read_context`：完整 handoff 预览可用，只有被截断时才必须分页；同时记录每一步的可见正文与最终产物正文，供人工独立判断第三步是否回应具体质疑、第四步是否复查回应、最终报告是否保留未决项。报告证据断言与 `ValidateArtifact` 一致：`supported` 发现至少引用一个已读原文（`native_file`/`file` 等非 lead 捕获），所有引用必须已捕获，原文与辅助 lead 可并存；仅 lead 的结论仍必须标为 `insufficient_evidence`，否则发布校验失败。
- 历史复核题干只有“请另一位成员独立复核刚才那份研究报告的结论与限制”，不写工具名、UUID 或步骤。该旧用例验收要求出现真实的 `handoff` 复核步骤、至少一个真实的已提交消息引用、复核步骤成功；它不强制历史 artifact ID/版本，因此只能证明“发现了历史消息”，不能单独证明全文报告已被复核。
- 独立 exact-artifact 用例（`TestLiveDeliveredHistoryArtifactReview`，可单独 `-run`）：临时 SQLite 种入一个合成 `succeeded` Run，其 `delivery` 同时包含一条很短的“报告已发布”摘要消息和 `research_report` 的确切版本；报告正文在 6000 字节预览之外含摘要不存在的尾部反例。自然用户句只要求“另一位成员独立复核报告正文而不是只看摘要，并回传关键反例”，不写 MCP 名、角色步骤或 UUID。断言复核成员从请求元数据取得真实 `产物ID@版本号`、以 `read_context field=artifact_json` 读取该确切版本、输出正文独有反例，并由协调者最终交付回传；若只读摘要或换用其他版本即失败。该用例只证明真实模型能接管已提交的合成历史产物，不声称新的联网研究质量。
- 本轮实际结果见下方“本轮验证状态”。真实 provider 失败会按失败记录，不复述为成功。

## 本轮验证状态

验证日期：2026-10-29。环境：本机既有 Claude CLI 与默认模型，临时合成目录与临时 SQLite；未改动认证、endpoint、模型、真实数据库或 CLI 配置。

### 自动化

- `GOFLAGS=-p=1 go test ./internal/workflow -count=1`：通过（37.351s）。包含新增行为用例 `TestDeliveredHistoryArtifactExactVersionIsReadableAndHandedOff`：历史摘要消息与 `research_report@1` 同属权威交付；请求只暴露该确切版本；`read_context field=artifact_json` 可逐页读到 6000 字节预览之外的尾部反例；`report@2`、同 thread 未交付产物、旧历史无 delivery 产物、其他 thread 产物均不可读；交接给复核者后 handoff/冻结范围仍只有 `@1`；派发与 `planDigestFor` 的输入摘要一致。
- `go vet ./internal/workflow`、`go build ./...`、`gofmt`：通过。

Codex 对最终代码的独立检查：完整 `GOFLAGS=-p=1 make test` 通过（含 Go race、随机顺序、原生 CLI 集成、25 个前端测试文件共 179 项）；7 项 Chrome E2E 通过（30.7s）；`make check-contract`、凭证检查、服务端构建和 `git diff --check` 通过。独立编写的三个反例分别验证拒绝调用编号不能复用、超预算历史不会整轮消失、历史发布摘要不能代替确切报告全文，均已从失败转为通过。

作者实施时曾有一次包测试失败，当时输出被截断，未保留具体失败用例。作者后续复测及 Codex 的最终全量检查通过；Codex 另以 `go test -race -shuffle=on -count=3 -timeout=8m ./internal/workflow` 保留完整日志重复三次，全部通过（189.196s），未复现该失败。该次瞬时失败的原因仍未定位，不能把后续通过说成已经查清原因。

其他模式的真实模型复验：Claude + Gemini 的短答与八步讨论通过（210.29s），包括作者纠正草案、最终回复消费纠正；真实 Claude 的四步工程开发通过（134.55s），使用临时 Go 项目、实际 diff 与宿主测试证据。Claude 2.1.312 对 Bash、Task 和未开放 MCP 的固定拒绝恢复由安装版 CLI 配合本地假模型分别验证通过（2.23s）；这三项只属于 CLI 边界证据，不代表真实上游研究质量。

### 真实 native 模型：原自然 Research 与历史复核

- 原自然 Research 五步 Run 状态 `succeeded`，人工逐条核对可见正文确认“研究→质疑→作者修正→复查→报告”确实互相回应，最终报告有原文证据；这一结论仍成立。
- 该轮自动化子测试当时 FAIL 属于断言误报：一条 `supported` finding 同时引用 4 个 `native_file` 原文和 2 个 `Glob` lead，旧断言要求每个引用都是文件来源，比产品 `ValidateArtifact` 更严。已改为与产品一致：`supported` 至少有一个非 lead 原文依据，所有引用必须已捕获，原文与 lead 可并存；仅 lead 的结论仍必须为 `insufficient_evidence`。本轮未再重跑 8 分钟研究。
- 该轮之后的自然历史追问（约 519 秒）状态成功，root→reviewer→continuation 链存在；但复核者明确声明它只读到“报告已发布”摘要与发布消息，未读到 `research_report` 全文。因此该次结果只记为“历史消息可发现”，**不能**记为全文报告复核通过；本轮修复正是补上这个缺口。

### 真实 native 模型：exact delivered artifact（本轮新验收）

`COCOGEM_CHECK_LIVE_LOOP=1 GOFLAGS=-p=1 go test ./internal/workflow -run '^TestLiveDeliveredHistoryArtifactReview$' -count=1 -v -timeout 25m` → PASS（144.63s，`EXIT=0`）。

- 种入的合成历史：`succeeded` Run 的 delivery 包含一条 108 字节“报告已发布”摘要和 `research_report@1`；报告正文 16,499 字节，正文尾部反例 `REPORT_TAIL_COUNTEREXAMPLE_9C3F` 位于所有 6000 字节预览之外。
- 自然用户句不写工具名、角色步骤或 UUID。真实模型根步骤在请求元数据中发现确切 `产物ID@版本号`，交接 refs 为 `[报告ID@1, 摘要消息ID]`。
- 复核成员以 `read_context(field=artifact_json)` 从 offset 0 分页到 16,382 字节并在 `has_more=false` 停止，逐字引用正文独有的 `REPORT_TAIL_COUNTEREXAMPLE_9C3F`；协调者 continuation 将其回传并进入最终交付。
- 该证据证明真实模型能接管已提交的合成历史产物并读取确切交付版本；不声称新的联网研究质量，也不代表旧的自然 519 秒追问已补跑。

## 限制与未决

- 历史窗口中只有“已交付且入选窗口”的确切产物版本可读：不枚举最新版本、不按类型或标题猜测、不回填旧历史无 delivery 的产物，也不暴露未交付、草稿、处理中或其他 thread 的版本。更早的整轮记录仍可能因条数或字节预算被丢弃，跨 Run 检索不在本轮范围。
- 自然语言委派取决于真实模型行为，测试记录可见正文供人工判断，不把关键词命中当作证据消费。
- 原生工具拒绝恢复只适用于 Claude 的固定 `No such tool available`；其他 CLI、其他拒绝文本和真实工具副作用仍需 fail closed。
- `native_file`/`native_web` 仍受既有证据强度规则约束；CLI 观察不等于原始网页或真实供应商验证。
- 诊断元数据不是来源证据，不能支撑 `supported`/`confirmed`，也不会替代 `read_context` 的确切提交内容。
- 旧历史没有交付记录时不回填、不推断；旧两步计划的 reviewer 发布兼容保持不变。
- 本轮不启动/重启真实 dev 服务，不修改用户数据库或 CLI 配置，不 commit/push/merge。
