# 统一动态协作内核、Worktree 双层沙箱、两档权限重塑、工程技能与任务状态改造

日期：2026-11-13

状态：**需求已确认，待实施合同（已完成独立自审修订）。** 本文件是设计与实现契约，不能单凭本文宣称相关功能已交付或已完成真实平台验证。按 `AGENTS.md` 规定，各工作包完成实现并通过对应检查后，再同步更新 `docs/product.md`、`AGENTS.md` 及标注 `docs/redesign.md` / `docs/2026-11-08-permissions-and-ui-redesign.md` 中的替代关系。

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
| D02 | **采用彻底硬切（Fresh Start, Clean Cutover）**：本项目处于早期阶段，通过物理删除旧 Schema、执行“清零重构”，不再维护旧数据兼容性与迁移脚本。阶段 1 启动时直接重置数据库，保障架构纯粹性，降低维护成本与兼容性故障风险。 |
| D03 | **升级为统一双层 Git Worktree 毫秒级隔离 + 元数据保护与孤儿回收**：将作者工作台（`Candidate`）与 Reviewer 验证台（`verify_dir`）统一升级为位于 `$COCOGEM_DATA_DIR/executions/` 下的 **`git worktree add --detach`**（共享原仓对象池，同步原工作区未提交 tracked/untracked 变更；非 Git 目录回退受界 `file_copy`）；调整 `workspace_executions` 外键与唯一索引以同时支持**普通会话级（Thread-scoped）候选区**与**多子任务并行写分支**；在 OS 沙箱层按“先放行 worktree 元数据、后置覆盖锁死 `.git/hooks`、`.git/config`、`.git/refs/heads/*` 与 `packed-refs`”保护原主仓；并在服务启动与会话/任务删除时自动执行 `git worktree remove/prune` 回收孤儿句柄。 |
| D04 | **下沉硬底座一 —— 多轮候选连续性、变更验证守卫与 Apply/Discard 闭环**：① 同一 Run 内多人并行步（`@A @B`）固定为步骤级只读直连源目录；单写者步（含普通会话快车道及 `promote_to_task` 升级后的接续步）挂载 `Candidate` worktree；② **多轮连续性**：同会话/同任务在 `Candidate` 未应用前自动复用同一 worktree 持续演进，点击「应用到项目目录（Apply）」或「丢弃候选变更（Discard）」终结后，下轮写操作再从最新原目录派生新 `Candidate`；③ 结算自动计算 `CandidateVerification`（`verified` / `unverified_mutation`），Apply 前校验原目录受影响文件基线哈希防覆盖冲突。 |
| D05 | **下沉硬底座二 —— 非作者互斥与 Reviewer 独立 Worktree 验证沙箱**：任何 `role="reviewer" \| "critic"` 独立复核：① 硬性拦截目标成员等于原作者；② **在 `prepareStep` 启动子进程前即刻物化独立 `verify_dir` worktree**，复核会话固定采用 **`fresh_only` 全新会话**（既防销毁临时目录撞碎原生 CLI `--resume`，又保障无旧印象污染的独立视角）；③ 在全局权限模式内向 Reviewer 完整开放 `verify_dir` 下的命令、定向测试、临时反例脚本编写与页面预览；④ **作者 `Candidate` 目录保持物理 0 字节回写**，审结自动清理 `verify_dir` 并断言作者 `TreeDigest` 不变。 |
| D06 | **下沉硬底座三 —— 一手来源托管守卫（Original Source Custody）**：保留 `publish_artifact`（`note`、`research_report`）与 `save_claim` 的来源真实性校验，搜索摘要（`lead`）不能充当已证实结论证据。 |
| D07 | **收敛全局权限为 `沙箱自主（默认） / 完全访问` 两档 + 单轮「仅规划」与步骤级只读锁**：① 移除尴尬的全局 `controlled` 档位，全局仅设 **`workspace_autonomous`（沙箱自主，默认档）** 与 **`full_access`（完全访问）** 两档；② 在 `workspace_autonomous` 下，`Candidate / verify_dir` worktree + `/tmp`（`/private/tmp`）临时复现区 + 本地 `127.0.0.1` 回环端口完全免批自主读写与执行（**日常开发 0 弹窗**），改用 AST 命令解析根除 `2>/dev/null`、`//`、正则与本机只读工具链误报，并对 `~/.ssh`、`~/.aws` 等敏感资产采用**“内核默认硬拒绝 + `Risk="sensitive_path"` 预拦截单次高危审批豁免（禁建持续授权）”**——仅跨界访问宿主其他目录、触碰高敏路径或执行不可逆外部写操作才弹审批卡；③ **“只读”下沉为单轮/步骤级保护**：Composer 提供仅作用于当次发送的轻量 **「仅规划（Plan-Only）」** 开关（配合多人并行步自动只读锁），启用时保留五家 CLI 原生高速只读工具与 `git_inspect`、内核硬禁写文件并直接拦截副作用工具引导输出方案，**绝不推进全局 `permission_epoch`、不误伤后台其他活跃任务**；④ **持续授权（`ConversationGrant`）升级为 `动作层级 + 物理目录子树包含匹配`**。 |
| D08 | **双向交接契约：接球验真（Receive Handoff Grounding）与防乒乓收敛阀**：队友交接描述一律视为 T2 候选声明，执行副作用前在可用工具内用 T0（用户原话/`TaskRevision`/真实文件）或 T1（宿主 `diff/test/image` Source）独立核验；能自查不打回，同一问题点对点 `relay` 返工硬上限 **2 轮**，超限收敛交付分歧或交协调者/`ask_question` 裁决。 |
| D09 | **视觉证据第 0 步分流闸门（Vision Gate-0）与防 SSRF 受控页面探针（`preview_page`）**：纯后端/CLI/算法改动硬性豁免截图要求；新增 `preview_page` 仅连接宿主自建随机 loopback 实例并开启 CDP 非同源请求硬拦截（杜绝访问 Cocogem 自身 API 等本机端口 SSRF），截图直接存为宿主 `Source{Kind: "image"}` 零源码树污染；无浏览器引擎时诚实披露未渲染范围。 |
| D10 | **拓宽宿主受控工程验证工具与 Diff 兜底诊断**：`run_test` 扩展支持 `filter` 单测过滤及 `go/npm/pnpm/pytest/cargo` 与 `typecheck/lint/build`（补齐 Linux 运行态 `bwrap` 可用性探针，兼容 Ubuntu AppArmor 受限 CI 诚实降级），新增确定性只读 `git_inspect`；内置 `AnalyzeDiffHygiene` 对单文件净增 ≥3 层兜底分支附加非阻塞根因核查提示。 |
| D11 | **固定 10 个核心内置技能并严控上下文预算**：支持受限 YAML frontmatter、`_shared/` 参考层与项目本地 `.agents/skills/` 安全只读发现；固定 10 个正交内置技能 ID，单条描述 ≤240 字符（`BOOTSTRAP` 总索引 ≤1.2K tokens），单篇正文 ≤90 行。 |
| D12 | **五轴风险路由替代固定串联流水线**：局部可逆小改走单成员产品快车道（自测 + 变更守卫即交付）；触及状态机/持久化/安全权限/公共契约/不可逆轴时拉起跨成员（优先跨 CLI/模型家族）独立复核；跨阶段大目标升级持久 `Task` 分治推进。 |
| D13 | **统一“球权/注意力”状态同步与任务清单视觉减负**：后端采用批量预取（零 N+1）一次性组装挂起权限、未决问题、子任务状态并修复已验收会话追问重激活；前端统一唯一 `taskPhaseMeta` 与 SSE 实时刷新；任务清单砍掉常态两行重复套话，落地“紧凑两行卡片 + 仅异常态亮 `3px` 左边单行露因 + 底部已完折叠抽屉 + 双向消息定位”。 |
| D14 | **补齐步内断点续接与人工继续解锁（Intra-Step Checkpoint Continuation）**：在保留步间精准续接（`RetryStep`）基础上，打通同一步骤中途失败/超时的断点恢复——① 原生 CLI 流吐出 `session_id` 首事件即刻落盘，同 `Candidate` worktree 下人工继续优先 `--resume` 原生会话；② 无法 `--resume` 时自动注入 `Checkpoint Digest`（当前 worktree 已改文件摘要 + 已完成工具轨迹 + 中断原因）接着做而非推倒重来；③ 机器自动重试遇未知副作用继续停机保护，但**用户显式点击「继续 / 重试该成员」直接解锁基于当前 worktree 现场续跑**，不再抛 `ErrRecoveryUncertain` 卡死。 |
| D15 | **五家 CLI 原生目录会话落盘与全会话跨轮续聊（Native-Home Session Persistence）**：借鉴 `clowder-ai` 做法，不再用临时目录覆盖 `CODEX_HOME / CLAUDE_CONFIG_DIR` 或传 `--ephemeral / --no-session` 丢弃会话——① 打通普通会话（`Thread`）与持久任务（`Task`），Cocogem 数据库按 `(thread_id, member_id, work_root)` 记录 `native_session_id` 与已暴露上下文增量账本，真实会话文件统一交回 Claude、Codex、Gemini、Pi、OpenCode **各自原生管理目录**维护（保住 OAuth 令牌自动续期、Prompt Cache 命中与外部终端直连排查），当次 MCP 与规则仅靠命令行参数隔离；② 沙箱仅精准放行**当前成员所属那一家 CLI 的原生状态目录**；③ 仅一次性 Reviewer 验证副本（`verify_dir`）开启无痕模式（防已销毁临时目录污染原生历史列表），`Candidate` 在 Apply/Discard 切换目录根时自动滚动新原生会话。 |

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

### 3.1 一次性 SQLite 迁移与 Clean Cutover 物理清理

1. **启动迁移（`internal/store/migrations.go`）**：
   - 将未终态旧预设 `Run` 结算为 `canceled`；
   - 将全局权限模式平滑归一至新两档（原 `controlled` 与 `project_autonomous` 统一升级为 `workspace_autonomous`，`full_access` 保持 `full_access`，全新数据库初始默认值为 `workspace_autonomous`）；
   - 重建 `workspace_executions` 与 `execution_sessions` 表：统一新增 `thread_id TEXT NOT NULL REFERENCES threads(id)`，将 `task_id` 改为可空 `TEXT REFERENCES tasks(id)`（使普通未升级对话也能挂载 `Candidate` worktree 并持久化各家 CLI 原生会话映射；调用 `promote_to_task` 升级时原地补填 `task_id` 无缝继承已有 worktree 与全部成员原生会话），并将活跃执行唯一索引调整为 `UNIQUE(COALESCE(task_id, thread_id), canonical_root) WHERE status IN ('active','quarantined','uncertain')`；
   - 清理 `threads`、`runs`、`settings` 中的废弃 `workflow` 与 `assignments` 字段。
2. **代码与契约零残留**：物理删除 `choose_workflow`、`routing.go` 的 workflow 分类器、`plan.go` 四套死板步骤模板，以及 OpenAPI 与前端 schema 中的 `Workflow` 枚举与 `assignments` 字段。

### 3.2 双层 Git Worktree 物理隔离、Seatbelt 规则顺序与孤儿回收

所有派生工作台统一存放在 `$COCOGEM_DATA_DIR/executions/worktrees/` 私有目录下，绝不污染用户项目目录或同级父目录：

```text
用户绑定项目根目录 (Workspace.ProjectPath) —— 始终保持原样，未点 Apply 前 0 字节改动
  └── .git/
       ├── objects/ & worktrees/<curr_id>/  ──► [Seatbelt/bwrap 先置 allow：保障 git status/add/diff 正常锁 index 与写对象]
       └── hooks/, config, packed-refs, refs/heads/* ──► [Seatbelt 后置覆盖 deny / bwrap --ro-bind：硬防篡改主分支与注入恶意 hook]
```

1. **Author Candidate Worktree 创建与脏工作区完整同步**：
   - 执行 `git -C <ProjectPath> worktree add --detach <cand_dir> HEAD`（若目标为非 Git 普通目录，或刚 `git init` 尚无任何提交的 unborn `HEAD` 空仓，自动回退为受界 `file_copy` 隔离快照）；
   - 若用户原项目存在未提交改动，同步应用 `git diff --binary HEAD`（覆盖已跟踪文本与二进制改动），并按 `git ls-files -z --others --exclude-standard` 拷贝非忽略、非敏感的未跟踪新建文件；记录此时受影响工作集快照 `base_tree_digest`。
2. **多轮对话连续性与 Apply / Discard 状态机**：
   - **未应用续写（Continuity）**：同一会话（或同一持久任务）存在活跃未应用的 `Candidate` 时，后续非只读轮次默认复用该 `Candidate` worktree 持续迭代，保留上一轮未应用改动；
   - **应用回原项目（Apply Candidate）**：用户点击「应用到项目目录」时，宿主逐文件校核原 `ProjectPath` 当前状态相对 `base_tree_digest` 未发生冲突改动后原子写回（含新建与删除），将该 `Candidate` 标记为 `applied` 并执行 `git worktree remove --force` 回收；此后新一轮写操作将基于最新 `ProjectPath` 重新派生干净 `Candidate`；
   - **丢弃变更（Discard Candidate）**：用户点击「丢弃候选变更」时，标记 `discarded` 并立即清理该 worktree，原目录毫发无损。
3. **Reviewer Verification Worktree（`verify_dir`）**：
   - 在 `prepareStep` 派发前基于当前 `Candidate` 精确树状态派生独立 `rev-<step_id>` worktree，复核会话固定设为 **`fresh_only`**；
   - 作者 `Candidate.Path` 与原 `ProjectPath` 对 Reviewer 内核级只读（0 字节回写）；审结自动销毁 `rev-<step_id>` 并断言作者 `Candidate` 的 `TreeDigest` 逐字节不变。
4. **崩溃恢复与孤儿 Worktree 清扫**：
   - 服务启动恢复（Startup Reconciliation）以及删除会话/删除任务（`DeleteThread` / `DeleteTasks`）时，扫描 `$COCOGEM_DATA_DIR/executions/worktrees/` 与已终结执行记录，自动移除残留目录并对关联 `ProjectPath` 执行 `git worktree prune --expire now`，确保用户主仓 `.git/worktrees` 零僵尸残留。

### 3.3 步间与步内双层断点续接合同（`retry.go` & `session.go`）

确保某一步报错、网络中断、超时或自动重试耗尽后，用户人工点击继续时**既不重跑已完成队友，也不丢弃该成员已经做完的半程工作**：

1. **步间精准续接（保持并适配动态图）**：`RetryStep` 完整保留已成功的上游步骤结果与产物引用，仅从目标失败/中断步骤重新调度并唤醒受阻下游，且标记 `AutomaticContinuation = false`（人工恢复不受自动续办/修复次数上限阻拦）。
2. **工作区文件现场零丢失**：中途失败或超时不会回滚或销毁作者 `Candidate` worktree，成员中断前已写入磁盘的代码改动完整保留。
3. **流启动即刻持久化 `NativeSessionID` + 优先原会话 `--resume`**：
   - 原生 CLI 流收到首个会话标识事件（如 Claude `system/init`、Codex `thread/started` 等）时，**立即事务落盘 `NativeSessionID`（标记 `started_resumable`）**，不再等整步成功结束才写入；
   - 人工续跑时，只要绑定同一 `Candidate` worktree 路径且指纹/可见性 epoch 未变，优先直接以 `--resume <NativeSessionID>` 唤醒原 CLI 会话接着中断点继续执行。
4. **兜底注入「中断现场摘要（Checkpoint Digest）」**：
   - 若对应 CLI 不支持 `--resume` 或原生会话恢复失败回退至新会话，宿主自动在续跑输入头部注入结构化 `Checkpoint Digest`：**当前 `Candidate` worktree 的已修改/新建文件清单（`git status --short` + `diff --stat`）+ 上次尝试已完成的最后若干条工具调用回执 + 未完草稿尾部摘要 + 具体中断/报错原因**，明确指示成员核对现有文件后接着完成剩余部分，严禁无视现场从零重写。
5. **人工显式继续解锁 `uncertain` 状态**：
   - 后台机器自动重试遇到已发生文件修改或命令副作用的中断（`mutated / uncertain`）时，继续严格停机防盲目重放；
   - 当**用户人工点击「继续 / 重试该成员」**时，视为对当前 `Candidate` 现场的明确人工确认：宿主重新校准当前 `Candidate` 树摘要并放行续跑，不再硬抛 `ErrRecoveryUncertain` 锁死恢复路径（若用户不想要半程脏改动，可先点 Discard 再重跑）。

### 3.4 五家 CLI 原生目录会话保留、增量游标与上下文生命周期合同（`session.go`, `contextplan` & `nativecli/`）

借鉴 `clowder-ai` 的会话原生落盘与增量上下文架构，并与 Cocogem 现有的 `ContextPlan` + `read_context / read_source` 精确分页底座融合：

1. **统一交回各家 CLI 原生目录管理，参数级隔离当次 MCP/规则**：
   - 移除覆盖型临时 `CODEX_HOME`、`CLAUDE_CONFIG_DIR` 及常规轮次的 `--ephemeral / --no-session-persistence / --no-session` 参数（尊重用户自身环境变量配置，默认使用 `~/.claude`、`~/.codex`、`~/.gemini`、`~/.pi` 与 OpenCode 原生存储）；
   - 当次宿主 MCP Server、工具白名单与禁用全局指令（如 `--mcp-config <tmp> --strict-mcp-config --setting-sources ""`、Codex `-c mcp_servers.cocogem...`）全部通过**单次启动参数**注入，零污染用户全局 CLI 配置；
   - **五家 CLI 续聊协议对齐**：Claude（`--resume <id>`）、Codex（app-server `thread/start` & `thread/resume` / exec `resume`）、Gemini（`--resume <id>`）、Pi（`--session` 持久化续接）、OpenCode（`--session <id>`）。
2. **按成员水位线的“纯增量注入（True Delta）”——根除 `O(N²)` 历史重复拼接**：
   - 打通普通对话（`Thread`）与持久任务（`Task`）（解除 `request.go:sessionDeltaRequest` 与 `contextplan` 强依赖 `TaskID != ""` 的限制），按 `(thread_id, member_id, work_root)` 记录每位成员的 `NativeSessionID` 与已确认消费水位线 `AcknowledgedExposedRefs`；
   - **修复现代码 `request.go` 在 `resume` 轮仍重复拼接全量 `Shared history` 的膨胀缺陷**：确认进入 `resume` 时，跳过静态系统引导前缀与该原生会话已签收的历史消息（包含该成员自己的旧回复），**仅注入上次退场后新增的用户消息、其他队友已送达回复（自动剔除已取消/未送达/错误占位消息防上下文污染）及变更的任务约束**。
3. **真实握手验真（Continuity Handshake）与超限封存换桌（Auto-Seal Capsule）**：
   - **不盲信本地绑定记录**：适配器核对 CLI 实际返回的运行时会话回执；仅当确认命中原会话时才走纯增量；
   - **超限/损坏无感换桌**：当长会话内部累积工具输出触及窗口预警/上限（`context_length_exceeded`），或因前轮强行停止导致原生会话损坏（如残缺 thinking 签名被拒）时，宿主自动将旧 `ExecutionSession` 标记为 `sealed/retired`，**透明重开全新原生会话**，并注入一张紧凑的**连续性胶囊（Continuity Capsule：当前活跃 `Candidate` worktree 路径与变更摘要 + 关键进展 + `ContextPlan` 有界状态与 `read_context/read_source` 分页引用）**，实现无限长会话不断忆、不报错卡死。
4. **头 40% + 尾 60% 预览截断（Head-Tail Truncation）**：
   - 将消息与来源摘要截断策略从“仅留头部”升级为 **保留前 40% + `[...省略 N 字节，用 read_context/read_source 翻阅原件...]` + 保留后 60%**（防止截丢位于消息末尾的核心结论与验收条件），并对回复引用内联短预览减少冗余工具往返。
5. **最小权限沙箱放行与一次性审查无痕例外**：
   - OS 沙箱每次**仅放行当前成员所属那一家 CLI 的原生状态目录读写**；
   - 仅 `role="reviewer" | "critic"` 的一次性 `verify_dir` 复核步开启无痕会话（防短期已毁目录堆积在原生历史列表中）；`Candidate` 在 Apply/Discard 切换目录根后自动滚动新原生会话。

---

## 4. 全局两档权限 + 单轮「仅规划」与零打扰沙箱合同（`internal/workspace`, `project`, `nativecli`）

借鉴 `clowder-ai` 将“只读锁”与“全局环境信任度”解耦的设计（避免切换全局只读误伤其他正在运行的任务），权限体系拆分为**全局两档模式**与**单轮/步骤级只读锁**两个正交切面：

### 4.1 全局两档模式边界矩阵（`ExecutionMode`）

| 能力维度 | 1. 沙箱自主（`workspace_autonomous`，**默认档**） | 2. 完全访问（`full_access`） |
| --- | --- | --- |
| **核心定位** | 95% 日常编码、调试、`/tmp` 复现、测试与本地联调主力档（**工作台内 0 弹窗**） | 跨多仓联动改造、安装系统软件、宿主机全局运维 |
| **Candidate / Verify Worktree 内读写与执行命令** | **全自动沙箱放行（0 弹窗）** | 全自动放行 |
| **`/tmp`（`/private/tmp`）与私有 `$TMPDIR` 临时复现** | **直接放行读写与执行（0 弹窗）**（排除系统敏感 socket 如 `/tmp/ssh-*`） | 全自动放行 |
| **本地回环网络（`127.0.0.1` / `localhost`）** | **允许子进程绑定随机本地端口并回环自连**（阻断直连 Cocogem 自身监听端口） | 全自动放行 |
| **公共互联网只读检索与包依赖拉取** | 允许检索与通过受控代理拉取依赖 | 全自动放行 |
| **本机语言工具链只读调用**<br>（`/opt/homebrew`、`/Library/Developer`、`~/.cargo`、`~/.nvm`、`~/.pyenv`、`~/.volta`、`~/.local/share/mise`） | **只读与执行自动放行，试图写入/篡改工具链目录硬拦截** | 全自动放行 |
| **高敏凭据路径**<br>（`~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、钥匙串、浏览器凭据库、外部 `.env`/私钥） | **内核默认硬拒绝 + `Risk="sensitive_path"` 单次高危审批**（禁建持续授权） | 按完全访问执行（硬链接越界保留高危提示） |
| **跨出项目与 `/tmp` 访问宿主机其他目录**<br>（如 `../other-repo`、`~/Documents`） | **弹卡申请**（支持按目录子树授权本会话持续复用） | 全自动放行 |
| **不可逆外部写副作用**<br>（`git push`、公网非只读外部写请求、系统级 `sudo`/全局安装） | **弹卡申请** | 全自动放行 |

### 4.2 单轮「仅规划（Plan-Only）」与内部步骤级只读锁（`RunInput.PlanOnly` / `Step.ReadOnly`）

当用户只想讨论架构、走读代码或先确认方案再动手（或系统自动运行 `@A @B` 多人并行盲评步、恢复对账步）时，通过本轮/本步只读锁实现，**不改变全局 `ExecutionMode`、不推进全局 `permission_epoch`、不影响其他后台会话与任务**：

1. **触发入口**：
   - **手动入口**：Composer 输入栏提供轻量的单轮切换开关 **「仅规划」**（发送该条消息时携带 `plan_only: true`）；
   - **自动入口**：同一 `Run` 内的多人并行盲评步（`len(parallelSteps) > 1`）自动设置 `Step.ReadOnly = true`。
2. **执行期硬保障（零弹窗、零改写）**：
   - 不派生写 `Candidate` worktree，直连源目录（或当前已有未应用 `Candidate`）做只读分析；
   - **保留五家 CLI 原生高速只读工具**（如 Claude `--permission-mode plan`、Codex `--sandbox read-only`、原生 `Read / Glob / Grep`）以及宿主只读工具（`read_file`、`search_content`、`git_inspect`、`preview_page(static)`、`web_search`、`fetch_url`）；
   - OS 内核沙箱强制挂载项目树与外部目录为只读（`(deny file-write*)`，仅放行 CLI 进程必需的最小私有运行缓存）；
   - 若模型在 `PlanOnly / ReadOnly` 轮次尝试调用写文件或副作用命令，宿主与原生门禁**直接返回确定性拒绝提示（不弹审批卡打断用户）**，引导模型基于只读证据输出分析与实施方案。

### 4.3 根治误报弹窗与提升授权灵活性的四项底层改造

1. **标准 Shell AST 解析替代手工切词（重构 `nativecli/approval.go`）**：
   - 引入 POSIX Shell AST 解析器（如 `mvdan.cc/sh/v3/syntax`）提取真实命令节点、重定向文件目标与路径参数；
   - 彻底根除 `2>/dev/null`（放行 `/dev/null`、`/dev/zero`、`/dev/urandom`、`/dev/fd/*`）、`jq '.a // empty'`（字符串字面量不再误判为根目录 `/`）、`awk '/ERR/'` 正则字面量及本机工具链可执行路径导致的假越界弹窗。
2. **敏感路径“内核默认拒绝 + 单次审批豁免”闭环**：
   - 未获批状态下，Seatbelt / `bwrap` 默认对全部高敏凭据路径写入 `(deny file-read* file-write*)` 规则（防复合 shell 绕过偷读）；
   - 当工具或 AST 明确命中高敏路径时，标记 `Risk = "sensitive_path"` 发起高危单次审批（UI 醒目警示且**禁用“本会话持续允许”按钮**）；仅当用户点击“仅本次允许”后，当次专属沙箱 profile 才精准放行该单一目标，执行完立即失效。
3. **`ConversationGrant` 升级为「动作层级 + 物理目录子树包含」**：
   - 授权匹配从 `tool_name == X && scope == exact_file_path` 升级为 **`grant.ActionLevel 覆盖请求动作（read < write_execute） && w.ScopeContains(grant.NormalizedResourceScope, target)`**（基于 `EvalSymlinks` 后的真实物理路径比较）；
   - 批准外部目录 `../other-repo` 后，同会话内宿主工具与原生 CLI 访问该子目录下任意文件均直接命中放行；审批卡对外部路径请求同时提供「仅允许此文件」与「本会话允许所在目录 `<parent_dir>/`」（硬禁将 `/`、`$HOME`、`/Users`、顶层 `/tmp` 存为宽泛目录授权）。
4. **五家原生 CLI 适配器全面对齐（清理 `internal/nativecli/` 全部 63 处旧模式判断）**：
   - 统一更新 Claude（control 协议）、Codex（app-server）、Gemini（gate）、Pi（gate）、OpenCode（HTTP gateway）：按 `Step.ReadOnly` 优先施加只读锁，否则按 `workspace_autonomous`（双层 Worktree + `/tmp` + loopback 沙箱 + 越界回调）或 `full_access`（无沙箱 + 实时 epoch 核验）运行。

---

## 5. 宿主工程配套能力设计（`run_test`、`git_inspect`、`preview_page` 与 Diff 诊断）

### 5.1 拓宽受控检查工具（`run_test`）与确定性只读 `git_inspect`

1. **`run_test` 拓宽 schema**（由宿主按枚举构造确定性 `argv`，零 shell 拼接）：
   - `toolchain`: `"auto | go | npm | pnpm | yarn | pytest | cargo"`
   - `kind`: `"test | typecheck | lint | build"`
   - `target`: 相对子目录或包路径（默认 `"."`）
   - `filter`: 可选单个测试匹配名（严格校验合法字符集 `^[A-Za-z0-9_./:-]+$`，支撑 TDD 单测秒级红绿循环）
   - **Linux 沙箱实测探针（兼容 Ubuntu 24.04 AppArmor CI）**：运行最小无害 `bwrap` 实测探针（而非仅查 `LookPath`）；当宿主机内核启用受限 AppArmor 阻止非特权 userns 时，按 `AGENTS.md` 诚实报告 `linux_bwrap_userns_unavailable` 能力状态并安全回退审批，不伪造沙箱已生效，也不导致 GitHub Actions `ubuntu-latest` 单测意外崩溃。
2. **新增确定性只读 `git_inspect` 工具**：
   - 支持 `subcommand: "status | diff | log | show"` 与受限安全参数（硬编码附带 `--no-ext-diff --no-textconv -c core.hooksPath=/dev/null`），在单轮「仅规划」及所有只读步骤中零风险查看仓库变更与提交历史。

### 5.2 防 SSRF 的宿主受控页面探针（`preview_page`）

1. **零任意地址输入 + CDP 浏览器非同源硬拦截**：
   - 不接受任何 URL 或端口号参数，仅接受绑定项目内的相对入口（`mode="static"` 由宿主起内存 `127.0.0.1:0` 只读静态服务，在 `PlanOnly` 下亦可安全使用；`mode="command"` 在独立进程组启动项目预设预览脚本并注入随机空闲端口，在 `workspace_autonomous` 与 `full_access` 下自动运行，退出时 `kill -PGID` 整组清场）；
   - 无头 Chromium/Chrome 实例启用 CDP 请求拦截器——**仅放行宿主当次创建的唯一 `http://127.0.0.1:<ephemeral_port>` Origin 及 `data:/blob:`**，任何指向 Cocogem 管理端口、其他本机端口或公网的请求一律 `Abort`。
2. **零目录污染**：
   - 截图直接写入宿主附件存储并注册为 `Source{Kind: "image"}`（返回 `source_id` 供 `view_image` 读取真实像素），同步返回 `console_errors`、`uncaught_page_errors` 与 AXTree 摘要；**Worktree 目录 0 文件新增，绝不破坏 `TreeDigest`**；无可用浏览器内核时返回 `browser_engine_unavailable`。

### 5.3 候选 Diff 静态坏味道提示（`AnalyzeDiffHygiene`）

单文件净增 ≥3 层兜底控制流分支（连续新增 `else if`、空值回退或吞错分支）时，在观测与复核摘要中附加非阻塞提示，引导检查根因与状态契约。

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

1. **后端批量预取与统一球权投影（零 N+1 查询）**：
   - `TaskBoard()` 单次读事务批量拉取任务、最新 `Runs`、`open Questions`、`pending ToolPermissionRequests`，内存 `O(N)` 组装 `pending_permissions`、`open_questions`、`active_children_count`、`attention_children_count` 与 `candidate_verification`；
   - `AskQuestion(blockingScope="task")` 原子写入 `WaitingReason="question"`；`active_children_count > 0` 时父任务保持 `running` 严禁抢跑进 `review`；`completed` 任务的关联会话收到新推进消息时自动推进 `CurrentRevision + 1` 平顺重激活，消除 `409 Conflict`。
2. **前端唯一 `taskPhaseMeta` 与 SSE 实时同步**：
   - 删除 `TaskTodoList.tsx` 重复分类器，左侧会话栏、会话顶栏关联任务胶囊、任务清单/看板与详情页 100% 共享唯一分类逻辑；SSE 事件实时触发顶栏胶囊与侧边栏状态标刷新。
3. **任务清单视觉减负（`YarnBall` 轻量层级）**：
   - 移除常态任务行固定渲染的两行重复套话（`当前 / 下一步`），收敛为紧凑两行呼吸感布局；
   - 常态 `1px` 细边框，**仅 `attention`（待授权/待回答）与 `failed` 卡片亮起 `3px` 语义警示左边框并单行直出真实阻塞原因**；
   - `completed / canceled` 默认收拢进底部折叠抽屉（`已验收 N 项 ›`）；支持会话顶栏轻量任务概览与来源/待批消息双向锚点定位。

---

## 8. 跨子系统联动与边界回归防护合同

经全库跨模块扫描（`httpapi/trash.go`、`workspaces.go`、`runs.go`、`workflow/file_tools.go`、`revocation.go`），以下 6 个周边功能受本次架构改造直接波及，须按本合同同步闭环以防回归：

### 8.1 办公附件与交付目录（`$COCOGEM_IO_DIR`）与代码 `Candidate` Worktree 正交分离

- **冲突根因**：现代码 `request.go:462` 将 `WorkingDirectory` 指向 `TaskDirectory`（存放上传附件 `inputs/` 与自动收集交付物 `outputs/`），若代码任务将 `cwd` 切至 `Candidate` worktree，直接混用会导致：① 上传附件相对路径失效；② 生成的导出文件落入代码树污染 Git 状态并破坏 `CandidateVerification`；③ 普通未转任务的对话因 `TaskDirectory == ""` 无法使用 `publish_file` 与自动产物收集。
- **正交方案**：
  - 为每个涉及附件或文件产物的会话/任务分配独立于代码树的宿主文件交换区 `$COCOGEM_IO_DIR/{inputs,outputs}`（位于 `$COCOGEM_DATA_DIR/io/` 下，普通 `Thread` 与 `Task` 通用）；
  - 绑定本地代码项目时，子进程 `cwd` 固定为 `Candidate`（或 `verify_dir` / 只读源目录），同时通过环境变量与上下文明确传入 `$COCOGEM_IO_DIR` 的 `inputs/`（只读）与 `outputs/`（可写）绝对路径并在沙箱中放行；未绑定代码项目时，子进程 `cwd` 直接指向 `$COCOGEM_IO_DIR`；
  - `collectOutputFiles()` 统一从 `$COCOGEM_IO_DIR/outputs/` 采集交付件，确保办公文档交付与代码 Git Worktree 互不污染。

### 8.2 回收站软删保留、误删恢复与到期清理（`trash.go` & `workspaces.go`）

- **冲突根因**：侧边栏删除会话走的是回收站软删除（`thread.DeletedAt`），保留到期后才由 `PurgeExpiredThreads` 物理清理。若软删时立刻销毁 `Candidate` worktree，用户点「恢复」会丢失未应用代码改动；反之若永久删除、到期自动清理或 `deleteWorkspace` 未调执行管理器，会在磁盘和用户主仓 `.git/worktrees` 留下永久孤儿。
- **联动方案**：
  - **移入回收站（`deleteThread`）**：撤销会话持续授权、取消挂起审批，**冻结保留未应用的 `Candidate` worktree**，保证从回收站恢复后未应用代码改动完好无损；
  - **永久删除 / 到期自动清理 / 删除工作空间（`permanentDeleteThread`、`PurgeExpiredThreads`、`deleteWorkspace`）**：同步销毁关联的 `Candidate` worktree 与 `$COCOGEM_IO_DIR`，并对原绑定仓库执行 `git worktree prune`。

### 8.3 工作空间更换或清空绑定项目路径（`updateWorkspace` 变更 `Root`）防串台

- **冲突根因**：现代码 `workspaces.go:75` 只要当前无 `running` Run 就允许直接修改 `Workspace.Root`。改路径后若旧会话仍挂着指向上一个仓库的未应用 `Candidate` 或旧仓库 `NativeSessionID`，下一轮对话会写错仓库或跨仓串忆。
- **联动方案**：当 `updateWorkspace` 修改或清空 `Workspace.Root` 时，若该工作空间下存在 `unapplied` 状态的 `Candidate` 变更，返回明确 `409 Conflict` 提示用户先在对应会话选择「应用或丢弃现有候选变更」（或显式确认丢弃）；路径变更提交后，自动将该工作空间下绑定旧 `CanonicalRoot` 的全部 `ExecutionSession` 标记为 `sealed`，下一轮在新项目路径下自动开启新原生会话。

### 8.4 移除成员或吊销权限（`revocation.go`）不触碰用户原生 CLI 目录

- **冲突根因**：现代码 `cleanupRevokedNativeSessions` 假定会话文件存在宿主私有目录并执行 `os.RemoveAll`。切回五家 CLI 各自原生目录（`~/.claude`、`~/.codex` 等）后，绝不能去删用户的原生目录文件，但又必须阻止被吊销前的会话被继续复用。
- **联动方案**：成员移除、禁用或权限收紧时，仅在 Cocogem 自身 SQLite `execution_sessions` 表中将对应记录硬标记为 `revoked`（永不再发起 `--resume`），停用旧私有会话目录物理删除逻辑，**0 字节触碰用户本机 CLI 原生目录**。

### 8.5 点击「停止协作（Stop）」的整进程组清场与残损原生会话自愈

- **冲突根因**：用户中途点停止（`CancelRun`）强杀进程时：① `preview_page(command)` 或 `run_test` 派生的后台孙进程若未按进程组清理会残留占端口；② 原生 CLI 在生成中途（如 thinking 签名未闭合）被信号中断后，部分供应商下一轮 `--resume` 会返回会话格式损坏 400 错误。
- **联动方案**：所有宿主执行工具与原生 CLI 统一启用独立进程组 `Setpgid: true`，取消/超时一律向 `-PGID` 整组清场；强杀中断后的下一轮 `--resume` 若被 CLI 判定会话残损，由第 `3.4` 节 **Continuity Handshake 自动捕获并无感切换新原生会话 + 注入连续性胶囊续跑**，绝不向用户抛错卡死。

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
| **A01** | **Fresh Start 初始化与零污染**：启动时自动识别数据库为空并执行全套 Schema 初始化；业务层、OpenAPI 与前端 0 旧数据残留；验证启动后无任何废弃 `workflow`、`assignments` 或 `choose_workflow` 逻辑。 |
| **A02** | **双层 Git Worktree、脏工作区同步与多分支并行**：① 普通未升级会话快车道与持久 `Task` 均可挂载 `Candidate` worktree；② 原项目含未提交 tracked 与 untracked 新建文件时，`Candidate` 完整可见；③ 两个并行 `delegate_task` 子任务可在同一绑定项目上同时创建各自独立的 `Candidate` worktree 不触发唯一索引冲突。 |
| **A03** | **主仓 `.git` 元数据保护与孤儿清扫**：沙箱内执行 `git status / add / diff` 正常成功，但尝试修改原主仓 `.git/hooks`、`.git/config`、`packed-refs` 或主仓分支引用被内核拒绝；启动恢复与永久删除后主仓 `git worktree list` 无僵尸残留。 |
| **A04** | **多轮候选连续性与 Apply / Discard 冲突保护**：未点 Apply 前同会话第 2 轮修改自动延续第 1 轮 `Candidate` 内容；点击 Apply 在原目录无冲突时原子写回并回收 worktree，原目录文件已被外部修改时拒绝覆盖并返回冲突路径；点击 Discard 干净销毁候选且原目录 0 改动。 |
| **A05** | **动态路由与并行只读保护**：`@A` 单人执行、`@A @B` 多人并行独立盲答（本轮草稿互不可见且并行步自动挂载 `Step.ReadOnly` 禁写项目目录）、`promote_to_task` 原子继承/挂载 `Candidate` worktree 均正确工作。 |
| **A06** | **通用变更验证守卫**：修改后运行覆盖改动的检查通过则记 `verified`，未测、失败或测后又改文件记 `unverified_mutation`。 |
| **A07** | **Reviewer 独立验证 Worktree 与 `fresh_only` 会话**：禁止派给作者自审；Reviewer 在派发前物化的独立 `verify_dir` 内可跑命令、跑多语言测试、写临时反例脚本；审结 `verify_dir` 已销毁且作者 `Candidate` `TreeDigest` 逐字节不变，越界写 `Candidate` 被硬拦截。 |
| **A08** | **防乒乓收敛上限**：`approved` 匹配候选摘要即刻结束；同链连续 `changes_requested` 满 2 轮后阻止第 3 次返工 `relay` 并如实交付现状与分歧。 |
| **A09** | **单轮「仅规划（Plan-Only）」隔离性**：开启 `plan_only` 发送消息时，五家 CLI 原生只读检索与宿主 `git_inspect` 正常可用，项目目录内核锁死只读，写文件或副作用命令直接返回只读拒绝提示（不弹卡打断用户）；且不改变全局 `ExecutionMode`、不推进 `permission_epoch`、不打断其他后台活跃任务。 |
| **A10** | **`workspace_autonomous`（沙箱自主）零误报体验**：在候选 worktree 与 `/tmp`（`/private/tmp`）内读写复现文件、运行含 `2>/dev/null`、`jq '//'`、正则、只读调用本机 `~/.cargo`/`~/.nvm` 工具链以及绑定本地 `127.0.0.1:0` 端口跑 `httptest` 均 **0 弹窗直接成功**。 |
| **A11** | **`workspace_autonomous` 敏感路径双保险与越界硬拦截**：① 复合 shell 试图偷读 `~/.ssh` 被内核默认拒绝；显式工具访问 `~/.ssh` 触发 `Risk="sensitive_path"` 单次高危审批（禁建持续授权）；② 跨出项目与 `/tmp` 访问 `../other-repo` 或发起 `git push`/外部写请求准确弹卡，且无法直连 Cocogem 自身管理监听端口。 |
| **A12** | **目录子树级 `ConversationGrant` 跨工具复用**：批准“本会话允许所在目录 `/path/to/ext-dir/`”后，同会话内宿主工具与原生 CLI 操作该子目录下多个新文件均直接命中放行；禁止创建 `/`、`$HOME`、`/Users`、顶层 `/tmp` 宽泛授权。 |
| **A13** | **拓宽版 `run_test` 与 `git_inspect`**：`run_test` 支持 `filter` 单测过滤与主流工具链，Linux 运行态 `bwrap` 探针在受限 CI 环境不误崩；`git_inspect` 禁用外部 diff/hook 安全返回仓库状态。 |
| **A14** | **`preview_page` 渲染与防 SSRF**：CDP 拦截器阻断对其他本机端口与公网的请求；生成 `Source{Kind: "image"}`，Worktree `TreeDigest` 零变化且无残留子进程。 |
| **A15** | **视觉 Gate-0 豁免**：纯后端改动硬豁免截图要求；`browser_engine_unavailable` 时如实披露未渲染验证。 |
| **A16** | **接球验真（Claim Grounding）**：上游 T2 声明与真实文件/测试结果（T0/T1）冲突时，接球方识别 `mismatch` 并带证据拒绝盲目推进。 |
| **A17** | **Diff 兜底堆叠诊断**：单文件净增 ≥3 层兜底分支时产出非阻塞提示。 |
| **A18** | **10 项固定技能门禁与上下文预算**：精确包含第 6.2 节 10 个技能 ID、单描述 ≤240 字符、正文 ≤90 行、零死链、零品牌名耦合，改动任一技能或 `_shared` 均更新 `SnapshotHash`。 |
| **A19** | **项目本地只读技能安全挂载**：合规 `.agents/skills/` 可被只读发现并计入快照哈希；符号链接越界与覆盖内置同名技能均被拒绝。 |
| **A20** | **待批权限直通任务状态**：挂起 `ToolPermissionRequest` 时，任务清单、顶栏胶囊与左侧会话行同步变为“需要你（等待授权）”，点击直达对应会话审批卡。 |
| **A21** | **提问状态零分裂**：`AskQuestion` 后列表、顶栏与详情状态 100% 一致，解答后自动恢复。 |
| **A22** | **父子任务收敛与批量预取零 N+1**：存在未收敛子任务时父任务保持“进行中”不抢跑进“待验收”；`TaskBoard` SQLite 查询次数恒定不随任务数线性增长。 |
| **A23** | **已验收会话平顺重激活**：`completed` 任务的关联会话发新推进指令自动演进新 `TaskRevision` 激活执行，不再报 `409 Conflict`。 |
| **A24** | **一手来源守卫**：`publish_artifact` 与 `save_claim` 继续拦截拿搜索列表页（`lead`）充当已证实结论证据。 |
| **A25** | **实时权限收紧失效**：运行中从 `full_access` 切回 `workspace_autonomous` 时，推进 `permission_epoch`，立即停止不可收紧的越界原生执行并使旧挂起请求失效。 |
| **A26** | **步内断点续接与人工继续解锁**：成员在步骤中途已修改 `Candidate` 文件后中断时，已改文件保留、流启动已落盘 `NativeSessionID`、人工点击继续解锁 `uncertain` 并优先 `--resume` 或通过 `Checkpoint Digest` 续跑。 |
| **A27** | **五家 CLI 原生目录会话保留、纯增量防膨胀与超限自动换桌**：① 五家 CLI 会话均保存在各自原生目录，沙箱仅放行当前成员对应 CLI 目录；② `@A -> @B -> @A` 交错对话时，`A` 在 `resume` 轮跳过静态前缀与已签收历史（仅收纯增量，消除 `O(N²)` 重复历史），长消息预览采用头 40% + 尾 60% 截断；③ 模拟原生会话超限或强杀残损时，Continuity Handshake 自动封存旧会话、透明重开新会话并注入连续性胶囊不报错中断。 |
| **A28** | **附件交付目录与代码 Worktree 正交隔离**：在绑定 Git 仓库的普通会话或任务中上传附件并导出交付文件时，附件读取与 `publish_file` 正常走 `$COCOGEM_IO_DIR/{inputs,outputs}`，代码 `Candidate` worktree 保持干净且不误将导出办公文件混入源码 diff。 |
| **A29** | **回收站软删恢复与硬删/换根联动**：会话移入回收站再恢复后，未应用的 `Candidate` 代码改动完好保留；从回收站彻底删除、到期 `Purge` 或删除工作空间后，对应 worktree 与 IO 目录被彻底清理且主仓无残留；存在未应用 `Candidate` 时修改 `Workspace.Root` 返回冲突保护提示，换根后旧会话自动滚动不跨仓串台。 |
| **A30** | **成员吊销与手动停止清场**：移除成员仅将 SQLite `execution_sessions` 标记为 `revoked` 而不删除用户本机 `~/.claude` 等原生目录文件；运行中点击 Stop 整进程组回收 `run_test` / `preview_page` 子进程无端口残留。 |

### 10.2 前端与交互验收（V01–V07）

| 编号 | 视觉与交互检查标准 |
| --- | --- |
| **V01** | Composer 无旧工作方式选择器与 `/` 模式菜单；底部清晰展示全局两档（沙箱自主 / 完全访问，与设置页实时同步）及单轮轻量「仅规划」开关。 |
| **V02** | 欢迎页无旧三模式卡片；深色、浅色、暖色三套主题及桌面（1440×1000）与窄屏（390×844）布局协调无溢出。 |
| **V03** | “设置 → 协作”仅保留默认协调者与未来根任务预算配置；“设置 → 权限”展示全局两档并支持查看与撤销目录级会话持续授权。 |
| **V04** | 会话交付条与任务人工验收弹窗清晰展示 `CandidateVerification` 状态及“应用到项目目录 / 丢弃变更”操作。 |
| **V05** | 任务清单移除常态两行重复套话，保持紧凑两行呼吸感卡片；常规 `1px` 细边，仅“需要你 / 执行失败”亮 `3px` 语义左边并单行露因；已验收/已取消默认收拢于底部折叠抽屉。 |
| **V06** | 左侧会话栏、会话顶栏任务胶囊、任务清单与看板四处对同一任务的状态文案与色调 100% 一致，由 SSE 实时驱动刷新。 |
| **V07** | 会话顶栏点击关联任务可就地查看紧凑概览或进入验收；任务卡片跳转精确回到关联会话及待批权限卡 / 来源消息锚点。 |

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
