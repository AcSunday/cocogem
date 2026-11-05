# Cocogem 产品文档

本文是当前产品行为、实现边界和验收标准的统一说明。功能以仓库代码为依据；自动化、安装版 CLI 协议检查与真实供应商验证分别判断，不把历史测试记录视为当前环境的保证。快速上手见 [README](../README.md)。

[项目协作内核重构设计](redesign.md) 是目标架构 RFC。本文只记录当前代码已经实现的行为；领域模型、合成模型故障注入、真实 CLI/OS 探测和真实供应商任务评测分别判断，不把 RFC 目标或 fixture 通过当成实机验收。

## 定位与范围

Cocogem 是面向本地单用户的多 Agent 协作项目空间。它调用本机已可用的 Claude Code、Codex、Gemini、Pi 或 OpenCode，让成员围绕持久目标讨论、研究、分析数据或执行受控开发，并保存任务版本、消息、来源、协作回执、验收记录和交付物。

产品以项目、任务和成果为中心：默认展示问题、成员回复及最终成果，执行过程按需展开。用户选择成员与任务目标；平台负责身份、权限交集、有限计划、来源引用、根任务预算及状态恢复，不按 CLI 品牌推定成员能力。

当前不提供多人账户、公网托管、任意扩展或跨项目自动共享。网页不是原生终端的完整替代品，不接管用户已有的交互会话。登录网页抓取、Office 文档和 OCR 不在当前功能范围；CSV 与受限单页文本 PDF 可导入，PDF 加密、多页、对象流及复杂版式会明确拒绝。跨 adapter×OS 的通用原生 shell 隔离、自动集成/部署和定时 Schedule 尚未形成已验证执行闭环；显式 `full_access` 的本机原生命令不等同于已隔离或可完整审计，不按已有持久唤醒能力宣称自动 Schedule 已交付。

## 核心对象

| 对象 | 含义 |
| --- | --- |
| 工作空间 Workspace | 项目边界，包含会话、成员授权、共享技能、执行模式及可选本地资料/工程目录 |
| 任务 Task / 任务版本 TaskRevision | 跨 Run 的工作身份，以及不可变的目标、范围、验收和允许动作版本 |
| 项目记录 ProjectRecord / 记忆 MemoryRecord | 有来源、可见性和不可变版本的有效约束、决定或派生经验 |
| 成员 Agent | 可复用的全局档案，含稳定 ID、名称、handle、职责、能力、CLI、模型和工具允许集 |
| 空间成员 WorkspaceMember | 将全局成员加入空间，单独保存启用状态和工具授权 |
| 会话 Thread | 保存参与者、协调者和有序历史，最多四名参与成员；内部任务线程默认不进侧栏 |
| 协作 Run | 一次用户提交或自动续办，冻结任务版本、成员配置、技能版本、授权和执行策略 |
| 步骤 Step / 尝试 Attempt | 步骤保存职责与依赖；每次执行尝试保存 CLI、连接、进程、会话、上下文及恢复信息 |
| 执行 WorkspaceExecution / 会话 ExecutionSession | 候选目录、基线和租约，以及一次性执行会话的指纹、披露范围和撤销版本 |
| 授权 Grant / 预算 BudgetAccount | 持久授权与撤销 epoch；根任务及其 owned descendants 共用的累计预算账本 |
| 验收 CompletionRecord / 副作用 EffectRecord | 绑定任务版本和证据的逐项验收，以及受管理效果的实际/不确定结果 |
| A2A 消息 / 投递回执 | 不可变任务间消息及逐目标 queued/presented/handled 等观察，不等同于验收完成 |
| 等待 Wakeup / 补充 Inbox / 问题 Question | 持久等待条件与代际、执行边界补充和绑定具体任务版本的问题 |
| 消息 Message | 用户输入与成员输出；长正文独立持久化，页面按需分页 |
| 来源 Source / 数据 DatasetManifest | 已捕获的原件、网页、文件、原生观察、diff、测试或分析证据及其数据集版本 |
| 产物 Artifact / 交付 Delivery | 不可覆盖的产物版本，以及成功 Run 明确交付的消息和版本引用；包含研究图表等类型 |

全局成员可用于多个空间，移除空间关系或删除空间不删除全局档案。旧成员迁移保留 ID，不因同名合并；历史消息、产物、任务版本和 Run 快照不随配置编辑重写。Run 执行成功只说明交付成立；Task 仍需当前必需验收全部满足才能完成，报告反馈不自动等同于任务验收。

## 首次使用与日常操作

1. 启动服务，首次打开网页时设置并确认本地访问密码；之后使用该密码解锁。
2. 直接输入目标。首次输入会在同一事务中创建默认「未命名项目」、会话和可编辑的默认成员档案；不要求先完成成员或项目向导。已有全局成员会复用，不从旧 Run 回填任务或验收。
3. 一个可用 CLI 时沿用该 CLI 的 provider、endpoint、模型与凭据并建立默认成员；检测到多个 CLI 时仍给出可直接使用的默认协调者和其他默认档案，成员配置保持可选。没有可用 CLI 时保留输入草稿并显示安装/连接诊断。
4. 研究目标不需要工程目录；第一次工程任务就地选择目录，目录授予读取意图，不隐含写入。所有新项目首次为 `controlled`，非受控模式必须在「编辑工作空间」或「会话设置」中显式选择。
5. 首次有效提交创建持久 Task 与 TaskRevision；后续补充形成不可变修订。项目首页用可筛选的任务清单展示待办、需要你处理、已验收和全部任务，并展示最近成果与有效项目资料。阅读流式回复、来源、执行过程及最终交付物；可停止协作、补充问题或记录人工验收。

成员创建与编辑不依赖当前是否存在空间，新档案不会自动加入空间。「设置 → 协作」保存默认协调者和各内置流程的角色分工；显式选择优先，默认值只从当前空间已启用且有必要权限的成员中生效，不扩大成员名单和授权。互斥角色必须由不同成员承担。已有会话和运行中的快照不被全局默认值改写。

成员模型为空或 `default` 时沿用本机 CLI 配置。`context_size` 是宿主输入的估算 token 预算，0 表示沿用 CLI 默认，显式值至少 1024 tokens；它只用于宿主限制，不改变 CLI 自身的上下文窗口。已有配置数值保留，但单位按估算 token 重新解释。`thinking_level` 为空沿用 CLI 配置，显式值按适配器校验：

| CLI | 可显式选择的 thinking 级别 |
| --- | --- |
| Claude | low、medium、high、xhigh、max |
| Codex | low、medium、high、xhigh、ultra、max |
| Gemini | low、high |
| Pi | low、medium、high、xhigh、max |
| OpenCode | 从当前模型的原生 variants 动态读取；留空沿用本机或 `provider/model#variant` 配置 |

头像支持本地 PNG/JPEG 上传：最多 512 KiB、单边不超过 2048 像素，检查 MIME、尺寸和完整解码。拒绝远程 URL、SVG 和非法图像；旧内置图案可继续显示与清空，新档案只使用上传图像或默认头像。头像不进入模型提示。

目录可通过浏览器逐层选择，也可输入绝对路径或 `~/` 路径；保存前校验并记录真实绝对路径。取消选择不改原绑定。`--allow-root` 可重复指定，限制可浏览和绑定的范围；未指定时由已解锁的本机用户选择目录。敏感目录、符号链接逃逸和不可读路径被拒绝；活跃协作期间不能更改绑定。

### 项目首页与会话界面

已有工作空间的默认首页是项目概览，而不是快捷场景列表。首页用统一任务清单展示需要你处理的问题、待办、正在推进、已验收和全部任务，支持筛选与暂停/恢复；点击任务进入其工作对话，「新任务」创建新的会话目标，「编辑项目」打开工作空间设置。任务清单与持久 Task 状态联动，不能用手动勾选替代验收。最近成果是已完成验收的任务入口，实际交付仍在会话中按 Delivery 的确切版本查看，不用最新消息时间推断成果。

普通对话的首次有效提交会在同一事务中创建持久 Task 和首个 TaskRevision；同一会话继续补充目标时创建不可变修订，后续独立目标可以新建任务。内部 consultation 和 owned delegation 使用独立任务线程，但不进入普通会话侧栏。任务详情保留目标版本、等待原因、验收状态及关联执行；执行中 Inbox 在安全的执行边界生效，问题卡绑定具体任务版本，旧回答不能误答已修订的问题。

项目级执行模式、持续授权和新建根任务预算默认值集中在「编辑工作空间」中，也可在「新建会话/会话设置」的协调者之前切换权限模式；变更立即更新整个项目工作空间，在途执行按新权限边界停止，历史 Run 保留冻结授权快照。项目首页不再额外堆叠设置卡片，任务页保持任务执行与验收职责。

侧栏提供空间切换、新建会话、标题搜索和会话菜单；菜单支持重命名、移入回收站及工作空间操作。主区显示消息和协作状态，过程摘要可展开，来源、成员、交接任务、产物与事件历史按需查看。

输入区 Enter 发送、Shift+Enter 换行；输入法确认不会误发送，`@` 支持键盘补全。自由协作使用 `@` 和自然语言路由，不提供自动指派开关或数据共享确认门禁。Markdown 成员提及可查看档案提示，但助手正文中的 `@` 不会触发派发。

页面支持浅色、深色、暖色和跟随系统，保存本机外观偏好；支持移动端、键盘操作、焦点反馈及减少动态效果。消息可查看原文和模型信息，长内容分页，报告可打开阅读并导出。

### 回收站与清理

会话先软删除，保留身份及完整历史，可在保留期内恢复。默认保留 7 天，可在全局设置中调整为 1–3650 天；到期由维护任务清理，也可显式永久删除。永久删除不可恢复。

正在运行或仍有待核实归属进程的会话不能删除；删除空间也受活跃协作及进程检查约束。永久删除会清理对应运行、消息、来源、产物、事件和正文，保留独立的全局成员档案。

## 协作方式

### 自由协作

用户可显式 `@` 一个或多个成员，或通过自然语言描述任务与分工。明确收件人优先，歧义请求先澄清；路由预览和提交绑定同一任务及配置，配置过期时重新确认实际计划。

未指定流程和收件人时，协调者在任务计划步骤内决定方法，无需额外分类调用：可以直接回答，通过受控交接自由分工，或调用一次 `choose_workflow` 选择协作讨论、Research 或只读工程分析。选择只能使用本轮名册中的成员，并校验角色和工具；不能改写用户显式流程、已接受计划或已提交交接，不能自行升级为工程开发。

实际方法、选人原因和计划保存在 Run，界面读取持久记录，不按最新回复猜测。全局设置仅提供内置流程的角色默认值，不为自由协作增加手动自动分工控件。

### 内置流程

| 方式 | 执行顺序与交付 |
| --- | --- |
| 协作讨论 | 2–4 人独立观点 → 逐人交叉质疑 → 汇总草稿 → 每位成员检查是否准确表达其修正后的观点 → 协调者交付唯一 note |
| Research | 研究 → 独立质疑 → 研究员逐项回应 → 质疑者复查并保留未决项 → 协调者交付唯一 research_report |
| 工程分析 | 分析 → 独立复核 → 分析员逐项答辩 → 最终复核者交付唯一 code_analysis；全程只读 |
| 工程开发 | 实现与测试 → 独立复核与复测 → 开发者消费确切复核意见、修复并复测 → 最终复核者交付唯一 engineering_report |

工程协作默认选择「分析（只读）」，只有用户显式选择「开发」才进入写入与测试流程。旧 `code_analysis` 工作方式保持只读兼容；历史两步复核计划继续可恢复。

研究员和质疑者、分析员/开发者和复核者不能是同一成员。汇总必须保留真实分歧，不把投票、确认或成员一致当成外部证据。中间草稿和步骤回复不发布正式报告；只有指定的最终步骤承担发布义务。

### 成员交接与共享技能

自由协作使用现有的 scoped MCP bridge，不解析助手正文派发任务：

| 工具 | 契约 |
| --- | --- |
| handoff_task | 提交当前 Run 内的任务，使用真实目标成员 ID、职责、目标与确切上下文引用 |
| get_task | 查询当前 Run 中任务自己的持久状态及后续回传结果 |
| read_skill | 按需读取当前 Run 启用的共享技能和允许的参考文件 |
| read_context / read_source | 分页读取当前步骤已获授权的确切消息、产物版本和捕获来源 |

`handoff_task` 使用发送步骤内稳定的 `request_id` 幂等去重，同一 ID 修改内容会被拒绝。不允许虚构成员、自我委派或跨空间派发。`context_refs` 最多 12 个，必须是完整消息 ID、来源 ID 或 `产物ID@版本号`；短 ID、步骤 ID 和越权引用被拒绝，独立复核也不能引用目标成员自己的作品作为待审对象。

平台接收任务不等于任务完成。当前 CLI 调用结束后，平台运行已提交子任务，依赖收敛后再调用发送者回传成功或失败结果。独立步骤可并行，嵌套委派会延后消费者；每轮最多 12 次交接、32 步、4 层委派。内置流程保持原计划。

共享技能唯一源为 `internal/skills/library/`，随程序嵌入。内置 task-orchestration、task-handoff、independent-review、synthesize-results。启动时提供 bootstrap 与索引，正文和参考文件按需读取，不安装到用户 CLI 配置目录。技能只描述运行时角色、能力及方法，不绑定具体成员、handle、CLI 或模型。

工作空间技能列表为 null 时默认全部启用，空列表表示全部停用。Run 记录启用项、内容版本与快照摘要；`skill_read` 表示返回了内容，不证明模型遵守了全部说明。

## 工具与执行边界

有效工具取全局允许、持久项目 Grant、任务动作、本轮上限和步骤角色的交集。新成员默认只获得只读工具及产物发布工具，工程写入和测试必须额外授权。派发和每次工具调用都会重新检查停用、成员关系、任务 revision 与当前授权 epoch；撤销先关闭新准入并隔离关联会话，再停止执行，无法核实时保留 `revocation_uncertain`。模式切换不重置预算，也不会让旧 Run 扩大授权。

项目在「编辑工作空间」和「新建会话/会话设置」的权限模式入口持久保存 `controlled / project_autonomous / full_access` 三档选择和用户来源。`project_autonomous` 因文件、秘密环境、网络、后代生命周期和 Git 元数据隔离尚无 adapter×OS 正反例实测，当前有效执行继续回退到 controlled；它记录请求模式与未强制约束，但不会获得 native_read/native_write/native_execute。显式 `full_access` 在支持的 Claude 适配器上启用任务已授权范围内的原生 Read/Write/Edit/Bash 与受管 session，并在 Run 中记录无法强制的隔离项；其他适配器仍走受控工具路径，不能据此宣称全平台完全访问。原生效果审计为 partial，不能把提示词、工具列表或工作目录隔离描述成可阻止任意本机 shell 或完整审计。

| 工具 | 行为 |
| --- | --- |
| list_files / read_file / search_content | 在绑定目录或本次候选目录内列出、读取及搜索文本；搜索命中只作导航线索 |
| web_search / fetch_url | 发现公共资料、读取原文并捕获来源；搜索摘要不算已读原文 |
| read_source / read_context / read_task | 读取已授权快照和同一协作域的任务，支持有界分页，不扩大范围 |
| publish_artifact / save_claim / save_memory / save_dataset | 校验来源、生成执行披露范围和持久对象契约；accepted/supported 不自动降低资料可见性 |
| analyze_csv | 只分析已授权的不可变 CSV 原件，执行有界进程内统计并生成安全 SVG 图表与数据集版本，不启动 shell、网络或原文件读写 |
| handoff_task / send_task_message / delegate_task / yield_run | 分别处理 Run 内有界交接、持久 A2A、owned 子任务及有等待条件的执行段让出 |
| write_file / patch_file | 仅获授权的工程开发步骤可用，检查路径、完整文件哈希和内容限制 |
| run_test | 仅获授权的工程开发步骤可用，固定 Go/npm 测试命令，通过支持的 OS 沙箱执行 |

### 原生 CLI

Web 默认每个步骤使用新会话和隔离配置，复用本机 CLI 现有 API Key、endpoint、provider、默认模型和允许的执行配置；不继承无关 hooks、插件、外部 MCP、技能或通用工具权限。受控执行不跨 Attempt 恢复原生会话；显式 `full_access` 仅在适配器提供可核对 resume ACK、指纹与 visibility epoch 未变化时复用后端私有 session，详见下文。模型错误按实际原因报告，不能用要求 CLI 登录代替配置转发诊断。

Claude 在受控模式可按有效授权启用 Read、Glob、Grep、WebSearch、WebFetch；显式 `full_access` 且任务授权 native_write/native_execute 时再加入 Write/Edit/Bash，并以任务范围而非沙箱承诺执行。Codex 不开放原生文件工具，原生合并联网工具要求搜索和读取授权同时存在。Gemini 的流式工具结果不提供完整可捕获正文，保持 MCP 文件/联网；Pi 关闭原生读、写、编辑及 shell，通过受限 MCP 使用宿主工具。OpenCode v2.2.5+ 使用独立临时服务，关闭原生工具及技能发现，通过同一受限 MCP 使用宿主工具；先等待 MCP 连接及工具目录刷新，再开始模型回合。Web 直接订阅独立服务的 `session.text.delta` 增量事件，约每 100 毫秒推送页面；文本块结束时校验完整正文，防止增量重复、缺失或前缀冲突，执行成功事件与有效最终步骤同时成立才算完成。

OpenCode 复用用户配置目录（支持 JSON/JSONC、`OPENCODE_CONFIG`、`OPENCODE_CONFIG_CONTENT` 及连接字段的环境/文件变量）、有效凭据与 `provider/model` 默认选择。Web 每步只复制连接配置和有效凭据到临时目录，不复制用户数据库、会话历史、成员提示、插件或外部 MCP。连接检查冻结实际模型、供应商与端点；`provider/model#variant` 可指定供应商支持的变体，成员思考级别按当前模型声明的 variants 动态显示，可通过 thinking_level 显式选择；选择优先于模型标识中的 #variant，不支持的值在执行前拒绝。未知模型、认证失败、截断与缺失完成结果均按失败处理。

Codex 内置的 MCP 资源助手不等于宿主工具授权。平台不提供资源读取：仅将空资源发现结果或 scoped bridge 明确返回的“不支持该资源方法”视为可继续的反馈，并记录诊断通知，让模型继续使用本步已授权工具。非空资源结果、陌生服务器、未知工具及缺少完成结果的调用仍终止执行。

CLI 原生工具库存由 CLI 强制限制，解析器再核对调用和结果配对，只有匹配的结果可捕获为来源。原生网页结果是 CLI 处理后的观察，不等同于宿主抓取的原始网页；原生网络由 CLI/供应商策略约束，不承诺宿主的 URL/DNS/SSRF 校验覆盖该路径。

「设置 → 服务」区分安装、版本、调用适配、隔离状态及 CLI/模型连接验证时间。版本只作诊断和已知不兼容拦截，关键协议与工具隔离须实际探测。每次尝试及重启恢复前重新检查程序路径、大小、修改时间和 inode；程序变化不能复用旧结论。无法确认隔离时阻止执行并保留检查点。

### 候选执行目录与会话

正式服务的工程写分支通过 SQLite 租约创建独立候选目录：Git 来源使用不共享对象硬链接的本地 clone，非 Git 来源使用有大小、文件数和敏感路径限制的副本，记录基线、树摘要、owner、lease epoch 和实际差异。过期租约或原 owner 未核实停止时拒绝接管，不以 TTL 到期证明进程已退出。目录互斥不是 OS 沙箱；候选结果只记录实际树变化，**不会自动合并、更新用户源目录、部署或证明外部副作用已核实**。

宿主模型与原生 CLI 的 Task Attempt 都持久化独立 ExecutionSession，保存成员/角色/连接/执行目录/授权/上下文指纹、已暴露引用和 visibility epoch。受控执行按 fresh-only 处理：一次性 session ID 不允许跨 Attempt resume。`full_access` 下仅当适配器报告 `session_resume_ack=supported`、Run 指纹与 visibility epoch 未变化且 runner 返回匹配的原生 session 身份时，才在 Cocogem 私有目录复用同一任务/成员/角色/执行分支的 session；没有可核对的 ACK 时仍从工作记录新建，跨线程、成员或 CLI 从工作记录继续。撤销关联资料权限后旧 session 不能继续派生发布。

### 受控工程开发

`write_file` 创建或替换最多 32 KiB 的 UTF-8 文件。已有文件必须提供完整文件 `expected_sha256`；新建要求目标不存在。`patch_file` 需要单独授权，对最多 1 MiB 的已有 UTF-8 文件执行一次精确替换：旧片段非空且恰好匹配一次，新旧片段合计最多 32 KiB，也须匹配完整文件哈希。

写入限定于工程根，拒绝符号链接、硬链接、敏感路径、非普通文件、非 UTF-8、过大内容和过期哈希。重复提交相同修改只在能证明已应用时幂等返回。成功写入生成实际 unified diff 来源；进程内写入与证据读取共享锁，但不提供针对外部编辑器并发写入的完整 compare-and-swap 保证。

开发者要求 write_file 和 run_test；复核者要求 publish_artifact 和 run_test，但不获得 write_file/patch_file。分析动作不需要 run_test，也不提供写入或测试工具。

`run_test` 不启动 shell，只执行固定的 `go test -count=1 ./...` 或 `npm test`，默认 120 秒，上限 600 秒。macOS 使用 deny-default sandbox-exec，限制网络、系统路径、凭据、进程和 AppleEvents；Linux 当前明确阻止未实机验证的沙箱测试，其他不支持的平台也失败关闭。

测试本身仍能改写允许的工程路径。因此工程报告的已验证结论必须同时引用本次 Run 的最新 diff、最后写入之后退出码为 0 的测试，以及测试前后和当前一致的工程树摘要。测试改动交付树、证据缺失或已过期时，只能报告 insufficient_evidence，不能声称开发已验证。

## 来源、上下文与交付

来源保存捕获内容、类型、标识、时间、完整性及截断信息。文件行号、网页正文、原生工具观察、diff 与测试记录各自保留来源性质。搜索结果和未读取的 URL 仅为线索，不能提升成已核实结论；没有公开搜索或原文时明确说明限制。

每个步骤只读取派发时冻结的可见范围，包括必要的共享历史和确切上游产物。未提交内容、失败消息正文、其他会话和未授权兄弟步骤的正文不能进入证据；最近失败轮次中已成功提交的回复仍可作为共享历史上下文，但不自动升级为交付或已验证结论。长消息、来源和产物支持基于确切引用的分页，模型输入预算不截断已保存的原始正文。最近失败、取消或中断的状态可作为诊断上下文，但不是研究依据。

ContextManifest 保存任务版本、scope digest、渲染 digest、真实宿主输入字节数及 inline/reference 条目数，并对相同政策、范围和渲染内容生成稳定复用键；这些值描述可复用条件和实际测量，不代表供应商缓存命中或模型已理解。当前没有基于真实对照任务集的后台摘要质量/成本结论，不默认宣称压缩节省费用。

### 资料、研究与披露

CSV 以内容寻址原件保存，解析最多 5000 行和 32 KiB/单元格，支持行列定位。`analyze_csv` 进一步限制原件不超过 32 MiB，统计有限数值的计数、缺失、非法、最小/最大/求和/均值，图表最多绘制前 100 个数值；统计覆盖所有数据行，图表不是全量散点。数据集 manifest 记录来源、schema、处理步骤和 digest，新版本不覆盖旧结果。SVG 由前端白名单重建，不直接注入原始 SVG/HTML。

文本 PDF 当前只支持恰好一页、非加密、无对象流且能提取文字的文件，保留原件和 `page:1` 定位。其他 PDF、扫描件/OCR、复杂版式和 Office 格式明确报告未支持，不能把空解析当作已读。

Claim、MemoryRecord、ProjectRecord、DatasetManifest、Artifact 与 A2A 正文继承显式证据和生成 Attempt 的全部可证明暴露范围。没有 refs 的改写不是“干净房间”证明；私密派生资料跨 Task、跨协作域或跨角色发布会被拒绝。`handled` 回执只表示有关联回答或处置，不是 Task completed；跨独立根任务的答疑创建请求方预算下的内部 consultation，不重开原任务。

成功 Run 在同一事务中记录最终消息 ID 和确切 `产物ID@版本号`。会话的正式产物列表以该交付记录为准，不按最后时间戳或最新版本猜测。中间草稿保留为可追溯历史；旧记录没有 Delivery 时不推断、不回填，保留原有终态产物可见性。

产物类型包括 note、research_report、code_analysis、engineering_report、data_chart。发现需要相应来源、状态和限制；人工反馈独立保存，不改模型原始正文。导出支持 Markdown 和 JSON，内容对应所选确切版本。

## 状态、长任务与恢复

Run 可处于 queued、running、succeeded、failed、canceled、interrupted，以及新 Task 执行段使用的 yielded。yielded 保存 continuation、等待谓词和已知 effect 状态后释放执行约束，不是成功交付；唤醒创建关联 `continued_from_run_id` 的新 Run。等待登记与当前事实原子核对，事件先到可补配，重复事件、旧 wait generation 和旧 lease 不能推进新任务，重启会扫描未决条件。桌面服务关闭时不执行唤醒，定时 Schedule 尚未提供。

当前使用有界并发调度处理独立任务的 Run，默认服务最多同时执行 4 个，可用 `COCOGEM_MAX_PARALLEL_RUNS` 调整为 1–16；同一 Task、同一 Thread 各最多一个活跃 Run，独立任务和 Run 内独立步骤可并行。默认不设 Run 或步骤累计时长上限，不保证供应商 token、费用和存储有硬上限；可以通过环境变量启用期限。默认等待首个有效响应 10 分钟、无有效进展 15 分钟后告警；默认 warn 只报告，选择 stop 才终止停滞步骤。0 关闭对应检测，非法或负数配置拒绝启动。

根任务及 owned descendants、修订、重试和 consultation 共用一个预算账户。全局默认及项目覆盖为自动续办 20 次、并行分支 4、写分支 2、同问题修复 3 轮、连续无进展失败 2 次；支持任务级版本化调整、持久 reservation、并发准入和有限修复。额度耗尽、预算下调和暂停只阻止相应新准入，不伪造完成，也不靠增加预算复活暂停/取消树。当前账本覆盖 Run、Run 内 Step 和 owned 子 Task；独立后台作业及逃逸后代没有可靠持久计数，不宣称完整并发、副作用审计或硬 token/美元上限。

网络、限流和供应商暂态错误最多自动重试 3 次，即最多 4 次尝试，使用有上限的退避；认证、未知错误、用户取消、期限、结果不确定或已提交交接不能盲目重放。失败尝试和重试原因独立持久化。

服务停止或异常退出后，先核对旧执行租约和进程归属，只处理本平台记录且身份匹配的进程。已完成步骤不重跑，未完成步骤从持久检查点继续；无法核实的结果保留中断并要求人工处理。用户取消的协作不会自动复活。

显式「继续执行」会重新验证 CLI 程序、模型/连接、目录绑定和权限。模型、供应商或服务地址变化时保留检查点并要求新 Run；恢复是新的执行尝试，不等于恢复用户原生交互 session。失败的子任务仍可将缺口回传协调者，整轮保留真实失败状态。

失败或取消的 Run 可以「重新运行」：平台按原接受输入创建新的 queued Run，并用 `retry_of_run_id` 关联来源；不回写或复活原 Run，也不重复用户消息。重试使用当前成员、权限、目录与连接配置，不能补足已失效的成员或授权。重试请求在已有同源重试排队时重复提交会返回原 Run，避免重复排队。

后续追问会带上会话内最近一轮已提交消息；即使该轮最终失败或中断，成员先前已完成并提交的回复仍作为共享历史进入上下文，失败、未完成或其他会话的正文不会进入。完整轮次优先于零散片段，四人讨论在常规长度内可整轮保留；更早的完整成功轮次在有界预算内继续保留，超长内容通过确切 ID 分页读取。

共享历史默认上限为 128 条消息 / 64,000 估算 tokens；成员显式设置 `context_size` 时，其历史预算按该输入 token 预算的 60% 缩放，其余留给当前任务、角色指令、上游交接、来源指针和工具定义。成员未设置 `context_size` 时使用服务默认值，可通过 `COCOGEM_HISTORY_MESSAGES` 和 `COCOGEM_HISTORY_TOKENS` 调整。token 数使用保守估算（ASCII 约 4 字节/token，非 ASCII 字符按 1 token/字符），不保证与供应商 tokenizer 完全一致；单条消息预览默认约 4K tokens，大上下文成员最高约 32K tokens，超出部分仍可通过 `read_context` 读取完整正文。

## 配置与启动

开发需要 Go 1.26 系列且至少 1.26.10、Node.js 22.12+ 和 npm，建议 Node.js 24 LTS。构建后的 Web 服务无需 Node.js；CLI 执行仍依赖本机可执行程序。

```sh
make setup
make dev                 # 前端 127.0.0.1:5173，代理本地服务
make build
./dist/cocogem-server     # 默认 127.0.0.1:8741
```

服务参数：

| 参数 | 默认与用途 |
| --- | --- |
| --listen | 127.0.0.1:8741，只接受回环 IP |
| --web-dir | 优先可执行文件同目录的 web/，否则使用 web/dist |
| --allow-root | 可重复；限制可绑定目录及其子目录 |

环境变量必须在启动时传入，程序不自动加载 `.env`；示例见 [`.env.example`](../.env.example)。

| 变量 | 默认与用途 |
| --- | --- |
| COCOGEM_DATA_DIR | os.UserConfigDir()/cocogem，保存 SQLite 与本地访问密码派生值 |
| COCOGEM_SEARCH_PROVIDER | auto；可选 brave、bing、duckduckgo、public、off |
| BRAVE_SEARCH_API_KEY | 可选，仅用于 Brave 在线搜索，无关模型连接 |
| COCOGEM_WEB_PROXY | 默认直连；env 使用代理环境变量，system 读取 macOS 系统代理，也可指定 HTTP/HTTPS/SOCKS5 地址 |
| COCOGEM_RUN_TIMEOUT / COCOGEM_STEP_TIMEOUT | 0，关闭累计期限；支持 Go duration，如 2h |
| COCOGEM_MAX_PARALLEL_RUNS | 4；同时执行的独立 Run 数，范围 1–16；同一 Task 和 Thread 仍各自串行 |
| COCOGEM_HISTORY_MESSAGES | 128；默认共享历史消息条数，范围 1–1000 |
| COCOGEM_HISTORY_TOKENS | 64000；默认共享历史估算 token 预算，范围 1024–2000000 |
| COCOGEM_FIRST_RESPONSE_TIMEOUT | 10m；0 关闭首响应检测 |
| COCOGEM_STALL_TIMEOUT | 15m；0 关闭无进展检测 |
| COCOGEM_STALL_ACTION | warn；stop 才停止停滞执行 |

auto 优先使用已配置的 Brave，否则回退公共搜索（Bing RSS，再到 DuckDuckGo HTML）；搜索失败明确报告。`make dev` 在未设置代理变量时选择 system，显式空值表示直连。系统代理只读取 host/port，不处理 PAC、订阅或代理凭据；启用原生联网的 CLI 子进程可接收已解析代理，loopback MCP 保持直连并保留原 NO_PROXY。

### 本地访问与数据管理

网页使用首次初始化的密码，长度 12–128 字符。随机盐与 PBKDF2-HMAC-SHA256（600,000 次迭代）的派生值写入私有 auth.json，不保存明文。会话使用 HttpOnly、SameSite=Strict cookie，24 小时或服务重启后失效；变更请求检查 CSRF、Host 与 Origin，连续 5 次密码错误暂停 1 分钟。「锁定工作空间」撤销当前浏览器会话，后台协作继续。

数据目录权限为 0700，auth.json 为 0600。停止服务后整体备份数据目录，恢复前也先停止服务；不要遗漏 SQLite WAL 或密码文件。忘记密码时，停止服务并将 auth.json 移到安全备份位置，再启动重新初始化；数据库保留，网页不提供免密码重置。

本地存储的消息、捕获来源和工程内容可能敏感。模型服务会收到执行所需问题与片段，搜索服务会收到查询；只处理允许分享的数据。完整威胁边界与漏洞报告方式见 [SECURITY.md](../SECURITY.md)。

### 原生命令行

```sh
go run ./cmd/cocogem "你好"                 # 默认 Claude
go run ./cmd/cocogem codex "记住项目名 Cocogem"
go run ./cmd/cocogem codex --resume latest "项目名是什么？"
go run ./cmd/cocogem gemini --resume <session-id> "继续"
go run ./cmd/cocogem opencode "你好"
```

CLI 名支持 claude、codex、gemini、pi、opencode，选择参数和 `--resume` 放在提示词之前。默认新建会话，成功后将真实 session ID 写到 stderr，并以 0600 权限原子更新当前目录 `.cocogem/<cli>.json`。`latest` 只读本目录、本 CLI 的最近成功记录；缺失、损坏或恢复失败时报错，不静默新建，失败不覆盖旧记录。

原生历史由对应 CLI 管理，会话不能跨 CLI 互换。OpenCode 保留 `ses_` 原生会话 ID，其他 CLI 保留 UUID。提示词使用独立 argv 或 stdin 传递，不进行 shell 拼接。终端模式遵守原生 CLI 自身权限，不作为网页执行隔离边界。实现及兼容测试保留在 `internal/cli/`，不提供公共 Go SDK。

## 项目结构

```text
cmd/
  cocogem/          原生命令行入口
  cocogem-server/   本地 Web 服务入口
  contract/        OpenAPI 生成
  checksecrets/    已跟踪文件的凭据扫描
  smoke/           正式服务、持久化和备份恢复检查
  release/         本地候选包与校验和
internal/
  cli/             原生 CLI 参数、事件、会话与进程
  nativecli/       Web CLI 适配、配置转发、受限 MCP、隔离探测
  agent/           内部执行接口与工具循环
  model/           内部模型协议
  workflow/        路由、计划、交接、调度、交付与恢复
  project/         持久任务、授权、预算、资料、A2A 与唤醒领域服务
  execution/       候选目录、执行租约、基线与实际树差异
  contextplan/     上下文清单、预算、分页读取与复用摘要
  workspace/       领域对象、校验和不变式
  store/           SQLite、事务、正文存储与迁移
  tools/           文件、网页、工程写入与沙箱测试
  proc/            进程身份和归属核实
  skills/library/  单一源共享技能
  httpapi/         REST、SSE、本地认证及契约
  testutil/        仅测试使用的合成模型与网页
web/src/
  app/             页面状态、空间与会话交互
  api/             HTTP 客户端及生成类型
  components/      页面组件、表单和阅读器
  lib/             展示与交付辅助函数
  styles/          CSS Modules
  test/            前端测试环境
web/e2e/           浏览器行为测试
api/openapi.yaml   生成的 HTTP 契约
scripts/           开发进程与许可工具
examples/          可公开的样例资料
```

入口保留在 cmd/，实现放在 internal/，测试与实现同目录。后端使用标准库 HTTP、显式依赖注入和 SQLite，前端使用 React、TypeScript、Vite。web/go.mod 只阻止 Go 工具遍历 node_modules，不是独立后端模块。

### HTTP 与生成契约

完整接口以 [`api/openapi.yaml`](../api/openapi.yaml) 为准，前端类型为 [`web/src/api/schema.ts`](../web/src/api/schema.ts)，由 `make contract` 生成，不手改。认证入口为 `/auth/status`、`/auth/setup`、`/auth/login`；其余接口位于 `/api/v1`：

| 分组 | 主要接口 |
| --- | --- |
| 服务与配置 | system、system/verify、system/model-options、directories、settings、auth/logout |
| 空间与成员 | workspaces、agents、workspaces/{id}/members/{agentID} |
| 会话与清理 | threads/{id}、trash、threads/{id}/restore、threads/{id}/permanent |
| 项目与任务 | workspaces/{id}/overview、workspaces/{id}/tasks、tasks/{id}、revisions、dependencies、completions、actions、records |
| 补充与问题 | tasks/{id}/inbox、tasks/{id}/questions、questions/{questionID}/answers |
| 预算与授权 | tasks/{id}/budget、workspaces/{id}/budget-policy、execution-policy、grants、grants/{memberID}/revoke |
| A2A 与唤醒 | tasks/{id}/messages、collaboration-messages/{id}、delegations、handoffs、wakeups、wake-events |
| 资料与记忆 | tasks/{id}/claims、datasets、workspaces/{id}/memories、executions/{id}、effects/{id} |
| 路由与运行 | threads/{id}/routing-preview、threads/{id}/runs、runs/{id}、context、cancel、resume、retry |
| 输出与历史 | runs/{id}/events（SSE）、event-history、messages/{messageID}/content |
| 来源与产物 | sources/{id}、threads/{id}/artifacts、artifacts/{id}/versions/{version} 及 export |

配置修改使用 revision/If-Match 防止过期覆盖，提交与交接有稳定幂等标识。提交结果因断网而未确认时，页面保留原请求与重试入口；重试复用原幂等键，不新建重复 Run。SSE 使用持久事件序号重放，刷新、断线和较晚快照不得丢失较新消息或把终态回退为执行中。API、OpenAPI、生成类型、fixtures 和行为测试必须同步。

## 验证与验收

GitHub Actions 在 push 与 Pull Request 时只运行 test 任务：保留 Go 版本和操作系统矩阵，安装依赖后执行 `make test`。构建、契约、浏览器、安全扫描和候选包命令保留为本地验证入口；工作流不执行打包、发布或部署。

| 命令 | 验证范围 |
| --- | --- |
| make test | Node 脚本、gofmt、go vet、全包 race/shuffle、终端 CLI 集成、前端 Lint/类型/单元测试 |
| make check-contract | 重新生成 OpenAPI 和前端类型，检查漂移 |
| make build | 前端生产包及两个 Go 可执行文件 |
| make e2e | 构建、正式服务 HTTP smoke、持久化/备份恢复及隔离浏览器流程 |
| make verify-redesign | 使用真实开发服务验证首问开箱、持久任务、Inbox 补充、任务预算与项目覆盖不回溯改写既有任务 |
| make probe-cli | 已安装 CLI 对本地假模型和 scoped MCP 的实际协议检查 |
| make probe-isolation | 已安装 CLI 的生产隔离能力检查 |
| make security | 已跟踪文件的凭据检查、Go 可达漏洞扫描及 npm 生产依赖审计 |
| make notices / make release | 第三方许可；本地候选包和 SHA-256，不推送、不发布 |

首次浏览器测试在 web/ 下执行 `npx playwright install chromium`，或用 `PLAYWRIGHT_CHANNEL=chrome make e2e`。浏览器 fixture 服务仅测试启动，不作为产品运行模式。

验收应覆盖以下行为：

- Web 密码与 CLI 凭据独立，既有模型连接配置正确转发，安装与真实连接状态明确区分。
- 全局成员跨空间复用、授权取交集、稳定 ID 迁移、运行快照和历史不被编辑改写。
- 路由歧义不误派发，显式收件人优先，自由协作只经受控 MCP 派发，内置流程保持接受的计划。
- 研究和工程复核读取确切上游成果，分歧与证据缺口进入最终产物，草稿不冒充交付。
- 工程开发显式授权、路径/哈希/内容限制、独立复测与工程树证据成立；只读模式绝不升级。
- 停止、暂态重试、失败重跑、并行会话、重启、显式恢复、权限撤销和不确定进程均保留真实状态，不重复交接或复活已取消任务。
- 长正文完整保存并有界读取，SSE 重放与快照合并正确，回收站恢复/过期清理及数据备份有效。
- Enter/换行、IME、@ 补全、移动端、主题、键盘焦点和可访问性保持兼容。

真实模型验收可使用[公开合成研究样例](../examples/research-scenario.md)，核对约束、过期信息、来源冲突及未决项是否保留。

自动化与合成模型只能证明被测行为。真实 CLI 安装、真实供应商调用、Linux 沙箱、长期运行及公开发布必须按实际环境另行验证；本产品文档不替代这些实机证据。
