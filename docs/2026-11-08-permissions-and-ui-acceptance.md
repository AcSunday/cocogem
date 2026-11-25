# 全局权限与界面改造验收记录

日期：2026-11-08

状态：**本机已验证的交付范围通过验收，服务已启动；保留明确的原生协议与平台能力缺口。** 19 条界面批注、全局权限/预算、宿主审批、数据迁移与关键回归已通过。Codex 原生项目外按次扩权未实现，已关闭该路径并由范围受限的宿主工具承接，不把它记为 A10 的完整原生通过。后续静态核对发现的候选目录限制已在阶段 2 由按作用域 worktree + Apply/Discard 闭环替代，重启恢复与外部请求重定向的限制仍保留，详见下方能力缺口；原有通过记录不能覆盖这些情况。本文不把组件测试、合成模型或安装版检查视为全部平台/供应商验证完成。

执行合同：[改造文档](2026-11-08-permissions-and-ui-redesign.md)。用户已授权 OpenCode 实施，Codex 独立验收并反馈修复。

## 2026-11-11 本机协调者故障复核（变更尚未部署）

- 用部署版本的原始代码、实际工作区路径和只读合成请求复现 Gemini 项目自主步骤启动失败：界面显示缺少终止结果，CLI 实际报告认证方式无效。隔离配置缺少原有认证类型；当工作目录不经过用户配置目录时，原有连接环境文件也无法被 CLI 发现。修复后在隔离目录和合成端点验证了认证配置、冻结连接及任务正文转发。
- 本机 8 个启用成员的真实供应商配置分别完成普通会话、项目自主任务、实际交接并续答，以及临时项目文件修改和字节校验。覆盖 Claude、Codex、Gemini 和 OpenCode；交接的接收成员是确定性测试替身，不能据此推断真实多人协作质量。没有重放用户任务或写入日常数据库。
- 后续使用可重复的 `TestLiveCoordinatorProfiles` 复跑一个 Gemini 配置时，普通会话、项目任务和交接通过；临时文件已修改后供应商返回连接错误，该文件任务最终失败，没有自动重放。随后的独立项目自主只读连接检查通过；原始连接错误的具体原因没有保留，不能把该次失败记为成功。测试由 `COCOGEM_CHECK_LIVE_COORDINATORS=1` 和 `COCOGEM_LIVE_COORDINATOR_PROFILES` JSON 数组显式启用，数组项使用 `name`、`executor`、`model` 及可选的 `thinking`、`description`、`capabilities`，不读取日常数据库。
- 使用同一可重复测试的 `COCOGEM_LIVE_COORDINATOR_PHASE=handoff` 模式复验 8 个配置，全部观察到测试成员交接成功、协调者续答成功，且协调者自己的续答包含测试成员的结果；该模式没有重跑文件操作。
- 路由对故障记录的处理改为模型语义判断（提示词明确「失败/状态记录不是指派」），不再按消息措辞在代码里匹配。用当前 8 位成员的真实供应商配置复验同一条「失败成员=X」的原消息：全部返回 discuss→协调者，没有误派或发送阻断；无法用正文原句核实的模型指派会被丢弃并沿用默认流程，不再以内部错误拒绝发送；只有与成员同句的否定/条件仍会澄清。可重复检查为 `TestLiveRoutingKeepsFailureRecordsOutOfDispatch`（与协调者测试共用配置，本次以三花猫、拿破仑两个配置复跑通过），全程使用隔离夹具，不读写部署数据。
- Pi 1.5.0 虽未分配给当前启用成员，也单独以现有默认连接完成了真实供应商的会话、项目任务、交接和临时文件修改。修复前项目自主路径禁用了内置 MCP，导致无法调用交接工具；修复后安装版测试确认平台工具可调用，项目扩展和其他 MCP 不加载，原生审批拒绝和按次允许仍生效。平台工具的桥接与 Pi 流事件不再重复显示为两次调用。
- 最后一次全仓 `make test` 在 `TestRunNativeToolLoop` 失败：异步 `native_session` 事件插在工具开始和结束事件之间，而该测试要求两者相邻。没有修改该无关测试；其后该测试单独 `-race -count=20` 和整个 nativecli 包的乱序竞态复跑通过，前端 lint、类型检查及 412 项测试单独通过。开发服务冒烟项为保护运行中的实例而过滤。
- 安装版 Gemini 0.67.0 的授权拒绝、允许一次、项目外批准后继续及错误拒绝，以及 OpenCode 2.2.6 的网关与授权回归使用本地合成模型验证；真实供应商调用与合成协议检查是不同证据。
- 当前日常工作区绑定的大目录超过 50,000 项硬链接扫描上限，原生文件和命令效果仍按现有规则拒绝。将绑定目录缩小到实际项目可避免扩大授权范围；本次未更改日常工作区设置，也未把其他系统或超大目录边界记为通过。

## 2026-11-10 跨平台 CI 修复

- macOS profile 测试保留在 Darwin 专属测试中；宿主命令审批在 macOS 验证真实执行，在 Linux 容器验证沙箱不可用时失败关闭、没有文件副作用、失败审计及单次授权不可复用。非 macOS 原生沙箱拒绝初始化也有专用测试。
- macOS 和 Linux 容器的上述定向测试均使用 `go test -race -count=1` 通过。Linux 结果仅证明当前拒绝行为，不代表 Linux 的原生沙箱或受控命令隔离已经实现或实机验证。
- macOS `make test` 全部通过（含 412 项前端测试）；Linux arm64 容器中 Go 1.26.10 的 `go test -race -shuffle=on ./...` 全部通过。GitHub CI 的不同系统和版本结果以对应运行记录为准。

## 2026-11-09 任务页补充

- 根据用户反馈，视图切换移至顶栏右上角；看板桌面内容居中，最大宽度 1280px、列区最大高度 720px。此调整取代下方历史验收中“满宽、满高”的布局描述和测量。
- 日常服务实际页面测量：1920×1200 看板列区为 1280×720px；1247×1219 可见五列；390×844 切换控件高 40px、页面无横向溢出。窄屏为浏览器视口模拟，不代表移动真机验证。
- `PLAYWRIGHT_CHANNEL=chrome npx playwright test e2e/layout-regression.spec.ts` 四项通过，覆盖宽屏限幅、刷新后的视图、360px 控件和长列内部滚动；`e2e/task-workspace.spec.ts` 的任务创建、导航和选择流程通过；组件测试覆盖切换及多选退出。浏览器回归使用隔离 fixture，不作为真实供应商或其他平台验证。

## 基线与改动保护

- 开工 HEAD：`a1496dc2b8616828bcd874b79e4bbe13e4a5b976`。工作区原有大量未提交改动，已保存 539 个版本控制范围内文件的独立基线快照和校验清单。
- 开工前 `make test` 通过：Go 格式、vet、竞态测试；前端 lint、类型检查和 40 文件 / 287 项测试。
- 实施期间不提交混合原有改动，不修改 Git index，不清理用户工作区；测试数据库、原生工具标记、模型 fixture 和截图均位于临时目录。
- 浏览器人工检查使用独立的 `TestBrowserServer`、临时数据库和测试模型。最终启动日常服务时先备份旧库，再应用已授权的一次性迁移；对照备份确认成员、任务、会话、消息、成果和预算账本内容不变。

## 已独立验证的结果

| 范围 | 实际证据 | 证据边界 |
| --- | --- | --- |
| 前端组件与类型 | 初轮 47 文件 / 409 项独立通过；最终统一回归为 47 文件 / 411 项，lint 与类型检查全部通过 | 后续纯文档与验证脚本变更单独检查；最终浏览器旅程另行记录。 |
| 完整统一回归 | 修复竞态和锁顺序后独立 `make test` 通过：全包 Go race/shuffle、vet、格式、Node 脚本、前端 lint/类型及 47 文件 / 411 项测试 | 使用 `GOMAXPROCS=4 GOFLAGS=-p=2` 控制本机资源竞争，保留全部竞态与乱序检查，没有跳过失败用例。 |
| 正式构建与浏览器 | 独立 `PLAYWRIGHT_CHANNEL=chrome make e2e` 通过：正式服务 HTTP、进程退出、备份恢复 smoke 与 33 条 Playwright 旅程 | 包含真实宿主审批端点和实际临时文件效果；浏览器模型仍为 fixture，不冒充真实供应商。 |
| 契约一致性 | 两次独立 `make contract` 均成功，第一次无漂移，第二次结果仍逐字一致 | OpenAPI 与生成 TypeScript 同步。 |
| 权限服务、存储与领域 | `go test -race -shuffle=on ./internal/project ./internal/store ./internal/workspace` 三包通过；另独立跑权限定向竞态回归通过 | 证明服务/存储行为，不等于所有原生适配器已接入。 |
| 宿主审批与原生回调 | `go test -race ./internal/workflow -run 'TestHostApproval\|TestNativeAuthorizer\|TestReviewWorkflow' -count=1 -timeout 45s` 通过，3.685 秒 | 临时文件和宿主/回调行为；真实供应商未因此通过。 |
| 受控命令与操作实际效果 | 修复后独立复跑 `TestControlledApprovedCommandReallyRuns` 和五项 `TestReviewWorkflow` 全部通过；受控命令先审批再生成临时文件，拒绝无效果，预授权不重复询问，失败/并发结算正确 | 关闭了“批准后仍被旧执行器拒绝”的原红测；最终竞态与全量回归仍需统一结果。 |
| Claude 安装版 | `COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1` 下的 AutonomousBoundary、ReadBoundary、HardlinkBoundary 通过，6.545 秒 | 真实 Claude 2.1.318 + loopback 模型；覆盖指定项目内/外操作，不扩大为所有输入与系统均已验证。 |
| Codex 最终原生边界与配置 | Codex 独立复跑 `TestInstalledCodex(AppServer\|PermissionProfile\|Hardlink)` 全部通过，1.839 秒：项目内执行/完全访问准入、明确项目外请求拒绝、隐藏脚本、硬链接、模型/思考强度/连接 header 转发、工具目录收口、无效配置有界失败 | 真实 Codex 0.166.0 + loopback 模型。原生项目外按次范围无法表达，直接拒绝且不消费宿主授权；宿主 MCP 承接。早期 workspace-write 允许例已被这一收口实现替代，不能继续算作原生外部闭环。 |
| Codex 隐藏脚本与不重放 | 隐藏脚本先在项目内追加计数，再读取项目外文件；外部读取被 OS 拒绝，计数恰好一次，无字节泄漏。项目 `.codex/config.toml` 试图放宽静态 profile 仍失败；硬链接读写均拒绝，外部原件不变 | 使用每次随机命名的权限配置、显式根目录 deny、确切项目/任务根和硬链接 deny；没有用路径字符串分类代替 OS 执行边界。 |
| OpenCode 安装版 | 独立运行 PermissionRoundTrip 四例通过，3.474 秒：项目内执行、项目外拒绝、批准后同次继续、完全访问回调核验 | 真实 OpenCode 2.2.6 + loopback 模型；由私有执行前插件和逐次宿主约束执行，不是另开整轮重放。 |
| Gemini 安装版 | 独立运行拒绝、允许一次、项目外批准后同次继续、回调错误拒绝四例通过；范围不匹配例修正预先存在文件的错误断言后独立复跑通过，6.914 秒 | 真实 Gemini 0.67.0 + loopback 模型。范围反例要求新文件不产生且原文件内容不变，不能把“原文件存在”误报成操作已执行。 |
| 真实供应商审批与文件交付 | OpenCode 实现阶段通过后，Codex 最终独立复跑 `TestLivePermissionFlowDefaultAutonomous` 通过，33.312 秒；复用既有 Claude 连接，恰好一次单次决定，真实原生工具与 publish_file 回执，下载 29 字节，SHA-256 为 `48959aa003bcd91b8dd9931ba9daff03f83fa16c7b1e27c54b4c0ef9e3c2179c` | 证明新默认模式、实际授权和不可变交付闭环；不代表另外四种适配器真实供应商任务均已通过。 |
| 任务精准重试 | 真实 RetryStep → Engine.Start → runner → 宿主读取临时字节；仅失败步骤执行一次，完成同伴与原 Run/Step 不变，根预算沿用，重复重试不再执行 | 带 Task 的路径通过。无 Task intake 原红测也已在修复后通过，未创建 Task 或预算。 |
| 人工验收 | 实际浏览器中“去验收”打开独立弹窗，提交确切成功 Delivery 后进入“已验收”，进度 1/1 | 独立临时 Task、合成模型交付；证明 UI/API 人工流程。 |
| 看板长列 | 实际页面同列 22 个任务，卡片自然高度 82px；列 `scrollHeight=1975`、`clientHeight=926`、`overflow-y=auto`，页面无横向溢出 | 1247×1219 深色；其他尺寸/主题分别记录检查，不以该测量覆盖全部视觉要求。 |
| Composer | 实际页面桌面/移动 placeholder 匹配合同；提示浮层可点击显示、移动端不裁切；提示图标最终为 24×24、透明背景、无阴影 | 390px 与 1247px 实际页面检查；IME、补全、键盘另有行为回归。 |
| 移动首页与任务详情 | 移动首页无项目摘要占位；详情停止状态各位置显示“已停止”；无任务设置、内嵌验收或参考来源区域 | 临时数据实际页面；相关专用逻辑与请求另有组件/调用链检查。 |
| 凭据与补丁检查 | `go run ./cmd/checksecrets` 和相关 `git diff --check` 通过 | 针对当时工作树；最终交付前再次检查新增内容。 |
| 非 macOS 编译兼容 | 修复通用函数位置和平台桩接口后，`GOOS=linux GOARCH=amd64 go test -c ./internal/nativecli` 独立交叉编译通过 | 只证明构建兼容；Linux 的原生命令隔离仍明确不可用，不作为真实平台边界验证。 |
| Pi 安装版 | 独立运行五例通过，3.574 秒：项目内自主、拒绝无效果、项目外批准后同次继续、范围不匹配、回调错误拒绝 | 真实 Pi 1.5.0 + loopback 模型，私有 RPC/扩展路径；进程正常结束，不以超时或启动失败当作拒绝证据。 |
| 普通依赖下载 | `COCOGEM_CHECK_NATIVE_DEPENDENCIES=1` 的 `TestNativeSandboxDependencyInstall` 独立通过，2.561 秒 | 实际受约束 npm 包下载，生成临时归档；外部变更方法的允许/拒绝另由本地 TLS handler 测试证明。 |
| 最终 Gemini 兼容与原生回归 | 旧 MCP 配对回归修复后，独立运行 Gemini ToolRegistry、IsolationProbe 与五项新 Gate 测试全部通过，15.561 秒；随后整个 nativecli 包 `-race -count=1` 通过，24.749 秒 | 初次旧路径失败已保留日志；新原生路径和旧受控路径分别验证，没有放松调用/结果身份校验。 |
| 授权等待超时 | 独立运行 `go test -race ./internal/workflow -run 'Test(HumanPermissionWait\|RequestToolAccessWait\|NativePermissionWait)' -count=1 -timeout 60s` 通过，6.288 秒 | 宿主实际临时文件效果、主动申请与原生回调分别等待超过短执行预算；含不暂停的失败对照与取消无效果。真实 bridge 的可暂停预算另有 nativecli 行为测试。 |
| Gemini/Pi 全部根目录与外部 cwd | 独立运行 `go test -race ./internal/nativecli -run '^TestGate' -count=1 -timeout 90s` 通过，1.741 秒 | 两个会话构造器均接入全部实际根的硬链接扫描；扫描未知时拒绝，危险链接不修改外部原件；外部 cwd 批准后同次实际执行，另一未批准路径仍被拒绝。 |
| 第一问与全局设置旅程 | 独立 `node scripts/verify-redesign.mjs` 通过；使用隔离正式服务与已有真实 CLI 配置，覆盖第一问、持久 Task、刷新导航、全局权限与未来根预算、移动摘要及新空间 | 修复旧验证脚本对已删除空间权限/预算和折叠 DOM 的假设；没有连接或修改日常数据库。 |
| 最终收尾检查 | 最后原生变更后整个 nativecli 包 `-race -shuffle=on -count=1` 独立通过，25.417 秒；`go vet ./...`、`make build`、Go 格式、`git diff --check`、`checksecrets` 与 Linux nativecli 交叉编译均成功 | 完整 `make test`/33 条浏览器旅程此前已通过；收尾变更只涉及原生/等待预算及文档，针对受影响范围复验，没有重复无关的真实供应商任务。 |
| 真实旧库与本地服务 | 原库完整性备份后启动 `node scripts/dev.mjs`；5173 页面、8741 `/auth/status` 均 HTTP 200，网页显示“欢迎回来”；全局模式为 `project_autonomous`、epoch 1，迁移标记存在 | 13 张相关表逐行内容指纹与备份一致：7 成员、10 空间关联、27 会话、2 Task/修订/预算账本、58 Run、192 Step、187 Attempt、231 消息、22 成果；设置原预算/分工/保留期未变；SQLite 完整性 ok、外键错误 0。未读取或更改网页密码。 |

## 独立审查发现并已修复的回归

- 验收弹窗冻结目标修订、generation、digest、条件和候选；轮询目标变化后旧判断不能套用新目标。保存期间所有关闭途径受控，迟到回调不会关闭新弹窗。
- 工具事件使用 Run、Step、Attempt、调用身份配对，防止重试 ID 串配；长运行新调用不误报未知，停止后不永远等待授权；待授权操作在外层折叠区之外。
- 审批等待作为同一次工具调用的中间状态，最终不重复计数或留下虚假等待/未知行。
- 提示图标首次真实点击的 focus/click 顺序、全局按钮样式覆盖、看板长列压缩、窄屏头部按钮裁切均已交回 OpenCode 修复并增加行为/浏览器回归。
- 全局设置迟到 GET/PATCH 不回退新修订；同字段 CAS 冲突不静默覆盖；跨会话授权列表不接受旧会话迟到响应；同请求多个界面实例共享决定状态。
- 权限服务校验完整调用身份、参数与工具范围；单次消费持久化；实时 epoch 收紧不延续旧权限；旧空间授权不再作为隐藏设置生效。
- 宿主操作批准后带确切范围执行真实字节，失败不记录成功；并发调用的消费和结算相互独立。

## 逐项复核

- A01–A09：通过存储迁移、权限服务/API、并发竞态、真实宿主批准/拒绝/同会话共享和跨窗口浏览器旅程；授权等待的执行预算暂停已单独复验。
- A10–A11：Claude、OpenCode、Gemini、Pi 的本机安装版路径及宿主操作已验证；Codex 项目内命令、隐藏读取/硬链接拒绝和不重放已验证。Codex 原生项目外按次扩权、原生图片逐调用准入及原生命令联网不记为通过，分别采用宿主范围工具或明确拒绝；没有静默使用完全访问。
- A12–A13：连接转发、Web 独立登录、完全访问与职责上限通过回归；真实供应商闭环有 Claude，以及隔离数据库副本中的 Gemini 受控模式汇总。Gemini 项目自主模式的 SSE 和审批边界使用已安装 CLI 与本地协议夹具验证，不能据此声称其真实供应商闭环或其他平台已验证。
- A14–A19：通过组件、API、33 条浏览器旅程与实际页面复核；任务精准重试、旧历史、不可变下载和来源隔离均有专用反例。
- V01–V06、V15–V16、V19：Composer 提示/placeholder/模式比例、权限按钮、任务视图切换、整卡 hover、满高长列及窄屏点位通过行为和实际页面检查。
- V07–V14、V17–V18：详情状态/时间线/按钮/要求展开、删除任务设置及来源、独立人工验收弹窗通过行为、调用链与实际页面检查。

截图覆盖浅色、深色、暖色看板、桌面/移动权限、提示、清单、批准前后及验收弹窗。移动端证据来自本机浏览器的窄视口和触屏事件；没有把它写成 iOS/Android 真机软键盘验证。

## 能力缺口与未验证范围

1. Codex 0.166.0 的命令审批响应不能表达按次外部目录范围，额外权限 overlay 只有 turn/session，原生 `view_image` 没有逐调用宿主准入。项目内自主时不开放这些扩大路径；原生调用明确拒绝后，模型可使用受统一审批约束的宿主工具。原生项目内命令仍保留，不把这一限制描述为所有适配器的完全原生闭环。
2. 原生 Codex 命令网络保持关闭，公共检索和项目依赖可由宿主工具承接。安装版协议及本机边界通过不代表该适配器真实供应商任务已验证。
3. 非 macOS 实机隔离、另外四种适配器的真实供应商任务、真实公开发布/部署、移动真机软键盘仍为未验证。公开发布/部署的决定和单次效果已用本地 TLS handler 验证，没有对外发布测试内容。
4. 候选目录隔离已在阶段 2 改为按会话/任务作用域的 `git worktree add --detach`：未绑定记录、并行子任务各自成域，未应用前源目录字节不变，Apply 前逐文件核对基线并拒绝覆盖外部修改，Discard 只销毁候选。相应行为由 `internal/execution`、`internal/workflow`、`internal/httpapi/candidates_test.go` 与前端 `CandidateBar` 测试覆盖；仍未在真实多仓大项目与全部平台组合上实测性能与 `git worktree prune` 边界。代码见 [`execution.go`](../internal/workflow/execution.go)、[`manager.go`](../internal/execution/manager.go)、[`candidates.go`](../internal/httpapi/candidates.go)。
5. 后续静态核对发现，重启恢复在已确认进程退出或停止、但结果尚未提交时，仍可能重新排队未完成步骤。此前同次调用内不重放的测试不能证明重启窗口中未记录的原生副作用不会重复。代码见 [`connection_check.go`](../internal/workflow/connection_check.go)、[`recovery.go`](../internal/workflow/recovery.go) 和 [`recovery_settle.go`](../internal/workflow/recovery_settle.go)。阶段 2 已让用户人工「继续」放行对应候选现场并注入检查点摘要，自动恢复仍不在未核实副作用上重放。
6. 已修复（2026-11-25）：宿主外部 HTTP 写入此前只在首次请求前检查 HTTPS 和目标名单，默认客户端会跟随重定向，批准的请求体可被白名单内主机的 307/308 转发到名单外或降级到明文 HTTP。`NewHTTPEgress` 现在复制客户端并禁止自动跟随，重定向响应按失败返回；`TestHTTPEgressDoesNotFollowRedirects` 覆盖跨主机与降级两种情形，修复前该测试失败。仍只用本地 TLS handler 验证，没有对外发布测试内容。代码见 [`egress.go`](../internal/tools/egress.go)。

## 交付与运行

- 服务：[本地工作空间](http://127.0.0.1:5173/)。启动后使用原网页密码解锁；无需修改 CLI 登录。
- 迁移前备份：`/Users/sunday/Library/Application Support/cocogem/backups/before-global-permissions-20261108T123654Z.sqlite`，备份完整性已核验。
- 实际界面截图：`/Users/sunday/.codex/visualizations/2026/11/08/01a1ba17-77ab-79f3-81ba-df3777181be1/cocogem-redesign/`，含 10 张隔离测试环境的真实页面截图，无用户会话内容。
- 详细测试、基线、各 OpenCode 工作包和独立复验日志保存在 `/var/folders/7_/srdsmgns77l_n36f3cb1r0xw0000gn/T/cocogem-redesign-20261108-gbvllu6h/`。没有混合提交原有工作区改动。

## 阶段 2 补充验证（worktree、上下文与续接）

本节只记录阶段 2 的实际验证范围，不把 Go 单元/HTTP 测试或浏览器夹具当作真实供应商或多平台完成。

- 候选隔离：`internal/execution` 覆盖 `git worktree add --detach` 共享对象池、脏工作区同步、同作用域单候选、并行任务各自候选、Apply 写回新建与删除、外部修改 409、Discard 零改动、worktree 与 `prune` 回收、孤儿目录清理；非 Git 目录回退到受界副本并拒绝复制期间源树变化。
- 复核沙箱：`verify_dir` 从作者确切树派生、复核方写入不回流、销毁后作者摘要不变；`internal/workflow` 覆盖复核步骤使用沙箱根与其只读/审批边界。
- HTTP/UI：`internal/httpapi/candidates_test.go` 走完整列表/Apply/409/Discard 旅程；前端 `CandidateBar` 与 `App` 覆盖二次确认、未验证变更呈现、运行期锁定、冲突保留重试，`web/e2e/candidates.spec.ts` 覆盖浏览器点击路径。
- 上下文与续接：真实增量（已确认引用账本）、头 40%/尾 60% 截断、人工继续放行 uncertain、检查点摘要注入均由 `internal/workflow` 行为测试覆盖。
- 生命周期：软删冻结候选、永久删除/Purge/删空间回收、换根 409 与 session seal、撤销只封存 DB 行不触碰用户 CLI 目录，均由 Go 测试覆盖。
- 未验证：真实大仓 worktree 性能、非 macOS 的 `git worktree`/副本边界、五家 CLI 在各自原生目录中的真实跨轮续聊（按计划留到阶段 3/4/6 的真实 CLI 探针）。

## 阶段 3 补充验证（两档权限、仅规划、AST 与原生目录会话）

本节只记录阶段 3 的实际验证范围；真实五家 CLI 的跨轮续聊探针仍按计划单独执行。

- 两档权限：旧 `controlled`/`project_autonomous` 归并为默认 `workspace_autonomous`，与 `full_access` 一起成为唯一两个生效值；Settings PATCH 拒绝其它值，未改变的模式不推进 `permission_epoch`，真实切换在同一事务内推进 epoch 并使旧请求/持续授权失效。Composer 与 Settings → 权限显示同一份全局值；Historic `controlled` 记录按防御性默认映射到 `workspace_autonomous`，不会静默升级为完全访问。
- 仅规划：`RunInput.PlanOnly` 使该轮不派生候选 worktree，单成员步骤自动 `Step.ReadOnly`，原生 `Write/Edit/Bash` 被移除，宿主写文件与副作用命令返回确定性 `read_only_step` 拒绝而不是弹审批卡；全局模式与 epoch 不变。多成员并行扇出仍自动只读。
- 授权层：`ConversationGrant` 从精确文件匹配升级为动作层级 + 物理目录子树；批准目录后该子树内新文件与覆盖动作复用授权，读授权不放开写/执行；`/`、`$HOME`、`/Users`、顶层 `/tmp` 被拒绝存为持续授权。高敏凭据路径可「仅本次允许」但不能建持续授权，UI 同时禁用「本会话持续允许」。
- AST：`internal/nativecli` 引入 `mvdan.cc/sh/v3/syntax` 提取命令与重定向目标；`2>/dev/null`、`jq '//'`、awk 正则与解释器路径不再成为审批范围；解析失败的残损命令回退到保守扫描。宿主 `run_command` 的审批范围同样使用 AST 目标；直接 argv 调用使用路径参数扫描。
- 沙箱：命令沙箱与原生 CLI 沙箱显式放行候选 worktree 的 git 元数据目录、只读挂载 IO `inputs/`、可写挂载 `outputs/`；源仓 `hooks`/`config`/`refs/heads`/`packed-refs` 以写拒绝规则覆盖。
- 原生目录：托管会话使用各 CLI 自己的状态目录；不再用临时 `CODEX_HOME`/`CLAUDE_CONFIG_DIR` 覆盖，也不在常规轮次加 `--ephemeral`/`--no-session-persistence`。一次性 reviewer `verify_dir` 保持无痕。用户设置的 CLI 环境变量优先。
- 未验证：阶段 3 只完成代码与行为测试；真实五家 CLI 的跨轮 `--resume`、OAuth 续期与 prompt cache 命中按阶段 4/6 的真实探针单独验证。Linux `bwrap` 仍未在实机验证，保持失败关闭。

## 阶段 4 补充验证（工程工具链、git_inspect、preview_page、Diff 提示）

- `run_test`：枚举化 argv（`auto|go|npm|pnpm|yarn|pytest|cargo` × `test|typecheck|lint|build`），`filter` 严格字符集并在 `go -run` / `pytest -k` / 包管理器脚本参数中真实生效；`auto` 通过真实 manifest 探测；typecheck/lint/build 输出明确标注"仅支持性证据"。Go 单测过滤实测只运行匹配测试；macOS 真实沙箱仍按既有边界测试；Linux 增加最小无害 `bwrap` 实测探针，受限 AppArmor/userns 报告 `linux_bwrap_userns_unavailable` 并失败关闭，交叉编译不等于验证。
- `git_inspect`：真实仓库实测 `status/diff/log/show`，外部 diff、textconv 与仓库 hook 均未执行；路径穿越、`--exec` 形式参数、任意 revision 与超限 limit 被拒绝；输出为有界 JSON。
- `preview_page`：本机 Chrome 真实渲染成功（截图 7.7KB、标题正确）；对另一回环端口与公网的 fetch 被 CDP 拦截器中止且页面标题未变；静态服务只暴露入口所在子树；项目 `TreeDigest` 前后一致；截图注册为 image `Source`；只读轮次的 `command` 模式确定性拒绝。
- Diff 提示：三层兜底分支触发非阻塞提示；以删除分支为主的清理不触发；注释与普通两分支守卫不误报。
- 未验证：Linux `bwrap` 完整边界（当前仍失败关闭）、非 Chromium 浏览器内核、真实项目 `mode=command` 预览脚本的多框架组合。

## 阶段 5 补充验证（共享技能引擎）

- 固定 10 个内置技能：`task-orchestration`、`task-handoff`、`independent-review`、`synthesize-results`、`file-delivery`、`writing-plans`、`debugging`、`tdd`、`quality-gate`、`receive-review`；`library_test.go` 静态校验数量、`Use when / Not for / Output` 标记、240 字符描述上限、90 行正文上限与 bootstrap 索引预算。
- 受限 frontmatter：支持 `name`、`category`、单行与折叠/字面多行 `description`；未知字段、重复字段、缺失标记和超限均拒绝。
- 跨技能参考层：`library/_shared/references/{claim-grounding,lane-selection,evidence,convergence}.md`，通过 `read_skill(skill_id="_shared")` 读取；`_shared` 不是可启用技能；任一共享文件变更都会改变 `SnapshotHash`。
- 项目技能：`.agents/skills/<id>/SKILL.md` 只读发现，最多 8 个；符号链接目录、敏感路径、非 `SKILL.md` 资源与同名内置技能均被拒绝；项目技能进入 `CatalogueFor`/`SnapshotHashFor`，编辑后快照变化。
- 未验证：项目技能在真实第三方仓库约定下的可用性、非 ASCII frontmatter 的边界组合。

## 阶段 6 补充验证（任务与会话状态同步、清单视觉）

- 任务板零 N+1：`TaskBoard()` 在一次读事务内用固定数量批量查询组装任务、最新修订、账本、完成记录、最新 Run、未决问题、待批授权与未应用候选；新增 `pending_permissions`/`open_questions`/`active_children_count`/`attention_children_count`/`candidate_verification` 投影，`TaskView` 不再逐任务读取。
- 提问状态同步：`AskQuestion(blockingScope="task")` 在同一事务写入 `WaitingReason="question"`；解答最后一个未决问题后恢复 ready，仍有其它问题则保持等待。
- 父子收敛：进行中的子任务使父任务保持进行中（`taskPhase` 不再因父任务自身 Run 成功而进入待验收）。
- 平顺重激活：已验收任务的关联会话收到新的推进消息时推进目标修订并重新激活，历史验收记录保留。
- 前端唯一分类器：删除 `TaskTodoList.tsx` 的重复分类器，列表/看板/详情/会话胶囊共享 `taskPhaseMeta`；待批授权与未决问题统一显示「需要你」，胶囊在 SSE 运行状态变化时即时刷新并保留 30 秒兜底。
- 清单视觉：删除常态行的「当前/下一步」两行套话，收敛为标题 + 来源/负责人/子任务两行；仅 `attention`/`failed` 行使用 3px 语义左边框并单行直出真实阻塞原因；已验收/已取消默认收进底部「已验收 N 项 ›」抽屉。
- 未验证：真实供应商长列表下的滚动性能与移动窄屏布局仍只做了本机浏览器检查，未做 iOS/Android 真机验证。

### 阶段 6 真实 CLI 验收发现并修复的回归

- `make probe-cli` 暴露 `TestInstalledCodexAppServerInProjectApproval` 失败：Codex app-server 把每个原生命令包装为 `/bin/zsh -lc "<command>"`，阶段 3 引入的 AST 分类器把整段引号内脚本当成一个超长路径参数，项目内重定向被判为区间外目标并在宿主回调前直接拒绝。
- 二分定位：`84a9839` 通过，`3d6c299` 起失败；与本机 CLI 版本无关。
- 修复：`shellast.go` 识别 shell 解释器，递归解析 `-c` 脚本（`-lc`/`-ec` 等聚簇、`-o` 取值、`--` 终止，深度上限 3），对无法静态求值的脚本保留保守 token 扫描；新增 `TestShellASTParsesInterpreterScripts` 与深度上限测试。
- 复验：`make probe-cli` 全部通过（Claude/Codex/Gemini/Pi/OpenCode），`go test ./internal/nativecli ./internal/tools` 通过。
- 门禁：`make check-contract`、`make test`、`make build`、`make e2e`（37/37）、`make verify-redesign` 均通过。`make verify-redesign` 在整套 e2e 之后曾出现一次不可复现的 Node 异常退出，随后同序列连续 4 次通过，未定位到稳定复现路径，按偶发记录而不视为已修复缺陷。

### CI 环境暴露并修复的三处问题

- `test (stable, ubuntu-latest)`：间接依赖 `github.com/go-json-experiment/json` 的旧伪版本引用当前标准库已不存在的符号。升级到 `v1.27.2`（其 `go.mod` 仍声明 1.26）；`1.26.x` 与 `stable` 两套工具链的 `-race -shuffle=on` 全量测试均通过。
- `preview_page` 被通用工具注册表统一限制在 20s，冷启动浏览器在负载较高的 Linux runner 上耗尽预算后报 `css.EnableParams: context deadline exceeded`。现在注册表让 `run_test`/`run_command`/`preview_page` 各自使用经过校验的显式超时（`toolBudget`），浏览器冷启动另有独立上限，不再占用页面渲染预算。
- `TestProjectQuestionAnswerAndBudgetContinuationHTTPJourney` 在预算账本并发落定时对 `If-Match` 读到过期 revision 而得 409。409 是设计行为，测试改为重新读取当前 revision 后重试，并连续重复运行验证。
- 修复后四个矩阵组合（1.26.x / stable × ubuntu / macOS）全部通过。
