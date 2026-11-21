# 统一动态协作内核、Worktree 双层沙箱、两档权限重塑、工程技能与任务状态改造

日期：2026-11-13

状态：**需求已确认，已按阶段 1–6 实施并合入主分支；具体验证证据、原生协议与平台未验证范围以[验收记录](2026-11-08-permissions-and-ui-acceptance.md)为准。** 本文件是设计与实现契约，不能单凭本文宣称全部平台或供应商能力已交付。按 `AGENTS.md` 规定，各工作包完成实现并通过对应检查后，已同步更新 `docs/product.md`、`AGENTS.md` 及标注 `docs/redesign.md` / `docs/2026-11-08-permissions-and-ui-redesign.md` 中的替代关系。

适用仓库：Cocogem（核对基线：`4b8a2cf`，分支：`feat/unified-collaboration-and-engineering-skills`；工程方法论、权限减负与任务注意力交互参考：`clowder-ai`）。

---

## 1. 背景、核心目标与已确认决定

### 1.1 问题诊断

当前 Cocogem 在多 CLI 编排、权限账本与持久任务上已具备良好骨架，但在实际工程使用中存在五处核心痛点：

1. **双轨协作撕裂，预设流程过于僵硬**：
   - 内置预设流程（`engineering` 4 步、`research` 5 步、`discussion` 最多 11 步）在启动瞬间写死 DAG，无论改 3 行小问题还是跨模块演进都强制走满流水线并拼装重型报告；自由协作（`free`）虽灵活却缺少代码变更验证守卫，且 `WriteBranch` 硬编码绑死在 `workflow == "engineering"` 上（`promote_to_task` 中途升级任务还会漏建候选目录，未升级为 `Task` 的普通会话也因 `workspace_executions.task_id NOT NULL` 外键限制无法挂载候选目录）。
2. **执行隔离过重，且 Reviewer 权限一刀切过窄**：
   - 现有 `internal/execution/manager.go` 对 Git 仓库采用 `git clone --no-local --no-hardlinks` 全量深拷贝，大仓库启动慢、占双倍磁盘，且 `one_active_execution_root` 唯一索引锁死同一项目只能有 1 个活跃执行目录，使多子任务并行写分支（`WriterBranches`）无法落地；
   - `internal/workflow/request.go` 对 `reviewer / critic` 一刀切封禁 `run_command` 与写临时文件，导致 Reviewer 无法跑非 Go/npm 测试、无法写最小反例探针脚本，退化为纯肉眼读代码。
3. **权限档位拧巴，日常 `/tmp` 复现与无害命令疯狂弹窗**：
   - 现行 `controlled` 档位定位尴尬（允许直接改项目源码，跑命令却每条弹窗，还剥夺了原生 CLI 的高速只读检索工具；若做成全局只读档位又会误伤其他正在后台运行的会话与任务）；
   - `project_autonomous` 档位在内核禁掉了本地 `127.0.0.1` 回环端口（导致 `httptest.NewServer` 等常规单测报错）、把 `/tmp` 临时复现当越界弹卡、`shellPathTokens()` 手工切词把 `2>/dev/null`、`//` 运算符、正则与本机工具链（`~/.cargo`、`~/.nvm`）误判为攻击系统目录；且「本会话持续允许」绑死单文件名与单个 `tool_name`，点一次根本不管下一次。
4. **软工程技能极薄，且宿主工程配套工具不足**：
   - `internal/skills/library` 仅有 5 个薄协调技能，缺乏计划状态门禁、根因调试、务实 TDD、交付前真实入口自检（Dogfood）、同型缺陷清扫等核心工程技能；同时缺少零目录污染的受控页面渲染探针与细粒度多工具链测试支持。
5. **任务 ↔ 会话状态投影失真，任务清单视觉层级过重**：
   - 待批权限在任务清单隐身（误显“进行中”）、会话顶栏胶囊漏传 `questions` 且靠 5 秒盲轮询、父任务无视活跃子任务抢跑进“待验收”、已验收会话追问报 `409 Conflict`、前端存在两套分裂分类器；列表每行固定堆砌两行模板套话（`当前 / 下一步`），视觉噪音大。

### 1.2 已确认的架构与产品决定

| 编号 | 决定 |
| --- | --- |
| D01 | **退役预设死板流程，收敛为单一动态协作内核**：全面移除 `engineering`、`research`、`discussion`、`code_analysis` 预设流程模板（含 Composer 工作方式选择器、`/` 方法菜单、Welcome 模式卡、设置页固定流程角色槽位、`choose_workflow` 工具及 `routing.go` 的 workflow 分类分支）。所有会话与任务统一运行在单一动态协作内核之上。 |
| D02 | **采用彻底硬切（Fresh Start, Clean Cutover）**：本项目当前处于内测阶段，不维护旧数据兼容性与迁移脚本；启动时**仅在明确检测到未初始化（缺 `schema_meta`）或旧 `schema_baseline` 时**清空并重建新 Schema，任何瞬时 SQLite 错误（如 `SQLITE_BUSY` / I/O 错）必须直接失败退出，严禁误清库。 |
| D03 | **分流直写与双层 Git Worktree 隔离 + 原生基线快照与孤儿回收**：① **普通未升级的单人对话默认直写用户绑定项目目录**（不自动创建 `Task`、不派生 `Candidate`、无 Apply 摩擦，IDE/热重载即时可见）；② **持久 `Task` 与并行 `delegate_task` 子任务启用位于 `$COCOGEM_DATA_DIR/executions/` 下的 `git worktree add --detach`**（共享原仓对象池，同步原工作区未提交 tracked/untracked 变更，并在 detached HEAD 上记录内部基线快照 `base_snapshot_oid`；非 Git 目录或 unborn `HEAD` 回退受界 `file_copy` 并以 `BaseFiles` 清单追踪增删改）；③ 调整 `workspace_executions` 唯一索引支持多子任务并行写分支；④ 在 OS 沙箱层放行 `<commonGitDir>/objects` 与 `.git/worktrees/<id>`、后置覆盖锁死 `.git/hooks`、`.git/config`、`.git/refs/heads/*` 与 `packed-refs`（含尚不存在的文件）；⑤ 服务启动与彻底删除/到期清理/删工作空间时**先采集记录再删库**，自动回收 worktree 与 IO 目录并对原主仓执行 `git worktree prune`。 |
| D04 | **下沉硬底座一 —— 多轮候选连续性、Diff 审阅、受影响文件级合并与分支逃生口**：① 同一 Run 内多人并行步（`@A @B`）固定为步骤级只读直连源目录（不建 Task、不派生写 Worktree）；普通单人对话直写源目录；持久 `Task`（含 `promote_to_task` 升级后的接续步）与并行子任务挂载 `Candidate` worktree；② **多轮连续性**：同任务在 `Candidate` 未应用前自动复用同一 worktree 持续演进，点击 Apply 或 Discard 终结后，下轮写操作再基于最新原目录派生新 `Candidate`；③ **Diff 审阅与验证守卫**：提供 Candidate 实时统一 Diff 接口供 `CandidateBar` 在 Apply 前审阅；结算自动计算 `CandidateVerification`（仅 `kind="test"` 通过且无后续改写记 `verified`，`typecheck/lint/build` 仅作支持性证据）；④ **文件级冲突检测与逃生口**：Apply 基于 `base_snapshot_oid` 仅比对**受影响文件**（支持互不重叠的并行子任务顺序无冲突 Apply，且兼容 Agent 在 Candidate 内 `git commit`），运行态禁止 Apply 并做原子写回；若与原工作区存在行级冲突（`409`），提供「导出为本地 Git 分支（`cocogem/cand-<short_id>`）」逃生口（或交协调者整合），绝不让用户卡死。 |
| D05 | **下沉硬底座二 —— 非作者互斥与 Reviewer 独立 Worktree 验证沙箱**：任何 `role="reviewer" \| "critic"` 独立复核：① 硬性拦截目标成员等于原作者（含中转自审）；② **在 `prepareStep` 派发前即刻物化独立 `verify_dir` worktree**（持久任务基于当前 `Candidate`，普通直写会话基于原项目当前树状态；非 Git 同步支持 `file_copy` 副本；保留工作区未提交 diff 形态使 `git diff`/`git_inspect` 可见），复核会话在 `prepareExecutionSession` 中硬性锁定 **`fresh_only` 无痕会话**；③ 在全局权限模式内向 `reviewer` 与 `critic` 同等开放 `verify_dir` 内的命令、定向测试、临时反例脚本编写与页面预览，且权限边界以 `verify_dir` 为根不误弹越界卡；④ **源树/作者 `Candidate` 保持物理 0 字节回写**，审结自动清理 `verify_dir` 并断言被审树 `TreeDigest` 不变（若被篡改必须令复核步骤失败）。 |
| D06 | **下沉硬底座三 —— 一手来源托管守卫（Original Source Custody）**：`publish_artifact`（`note`、`research_report`）与 `save_claim` 均强制执行来源真实性校验，搜索摘要（`lead`）不能充当已证实结论证据。 |
| D07 | **收敛全局权限为 `沙箱自主（默认） / 完全访问` 两档 + 单轮「仅规划」与诚实分层沙箱**：① 移除全局 `controlled` 档位，全局仅设 **`workspace_autonomous`（沙箱自主，默认档）** 与 **`full_access`（完全访问）**；② **认证隔离 + 凭据隔离为硬防线，AST 仅作 UX 降噪**：子进程绝不注入 Cocogem Web Session/Admin 凭据、MCP 网关仅认单步最小权限 Token，从而在 `workspace_autonomous` 下安全放行 `localhost:*` 双向回环（`run_test` 仍保持无网络隔离）；OS 沙箱层放行工作台 + 私有 `$TMPDIR` 与 `/tmp` 临时读写（排除 `/tmp/ssh-*`）及只读语言工具链，而宿主权限审批层保持严格项目/任务边界（显式指定 `/tmp/...` 路径仍弹审批卡并支持按 `/tmp/<子目录>` 持续放行）；内核默认硬拒高敏凭据路径（`~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、macOS Keychain 收紧 `mach-lookup`、浏览器凭据库、`.git-credentials`、`.npmrc`、外部 `.env`/私钥），显式请求命中敏感路径时走 `Risk="sensitive_path"` 单次高危审批（禁建持续授权且单次批准须在宿主工具与沙箱层真正放行该单一目标）；③ **Linux `bwrap` 未实机验证前保持失败关闭**：先执行真实 `bwrap` 探针，在未完成 Linux 实机边界测试前返回 `ErrSandboxUnavailable` 拒绝未隔离执行（需要时用户可显式切 `full_access`）；④ **单轮「仅规划（Plan-Only）」与并行只读锁**：Composer 提供仅作用于当次发送的轻量开关，内核沙箱硬写 `(deny file-write*)`、禁用 `run_test` 与 `preview_page(mode="command")`（含 `full_access + PlanOnly` 组合），保留五家 CLI 原生只读工具与 `git_inspect`，越界写操作先判定只读直接确定性拒绝（不弹审批卡），绝不推进全局 `permission_epoch`；⑤ **持续授权（`ConversationGrant`）拆分 `read < write < execute` 三档 + 物理目录子树匹配**，审批 API 与 UI 显式支持 `grant_scope: once | file | directory`，且统一应用于 `CheckToolAccess`、`WaitForDecision` 与同级请求唤醒。 |
| D08 | **双向交接契约：接球验真（Receive Handoff Grounding）与防乒乓收敛阀**：队友交接描述一律视为 T2 候选声明，执行副作用前在可用工具内用 T0（用户原话/`TaskRevision`/真实文件）或 T1（宿主 `diff/test/image` Source）独立核验；能自查不打回，同一问题点对点 `relay` 返工硬上限 **2 轮**，超限阻止第 3 次返工并**如实记录分歧（严禁把 `changes_requested` 静默改写成 `approved`）**，交协调者或 `ask_question` 裁决。 |
| D09 | **视觉证据第 0 步分流闸门（Vision Gate-0）与受控页面探针（`preview_page`）**：纯后端/CLI/算法改动硬性豁免截图要求；`preview_page` 禁止任意 URL/端口输入：`mode="static"` 仅在 `os.OpenRoot(projectRoot)` 内只读托管（防软链逃逸），`mode="command"` 按 **`execute` 动作受权限与 OS 沙箱约束且锁死 `cmd.Dir`**、独立进程组清场；无头 Chromium 开启 CDP 非同源硬拦截（含 **WebSocket 与 `Target.setAutoAttach`** 防弹窗/OOPIF/Worker 旁路 SSRF），真实采集 `console_errors`、`page_errors` 与 `AXTree`，截图存为宿主 `Source{Kind: "image"}` 零目录污染（渲染失败严禁存 0 字节图或报 `rendered: true`）；无浏览器引擎时诚实返回 `browser_engine_unavailable`。 |
| D10 | **拓宽宿主受控工程验证工具与 Diff 兜底诊断**：`run_test` 枚举化组装正确 `argv` 支持 `filter` 单测过滤（防前导 `-` 参数注入与 `../` 路径穿越、兼容 Go `TestFoo/SubTest`）及 `go/npm/pnpm/yarn/pytest/cargo` × `test/typecheck/lint/build`（仅 `kind="test"` 可将候选标为 `verified`，macOS 沙箱完整放行工具链 `ExecRoots` 读权限）；新增确定性只读 `git_inspect`（禁用 hooks/ext-diff/textconv/`core.fsmonitor` 并过滤敏感文件）；`AnalyzeDiffHygiene` 对实际候选累计 diff（覆盖原生 CLI 修改）检出 ≥3 层异常兜底分支时附加非阻塞提示（不误报正常 Go `if err != nil` / `return nil`）。 |
| D11 | **固定 10 个核心内置技能并严控上下文预算**：支持受限 YAML frontmatter、`_shared/` 参考层（全部参考文件名在索引/技能中可发现且零死链）与项目本地 `.agents/skills/` 安全只读发现（含 `.agents` 父目录软链校验）；固定 10 个正交内置技能 ID，单条描述 ≤240 字符（`BOOTSTRAP` 总索引 ≤1.2K tokens），单篇正文 ≤90 行。 |
| D12 | **五轴风险路由替代固定串联流水线**：局部可逆小改走单成员普通会话直写快车道（自测 + 变更守卫即交付）；触及状态机/持久化/安全权限/公共契约/不可逆轴时拉起跨成员（优先跨 CLI/模型家族）独立复核；跨阶段大目标升级持久 `Task` 启用 Worktree 分治推进。 |
| D13 | **统一“球权/注意力”状态同步与任务清单视觉减负**：后端 `TaskBoard()` 批量预取与 `taskInTx()` 单任务接口共享完整投影字段（待批权限、未决问题、子任务数、候选状态）并排除 `verify_dir`；`BindRunTask` 自动推进已验收任务 `CurrentRevision + 1` 重激活；前端左侧栏、顶栏胶囊、任务清单/看板与详情页统一唯一 `taskPhaseMeta` 与 SSE 实时刷新；任务清单落地紧凑两行卡片、仅异常态亮 `3px` 左边单行露因、底部已完抽屉计入 `completed + canceled`、待批权限卡直达关联会话审批卡与双向消息锚点定位。 |
| D14 | **步内断点续接、人工继续解锁与统一连续性胶囊（Continuity Capsule）**：在保留步间精准续接（`RetryStep`）基础上，打通同一步骤中途失败/超时的断点恢复——① 原生 CLI 流吐出 `session_id` 首事件即刻落盘，同 `work_root` 下人工继续优先 `--resume` 原生会话；② 收口唯一的结构化状态交接单 **`BuildContinuityCapsule`（交接单本身 ≤1.5K tokens，不限制总对话上下文）**，包含当前目标、工作台改动清单、中断原因/草稿尾部与 `read_context/read_source` 按需索引指针，同时服务 `RetryStep` 断点续接、Stop 损坏自愈、超限换桌与 `promote_to_task` 切根；③ 机器自动重试遇未知副作用继续停机保护，**用户显式点击「继续 / 重试该成员」（含 `RetryStep` 与 `Resume`）传 `humanConfirmed = true` 解锁基于当前现场续跑**。 |
| D15 | **五家 CLI 原生目录会话落盘与全会话跨轮续聊（Native-Home Session Persistence）**：坚持五家全通——Claude（`--resume`）、Codex（app-server `thread/start` & `thread/resume`，去掉临时 `CODEX_HOME` 与 `ephemeral: true`）、Gemini（`--resume`）、Pi（`--session`，去掉 `--no-session`）、OpenCode（原生存储与 session 续接）统一将真实会话文件交回各自原生管理目录维护（保住 OAuth 自动续期、Prompt Cache 与外部终端排查），当次 MCP 与规则仅靠命令行参数隔离；严格按成员已确认消息 ID 水位线执行纯增量注入（彻底禁止在 `resume` 轮重复拼接全量 `Shared history`）；任何 `--resume` 协议失败、Stop 残损或上下文超限由 Continuity Handshake 自动 `seal` 旧会话并透明重开新会话 + 注入 `Continuity Capsule`；仅一次性 Reviewer `verify_dir` 走无痕模式。 |

### 1.3 继续成立的系统不变量

- **Web 是唯一产品入口**：所有后端实现位于 `internal/`，原生 CLI 适配位于 `internal/nativecli/`，不新增独立终端产品命令。
- **实时权限 Epoch 与单次原子消费**：全局两档模式在 Composer 与“设置 → 权限”双向实时同步；从 `full_access` 收紧至 `workspace_autonomous` 推进 `permission_epoch`，使旧挂起请求与旧会话扩权立即失效；单次批准绑定调用身份与参数摘要原子消费一次，未知副作用不自动重放。
- **复用本机已有 CLI 配置**：继续直接复用本机 Claude / Codex / Gemini / Pi / OpenCode 的既有凭据、endpoint 与模型配置，不要求额外登录。
- **技能中立性**：`internal/skills/library` 严禁绑定具体成员名、handle、CLI 品牌或特定模型名称。
- **根任务预算与精准成员重试**：每个根任务独立累计预算账本；单成员失败精准重试不重置根预算、不破坏已完成同伴结果与候选文件。

---

## 2. 已核对的高影响架构风险与代码差距矩阵

| 代码位置 | 当前现状与潜在隐患 | 目标改造方案 |
| --- | --- | --- |
| `internal/workflow/plan.go`、`routing.go`、`verdict.go` | 硬编码 `discussion / research / code_analysis / engineering` 模板、`choose_workflow` 与 `engineering_report` 专有校验。 | **方案 A 彻底物理删除**预设流程编排器、`choose_workflow`、`assignments` 与旧专用报告模板；收敛为单一动态步骤调度与 2 轮复核收敛协议。 |
| `internal/store/schema.sql`、`internal/execution/manager.go`、`task_runs.go`、`task_intake.go` | ① `workspace_executions` 与 `execution_sessions` 的 `task_id` 均为 `NOT NULL` 外键，普通会话无法挂载候选区也无法保存 CLI 会话；② `one_active_execution_root(canonical_root)` 阻塞多 `delegate_task` 并行写分支；③ 全量 `git clone --no-local` 极慢；④ `promote_to_task` 升级后漏建 `Candidate`。 | 调整 `workspace_executions` 与 `execution_sessions` 同时支持 `thread_id` + 可空 `task_id`（`promote_to_task` 原地继承），活跃执行唯一索引改为 `(COALESCE(task_id, thread_id), canonical_root)`；升级为 **`git worktree add --detach`**（同步脏工作区、补齐 `worktree prune` 孤儿清理）；统一多轮复用与 Apply/Discard 状态机。 |
| `internal/workflow/step_prepare.go`、`session.go`、`request.go` | 原生 CLI 子进程启动即锁死 `cwd`；`session.go` 对 `TaskID == ""` 直接跳过会话管理；`request.go` 一刀切删了 Reviewer 的 `run_command`。 | 打通普通会话与任务的 `ExecutionSession` 跨轮增量续聊；`prepareStep` 派发前为 Reviewer 物化独立 `verify_dir` worktree 并固定 `fresh_only` 无痕会话；放开 Reviewer 在 `verify_dir` 内的命令、测试与反例脚本能力。 |
| `internal/workflow/{retry,retry_step,session,step}.go` | 步间可续接，但**同一步骤中途超时/报错无法断点续接**：`NativeSessionID` 等步骤跑完才签收导致中途失败必重开新会话；且中途一旦改过文件落入 `uncertain`，连用户手动点重试都会硬报 `ErrRecoveryUncertain` 锁死。 | 流启动收到原生 `session_id` 即刻持久化，同 `Candidate` worktree 下优先 `--resume`；兜底注入 `Checkpoint Digest`（当前已改文件与中断前工具轨迹）；人工点击继续明确解锁 `uncertain` 现场续跑（机器自动重试仍保持停机保护）。 |
| `internal/nativecli/{capabilities,provider,native,approval,codex_appserver,claude_control,gemini,pi,opencode,native_gate_run,sandbox_*}.go`、`internal/project/permissions.go`、`internal/store/permissions.go` | 63 处硬编码 `"controlled" / "project_autonomous"`；Codex 被覆写临时 `CODEX_HOME` + `ephemeral:true`、Pi 硬传 `--no-session`、Gemini 未接 `--resume`、Claude 普通会话传 `--no-session-persistence`；`project_autonomous` 禁 `127.0.0.1`、拦 `/tmp`；手工切词误报 `2>/dev/null`、`//`、工具链；`ConversationGrant` 绑死单文件与单工具名。 | 全局切换为 **`workspace_autonomous (默认) / full_access` 两档** + 单轮 `PlanOnly` 只读锁；**五家 CLI 会话统一落回各自原生管理目录并支持跨轮 Resume**（当次 MCP 仅靠参数隔离，沙箱仅放行当前 CLI 原生目录）；放开 worktree + `/tmp` + 本地回环，敏感路径单次高危豁免，AST 精准解析命令，目录子树级持续授权。 |
| `internal/tools/registry.go`、`runtest.go`、`sandbox_*.go` | `run_test` 仅支持全量 `go/npm`；Linux CI（Ubuntu 24.04 AppArmor）默认限制非特权 `bwrap`；无页面探针。 | 拓宽 `run_test` 支持 `filter` 与主流工具链，新增确定性只读 `git_inspect` 工具；Linux 执行真实运行态 `bwrap` 可用性探针；新增宿主自建端口 + CDP 非同源硬拦截的 `preview_page`。 |
| `internal/skills/library.go`、`library/*` | 仅单行 frontmatter、仅 5 个薄协调技能；无索引体积上限。 | 支持受限 YAML frontmatter、`_shared` 参考层与项目 `.agents/skills/` 只读挂载；固定 10 个标准内置技能 ID，单描述 ≤240 字符（总索引 ≤1.2K tokens）。 |
| `internal/project/service.go`、`control.go`、`web/src/**`、`scripts/verify-redesign.mjs` | `TaskBoard` 存在 N+1 查询且漏查待批权限；顶栏漏传 `questions`；双套分类器分裂；列表堆砌两行套话；`verify-redesign.mjs` 硬编码旧权限名。 | 批量预取聚合统一注意力状态，修复已验收会话追问重激活；全局唯一 `taskPhaseMeta` + SSE 即时刷新；落地轻量两行卡片、异常 `3px` 左边单行露因、底部已完折叠抽屉；同步更新 OpenAPI、前端与 `verify-redesign.mjs`。 |

---

## 3. 统一动态协作内核与双层 Git Worktree 隔离架构

### 3.1 Fresh Start 启动初始化与 Clean Cutover 物理清理（内测无历史迁移包袱）

1. **安全基线检测（`internal/store/schema.go`）**：
   - 彻底移除旧迁移脚本，采用单一权威基线 `schemaBaseline`；
   - **仅当 `schema_meta` 不存在（`no such table` / `sql.ErrNoRows`）或 `err == nil && baseline != schemaBaseline` 时**执行 `dropAllTables` 并初始化新 Schema；**任何 `SQLITE_BUSY` / I/O 等真实数据库错误必须直接返回错误失败退出，严禁误清库**；
   - `workspace_executions` 与 `execution_sessions` 同时支持 `thread_id` 与可空 `task_id`（upsert 语句必须同步更新 `task_id`；`SealThreadSessions` 同步更新索引列 `status='sealed'` 与 JSON `body`），活跃执行唯一索引为 `UNIQUE(COALESCE(task_id, thread_id), canonical_root) WHERE status IN ('active','quarantined','uncertain')`。
2. **代码与契约零残留**：物理删除 `choose_workflow`、`routing.go` 的 workflow 分类器、`plan.go` 四套死板步骤模板，以及 OpenAPI 与前端 schema 中的 `Workflow` 枚举与 `assignments` 字段。

### 3.2 直写快车道与双层 Git Worktree 隔离、Diff 审阅与分支逃生口

按协作形态分流，兼顾日常编码零摩擦与持久/并行任务强隔离：

1. **普通未升级单人对话（直写工作区快车道）**：
   - 未显式升级为持久 `Task`、非 `PlanOnly` 且非多人并行的单人对话，绑定项目后**默认直写用户绑定项目根目录（`Workspace.Root`）**，不自动创建 `Task`、不派生 `Candidate` worktree、无需手工 Apply，本地 IDE 与热重载即时生效；
   - 若在普通对话中调用 `promote_to_task` 升级，宿主基于当前最新项目工作区物化 `Candidate` worktree，自动更新 `task_id` 并因 `work_root` 变更滚动新原生会话（注入连续性胶囊）。
2. **持久 `Task` 与并行 `delegate_task` 子任务（双层 Worktree 隔离）**：
   - 所有派生工作台统一存放在 `$COCOGEM_DATA_DIR/executions/worktrees/` 私有目录下，绝不污染用户项目目录：
     ```text
     用户绑定项目根目录 (Workspace.ProjectPath) —— 持久任务未点 Apply 前 0 字节改动
       └── .git/
            ├── objects/ & worktrees/<curr_id>/  ──► [Seatbelt/bwrap 先置 allow：保障 git status/add/diff 正常锁 index 与写对象]
            └── hooks/, config, config.worktree, packed-refs, refs/heads/* ──► [Seatbelt 后置覆盖 deny / bwrap --ro-bind：无论文件是否存在均硬锁]
     ```
   - **Author Candidate 创建与基线快照（`base_snapshot_oid`）**：
     - 执行 `git -C <ProjectPath> worktree add --detach <cand_dir> HEAD`（非 Git 目录或 unborn `HEAD` 回退受界 `file_copy` 并以 `BaseFiles` 完整追踪增删改）；
     - 若原工作区存在未提交改动，屏蔽 `~/.gitconfig`（`GIT_CONFIG_GLOBAL=/dev/null`）同步 `git diff --binary HEAD` 与非敏感未跟踪文件，并在 detached `HEAD` 上打一个内部基线快照提交 `base_snapshot_oid`（不改主仓任何分支）；后续无论 Agent 是直接改文件还是在 Candidate 内执行了 `git commit`，真实改动统一按 `base_snapshot_oid..worktree` 计算，杜绝丢改动与脏基线误报。
   - **多轮连续性、实时 Diff 审阅、受影响文件级 Apply 与冲突分支逃生口**：
     - **未应用续写（Continuity）**：同任务存在活跃未应用 `Candidate` 时，后续非只读轮次默认复用该 worktree；
     - **Apply 前 Diff 审阅**：提供 `GET /api/v1/candidates/{id}/diff` 返回实时统一 Diff，前端 `CandidateBar` 内建可展开 Diff 审阅视图，彻底告别“盲点 Apply”；
     - **运行态锁定 + 受影响文件级冲突校核 + 原子写回**：运行中（`status == "active"`）禁止 Apply；Apply 基于 `base_snapshot_oid` 仅校核**本候选实际触及的文件**（含外部删除与新增检测），互不重叠的并行子任务可顺序无冲突 Apply；最终通过临时文件 + 原子替换写回原目录并回收 worktree；
     - **冲突逃生口（`Export Branch`）**：当原目录受影响文件自基线起发生外部修改或删除返回 `409` 时，提供「导出为本地 Git 分支（`cocogem/cand-<short_id>`）」接口与按钮（基于当前候选快照在主仓创建本地分支但不切换用户当前分支），绝不让用户进退两难；
     - **丢弃变更（Discard Candidate）**：标记 `discarded` 并立即清理该 worktree。
3. **Reviewer Verification Worktree（`verify_dir`）**：
   - 在 `prepareStep` 派发前物化独立 `rev-<step_id>`（持久任务基于当前 `Candidate`，普通直写会话基于原项目当前树；非 Git 目录支持 `file_copy`），同步未提交变更时**保留工作区未提交 diff 形态**（使 Reviewer 跑 `git status/diff/git_inspect` 直接看到作者改动）；
   - `prepareExecutionSession` 对 `role="reviewer" | "critic"` 硬性锁定 **`fresh_only`**，权限边界以 `verify_dir` 为根（绝不拿作者目录误判越界），`reviewer` 与 `critic` 享有同等命令/测试/反例脚本/预览权限；
   - 被审目录对 Reviewer 内核级只读（0 字节回写）；审结自动销毁 `rev-<step_id>` 并断言被审树 `TreeDigest` 逐字节不变——**若检测到篡改必须令该复核步骤失败**。
4. **崩溃恢复与孤儿 Worktree 清扫**：
   - 服务启动恢复（`Prune`）与永久删除/到期清理/删工作空间（`permanentDeleteThread`、`PurgeExpiredThreads`、`deleteWorkspace`）必须**先读取关联 execution 与主仓路径，再删数据库记录**，随后回收 worktree 与 `$COCOGEM_IO_DIR` 并对主仓执行 `git worktree prune --expire now`。

### 3.3 步间与步内断点续接及统一连续性胶囊（`retry.go` & `session.go`）

确保某一步报错、网络中断、超时或强杀停止后，续跑时既不重跑已完成队友，也不丢弃半程现场：

1. **步间精准续接**：`RetryStep` 完整保留已成功上游结果与产物引用，仅从目标失败/中断步骤重新调度并唤醒受阻下游，且标记 `AutomaticContinuation = false`。
2. **工作区现场零丢失**：中途失败或超时不回滚/销毁工作区与 `Candidate` worktree。
3. **流启动即刻持久化 `NativeSessionID` + 优先原会话 `--resume`**：收到首个会话标识事件即落盘 `NativeSessionID`，同 `work_root` 下优先 `--resume <NativeSessionID>` 续跑。
4. **统一连续性胶囊（`BuildContinuityCapsule`，单张交接单 ≤1.5K tokens）**：
   - 合并原 `Checkpoint Digest` 与换桌胶囊为唯一生成器，**仅限制交接单本身 ≤1.5K tokens（不限制整体对话或任务上下文窗口）**，固定包含：① 当前目标与生效 `TaskRevision` 约束；② 当前工作台改动摘要（`git status --short` + `diff --stat` 或非 Git 变更清单）；③ 上次中断/报错原因与未完草稿尾部 + 最后若干工具回执；④ 可按需查阅原文的 `read_context` / `read_source` 索引指针；
   - 统一服务 `RetryStep`（含新建 `step.ID` 的重试）、Stop 损坏自愈、上下文超限换桌与 `promote_to_task` 切根四个场景。
5. **人工显式继续解锁 `uncertain` 状态**：后台自动重试遇 `uncertain` 严格停机；**用户人工点击「继续 / 重试该成员」（含 `RetryStep` 与 `Resume`）统一传 `humanConfirmed = true`**，重新校准树摘要并放行续跑，不再抛 `ErrRecoveryUncertain` 卡死。

### 3.4 五家 CLI 原生目录会话保留、严格纯增量与自愈换桌（`session.go` & `nativecli/`）

1. **五家 CLI 全部回归原生管理目录与真实续聊（无例外缩水）**：
   - 移除覆盖型临时 `CODEX_HOME`、`CLAUDE_CONFIG_DIR`、`GEMINI_CLI_HOME`、`PI_CODING_AGENT_DIR`、私有 `OPENCODE_DB` 及常规轮次的 `--ephemeral / --no-session-persistence / --no-session`；
   - **五家协议全部打通并过实机探针**：Claude（`--resume <id>`）、Codex（app-server `thread/start` & `thread/resume`）、Gemini（`--resume <id>`）、Pi（`--session` 持久化续接）、OpenCode（原生存储 + session 续接）；当次 MCP 与规则仅通过单次启动参数隔离，零污染用户全局配置；
   - 仅一次性 Reviewer `verify_dir` 开启无痕模式；`Candidate` 在 Apply/Discard 或 `promote_to_task` 切换 `work_root` 时自动滚动新原生会话。
2. **按成员已确认消息水位线的严格纯增量注入（彻底消除 `O(N²)` 与双倍历史）**：
   - 按 `(thread_id, member_id, work_root)` 持久化 `NativeSessionID` 与已确认消息集合 `AcknowledgedMessageIDs`；
   - 确认进入 `resume` 时，**严禁再将包含全量 `Shared history` 的 `stepDynamicText` 拼回消息体**，仅注入上次退场后新增的用户消息、其他队友已送达回复及真正发生变化的约束（未变更约束绝不误标 `SupersededConstraints`）。
3. **真实握手验真（Continuity Handshake）与无感自愈换桌**：
   - 适配器核对 CLI 实际返回的运行时会话回执；
   - 一旦 `--resume` 遭遇上下文超限（`context_length_exceeded`）、Stop 强杀导致的会话残损（如残缺 thinking 签名报 400）或外部终端修改导致协议不匹配，宿主**在当次运行内透明 `seal` 旧会话、自动重开全新原生会话并注入 `Continuity Capsule` 继续执行**，不报错中断给用户。
4. **头 40% + 尾 60% 预览截断（Head-Tail Truncation）**：保留前 40% + 省略字节提示 + 后 60%，支持 `read_context/read_source` 精确翻页。

---

## 4. 全局两档权限 + 单轮「仅规划」与零打扰沙箱合同（`internal/workspace`, `project`, `nativecli`）

借鉴 `clowder-ai` 将“只读锁”与“全局环境信任度”解耦的设计（避免切换全局只读误伤其他正在运行的任务），权限体系拆分为**全局两档模式**与**单轮/步骤级只读锁**两个正交切面：

### 4.1 全局两档模式边界矩阵（`ExecutionMode`）

| 能力维度 | 1. 沙箱自主（`workspace_autonomous`，**默认档**） | 2. 完全访问（`full_access`） |
| --- | --- | --- |
| **核心定位** | 95% 日常编码、调试、测试与本地联调主力档（**工作台内 0 弹窗**） | 跨多仓联动改造、安装系统软件、宿主机全局运维 |
| **Candidate / Verify Worktree 与 `TaskDirectory` 内读写与执行命令** | **全自动沙箱放行（0 弹窗）** | 全自动放行 |
| **OS 沙箱内私有 `$TMPDIR` 与 `/tmp`（`/private/tmp`）临时文件** | **进程内部临时读写直接放行**（排除 `/tmp/ssh-*`）；**显式指名 `/tmp/...` 的工具目标**走项目外审批（支持按 `/tmp/<子目录>` 持续放行） | 全自动放行 |
| **本地回环网络（`localhost:*`）** | **`run_command` / `preview_page` / 原生命令允许子进程双向回环自连**（`run_test` 保持无网络隔离；Cocogem 管理 API 依赖认证隔离：子进程绝不注入 Web Session/Admin 凭据，MCP 网关仅认单步最小权限 Token） | 全自动放行 |
| **公共互联网只读检索与包依赖拉取** | 允许检索与通过受控代理拉取依赖（沙箱凭据隔离天然阻断无授权外部推送/发布） | 全自动放行 |
| **本机语言工具链只读调用**<br>（`/opt/homebrew`、`/Library/Developer`、`~/.cargo`、`~/.nvm`、`~/.pyenv`、`~/.volta`、`~/.local/share/mise`） | **只读与执行自动放行，试图写入/篡改工具链目录硬拦截** | 全自动放行 |
| **高敏凭据路径**<br>（`~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、macOS Keychain 收紧 `mach-lookup`、浏览器凭据库、`.git-credentials`、`.npmrc`、外部 `.env`/私钥） | **内核默认硬拒绝 + `Risk="sensitive_path"` 单次高危审批**（禁建持续授权，批准后宿主工具与当次沙箱 profile 真正放行该单一目标） | 按完全访问执行（硬链接越界保留高危提示） |
| **跨出项目与 `TaskDirectory` 访问宿主机其他目录**<br>（如 `../other-repo`、`~/Documents`、显式 `/tmp/...`） | **弹卡申请**（支持按目录子树授权本会话持续复用，允许 `/tmp/<子目录>`，禁止 `/`、`$HOME`、`/Users`、顶层 `/tmp`） | 全自动放行 |
| **不可逆外部写副作用**<br>（`git push`、公网非只读外部写请求、系统级 `sudo`/全局安装） | **弹卡申请** | 全自动放行 |

### 4.2 单轮「仅规划（Plan-Only）」与内部步骤级只读锁（`RunInput.PlanOnly` / `Step.ReadOnly`）

当用户只想讨论架构、走读代码或先确认方案再动手（或系统自动运行 `@A @B` 多人并行盲评步、恢复对账步）时，通过本轮/本步只读锁实现，**不改变全局 `ExecutionMode`、不推进全局 `permission_epoch`、不影响其他后台会话与任务**：

1. **触发入口**：
   - **手动入口**：Composer 输入栏提供轻量的单轮切换开关 **「仅规划」**（发送该条消息时携带 `plan_only: true`）；
   - **自动入口**：同一 `Run` 内的多人并行盲评步（`len(parallelSteps) > 1`）自动设置 `Step.ReadOnly = true`。
2. **执行期硬保障（零弹窗、零改写，对 `workspace_autonomous` 与 `full_access` 同等生效）**：
   - 不派生写 `Candidate` worktree，直连源目录（或当前已有未应用 `Candidate`）做只读分析；
   - **保留五家 CLI 原生高速只读工具**（如 Claude `--permission-mode plan`、Codex `--sandbox read-only`、原生 `Read / Glob / Grep`）以及宿主只读工具（`read_file`、`search_content`、`git_inspect`、`preview_page(static)`、`web_search`、`fetch_url`）；禁用 `run_test` 与 `preview_page(mode="command")`；
   - `model.Request` 与 OS 内核沙箱显式传递 `ReadOnly: true`，强制挂载项目树与外部目录为只读（`(deny file-write*)`，仅放行 CLI 进程必需的最小私有运行缓存）；
   - 若模型在 `PlanOnly / ReadOnly` 轮次尝试调用写文件或副作用命令（无论目标在项目内还是项目外），宿主**先于权限审批**直接返回确定性 `read_only_step` 拒绝提示（绝不弹审批卡打断用户）。

### 4.3 根治误报弹窗与提升授权灵活性的四项底层改造

1. **标准 Shell AST 解析作 UX 预检降噪，OS 沙箱与凭据隔离作硬防线（重构 `nativecli/approval.go` & `shellast.go`）**：
   - 引入 POSIX Shell AST 解析器（`mvdan.cc/sh/v3/syntax`）提取真实命令节点、重定向文件目标与路径参数（递归解析 `-c` 嵌套脚本，正确展开 `~` 与清理引号内 `//`/`/./`/末尾 `/` 路径，不将 `network:public` 误拼入项目目录）；
   - 彻底根除 `2>/dev/null`（放行 `/dev/null`、`/dev/zero`、`/dev/urandom`、`/dev/fd/*`）、`jq '.a // empty'`、`awk '/ERR/'` 与本机工具链路径误报；明确识别 `git push` / `sudo` / 外部写作为审批触发，同时始终以 OS 内核沙箱 + 凭据隔离作为硬边界。
2. **敏感路径“内核默认拒绝 + 单次审批真放行”闭环**：
   - 未获批状态下，Seatbelt / `bwrap` 默认对全部高敏凭据路径（`~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、浏览器凭据库、`.git-credentials`、`.npmrc`、`id_*` 私钥、外部 `.env`）写入 `(deny file-read* file-write*)` 并收紧 `mach-lookup`；
   - 无论 `read` / `write` / `execute` 动作命中高敏路径，均标记 `Risk = "sensitive_path"` 发起高危单次审批（UI 醒目警示且禁用持续授权）；仅当用户点击「仅本次允许」后，宿主文件工具与当次专属沙箱 profile 才精准放行该单一目标，执行完立即失效。
3. **`ConversationGrant` 升级为「三档动作层级 + 物理目录子树包含」**：
   - 动作层级严格拆分为 **`read (1) < write (2) < execute (3)`**（批准写文件绝不自动放开执行命令）；匹配条件为 `ActionLevel(grant) >= ActionLevel(req) && ScopeContains(grant.NormalizedResourceScope, target)`（基于 `EvalSymlinks` 后的真实物理路径），统一应用于 `CheckToolAccess`、`WaitForDecision` 与 `settleSiblingsUnderGrant`；
   - 审批后端 `DecisionInput` 与前端卡片显式支持 `grant_scope: "once" | "file" | "directory"`（外部普通路径提供「仅本次允许」「本会话允许此文件」「本会话允许所在目录 `<parent_dir>/`」，硬禁将 `/`、`$HOME`、`/Users`、顶层 `/tmp` 存为宽泛授权，但允许 `/tmp/<subdir>`）。
4. **五家原生 CLI 适配器全面对齐与 Linux 失败关闭**：
   - 统一更新 Claude、Codex、Gemini、Pi、OpenCode：按 `Step.ReadOnly` 优先施加内核只读锁（覆盖 `full_access + PlanOnly`），否则按 `workspace_autonomous` 或 `full_access` 运行；
   - Linux 执行真实 `bwrap` 运行态探针，在受限 AppArmor 或未完成 Linux 实机边界测试的主机上报告 `linux_bwrap_userns_unavailable` / `ErrSandboxUnavailable` 并失败关闭（不把未实机验证的 namespace 配置冒充为强隔离边界）。

---

## 5. 宿主工程配套能力设计（`run_test`、`git_inspect`、`preview_page` 与 Diff 诊断）

### 5.1 拓宽受控检查工具（`run_test`）与确定性只读 `git_inspect`

1. **`run_test` 拓宽 schema**（由宿主按枚举构造确定性 `argv`，零 shell 拼接，各工具链无重复子命令）：
   - `toolchain`: `"auto | go | npm | pnpm | yarn | pytest | cargo"`
   - `kind`: `"test | typecheck | lint | build"`（**仅 `kind="test"` 可产出验证行为的 `test` 证据将候选标为 `verified`**，`typecheck/lint/build` 仅记录支持性证据）
   - `target`: 相对子目录或包路径（默认 `"."`）
   - `filter`: 可选单个测试匹配名（严禁前导 `-` 与 `..` 路径穿越，兼容 Go 子测试 `TestFoo/SubTest`）
   - **macOS 沙箱完整放行工具链 `ExecRoots` 只读权限**；**Linux 沙箱实测探针**在受限 AppArmor 或未实机验证边界下报告 `linux_bwrap_userns_unavailable` / `ErrSandboxUnavailable` 并失败关闭。
2. **新增确定性只读 `git_inspect` 工具**：
   - 支持 `subcommand: "status | diff | log | show"` 与受限安全参数（硬编码附带 `--no-ext-diff --no-textconv -c core.hooksPath=/dev/null -c core.fsmonitor=false`，并过滤敏感路径防默认 `diff` 泄露 `.env`/私钥），在单轮「仅规划」及所有只读步骤中零风险查看仓库变更与提交历史。

### 5.2 防 SSRF 的宿主受控页面探针（`preview_page`）

1. **零任意地址输入 + 沙箱约束 + 全通道 CDP 非同源硬拦截**：
   - 不接受任何 URL 或端口号参数：`mode="static"` 通过 `os.OpenRoot(projectRoot)` 托管项目内只读静态文件（防软链越界），在 `PlanOnly` 下亦可安全使用；`mode="command"` **按 `execute` 动作受权限与 OS 沙箱约束、锁定 `cmd.Dir = workRoot`**，在独立进程组启动项目预览脚本并注入随机空闲端口，退出时 `kill -PGID` 整组清场；原生 CLI MCP 桥 `liveness` 豁免 20s 通用超时；
   - 无头 Chromium/Chrome 实例启用 CDP 请求拦截器 + **`Target.setAutoAttach` 与 WebSocket 拦截**——仅放行宿主当次创建的唯一 `http://127.0.0.1:<ephemeral_port>` Origin 及 `data:/blob:`，任何指向其他本机端口或公网的 HTTP/WS/弹窗/OOPIF/Worker 请求一律 `Abort`。
2. **零目录污染与真实可观测输出**：
   - 真实采集 `console_errors`、`uncaught_page_errors` 与 `AXTree` 摘要；仅在截图成功非空时写入宿主附件存储并注册为 `Source{Kind: "image"}`（渲染失败绝不写 0 字节图或返回 `rendered: true`）；**Worktree 目录 0 文件新增，绝不破坏 `TreeDigest`**；无可用浏览器内核时返回 `browser_engine_unavailable`。

### 5.3 候选 Diff 静态坏味道提示（`AnalyzeDiffHygiene`）

基于实际候选累计 diff（覆盖原生 CLI 与宿主工具全部改动，而非单次编辑或历史旧 diff），仅对单文件净增 ≥3 层异常兜底控制流分支（排除常规 Go `if err != nil` / `return nil` 等惯用错误处理）在观测与复核摘要中附加非阻塞提示。

---

## 6. 共享工程技能体系升级合同（`internal/skills`）

### 6.1 加载器与上下文预算硬门禁

1. 支持受限 YAML frontmatter（`name`、`category`、多行 `description`）与跨技能共享参考层 `library/_shared/references/*.md`；
2. **上下文预算硬上限**：固定 **10 个标准内置技能**，每条 `description` 必含 `Use when / Not for / Output` 且 **≤240 字符**（确保 `BOOTSTRAP` 索引稳定 ≤1.2K tokens），单篇 `SKILL.md` 正文 **≤90 行**，由 `library_test.go` 静态强校验；
3. 支持按项目只读发现 `.agents/skills/<id>/SKILL.md`（符号链接越界与敏感路径拦截，最多 8 个，内置同名技能不可被覆盖，纳入 `SnapshotHash`）。

### 6.2 固定的 10 个内置核心技能矩阵

| # | 规范技能 ID | 状态 | 核心机制 |
| --- | --- | --- | --- |
| 1 | **`task-orchestration`** | 升级 | 五轴风险选车道（局部可逆改动单人快车道直达；高风险选异构成员 `relay` 独立复核；跨阶段复杂目标升级持久 `Task` 并行 `delegate`）。 |
| 2 | **`task-handoff`** | 升级 | 发球契约 + **接球三问验真（Claim Grounding：T0/T1 独立核验上游 T2 声明）** + “能顺手自查不打回、同问题最多 2 轮返工”防乒乓纪律。 |
| 3 | **`independent-review`** | 升级 | 独立二源验真 + **基于独立验证 Worktree 跑真实测试/写最小反例探针/调 `preview_page` 验页面** + **视觉 Gate-0 豁免**（纯后端/CLI/算法硬豁免截图要求；无浏览器引擎时诚实披露限制）。 |
| 4 | **`synthesize-results`** | 精简保留 | 多分支结果汇总、保留独立复核反例与分歧、区分已证实事实与未验证限制。 |
| 5 | **`file-delivery`** | 保持 | 真实办公文档/表格/图像读写转换、产物重开校验与 `publish_file` 正式交付。 |
| 6 | **`writing-plans`** | **新增** | 原问题端到端对齐 + **状态对象门禁（明确生命周期所有者、转移事件、恢复/并发边界与不变量）** + 防线代价核算 + 实证查询项目真实构建测试命令。 |
| 7 | **`debugging`** | **新增** | 未知根因四步取证（输入输出边界追踪 → 同仓正常路径对照 → 单变量区分实验 → 进程/端口/worktree 实例核验），严禁盲猜缓存或同时改多处碰运气。 |
| 8 | **`tdd`** | **新增** | 务实红绿重构：**RED 是可信失败信号而非机械新建文件**（现有编译/类型/精准失败测试可直接作 RED；新行为/无复现缺陷先见预期原因失败再写实现；纯文档免测），善用 `run_test(filter=...)`。 |
| 9 | **`quality-gate`** | **新增** | 交付/提审前自检：回读原始目标 + **真实入口 Dogfood（含至少 1 个异常/空状态边界）** + 扫除把核心诉求留给 `TODO/后续再做` 的半成品尾巴 + 检查后 Worktree 卫生核对。 |
| 10 | **`receive-review`** | **新增** | 处理复核反馈：基于代码与测试证据核实（允许对 YAGNI 过度设计做有据澄清）+ **同型缺陷全 diff 一次性清扫（Failure-Mode Sweep：≥2 个同类问题时提炼不变量全量修完）**。 |

---

## 7. 任务 ↔ 会话统一状态同步与任务清单视觉重塑合同

1. **后端批量预取与统一球权投影（零 N+1 查询，单任务接口零缺字段）**：
   - `TaskBoard()` 单次读事务批量拉取任务、最新 `Runs`、`open Questions`、`pending ToolPermissionRequests`、`UnappliedExecutions`（排除 `kind='verify_dir'`），内存 `O(N)` 组装 `pending_permissions`、`open_questions`、`active_children_count`、`attention_children_count` 与 `candidate_verification`；**`taskInTx()`（供单任务详情、会话顶栏胶囊、项目概览使用）必须填充完全一致的 5 项投影字段**；
   - `AskQuestion(blockingScope="task")` 原子写入 `WaitingReason="question"`；`active_children_count > 0` 时父任务保持 `running` 严禁抢跑进 `review`；`BindRunTask` 检测 `completed` 任务的关联会话收到新推进消息时自动推进 `CurrentRevision + 1` 与 `EvaluationGeneration` 平顺重激活，消除 `409 Conflict`。
2. **前端唯一 `taskPhaseMeta` 与 SSE 实时同步**：
   - 删除 `TaskTodoList.tsx` 重复分类器，左侧会话栏、会话顶栏关联任务胶囊、任务清单/看板与详情页 100% 共享唯一分类逻辑与一致色调；SSE 事件（含运行态、权限审批、提问变化）实时触发顶栏胶囊与侧边栏状态标刷新。
3. **任务清单视觉减负与精准跳转（`YarnBall` 轻量层级）**：
   - 移除常态任务行固定渲染的两行重复套话（`当前 / 下一步`），收敛为紧凑两行呼吸感布局；
   - 常态 `1px` 细边框，**仅 `attention`（待授权/待回答）与 `failed` 卡片亮起 `3px` 语义警示左边框并单行直出真实阻塞原因**；
   - `completed / canceled` 默认共同统计并收拢进底部折叠抽屉（`已归档 N 项 ›`）；待批权限任务卡直达对应会话审批卡（含锚点滚动），支持会话顶栏紧凑任务概览与来源消息双向锚点定位。

---

## 8. 跨子系统联动与边界回归防护合同

经全库跨模块扫描（`httpapi/trash.go`、`workspaces.go`、`runs.go`、`workflow/file_tools.go`、`revocation.go`），以下 6 个周边功能受本次架构改造直接波及，须按本合同同步闭环以防回归：

### 8.1 办公附件与交付目录（`$COCOGEM_IO_DIR`）与代码 `Candidate` Worktree 正交分离

- **冲突根因**：现代码 `request.go:462` 将 `WorkingDirectory` 指向 `TaskDirectory`（存放上传附件 `inputs/` 与自动收集交付物 `outputs/`），若代码任务将 `cwd` 切至 `Candidate` worktree，直接混用会导致：① 上传附件相对路径失效；② 生成的导出文件落入代码树污染 Git 状态并破坏 `CandidateVerification`；③ 普通未转任务的对话因 `TaskDirectory == ""` 无法使用 `publish_file` 与自动产物收集。
- **正交方案**：
  - 为每个涉及附件或文件产物的会话/任务分配独立于代码树的宿主文件交换区 `$COCOGEM_IO_DIR/{inputs,outputs}`（位于 `$COCOGEM_DATA_DIR/io/` 下，普通 `Thread` 与 `Task` 通用）；
  - 绑定本地代码项目时，子进程 `cwd` 固定为工作根（普通对话为 `Workspace.Root`，持久任务为 `Candidate`，复核为 `verify_dir`，无论 `workspace_autonomous` 还是 `full_access` 均不覆写为 IO 目录），同时通过环境变量（`COCOGEM_IO_DIR`/`COCOGEM_IO_INPUTS`/`COCOGEM_IO_OUTPUTS`，原生 CLI 与 Codex `shell_environment_policy` 均显式放行）与上下文明确传入 `inputs/`（只读）与 `outputs/`（可写）绝对路径并在沙箱中放行；未绑定代码项目时，子进程 `cwd` 直接指向 `$COCOGEM_IO_DIR`；
  - `collectOutputFiles()` 统一从 `$COCOGEM_IO_DIR/outputs/` 采集交付件，确保办公文档交付与代码目录/Git Worktree 互不污染。

### 8.2 回收站软删保留、误删恢复与到期清理（`trash.go` & `workspaces.go`）

- **冲突根因**：侧边栏删除会话走的是回收站软删除（`thread.DeletedAt`），保留到期后才由 `PurgeExpiredThreads` 物理清理。若软删时立刻销毁 `Candidate` worktree，用户点「恢复」会丢失未应用代码改动；反之若永久删除、到期自动清理或 `deleteWorkspace` 先删 SQLite 记录再反查 execution/roots，会在磁盘和用户主仓 `.git/worktrees` 留下永久孤儿。
- **联动方案**：
  - **移入回收站（`deleteThread`）**：撤销会话持续授权、取消挂起审批，**冻结保留未应用的 `Candidate` worktree**，保证从回收站恢复后未应用代码改动完好无损；
  - **永久删除 / 到期自动清理 / 删除工作空间（`permanentDeleteThread`、`PurgeExpiredThreads`、`deleteWorkspace`）**：**必须先在删库前采集全部关联 execution、主仓路径与 task/thread IO 目录**，删库后同步销毁关联 `Candidate` worktree 与 `$COCOGEM_IO_DIR`，并对原绑定仓库执行 `git worktree prune`。

### 8.3 工作空间更换或清空绑定项目路径（`updateWorkspace` 变更 `Root`）防串台

- **冲突根因**：现代码 `workspaces.go:75` 只要当前无 `running` Run 就允许直接修改 `Workspace.Root`。改路径后若旧会话仍挂着指向上一个仓库的未应用 `Candidate` 或旧仓库 `NativeSessionID`，下一轮对话会写错仓库或跨仓串忆。
- **联动方案**：当 `updateWorkspace` 修改或清空 `Workspace.Root` 时，按工作空间维度（含 `thread_id IS NULL` 的任务级候选）检查是否存在 `unapplied` 状态的 `Candidate` 变更，存在则返回 `409 Conflict` 提示用户先应用、导出分支或丢弃；路径变更提交后，同步将绑定旧 `CanonicalRoot` 的全部 `ExecutionSession` 的索引列 `status` 与 JSON `body` 一并标记为 `sealed`，下一轮在新项目路径下自动开启新原生会话。

### 8.4 移除成员或吊销权限（`revocation.go`）不触碰用户原生 CLI 目录

- **冲突根因**：现代码 `cleanupRevokedNativeSessions` 假定会话文件存在宿主私有目录并执行 `os.RemoveAll`。切回五家 CLI 各自原生目录（`~/.claude`、`~/.codex` 等）后，绝不能去删用户的原生目录文件，但又必须阻止被吊销前的会话（含全部带 `task_id` 的任务级会话）被继续复用。
- **联动方案**：成员移除、禁用或权限收紧时，按 `(workspace_id, member_id)`（覆盖所有普通会话与持久任务级会话）在 Cocogem 自身 SQLite `execution_sessions` 表中将 `status` 索引列与 JSON `body` 硬标记为 `revoked` 并清空 `NativeSessionID`（永不再发起 `--resume`），停用旧私有会话目录物理删除逻辑，**0 字节触碰用户本机 CLI 原生目录**。

### 8.5 点击「停止协作（Stop）」的整进程组清场与残损原生会话自愈

- **冲突根因**：用户中途点停止（`CancelRun`）强杀进程时：① `preview_page(command)`、`run_test`、`run_command`、OpenCode plugin 或 `host_exec` 派生的后台孙进程若未按进程组清理会残留占端口；② 原生 CLI 在生成中途（如 thinking 签名未闭合）被信号中断后，部分供应商下一轮 `--resume` 会返回会话格式损坏 400 错误。
- **联动方案**：所有宿主执行工具（含 `command_project.go`、`run_test`、`preview_page`、`git_inspect`）与原生 CLI（含 OpenCode plugin、`host_exec`）统一启用独立进程组 `Setpgid: true`，取消/超时一律向 `-PGID` 整组清场；强杀中断后的下一轮 `--resume` 若被 CLI 判定会话残损，由第 `3.4` 节 **Continuity Handshake 自动捕获并无感切换新原生会话 + 注入连续性胶囊续跑**，绝不向用户抛错卡死。

### 8.6 退役前置慢速模型预路由（`/routing-preview` 串行往返清理）

- **冲突根因**：现代码发送消息前先串行请求一次大模型 `/routing-preview` 生成 `ProposalID`，原本是为了猜测预设 workflow 与角色槽位；预设死板流程退役后保留该串行往返只会白白增加每条消息 2–5 秒首字延迟。
- **联动方案**：移除发送前的大模型预路由阻塞，前端直接提交 `POST /api/v1/threads/{id}/runs`——显式 `@member` 由后端确定性语法解析毫秒级直达对应成员（单人或多人并行），未显式点名直接秒进会话协调者流式首步。

---

## 9. 分阶段实施路线（6 个全栈自洽垂直阶段）

由于 `cmd/contract` 直接反射 Go 结构体生成 `api/openapi.yaml` 与前端 `schema.ts`，且 `make test` 同时执行 Go 与前端测试，**严禁将后端结构体删改与前端契约同步拆在不同阶段导致中间阶段无法编译**。因此按“每一阶段均保持全栈契约自洽、测试全绿”划分为 **6 个垂直阶段**：

```text
阶段 1：Clean Cutover 统一动态协作内核 + 预设流程与慢速预路由全栈退役（Go + OpenAPI + Web）
   │
   ▼
阶段 2：双层 Worktree + 独立 IO 附件区 + 增量游标/换桌胶囊上下文 + 断点续接 + 回收站/换根联动
   │
   ├──► 阶段 3：全局两档权限 + 单轮「仅规划」锁 + 五家原生目录会话 + AST 零误报沙箱
   │
   ├──► 阶段 4：宿主工程工具链升级（拓宽 run_test + git_inspect + 防 SSRF 版 preview_page + Diff 诊断）
   │       │
   │       ▼
   │    阶段 5：共享技能引擎升级 + 10 项固定核心工程技能落地（依赖阶段 3/4 工具就位）
   │
   └──► 阶段 6：任务 ↔ 会话统一状态同步 + 任务清单视觉重塑 + 全链路真实 CLI 总验收
```

| 阶段 | 主要涉及目录/文件 | 阶段交付闭环目标 |
| --- | --- | --- |
| **阶段 1：统一动态协作内核与预设流程全栈退役** | `internal/store/migrations.go`、`internal/workspace/`、`internal/workflow/{plan,routing,verdict}.go`、`internal/httpapi/{runs,threads}.go`、`cmd/contract/`、`api/openapi.yaml`、`web/src/components/{Composer,Welcome,SettingsDialog}.tsx` | SQLite 清理旧 workflow/assignments；物理删除预设流程代码、`choose_workflow` 与前置慢速 `/routing-preview` 等待，实现单人直达、多人只读并行盲评与 2 轮防乒乓止损；同步运行 `make contract` 并清理前端工作方式选择器、Welcome 旧卡与设置固定槽位，确保全栈编译与契约通过。 |
| **阶段 2：双层 Worktree、上下文生命周期与跨模块联动** | `internal/store/schema.sql`、`internal/execution/manager.go`、`internal/contextplan/`、`internal/workflow/{execution,file_tools,request,step_prepare,session,retry,retry_step,revocation,task_intake}.go`、`internal/httpapi/{trash,workspaces}.go`、`web/src/` | 升级 `workspace_executions` 与 `execution_sessions` 支持普通会话与多分支并行 `git worktree`（含 `$COCOGEM_IO_DIR` 附件/交付正交分离、回收站软删保留与 `Purge`/换根联动）；落地 Reviewer 独立 `verify_dir`、`CandidateVerification` 与前端 Apply/Discard；落地按水位线**纯增量注入（消除 `O(N²)` 重复历史）+ 头 40%/尾 60% 截断 + 超限/残损自动封存换桌胶囊**与步内断点续接。 |
| **阶段 3：全局两档权限、五家原生目录会话与零打扰沙箱** | `internal/workspace/{actions,permission}.go`、`internal/project/permissions.go`、`internal/store/permissions.go`、`internal/nativecli/{capabilities,provider,native,approval,codex_appserver,claude_control,gemini,pi,opencode,native_gate_run,sandbox_*}.go`、`web/src/`、`scripts/verify-redesign.mjs` | 全栈切换为全局 `workspace_autonomous (默认) / full_access` 两档 + Composer 单轮「仅规划」开关与 `Step.ReadOnly` 锁；打通五家 CLI 会话存回各自原生目录与真实 Continuity Handshake 续聊（吊销时仅封存索引不删用户原生目录）；放行 worktree + `/tmp` + 本地回环，落地敏感路径单次豁免、AST 解析与目录子树级跨工具 `ConversationGrant`。 |
| **阶段 4：宿主工程验证与防 SSRF 页面预览配套** | `internal/tools/{registry,runtest,git_inspect,preview_page,diff_hygiene,sandbox_*}.go` | 拓宽 `run_test`（`filter` 单测过滤 + `go/npm/pnpm/pytest/cargo` + `typecheck/lint/build` + Linux `bwrap` 探针，全员独立进程组 `Setpgid` 防停止泄漏）；新增确定性只读 `git_inspect`；实现宿主自建端口 + CDP 非同源硬拦截的 `preview_page`；接入 Diff 兜底分支堆叠提示。 |
| **阶段 5：共享技能引擎与 10 项核心工程技能** | `internal/skills/library.go`、`internal/skills/library_test.go`、`internal/skills/library/**` | 升级受限 YAML frontmatter、`_shared/` 参考层、上下文预算硬门禁（10 个固定技能 ID、单描述 ≤240 字符、正文 ≤90 行）与项目 `.agents/skills/` 安全只读发现；完成全套升级版协调/复核技能与新增核心工程技能及静态校验测试。 |
| **阶段 6：任务 ↔ 会话状态同步、清单视觉重塑与总验收** | `internal/project/{service,control}.go`、`web/src/lib/tasks.ts`、`web/src/components/{TaskBoardPage,TaskTodoList,Sidebar}.tsx`、`web/src/app/App.tsx`、`web/src/styles/project.module.css`、`docs/` | 批量预取零 N+1 组装待批权限、未决问题与子任务状态，修复已验收会话追问重激活；全局唯一 `taskPhaseMeta` + SSE 即时刷新；落地紧凑两行卡片、异常 `3px` 左边单行露因、底部已完折叠抽屉与双向定位；完成全套 E2E、`probe-cli`、`probe-isolation` 真实 CLI 验收并同步 `docs/product.md` 与 `AGENTS.md`。 |

---

## 10. 验收标准清单（Acceptance Criteria）

### 10.1 内核、Worktree 隔离、上下文、权限与跨子系统行为（A01–A30）

| 编号 | 验收场景与必须结果 |
| --- | --- |
| **A01** | **Fresh Start 初始化与误清库防护**：启动时仅在空库或明确旧 `schema_baseline` 时执行重建，遇到 `SQLITE_BUSY`/I/O 错误直接失败不误清库；业务层、OpenAPI 与前端 0 废弃 `workflow`、`assignments`、`choose_workflow` 残留。 |
| **A02** | **普通直写快车道与持久/并行 Worktree 隔离**：① 普通未升级单人对话直写 `Workspace.Root` 且不自动建 Task；② 持久 `Task`（含 `promote_to_task` 升级）与并行 `delegate_task` 子任务各自创建独立 `Candidate` worktree（同步原工作区未提交 tracked/untracked 变更并记录 `base_snapshot_oid`），不触发唯一索引冲突。 |
| **A03** | **主仓 `.git` 元数据保护与孤儿清扫**：沙箱内放行 `<commonGitDir>/objects` 与 `.git/worktrees/<id>` 使 `git status / add / diff` 正常成功，后置锁死 `.git/hooks`、`.git/config`、`config.worktree`、`packed-refs`（含尚不存在的文件）与主仓分支引用；启动恢复与永久删除后主仓 `git worktree list` 无僵尸残留。 |
| **A04** | **多轮候选连续性、Diff 审阅、受影响文件级 Apply 与冲突分支逃生口**：未点 Apply 前同任务第 2 轮修改自动延续第 1 轮 `Candidate`；Apply 前可通过 Diff 接口查看真实统一 Diff；Apply 基于 `base_snapshot_oid` 仅比对受影响文件（互不重叠并行子任务可顺序无冲突 Apply，且兼容 Candidate 内 `git commit` 与外部删除检测），运行中禁止 Apply 并原子写回；原目录行级冲突返回 `409` 且支持一键「导出为本地 Git 分支」；Discard 干净销毁候选且原目录 0 改动。 |
| **A05** | **动态路由与并行只读保护**：`@A` 单人执行、`@A @B` 多人并行独立盲答（本轮草稿互不可见、不自动建 Task、不派生写 Worktree 且自动挂载 `Step.ReadOnly`）、`promote_to_task` 正确绑定 `task_id` 与创建/继承 `Candidate` worktree 均正确工作。 |
| **A06** | **通用变更验证守卫**：仅 `kind="test"` 通过且无后续改写记 `verified`（`typecheck/lint/build` 仅作支持性证据不伪标 `verified`），未测、失败或测后又改文件记 `unverified_mutation`。 |
| **A07** | **Reviewer 独立验证 Worktree 与 `fresh_only` 会话**：禁止派给作者自审（含中转自审）；`reviewer` 与 `critic` 均在派发前物化的独立 `verify_dir`（保留未提交 diff 形态，支持非 Git）内以 `fresh_only` 无痕会话跑命令、测试、反例脚本与预览且不误弹越界审批卡；审结销毁 `verify_dir` 并断言被审树 `TreeDigest` 逐字节不变（若被篡改必须令步骤失败）。 |
| **A08** | **防乒乓收敛上限**：`approved` 匹配候选摘要即刻结束；同链连续 `changes_requested` 满 2 轮后阻止第 3 次返工 `relay` 并如实交付现状与分歧（严禁静默改写为 `approved`）。 |
| **A09** | **单轮「仅规划（Plan-Only）」内核级隔离**：无论全局为 `workspace_autonomous` 还是 `full_access`，开启 `plan_only` 后五家 CLI 原生只读检索与 `git_inspect` 正常可用，`model.Request` 与 OS 内核沙箱强制锁死只读，禁用 `run_test` 与 `preview_page(mode="command")`，越界写操作先判定只读直接返回 `read_only_step`（绝不弹审批卡）；不改变全局 `ExecutionMode` 与 `permission_epoch`。 |
| **A10** | **`workspace_autonomous`（沙箱自主）零误报体验**：工作台与 `TaskDirectory` 内读写文件、进程在 OS 沙箱内使用私有 `$TMPDIR` / `/tmp`（排除 `/tmp/ssh-*`）、运行含 `2>/dev/null`、`jq '//'`、正则、只读调用本机工具链以及 `run_command` / `preview_page` 使用 `localhost:*` 双向回环均 **0 弹窗直接成功**。 |
| **A11** | **`workspace_autonomous` 敏感路径与认证/凭据硬防线**：① 内核默认拒绝 `~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、Keychain（收紧 `mach-lookup`）、浏览器凭据库、`.git-credentials`、`.npmrc`、外部 `.env`/私钥；② 无论 `read/write/execute` 命中敏感路径（含 `~` 展开与引号非规范路径）均触发 `Risk="sensitive_path"` 单次高危审批（禁建持续授权），批准「仅本次允许」后宿主工具与当次沙箱真正放行该单一目标；③ 跨出项目与 `TaskDirectory`（含显式 `/tmp/...` 路径）、`git push`、`sudo`、原生 `Bash` 外部网络准确弹卡；子进程无权凭无认证/单步令牌访问 Cocogem 管理 API。 |
| **A12** | **三档动作层级 + 目录子树级 `ConversationGrant`**：动作层级拆分 `read < write < execute`（批准写绝不放开执行）；审批卡与后端支持 `once / file / directory` 选择，批准目录后 `CheckToolAccess`、`WaitForDecision`、`settleSiblingsUnderGrant` 统一复用子树匹配；禁建 `/`、`$HOME`、`/Users`、顶层 `/tmp`（允许 `/tmp/<subdir>`）。 |
| **A13** | **拓宽版 `run_test` 与 `git_inspect`**：`run_test` 6 个工具链 × 4 种 `kind` 组装正确无重复 `argv`（支持 Go `TestFoo/SubTest`、禁前导 `-` 与 `..` 穿越、macOS 沙箱放行 `ExecRoots` 且保持无网络隔离），Linux 受限 AppArmor / 未实机验证主机返回 `ErrSandboxUnavailable` 失败关闭；`git_inspect` 禁用 hooks/ext-diff/textconv/`core.fsmonitor` 并过滤敏感文件。 |
| **A14** | **`preview_page` 沙箱化、防 SSRF 与真实可观测性**：`mode="static"` 通过 `os.OpenRoot(projectRoot)` 防软链越界；`mode="command"` 按 `execute` 受权限与 OS 沙箱约束且锁死 `cmd.Dir`；CDP 拦截非同源 HTTP、WebSocket 及 `Target.setAutoAttach` 子目标；原生 CLI MCP 桥豁免 20s 强杀；真实返回 `console_errors`、`page_errors`、`AXTree`；渲染失败不存 0 字节图不报 `rendered: true`；Worktree `TreeDigest` 零变化且无残留子进程。 |
| **A15** | **视觉 Gate-0 豁免**：纯后端改动硬豁免截图要求；`browser_engine_unavailable` 时如实披露未渲染验证。 |
| **A16** | **接球验真（Claim Grounding）**：上游 T2 声明与真实文件/测试结果（T0/T1）冲突时，接球方识别 `mismatch` 并带证据拒绝盲目推进。 |
| **A17** | **Diff 兜底堆叠诊断**：基于实际候选累计 diff（覆盖原生 CLI 修改）检出单文件净增 ≥3 层异常兜底分支时产出非阻塞提示（不误报常规 Go `if err != nil` / `return nil`）。 |
| **A18** | **10 项固定技能门禁与上下文预算**：精确包含第 6.2 节 10 个技能 ID、单描述 ≤240 字符、正文 ≤90 行、零死链（含 `_shared/references` 全部文件名可发现）、零品牌名耦合，改动任一技能或 `_shared` 均更新 `SnapshotHash`。 |
| **A19** | **项目本地只读技能安全挂载**：合规 `.agents/skills/` 可被只读发现并计入快照哈希；`.agents` 父目录与子目录符号链接越界、敏感路径及覆盖内置同名技能均被拒绝。 |
| **A20** | **待批权限直通全部任务视图**：挂起 `ToolPermissionRequest` 时，任务清单、`taskInTx` 单任务详情、会话顶栏胶囊与左侧会话栏同步变为“需要你（等待授权）”，点击直接跳转对应会话审批卡（含滚动定位）。 |
| **A21** | **提问状态零分裂**：`AskQuestion` 后列表、顶栏胶囊、侧边栏与详情状态 100% 一致，解答后自动恢复。 |
| **A22** | **父子任务收敛与批量预取零 N+1**：存在未收敛子任务时父任务在任务板、顶栏胶囊和详情页均保持“进行中”不抢跑进“待验收”；`TaskBoard` SQLite 查询次数经实测恒定（且 `UnappliedExecutions` 排除 `verify_dir`）。 |
| **A23** | **已验收会话平顺重激活**：通过真实 HTTP `POST /api/v1/threads/{id}/runs` 向 `completed` 任务的关联会话发新推进指令时，`BindRunTask` 自动演进新 `TaskRevision` 与 `EvaluationGeneration` 激活执行，不再报 `409 Conflict`。 |
| **A24** | **一手来源守卫**：`publish_artifact` 与 `save_claim` 均拦截拿搜索列表页（`lead`）充当已证实结论证据。 |
| **A25** | **实时权限收紧失效**：运行中从 `full_access` 切回 `workspace_autonomous` 时，推进 `permission_epoch`，立即停止不可收紧的越界原生执行并使旧挂起请求失效。 |
| **A26** | **步内断点续接、人工继续解锁与统一连续性胶囊**：成员中途已修改文件后中断时，已改文件保留、流启动已落盘 `NativeSessionID`、`RetryStep` 与 `Resume` 人工点击继续统一传 `humanConfirmed = true` 解锁 `uncertain`，优先 `--resume` 或自动注入 `Continuity Capsule`（≤1.5K tokens 交接单）续跑。 |
| **A27** | **五家 CLI 原生目录会话保留、严格纯增量与无感换桌**：① Claude/Codex/Gemini/Pi/OpenCode 五家全部回归原生目录并打通真实 `--resume` / `thread/resume` / `--session`；② `@A -> @B -> @A` 交错对话时，`A` 在 `resume` 轮跳过静态前缀与已确认消息 ID（彻底不拼回全量 `Shared history`，仅收纯增量），长消息预览采用头 40% + 尾 60% 截断；③ 模拟原生会话超限、Stop 强杀残损或外部改动时，Continuity Handshake 自动封存旧会话、在当次运行内透明重开新会话并注入 `Continuity Capsule` 不报错中断。 |
| **A28** | **附件交付目录与代码目录正交隔离**：无论 `workspace_autonomous` 还是 `full_access`，原生 CLI 与宿主工具在绑定项目时 `cwd` 固定为工作根，`$COCOGEM_IO_DIR/{inputs,outputs}` 通过环境变量透传（含 Codex）且读写正常，代码树/Candidate 保持干净。 |
| **A29** | **回收站软删恢复与硬删/换根联动**：会话移入回收站再恢复后，未应用 `Candidate` 完好保留；彻底删除、到期 `Purge` 或删除工作空间**先采集后删库**并彻底回收 worktree、thread/task IO 目录及执行 `git worktree prune`；存在未应用 `Candidate` 时修改 `Workspace.Root` 返回 `409`，换根后旧会话索引列与 JSON 一并 `sealed`。 |
| **A30** | **成员吊销与手动停止清场**：移除成员按 `(workspace_id, member_id)` 将所有普通会话与任务级会话 `status` 标记为 `revoked` 而不触碰用户原生 CLI 目录；运行中点击 Stop 对 `run_test` / `preview_page` / `run_command` / OpenCode plugin / `host_exec` 及五家 CLI 整进程组 `-PGID` 回收无残留。 |

### 10.2 前端与交互验收（V01–V07）

| 编号 | 视觉与交互检查标准 |
| --- | --- |
| **V01** | Composer 无旧工作方式选择器、无 `/` 模式菜单与旧流程提示文案；底部清晰展示全局两档（沙箱自主 / 完全访问，与设置页实时同步）及单轮轻量「仅规划」开关（切换会话自动复位，运行态禁用）。 |
| **V02** | 欢迎页无旧三模式卡片；深色、浅色、暖色三套主题及桌面（1440×1000）与窄屏（390×844）布局协调无溢出。 |
| **V03** | “设置 → 协作”仅保留默认协调者与未来根任务预算配置（无旧“模式分工”文案）；“设置 → 权限”展示全局两档、Linux 沙箱状态提示与目录级会话持续授权撤销列表。 |
| **V04** | 会话交付条（`CandidateBar`）与任务人工验收弹窗清晰展示 `CandidateVerification` 状态、**可展开的实时统一 Diff 审阅视图**、“应用到项目目录 / 丢弃变更”操作，以及 `409` 冲突时的「导出为本地 Git 分支」逃生操作。 |
| **V05** | 任务清单移除常态两行重复套话，保持紧凑两行呼吸感卡片；常规 `1px` 细边，仅“需要你 / 执行失败”亮 **`3px`** 语义左边并单行露因；`completed + canceled` 共同统计并默认收拢于底部折叠抽屉。 |
| **V06** | 左侧会话栏、会话顶栏任务胶囊、任务清单/看板与详情页四处对同一任务的状态文案与色调 100% 一致，由 SSE 实时驱动刷新（含运行中权限/提问变更）。 |
| **V07** | 会话顶栏点击关联任务可就地查看紧凑概览或进入验收；待批权限任务卡直接跳转关联会话并滚动锚定到审批卡 / 来源消息。 |

### 10.3 标准验证命令

每个工作包完成后须在仓库根目录依次通过：

```sh
make check-contract
make test
make build
make e2e
make verify-redesign
```

涉及原生 CLI 桥接、双层 Worktree 与沙箱边界的改动，额外执行并通过：

```sh
make probe-cli
make probe-isolation
```
