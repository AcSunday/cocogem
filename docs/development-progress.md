# v0.1 implementation progress

This file records verified feature commits and remaining release gates.
The product specification remains the acceptance source of truth.

## Frontend and runtime cleanup on 2026-10-20

- Implemented the warm minimal light appearance and VS Code style dark appearance,
  with system preference, saved appearance, collapsible context panels and inline
  artifact version cards. Drafts and expanded configuration survive stream updates.
- Removed the offline runtime, fixed sources/replies, special routing, launch flags
  and public `demo` contract fields. Production execution uses installed native CLIs;
  test collaborators are linked only by automated test binaries.
- Local frontend checks cover 18 behavior tests; Chrome browser checks cover theme
  restoration before React loads, both appearances at desktop/mobile widths, source
  capture, artifacts, cancellation and history. Real provider calls were not rerun.

| Feature | Status | Validation |
| --- | --- | --- |
| Existing CLI baseline | Preserved | `go test ./...` on original Go 1.22 |
| M0 engineering and contracts | Implemented | Go 1.26.9, public module, policies, generated OpenAPI/types, actual response conformance and drift checks |
| Persistent domain and records | Implemented | Naming negative cases; SQLite rollback, active-run uniqueness, terminal immutability, reopen and permissions; race tests and vet |
| M1 single-agent vertical flow | Implemented; live provider gate pending | Local CLI runtime, scoped MCP tools, persistent HTTP/SSE and authentication; historical Anthropic protocol tests are not web execution evidence |
| M2 names, routing and collaboration | Implemented | Renaming preserves identity; longest normalized names; semantic quote validation; finite parallel/serial workflows |
| M3 research, code analysis and artifacts | Implemented; live service gate pending | All three browser flows, real file tools, HTTP fixtures, captured sources, immutable versions, feedback and Markdown export |
| M4 recovery, security and interaction | Implemented | Cancellation, lost-response idempotency, long history, replay, restart, queue policy revalidation, commit-failure injection, backup/restore; WCAG automatic checks |
| M5 release candidate | Built; publication blocked | Clean checkout reproduced; macOS arm64 and Linux amd64 candidate smoke; notices/checksums; Go 1.26.9 security gate passed locally; live provider and remote CI still pending |

## Product alignment correction on 2026-10-19

The records below describe checks at the stated dates. Their offline demo
runtime was subsequently removed. Current browser tests use isolated test-only
fixtures; HTTP smoke checks production startup, workspace persistence and
backup/restore without model calls. Neither establishes live provider readiness.

- Web execution, routing and verification now use local CLI sessions instead
  of requiring Anthropic API credentials. Members choose the executor/model.
- Fixed conversation member/default-workflow scope, form revision handling,
  negated/contradictory routing, exact artifact handoffs, recent complete
  history, queued tool revocation and historical identity labels.
- Native CLI protocol/MCP tests and Go race tests pass. Frontend now has 13
  behavior tests; Chrome fixture E2E and real HTTP/backup smoke passed at that revision.
- The first real page probe failed because restricted launching omitted the
  existing native API connection configuration. After forwarding allowlisted
  connection fields, real page probes for Claude/Codex/Gemini passed. A real
  three-member Claude Research also completed with distinct sessions, captured
  sources, two handoffs and a report retaining critique. A separate
  Codex/Gemini/Codex Research, natural-language single-member dispatch and
  actual Codex cancellation also passed through the page.
  See [the alignment review](alignment-review.md) for findings and evidence.

## Toolchain update on 2026-10-18

- Upgraded both Go modules to require Go 1.26.9; CI covers 1.26 and stable.
- `go mod verify`, `make test` and `make security` passed on the new toolchain.
- The scanner reports no reachable vulnerabilities; required-module-only
  findings remain documented in [the scan record](security-scan.md).

## Directory cleanup on 2026-10-18

- Moved the native CLI into `cmd/cocogem` and `internal/cli`, preserving its
  arguments, output, exit codes and saved-session format. Added process tests
  for Claude/Codex/Gemini invocation, resume and failure without overwriting
  the last successful session. `make build` now builds both commands.
- Grouped frontend entry, API, generated schema, components/forms, styles and
  test setup. Updated generation, lint, test and documentation paths together.
- `make test`, `make check-contract`, `make security` and
  `PLAYWRIGHT_CHANNEL=chrome make e2e` passed after the move.
- A token comparison confirmed that the moved React function bodies were
  unchanged; generated schema and stylesheet contents also remain unchanged.
- The new CLI source command is `go run ./cmd/cocogem`; `go run .` no longer
  refers to a package. See [the directory guide](project-layout.md).

## Verified locally on 2026-10-16

- `make test`: Go vet/race/shuffle and frontend lint/types/tests, plus development-process ownership and license tests.
- `make check-contract`: generated OpenAPI/frontend types do not drift.
- `PLAYWRIGHT_CHANNEL=chrome make e2e`: real Go service, SQLite and native browser; all scenarios, sources, artifacts, retry/cancel, refresh, mobile navigation and axe WCAG checks.
- `go run ./cmd/smoke`: real process lifecycle, Research, captured evidence, export and stopped-database backup/restore.
- A separate clean local clone repeated setup, checks and browser tests without untracked project files.
- macOS arm64 archive executed with the same smoke. Linux amd64 archive executed inside an isolated, read-only, network-disabled Docker container on the local Linux arm64 engine; this is emulated amd64 execution, not a performance result.
- Linux container additionally passed `go test -race ./...` using the locally available Go 1.25.13 image. Application binaries were built with Go 1.25.14.
- Frontend production dependency audit: zero findings. Common-secret scan: passed.

## Remaining release gates

1. Real three-member Claude and Codex/Gemini Research examples are verified, as are natural-language dispatch, cancellation and all three CLI probes. The all-three-CLI-in-one-run attempt encountered upstream 502 errors; broader task quality and online discovery still need independent validation.
2. This correction has not been verified by GitHub-hosted CI. No tag or GitHub Release was created during this work.

All feature changes were committed separately after their relevant tests. Local
build artifacts are in ignored `dist/`; do not treat their existence as release
approval; live-provider validation and GitHub-hosted CI and broader real-task quality evaluation remain incomplete.

## 2026-10-20 本地密码与选择框

本地鉴权改为首次初始化设置密码、后续密码解锁，移除启动令牌链接；密码派生值持久保存，保留会话与 CSRF 校验。全站下拉框复用 Select 浮层组件与现有双主题。

验证：`make test`、`make build`、正式服务 HTTP/备份恢复 smoke、OpenAPI/前端类型重复生成无漂移，以及使用本机 Chrome 的 3 项 Playwright 测试均通过。包含密码初始化/错误重试/锁定与重入、方向键选项、深浅主题菜单、390px 窄屏与 Axe 无障碍检查。未调用真实模型。

## 2026-10-20 Codex 0.163 兼容

本机 Codex 已升级为 0.163.0，旧适配版本限制仍为 0.162.x，导致页面把新版本误报为缺少隔离参数。现支持 0.162.x 与 0.163.x，并分别返回“版本尚未验证”“能力探测失败”或缺失的具体参数；未验证版本继续阻止执行。

本机 0.163.0 的 `TestInstalledCodexConfiguredConnection` 与 `TestInstalledCodexToolRegistry` 已通过（含 race）：本地合成连接配置、默认模型、Bearer/Header、MCP 工具注册表、工具调用及模型续轮可用。该检查使用临时 CODEX_HOME 与本地假服务，不使用用户密钥，不代表本轮已验证真实供应商。

## 2026-10-21 输入区交互

Enter 发送、Shift+Enter 换行；中文输入法确认、@ 成员补全、连续按键与忙碌状态单独处理。自由协作去掉自动指派控件；流程分工与资料以浮层展开，停止按钮仅显示图标并保留无障碍名称。移除资料外发复选框、接口字段及路由/调度拦截，历史空间不需补确认。

验证：`make build`、`make test`（限制并发）、正式服务 HTTP/备份恢复 smoke、OpenAPI/前端类型重复生成无漂移，以及本机 Chrome 的 3 项 Playwright 测试通过。前端共 33 项测试，新增覆盖输入法、回车发送、换行、@ 补全、浮层关闭与停止图标；1125/605/390px 验证展开资料时工具栏坐标不变、没有横向溢出，历史未确认空间可直接执行。未调用真实模型，未提交或发布。


## 2026-10-24：全局成员、共享技能与异步 MCP 协作

已实现全局成员档案与空间授权分离、原成员 ID 保留迁移、动态名册、技能按需读取、有限异步交接、嵌套/并行结果回传和协作时间线。技能唯一源为 `internal/skills/library`，正文不绑定具体成员、CLI 或模型；空间可选择启用项，Run 保存内容版本。配置与边界见 [协作说明](collaboration.md)。

本地验证：`make test` 通过（含 Go race/shuffle、原生 CLI 集成和 44 项前端测试）；`make build`、生成契约重复运行无漂移、正式服务 HTTP/停止/备份恢复 smoke、5 项 Chrome 浏览器测试通过。浏览器测试包括真正页面→HTTP→SQLite→动态调度流程，模型使用隔离 fixture；另有明确标记的 UI 契约场景，不能冒充真实模型。

真实模型验收：Claude 协调者 → Codex 研究者 → 另一位 Claude 成员独立复核 → 协调者汇总，103.08 秒完成。5 个步骤成功、2 次持久交接、2 次后续调用；3 个成员实际通过 MCP 读取技能，复核引用确切已提交消息且作者不同，SSE 顺序连续，产出 1 份含任务引用的 note。任务只使用合成公开加法资料。存在被服务端拒绝后修正的工具请求，不宣称零重试或通用选人质量已证明。

Gemini 0.64.0 安装/隔离能力探测通过，但真实模型调用未通过：原有上游返回 HTTP 400，要求 OpenAI 兼容协议，拒绝 Gemini 原生协议。已提供脱敏的协议不匹配诊断；未改凭证、未要求登录、未将三个 CLI 的真实调用全部标为通过。真实证据与命令见 [验收记录](evidence/mcp-collaboration-20261024.json)。

安全：扫描发现 Go 1.26.9 的 GO-2026-6809，已在既定 1.26 线内升级到 1.26.10；最终 `make security` 通过，0 个可达漏洞、0 个受影响的已导入包，npm 生产依赖审计 0 项；扫描仍提示所需模块中 5 个未被当前构建调用的漏洞，不作全依赖无漏洞声明。

当前本地 18742 预览已使用新构建，迁移前保留私有 SQLite 备份，迁移后空间、会话、运行和消息数量一致。历史 CLI 配置和密码文件保留；服务重启后的旧 Web 会话失效，需要用既有密码重新进入。未提交或发布到远端，远端 CI 与其他操作系统发布验收未执行。


## 2026-10-24：修改协调者时保留当前会话

修复保存会话设置后误走导航清理逻辑、同会话 ID 不触发重载而使页面内容清空的问题。编辑已有会话现在只更新会话元数据；新建会话仍正常导航。回归覆盖选择协调者不提前提交、保存使用 PATCH 原会话、不新增会话或改 URL、原消息与 Run 一致、草稿/资料展开和阅读位置保留。Codex 安装信息与成员表单中的普通说明统一使用“说明”，不再标为“使用限制”。

验证：前端构建、类型检查、Lint、45 项单元测试及实际 Go 测试服务下 Chrome 工作空间流程通过。当前 18742 前端已更新，不重启服务、不更改 CLI 或会话数据；本次未调用真实模型。

## 2026-10-25：工作空间直接设置本机目录

修复目录下拉框仅列出启动参数、默认启动无法绑定本机目录的问题。创建和编辑工作空间均可输入完整路径，支持 `~/`，保存真实绝对路径；不设置 `--allow-root` 时由已解锁的本机用户选择，设置后仅能绑定允许目录及其子目录。服务端拒绝不存在、不可读、非目录、已知敏感路径及越界软链接，运行和工具调用重新检查绑定；协作运行期间不能改绑。文件工具仍只读，沿用原有范围和捕获限制。

保存失败保留表单内容，编辑已有工作空间保留当前会话、消息、草稿和资料展开状态。修复滚动表单后延迟滚动事件关闭新打开的下拉菜单；菜单跟随控件定位，键盘切换选项只滚动菜单。

验证：`make test`（Go vet、race/shuffle、原生 CLI 协议集成、Lint、类型检查及 48 项前端测试）、`make build`、6 项 Chrome 浏览器测试、正式服务 HTTP/停止/备份恢复 smoke、密钥扫描和差异格式检查通过。目录回归覆盖中文/空格路径、主目录展开、规范化、权限、普通文件和命名管道拒绝、显式范围/前缀近似路径/软链接越界、绑定被替换、持久化和真实临时文件读取。浏览器模型使用隔离夹具，本次未调用真实模型，未验证其他操作系统。

当前 18742 预览已备份 SQLite 并重启至新构建，原密码文件未变，工作空间、会话、Run 和消息数量一致；刷新后以原密码解锁。未提交或发布到远端。

## 2026-10-25：可视化选择工作空间目录

创建和编辑工作空间新增“浏览目录”，在原弹窗中逐层进入文件夹、返回上级、跳转主目录或允许目录列表，再选择当前目录。取消保留表单原值；选择只填入路径，保存工作空间才更新绑定。保留手动路径输入，支持浅色、深色和窄屏布局。

新增受本机会话、Host 和 Origin 校验保护的目录浏览 API，复用目录校验与显式允许范围。仅返回可访问子目录的名称和路径，过滤文件、隐藏项、已知敏感目录、软链接及越界路径；最多扫描 1000 条目，截断时提示使用路径直达。API/OpenAPI 和前端类型同步，重复生成无漂移；未新增依赖或操作系统选择器进程。

验证：`make test` 通过（Go vet、race/shuffle、原生 CLI 协议集成、Lint、类型检查、52 项前端测试）；`make build`、6 项 Chrome 流程、密钥扫描、正式服务 HTTP/备份恢复 smoke 和差异格式检查通过。浏览器覆盖逐层进入中文/空格路径、上级范围、空目录、取消保留、选择不提前写入、保存后刷新，以及明暗主题和 390px 无横向溢出。后端覆盖认证、来源校验、权限、敏感路径/文件/软链接过滤、越界拒绝和截断；前端覆盖加载错误、过期响应、Enter 不提前提交与焦点返回。

本地 18742 预览已用原数据目录启动新构建；启动前备份 SQLite，密码文件及历史数量一致，未更改实际目录绑定。本次未调用真实模型、未验证其他操作系统、未提交或发布到远端。

## 2026-10-26：长任务、重启恢复与 CLI 升级兼容

- 执行时限与停滞检测分离：默认 Run 6 小时、Step 1 小时、等待首个有效响应 10 分钟、无进展告警 15 分钟；可用 `COCOGEM_RUN_TIMEOUT`、`COCOGEM_STEP_TIMEOUT`、`COCOGEM_FIRST_RESPONSE_TIMEOUT`、`COCOGEM_STALL_TIMEOUT` 和 `COCOGEM_STALL_ACTION=stop` 调整。工具活动、协议事件和工具结果都算进展；等待上游成员显示为独立状态，不消耗运行中步骤的期限。
- 逻辑 Step 与 Attempt 分离。Attempt 保存执行器、模型、适配方式、CLI 版本与程序身份（绝对路径、大小、修改时间、inode）、平台拥有的原生会话 ID/模式、进程归属（PID、进程组、启动时间）和终态；历史消息、来源、产物和已完成步骤不被覆盖。
- 调度和恢复使用按 Run 的持久认领，记录服务 PID 与启动时间；活进程的认领不会被抢占，死亡或 PID 复用时通过比较交换接管。正常停止保留 `running` 检查点；异常退出后先核实旧进程归属，只终止本平台记录且身份匹配的进程，再从未完成步骤继续。已完成步骤不重跑，崩溃窗口不会重复发布产物或汇总。无法核实的进程或结果保留 `interrupted` 并阻止自动重跑；取消的 Run 不会自动复活。页面对可恢复中断提供显式“继续执行”。
- CLI 兼容改为按实际能力判断：版本仅用于诊断和已知不兼容基线；每次执行和恢复前重新探测受限参数、程序身份、连接配置、资料范围和成员授权，运行时验证会话初始化、MCP 工具清单握手、必需工具、终止事件和会话身份。新增可选非关键通知记录为 `native_notice`；未知工具动作、缺少终态和身份变化失败关闭。CLI 不兼容时保留检查点并显示具体原因，不切换供应商或模型，也不放宽工具权限。模型连接验证绑定当时的程序身份，CLI 被替换后旧验证不再显示为有效。
- 迁移只新增 `attempts` 和 `run_claims` 表，不重写成员 ID、历史消息、来源、产物或既有任务。旧版本遗留的 `running` 步骤若没有执行尝试归属记录，升级后保留为 `interrupted` 检查点并要求显式继续，避免在无法核实外部调用时自动重跑。
- 验证：`make test`（Go vet、race/shuffle、原生 CLI 集成、Lint、类型检查、53 项前端测试）、`go test -race -shuffle=on ./...`、`make build`、OpenAPI/前端类型重复生成稳定。新增后端覆盖长任务默认值/超时原因、正常停止与重启续跑、取消不复活、并发恢复只调度一次、崩溃窗口不重复、并行部分完成、残留进程核实与终止、进程身份不匹配、能力阻塞与显式恢复、CLI 版本/身份变化、依赖等待、必需工具预检、可选通知和未知工具动作。OpenAPI 新增 `Attempt`、`RunView.attempts`、执行尝试状态、恢复字段、`POST /api/v1/runs/{id}/resume` 和 CLI 隔离/协议字段。
- 无需模型的本机 CLI 探测（`make probe-cli`）通过：Claude 2.1.310、Codex 0.164.0、Gemini 0.65.0 均对本地假模型端点完成会话初始化、受限 MCP 工具清单、必需 `read_file` 预检、工具调用和续轮/终态；未使用账户凭证，也不代表真实模型质量。
- 真实 Claude 重启验证通过：生产服务使用隔离合成文本资料执行代码分析，在首个分析成员已启动并记录进程/会话后 SIGKILL 服务；重启时确认旧进程仍存活，按 PID+启动时间核实并终止，创建新 Attempt 从已保存上下文继续，随后独立复核并发布 `code_analysis`。Run `succeeded`、恢复 1 次，旧 Attempt 保留为 `interrupted`。本次重启点在首个步骤，尚无已完成步骤；已完成步骤/产物不重复由自动化崩溃窗口测试单独覆盖。证据见 [restart-recovery-20261026.json](evidence/restart-recovery-20261026.json)。
- 未执行：Codex/Gemini 的真实模型重启；实际包管理器升级期间的 CLI 替换（仅由自动化身份变化测试覆盖）；未来所有版本兼容性；GitHub CI、Linux 实机运行和其他操作系统。真实 Claude 验证只使用合成公开资料，不代表通用任务质量。
- 剩余限制：进程回收以记录的 PID、启动时间和进程组为准；自行创建新会话/进程组的后代不在该组内，当前隔离适配会阻止原生工具，但该边界仍应作为后续审计项。原生会话是临时隔离会话，新 Attempt 明确从持久化上下文继续，不声称恢复旧会话。停滞默认只告警，`COCOGEM_STALL_ACTION=stop` 才终止；Run/Step 总时限始终有效。

## 2026-10-26：审查问题修复

审查报告的 5 项 P1、2 项 P2 已修复，并将反例纳入回归测试：

- **连接变化**：Attempt 保存解析后的实际模型和非秘密连接身份（供应商、endpoint、协议、认证方式/环境键名和程序身份；不保存密钥）。恢复、继续和重试前重新解析并比较；默认模型、供应商或服务地址变化时保留检查点并明确阻塞，旧 Attempt 没有连接身份时要求新 Run。
- **失败回传**：恢复不再因为存在 failed/skipped 步骤就提前结束 Run；只要还有 pending continuation，就交回现有依赖调度规则，让协调者接收委派失败。普通依赖仍按既有规则跳过。
- **进程组清理**：`proc.Terminate` 现在核对整个记录的 PGID，等待成员退出，必要时升级 SIGKILL；恢复在任一组内成员仍存活时保留 uncertain，不启动新尝试。删除会话也会拒绝仍有存活归属进程组的记录。
- **隔离证据**：`Check` 只报告安装/参数候选（`Isolation=candidate`），不再从 `--help` 声称已隔离。首次执行/恢复前，生产路径用真实已安装 CLI 对本地假模型和受限 MCP 运行无凭证探测，验证会话、必需工具清单、工具调用、可见输出和终态；失败则阻止并显示具体原因。探测结果按程序身份缓存，失败 30 秒后可重试；页面在探测前显示“待验证”。
- **未知动作**：未知事件的类型和结构会检查 tool/command/shell/exec/permission/approval/sandbox/patch/function_call 等动作字段；未知动作失败关闭，可确认的非关键通知继续记录为 `native_notice`。
- **停滞误判**：Claude 的 system/status/idle 通知只更新诊断字段，不重置进展；不会压住停滞告警或 `STALL_ACTION=stop`。
- **旧等待状态**：前端根据后续工具完成、消息、步骤/尝试/恢复进展清除旧 liveness 警告；依赖等待仍单独显示。
- **产物重试**：最终步骤允许用新的合法内容替换尚未提交的暂存产物，仍然只提交一个最终版本；校验错误明确列出允许的 findings 状态。真实 Claude 复核曾在错误信息后反复重试并触发事件预算，本修复消除了该循环。

验证：

- `make test` 通过（Go vet、race/shuffle、原生 CLI integration、Lint、类型检查、54 项前端测试）；`go test -race -shuffle=on ./...` 通过。
- 审查新增反例在仓库回归测试中全部通过，包括失败回传恢复、连接身份变化、进程组子进程、未知工具动作、Claude 状态通知、产物替换和前端旧等待状态清除。
- `make probe-isolation` 通过：Claude 2.1.310、Codex 0.164.0、Gemini 0.65.0 的真实二进制对本地假模型完成受限 MCP/必需工具/工具调用/终态探测；`make probe-cli` 仍通过。
- 真实崩溃/重启复测：Claude 2.1.310 与 Codex 0.164.0 均通过。两者都在首个步骤持久化进程/会话后 SIGKILL，重启核实并终止整个旧进程组，第 1 次 Attempt `interrupted`（`owned_process_reclaimed`），第 2 次从上下文成功，复核成功并产生产物；Attempt 持久化了实际解析模型和非秘密连接身份，重启后匹配，未切换供应商。Codex 的一次重试在审查脚本的 90 秒首响应窗口内只收到 `turn.started` 后超时；未判为版本不支持，使用 5 分钟窗口重试后完整通过（产品默认首响应时限为 10 分钟，可配置）。Gemini 0.65.0 到达检查点并恢复计数 1，但第二次真实调用仍因现有上游连接/native API 协议不兼容而失败，未改凭证、未要求登录、未归为版本不支持、未声称成功。证据见 [restart-recovery-20261026.json](evidence/restart-recovery-20261026.json)。
- 未执行运行中真实包管理器升级、Linux 实机、远端 CI 或未来版本兼容性承诺；所有真实重启点均在首个步骤完成前，完成后/汇总前不重复由自动化崩溃窗口测试覆盖。


## 2026-10-27：长任务预算重审、完整输出和显式恢复

按 Clowder / Multica 的固定提交重新确认产品定位，完成 [长任务技术方案](long-task-budget-review.md) 的 P0/P1。由本机 Claude Code 实现，Codex 对照实施前工作树快照独立 review，提供反例测试并多轮反馈修复；不改原生 CLI 行为，不新增依赖，不更改用户的 key/endpoint/provider/model。

- Run/Step 默认总期限为 0；native 不再按累计 60 次工具、32MiB、65536 events 或 256KiB 可见文本强停。仍保留单次 I/O、协议/权限与委派图边界，首响应 10m、静默 15m 默认 warn，显式 0 关闭对应检查。
- 入队冻结策略；已知 deadline 跨 Attempt、重启和 Resume 保留。旧策略未知且没有已知 deadline 的历史任务从一次性 adoption 起应用当前策略，保留历史开始时间。
- 长正文按 UTF-8 块持久化，预览与前端事件窗口有界；HTTP/消息/事件历史分页与模型 read_context/read_source 全部接通。结构化 findings/limitations/source_ids 可以通过 artifact_json 分页完整取回，交接不再全量内联。
- liveness 不把 init/status/usage/空白/重复协议帧当进展；告警持久化并按 Step 清除。usage 保留 known/partial/unknown，已知 subtotal 不因缺口丢失。
- 真实错误优先于可恢复中断，保留所有失败详情；取消回调 join、进程组子进程收敛、uncertain 阻止重新执行。上下文溢出显式创建 fresh Attempt，无自动 provider retry，不冒充原生旧 session resume。
- draft 接受后立即落在原 Attempt，恢复时必须重新核实并提交；UUID/版本/重复 capture 不算新 checkpoint 内容，防止同计划无增量的反复溢出重播。
- 真实验收发现 Research 工具 schema 允许后端拒绝的 confirmed，已统一可见 schema 与 validator 并提供可纠正反馈；失败样例保留在证据中。

最终验证：`make test` 通过（Go vet/race/shuffle、原生 CLI integration、前端 lint/typecheck、17 文件 112 测试）；新构建后 Chrome 7 项 E2E、正式服务 HTTP/停止/备份恢复 smoke、契约重复生成与凭证扫描通过。Claude 2.1.311 / Codex 0.164.0 / Gemini 0.65.0 的安装工具清单及本地合成端点隔离探测通过，不能替代真实 provider 证明。

真实长任务：Claude Code 复用现有 deepseek-flash[1M]，167.34 秒完成研究/复核/汇总 3 步，研究者 71 次宿主工具调用、65 个文件窗口捕获；critic 与 coordinator 读到预览外尾部证据，最终 report 一次提交成功。Codex 逐段核对否定条件、日期、两组价格/单位、冲突与证据不足均保留，同时明确捕获次数不等于模型逐字阅读，全文覆盖性争议被保留。混合 Claude/Codex 协作同轮复验 91.81 秒通过，5 步、2 handoff、2 次协调者后续调用、3 位成员实际读取技能。

Gemini 真实调用仍受现有上游协议不匹配阻塞，未改凭据或把合成探测记为真实成功。P2 原生旧 session resume、Linux 实机与多日运行验收未执行；保留单 FIFO worker，默认不限累计时长/次数会增加费用、存储与排队等待。详见 [实现与验收报告](long-task-implementation.md) 与 [脱敏证据](evidence/long-task-20261027.json)。原有未提交修改和真实数据库均保留；本轮未 commit/push、未替换当前常驻服务。


## 2026-10-28：在线研究无反馈排查与真实页面复验

按用户要求由本机 Claude Code 排查和实现，Codex 对照本轮开始前的工作树快照独立复核，提供反例并多轮反馈。真实用户数据库只读定位；所有新调用、服务重建和浏览器验证使用隔离数据目录，保留既有未提交改动与 CLI 连接设置。

- 原始会话确实已调度并执行：约 137 秒内有 11 次搜索、34 次抓取，没有 assistant 正文，随后收到取消事件；未触发 Run/Step 或 liveness 超时。抓取记录包含 HTTP 403/404/451、DNS、连接与超时错误；5 个空正文页面被错误地记作成功。用户后续补充的 `INFO tool_finished ... tool_call_id=native-45 tool=fetch_url status=error` 已与持久记录精确对应：抓取 Gate 的 DeBank 文章时，DNS 返回含非公网/保留地址的结果，15ms 内被公网地址校验拒绝（`DNS resolved to a nonpublic address`）；约 0.58 秒后收到取消事件。INFO 是日志等级，error 是单次工具结果，不代表服务崩溃，也不是该工具错误自动终止了任务。
- 空白文本和仅有脚本的页面现在返回“无可读正文”的工具错误，不再保存为成功来源；错误回传模型且仍可继续研究。
- 主会话卡片展示最近工具、查询/URL、执行状态和失败摘要。工具开始/结束按 Step 与 Attempt 边界关联，覆盖并行复用 native ID 和分页缺开始事件；结束的空回复显示“本次回复没有正文”，不再显示生成中占位。
- 真实复验另发现部分搜索页被标成“已读原文”，结构化 supported 结论实际只有搜索摘要。常见搜索引擎结果页新增 `search_results` 来源类别，旧记录按 URL 辅助标注；正文仍可读取用于发现线索。新产物的 supported/confirmed 结论不能仅用搜索摘要或这些结果页支撑；工具提示与可纠正错误说明可读取原文或标为 `insufficient_evidence`。不迁移或改写旧来源和已发布产物。此检查只约束来源类型，不证明引用相关性或事实正确性。

独立验证：`make test` 通过（Go vet/race/shuffle、原生 CLI integration、前端 lint/typecheck、118 项单测）；开发服务生命周期检查实际执行，未因端口占用跳过；新构建的 7 项 Chrome E2E、正式 HTTP/停止/备份恢复 smoke、契约重复生成稳定和凭证扫描通过。

真实调用复用 Claude Code 2.1.311 的既有连接。第一轮仅含反馈/空正文修复时，原问题用 519.325 秒自然完成，175 次工具调用，最晚才返回正文；复核暴露搜索页证据缺陷。最终构建在全新隔离会话再次发送相同问题，190.502 秒自然完成，1.810 秒出现首条可见文本，41 次工具调用，产生一份 note；5 条结构化结论中 1 条 supported、4 条 insufficient_evidence、7 条 limitations，所有来源 ID 可回查，无空正文成功捕获。页面确认阶段性说明、来源标签、最终答复和产物可见；本轮浏览器无新增 warning/error。薪资与雇主评价未取得可靠原文，最终明确保留缺口；不能把任务运行成功等同于研究资料齐全。两轮调用不是受控性能对比，不承诺稳定提速。脱敏统计见 [web-research-20261028.json](evidence/web-research-20261028.json)。

未增加任务总时长/累计工具次数硬限制，未改模型、供应商、凭据或网络安全边界。无密钥搜索对部分查询仍返回重复/无关结果，登录墙、验证码、JavaScript 与外部网络故障仍限制覆盖；未新增浏览器抓取或登录绕过。未 commit/push 或发布。


## 2026-10-28：网页访问路径与系统代理根因修复

用户补充终端错误后，继续由本机 Claude Code 排查实现、Codex 独立复核和真实验收。根因是开发服务与浏览器的网络路径不同：macOS 已启用本地 HTTP/HTTPS/SOCKS 代理，开发后端却未取得对应环境变量而直连。直连 DNS 返回公网 A 与 Teredo 保留 AAAA，旧实现遇到任一非公网地址就拒绝整组结果；经现有代理的加密 DNS 对照返回不同的 Akamai A，且无最终 AAAA。未确定异常答案来自路由器、上游 DNS 还是链路干扰，不归因具体应用；未改系统 DNS、代理、VPN 或 CLI 连接设置。

- `make dev` 在 `COCOGEM_WEB_PROXY` 未设置时给后端传入 `system`；macOS 启动时只读系统已配置代理的主机/端口，3 秒有界，优先 HTTPS、HTTP、SOCKS。不读取 PAC、订阅或代理凭据。显式 URL、`env` 和显式空值优先；发布服务无配置时仍直连，非 macOS 当前没有系统代理发现实现。
- 系统代理读取失败显式报错，不静默切回直连；读取成功且无代理才走直连。`env` 在没有代理变量时不再把字面量当地址。
- 直连只拨经 `PublicIP` 校验后的数字地址；混合结果丢弃不安全地址，全不安全时仍拒绝。保留 TLS、URL、私网字面量和逐次重定向检查，无网站白名单。显式代理由代理解析和连接远端目标；应用无法在本地核实远端实际 IP，此信任边界已同步到产品规格和架构文档。
- Codex 复核后要求补充真正的拨号目标测试，而非仅检查取消错误；覆盖不安全地址在前/后、全不安全、解析失败、空结果、只解析一次及仅在已校验公网 IP 间重试。代理优先级测试通过本地代理实际收包验证。

真实验收使用新构建、既有 Claude Code 2.1.311 连接与隔离测试数据。为保留用户当时运行的开发服务，浏览器验收仅在临时入口覆盖监听端口与 Vite 转发地址，其余开发脚本及代理选择逻辑保持一致；未手工传入代理地址。只观察本轮后端进程，捕获到 7 条连接现有系统代理的 TCP 记录。官方文档与原失败 URL 的验收任务 95.808 秒完成，取得 12 个官方文档捕获（其中 API 子页可读），原 Gate URL 明确 HTTP 403；完全相同的原问题在全新会话 230.949 秒完成、54 次工具调用，实际读到 8 篇 V2EX 招聘原帖。两轮均没有 `DNS resolved to a nonpublic address` 错误，各发布一个 note 且所有来源 ID 可回查。招聘帖日期为 2021–2024 年，不能证明 2026 年报价；模型报告不等于逐条事实审计。脱敏统计见 [web-network-20261028.json](evidence/web-network-20261028.json)。

验证：`GOFLAGS=-p=1 make test` 全量通过（Go vet/race/shuffle、原生 CLI integration、118 项前端测试），开发服务生命周期检查在最终检查时实际执行且通过；新构建 7 项 Chrome E2E、正式 HTTP/停止/备份恢复 smoke、契约重复生成稳定、密钥扫描和差异格式检查通过。默认并发全量曾两次在既有 5 秒 CLI 能力探测边界失败；CC 固定 seed、单用例/单包与并发对照表明可在并发时偶发复现，单包正常，但未定位具体内核原因。最终通过降低 Go 包测试并发完成验证，没有修改产品探测超时或把初始失败抹去。

剩余限制：Gate 仍 403、DuckDuckGo 仍可能返回挑战页，Bing RSS 对部分查询仍有无关结果；复用代理恢复连通性不保证所有站点可读或搜索覆盖完整。未新增浏览器抓取、验证码绕过、PAC 或 Linux 实机验收，未写用户真实数据库，未 commit/push 或发布。临时服务和标签页验收后关闭；保留用户已有源码修改。

## 2026-10-28：原生只读工具优先、MCP 补充

按现有成员 grant 自动派生原生只读工具，不新增开关：Claude `Read`/`Glob`/`Grep`
及发现范围下的 `WebSearch`/`WebFetch`；Codex 在两项联网授权同时存在时启用合并 web 工具并声明 Responses web 能力；Gemini 因 stream-json 不暴露
实际工具结果正文而保持 MCP 联网。`given_sources` 与空 scope 不启用原生联网。
原生与平台 MCP 并存，实际失败仍可回退；路由、连接验证和隔离探测清空原生字段。

Claude 的配置目录与可读 cwd 分离：配置 0700/文件 0600；复测发现
`permissions.deny` 只对 cwd 生效、不覆盖 `--add-dir`，故原生文件步骤以工作区根
为 cwd，并禁用 `CLAUDE.md` 自动加载；纯联网步骤仍用空 cwd。`permissions.deny`
对齐敏感路径并额外拒绝配置目录；工作区根不安全时降级为 MCP 文件工具。解析器只接受实际 call/result 配对，
成功结果按 `native_file`/`native_web`/`native_search` 保存，失败原样可见且不造证据；
调用 ID 使用 `cli:` 前缀。新增平台工具 `list_sources` 返回本步可见来源的有界
元数据/ID，正文仍通过 `read_source` 读取。

验证（本轮）：`GOFLAGS=-p=1 make test` 通过（node 脚本、gofmt、go vet、
`go test -race -shuffle=on ./...`、integration tag、前端 lint/typecheck、120 项
前端测试）；另运行 opt-in `COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1`，真实 Claude
2.1.312 对本地假模型完成公共文件读取，拒绝 `.env`、`.git/config`、工作区外和
符号链接越界，且未加载工作区/父目录 `CLAUDE.md`。真实证据与能力缺口见
[对齐审查](alignment-review.md)。未修改用户 provider/model/key/endpoint，
未关闭 TLS，未声称三端真实 provider 全部验收。

## 2026-10-28：独立复审反例修正

Codex 对照本轮工作树运行独立 overlay 反例测试，9 项问题中第 1 项已由物理路径
解析修复；其余按最小改动处理，未新增模式、确认入口或权限平台：

- 空白原生输出不再成为来源；只有带 CLI `file` 元数据证明存在的空 `Read`
  才区别于空白网页保存。
- 原生联网摘要声明改为固定整句去重，不再被含“原生”二字的无关限制跳过。
- `list_sources` 按实际 JSON 字节（≤64 KiB）和 20 条上限动态分页，cursor
  记录实际结束 offset；后续条目可完整取回，不靠吞错误丢弃。
- 旧版 `kind=web` 的搜索结果页复用 `IsSearchResultURL` 判为 lead。
- 撤销成员工具、停用成员/全局档案、解绑成员后，`Engine.CancelMember` 复用
  既有 Run 取消机制停止正在运行的 native CLI；取消后的原生观察不保存、不发布。
  普通改名不取消。HTTP 行为测试覆盖三种撤销路径和改名反例。
- Claude `Read` 保留 `tool_use_result.file` 的 `filePath`/`numLines`/
  `startLine`/`totalLines`，按 CLI 自身范围与宿主截断标记片段；缺少完整性
  元数据时标记截断，不承诺全文，也不由宿主重读文件补证据。
- 原生联网子进程合并既有 `NO_PROXY` 与 `no_proxy`，保留 loopback 直连。
- Codex 正式 catalog 说明改为“仅使用本步实际提供的工具”，并验证正式
  `prepareCodex` 产物；Codex `open_page` 仍只作 `native_search` 线索。
- 第二轮反例后补充：`CancelMember` 内部使用有界 `WithoutCancel`，权限已提交
  后即使浏览器断开仍会停止 native 进程；原生 `tool_finished` 事件只保留有界
  坐标元数据，`file.content` 仅作为有界来源正文保存（取 CLI 实际返回，不重读
  文件）；`list_sources` 每页只转换最多 20 条候选，再按字节收缩。
- 第三轮反例（凭据隔离）：Claude/Codex/Gemini 的运行期文件统一放入本轮临时
  目录的 `.cocogem` 子目录，Claude 原生 deny 与宿主 MCP `tools.DeniedPath`
  同时拒绝该组件；资料根覆盖临时目录（如 `/`）时 MCP 文件后备也读不到私有
  MCP/token 配置，根内降级保留为第二层防护。原生 `file.content` 字段存在时
  直接采用实际返回正文（含更短或空内容），不以字符串长度猜测完整性。

本轮检查：两轮独立反例、受影响包 race、`make probe-cli` 通过；第三轮
`independent-overlay.json` 的 nativecli/workflow 全部反例通过，目标包测试与
opt-in `TestInstalledClaudeNativeReadBoundary` 通过；`GOFLAGS=-p=1 make test`
首次全量暴露 HTTP fixture 的旧临时目录断言，修正后重跑通过。此前的真实双成员
138.42 秒记录只是初步证据，不作为最终版本通过。

## 2026-10-28：Codex 配对遗漏修正与最终验收记录

Codex 独立复审发现 `parseCodex` 声明了 `webPending` 却从未写入/删除，
`item.started` 的 `web_search` 后直接 `turn.completed` 仍被接受。现已改为：
未完成的 web 调用保持 pending，同一 item 的 `item.completed` 时删除，成功终态
遇到 pending 按协议失败关闭；精确重放不会重新打开已完成项；只有
`item.completed`、没有 `item.started` 的合法流保持不变。新增 co-located 回归
覆盖缺少 completion 与精确重放，`independent-overlay.json` 的
nativecli/workflow 全部 `TestIndependent` 通过，`go test -race
./internal/nativecli` 通过。

最终真实验收：Codex case `b73f4ac8-4285-4c58-a973-5c482f00ce23` succeeded
（116.46 秒），确实调用了原生 web，但两条 `item.completed` 都只有
`action: other` 且没有 `results`，没有来源；模型诚实写明缺失后改用 MCP
搜索/取页完成 note。原生输出可能只有 metadata 或为空，空不作来源；旧单次
探针只证明调用路径，不作为正式产品的原生成功保存。工具调用本身确实
completed，不伪称失败。Gemini 继续 MCP 联网。

Chrome E2E 为 4 pass / 3 fail：collaboration-runtime.spec.ts:121 未发出
`POST /runs`、collaboration.spec.ts:248 刷新后任务折叠导致找不到已停止、
workspace.spec.ts:292 阅读 `scrollTop` 期望 107 实际 141。三项在
implementation-baseline 于本轮实施前逐个同样复现，属并行 UI 基线；本轮未
修改这些产品代码/测试。此处为隔离快照的中间检查，主工作区最终结果见下节。

## 2026-10-28：原生工具最终交付

本机 Claude Code 完成实现与多轮反例修复，Codex 独立验证后按实施前快照提取
41 个文件的本轮变更，三方合并至当前主工作区，保留原有未提交修改与并行 UI。
完整记录见 [原生工具实施与验收](native-tools-implementation.md) 和
[脱敏验收摘要](evidence/native-tools-20261028.json)。

双成员文件分析 succeeded（120.44 秒，尾部反证与有效来源保留）；Codex 原生
web 调用完成但无可保存内容，MCP 后备完成纪要（116.46 秒）；DeBank 原问题
原文复验 succeeded（293.16 秒，18 次 WebSearch、31 次 WebFetch，49 条原生
观察，1 份产物，20 个实际 native_web 引用有效，摘要限制自动附加）。

主工作区 `GOFLAGS=-p=1 make test` 通过（含 race/shuffle、终端 integration、
122 项前端测试），Chrome E2E 7/7 与生产 build/HTTP smoke 通过，契约生成
哈希稳定，密钥扫描和安装版 Claude 文件边界检查通过。隔离快照的 3 项 UI 基线
失败在保留当前主工作区并行 UI 修正后不再出现，不计为本轮实现成果。

原生优先采用实际工具名/映射与对应 MCP 后备描述，是模型指引，不是硬执行策略；
Gemini 仍 MCP，Codex 当前真实 case 无原生来源，原生联网不保证消除 403。
未做 Linux、完整研究事实审计、commit/push/发布。

## 2026-10-28：会话行更多菜单

会话列表每行右侧改为“更多”菜单，菜单只含重命名和删除。触发按钮仅在悬停或键盘焦点时显示，
选中当前会话不再常驻显示；窄屏和 `hover: none` 触屏常显。菜单打开后聚焦第一项，支持方向键、
Home/End、Escape 回焦触发按钮，点击菜单外、滚动或切换会话都会关闭。删除仍走原确认对话框和
后端 409/错误提示。重命名使用独立标题对话框和现有 `PATCH /threads/{id}`，只提交 `title` 与
`If-Match` revision；服务端 title-only 分支只校验标题，保留成员、协调者、流程、ID 和 revision，
成员停用后仍可改名。显式提交 `agent_ids` 等设置时仍走完整成员校验，显式空成员不会被跳过；
版本冲突返回 412。旧客户端仍可提交完整会话对象：`id`、`workspace_id`、`revision`、`updated_at`
只被接收并忽略，身份、版本仍由路径和 `If-Match` 决定，任意未知字段继续返回 400。
失败时保留已输入标题并显示错误。

验证：`go test -race ./internal/httpapi` 覆盖成员停用后改名、成员/角色不变、显式空成员被拒、
412、带伪造身份/版本字段的完整对象更新成功但身份不变，以及未知字段仍返回 400；
前端 `npm run lint`、`npm run typecheck`、`npm test`（134 项）和 `npm run build` 通过；
本次针对重命名改动复跑 `PLAYWRIGHT_CHANNEL=chrome npx playwright test e2e/workspace.spec.ts`
（1/1）。此前全量 `PLAYWRIGHT_CHANNEL=chrome npx playwright test` 7/7 通过。浏览器路径检查了
选中行按钮隐藏、悬停显示、菜单 Escape 回焦、重命名仅改标题（API 前后成员/协调者/流程一致）
以及经菜单删除。
