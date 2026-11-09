# 全局权限与界面改造验收记录

日期：2026-11-08

状态：**本机已验证的交付范围通过验收，服务已启动；保留明确的原生协议与平台能力缺口。** 19 条界面批注、全局权限/预算、宿主审批、数据迁移与关键回归已通过。Codex 原生项目外按次扩权未实现，已关闭该路径并由范围受限的宿主工具承接，不把它记为 A10 的完整原生通过。后续静态核对还发现候选目录、重启恢复及外部请求重定向的限制，详见下方能力缺口；原有通过记录不能覆盖这些情况。本文不把组件测试、合成模型或安装版检查视为全部平台/供应商验证完成。

执行合同：[改造文档](2026-11-08-permissions-and-ui-redesign.md)。用户已授权 OpenCode 实施，Codex 独立验收并反馈修复。

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
4. 后续静态核对发现，会话提交时创建工程候选目录仍依赖历史成员工具数组，而新建成员的该数组为空；未建立候选目录时执行根可能回退到绑定源目录。此前的候选目录和权限测试不能证明所有可写任务都与源目录隔离。代码见 [`runs.go`](../internal/httpapi/runs.go)、[`execution.go`](../internal/workflow/execution.go)。
5. 后续静态核对发现，重启恢复在已确认进程退出或停止、但结果尚未提交时，仍可能重新排队未完成步骤。此前同次调用内不重放的测试不能证明重启窗口中未记录的原生副作用不会重复。代码见 [`connection_check.go`](../internal/workflow/connection_check.go)、[`recovery.go`](../internal/workflow/recovery.go) 和 [`recovery_settle.go`](../internal/workflow/recovery_settle.go)。
6. 后续静态核对发现，宿主外部 HTTP 写入只在首次请求前检查 HTTPS 和目标名单；默认客户端可能跟随重定向，后续目标未重新检查。此前本地 TLS handler 的允许/拒绝测试不覆盖该重定向边界。代码见 [`egress.go`](../internal/tools/egress.go)。

## 交付与运行

- 服务：[本地工作空间](http://127.0.0.1:5173/)。启动后使用原网页密码解锁；无需修改 CLI 登录。
- 迁移前备份：`/Users/sunday/Library/Application Support/cocogem/backups/before-global-permissions-20261108T123654Z.sqlite`，备份完整性已核验。
- 实际界面截图：`/Users/sunday/.codex/visualizations/2026/11/08/01a1ba17-77ab-79f3-81ba-df3777181be1/cocogem-redesign/`，含 10 张隔离测试环境的真实页面截图，无用户会话内容。
- 详细测试、基线、各 OpenCode 工作包和独立复验日志保存在 `/var/folders/7_/srdsmgns77l_n36f3cb1r0xw0000gn/T/cocogem-redesign-20261108-gbvllu6h/`。没有混合提交原有工作区改动。
