# 工程协作（analysis / development）

状态：2026-10-28 在 macOS 上完成真实 deny-default 沙箱验证（真实 `go test`/`npm test` 通过，系统路径、网络、AppleEvents、launchctl/osascript 负例被拒）。Linux 在真实主机边界验证前明确 blocked，不视为已验证。

## 用户可见行为

- 工作方式选择“工程协作”时，输入区出现一个小的“工程动作”选择，默认“分析（只读）”，可切换“开发（可修改并测试）”。这是动作选择，不是同意门禁；不选择时始终按只读分析执行。
- 分析使用 `analyst` 与 `reviewer` 两个只读步骤，产物类型为 `code_analysis`。成员只能读取已绑定的工程目录并发布报告，不能写文件或执行测试。
- 开发使用固定的四步计划并至少保留一轮修复闭环：
  1. `developer` 实现修改并运行测试；
  2. `reviewer` 独立读取 diff/测试证据并自行复测，以普通步骤回复给出结论和真实 source ID；它不发布 artifact；
  3. `developer` 直接消费复核者的确切回复和来源引用：有问题时修复并复测，没有问题时只复测确认，不为了“有动作”而重写文件；
  4. `final_reviewer` 做最终独立复核并发布本 Run 唯一的 `engineering_report`。
- 只有 `final_reviewer` 能发布工程报告；中间步骤既没有 `publish_artifact` 工具，也无法通过执行门禁发布。
- 自由协作的 `choose_workflow` 可显式选择工程只读分析，但从不授权开发；开发只能由用户在输入区显式选择“开发”。旧 `code_analysis` 选择保持只读兼容。
- 全局协作设置按动作使用最小角色集：分析为 `analyst` + `reviewer`，开发为 `developer` + `reviewer`；开发动作的 `reviewer`（同一成员承担 `final_reviewer`）除发布权限外还必须具备 `run_test`，分析动作的 `reviewer` 只需发布权限，`run_test` 不是只读分析的门槛。默认填充不会让同一成员自审。
- 每个 Run 的 `engineering_action` 显式保存在输入中。`develop` 只接受 `workflow=engineering` 且用户明确选择该动作的提交；成员预先拥有 `write_file`/`patch_file`/`run_test` 授权本身不会把分析升级成开发。
- 旧的 `code_analysis` API、历史和会话设置继续可读可用，并始终保持只读；不会因为新增工程工具而获得写权限。
- 新安装的默认成员仍只获得只读工具。`write_file`、`patch_file` 与 `run_test` 必须由用户显式加入全局允许和空间授权；旧数据不扩权。`patch_file` 是额外的可选授权，开发计划仍要求 `write_file` + `run_test`，只授予 `patch_file` 不会替代 `write_file`。
- 有效工具 = 全局授权 ∩ 空间授权 ∩ 本轮 engineering_action ∩ 步骤角色。开发动作的 `reviewer`/`final_reviewer` 必须同时具备 `publish_artifact` 与 `run_test`；宿主不会向它们提供 `write_file`/`patch_file`。这只是宿主工具限制，**不是**让项目测试代码失去写能力：测试代码本身仍可写入工程内允许的路径，但这样的测试会改变工程树，不能作为已验证交付。其他角色在分析动作中没有任何写/测试工具。

## 宿主工具

### `write_file`

输入：`path`（相对路径）、`content`（完整 UTF-8 文本，最多 32 KiB）、`expected_sha256`（替换已有文件时必填）。

- `os.OpenRoot` 限定在绑定工程内；拒绝绝对路径、`..`、逐段符号链接、硬链接（`nlink > 1`）、非普通文件、非 UTF-8、NUL、敏感路径（`.env*`、`.git`、`.cocogem`、`.ssh`、`secrets`、`.git-credentials`、`id_rsa*`、`*.key`、`*.pem`、`*.sqlite` 等）。
- 新建要求目标不存在；替换要求当前 SHA-256 与 `expected_sha256` 一致。写入同内容重试幂等且不产生第二次 diff；目标在写入期间被改成其他内容时拒绝覆盖。
- 新建采用同目录临时文件 `fsync` 后 hard-link 提交：`link` 在目标已存在时返回 `EEXIST`，不会覆盖并发创建的文件（no-clobber 原子提交）。替换使用 `os.Root.Rename` 原子替换并保留原文件权限位。
- 本进程内所有 `write_file`、`patch_file`、`CurrentSHA256`、工程树摘要共享同一把写锁，因此同一服务进程的并发写入与证据读取不会互相穿插。**外部进程并发写不受这把锁约束**：替换路径在 rename 前会重读并比对当前哈希，但重读与 rename 之间仍存在窗口；本实现不提供 compare-and-swap，也不承诺阻止外部写者，只能检测并拒绝已观察到的变化。需要在外部编辑场景下强一致时，应由用户/工具在该工程上停止并发写入。
- 敏感路径策略由 `DeniedPath`/`SensitivePath`/macOS 沙箱正则共用同一组名称、前缀、后缀定义：所有标为 sensitive 的组件一定同时被工具拒绝；仅 `node_modules`/`dist`/`build` 允许项目自身测试读取但工具不可读写。新增名称只改一处即可同时生效。
- 成功时生成真实的 unified diff（自带行级算法，不依赖 git）并保存为 `diff` 来源，记录新文件 SHA-256。响应返回 `source_id`、`sha256`、`changed`。

### `patch_file`

输入：`path`（相对路径）、`expected_sha256`（当前完整文件哈希，必填）、`old_text`（必须非空且在文件中恰好出现一次）、`new_text`。

- 用于修改超过 32 KiB 的既有文件：`write_file` 的完整内容预算仍为 32 KiB，`patch_file` 只携带片段，`old_text + new_text` 合计最多 32 KiB；完整文件（修改前后）最多 1 MiB。
- 同一受控写路径：复用 `os.OpenRoot` 边界、逐段软链/敏感路径/硬链接/非普通文件检查、进程内写锁、临时文件 + `fsync` + 原子 rename、`expected_sha256` 防覆盖和真实 unified diff 来源。不引入 patch 格式解析、额外依赖或 shell。
- 恰好一次替换：`old_text` 为空、0 次匹配、多次匹配、哈希过期都拒绝且不写文件。`read_file` 返回的 `sha256` 是完整文件哈希（即使只返回片段），可直接作为 `expected_sha256`。
- 重复调用安全性：完全相同的重试若能从当前文件反向重建出 `expected_sha256` 对应的旧内容，并正向得到当前字节，则明确返回 `changed=false`（`reason` 说明已应用），不生成第二个 diff。缺少该证明时一律按哈希不匹配拒绝，不猜测“可能已经应用”。

### `run_test`

输入：`toolchain`（仅 `go` 或 `npm`）、`target`（相对工程目录）、可选 `timeout_seconds`（1–600，默认 120）。

- 固定 argv，无 shell、无字符串插值：
  - `go test -count=1 ./...`（使用已解析的精确 Go 工具链）；
  - `npm test --silent`（使用本机 node/npm，离线 npm 配置）。
- 测试进程使用全新环境白名单，不含用户 Home、模型凭证、代理或任意密钥；`TMPDIR` 指向本步私有临时目录且不等于模块根。`go env` 查询同样使用固定干净环境、私有工作目录与 5 秒超时，不继承服务进程环境、不读取工程内 env/workspace 配置。
- macOS 使用 `sandbox-exec` 的真正 **deny-default** profile（不是 allow-default）：
  - 显式 `deny network*`、`deny appleevent-send`；不授予任意 mach-lookup，只允许 Go runtime/libinfo 需要的 `com.apple.system.logger` 与 `com.apple.system.opendirectoryd.libinfo`。
  - 读数据仅允许 `literal /`、`/usr`、`/bin`、`/sbin`、`/System`、`/dev`、绑定工程、本步私有临时目录、精确 `GOROOT`、`GOMODCACHE`、精确 Node/npm 程序树；**不**开放 `/Library`、`/private/etc`、`/private/var/db`、`/opt/homebrew` 或整个用户 Home。
  - 写仅允许工程、本步私有临时目录和 `/dev/null`；工程内 `.env*`、`.git`、`.cocogem`、`.ssh`、`secrets`、密钥、sqlite 等由与文件工具相同的策略正则拒绝，即使文件在 profile 生成后才出现。
  - `process-exec` 不是无限制：只允许配置的 shell（`/bin/sh`、`/bin/bash`、`/usr/bin/env`）、工程内脚本、私有临时目录、精确 Go 工具链与 Node/npm 程序树。因此 `osascript`、`launchctl` 等系统工具在测试内无法启动；这两个二进制也被显式禁止读取，避免复制到工程后执行。
  - 实测（2026-10-28）：真实 `go test -count=1 ./...`、`npm test` 退出码 0；工程外读写、工程内 `.env`/`.git`/`*.key` 读写、`net.Listen`、连接、`/private/etc/hosts` 读取、`/Library` 与 `/private/var/db` 列目录、`osascript`/`launchctl` 执行与读取全部被拒。
  - 诚实边界：放宽 `process-exec` 的探针显示 `launchctl print system` 仍可查询 launchd（Seatbelt 的 mach-lookup/deny-default 规则未覆盖该 XPC 路径），所以系统守护进程隔离的实际保证是**可执行文件白名单 + 禁止读取/执行这些工具**；直接调用 launchd Mach/XPC API 的原生测试代码未被验证可阻断，属于未解决的已知残余风险，不在“已验证”范围内。本机 arm64 上复制 `launchctl`/`osascript` 会被系统以 SIGKILL 终止，但这不是本实现依赖的控制，也未在其他架构验证。
- Linux 当前明确 **blocked**：没有在真实 Linux 主机上验证 bubblewrap 边界前，`run_test` 直接返回 `sandbox_unavailable`，不会以未验证的 namespace 配置代替强隔离，也不会用交叉编译结果当作验收。待真实主机验证后再引入并重新跑边界负例。
- 进程使用独立 PGID；取消或超时先确认该 group 仍有存活成员再发送 `SIGKILL`，避免对已被内核回收/复用的 PGID 误杀无关进程组，然后等待整个组收敛。每次测试有界输出（32 KiB）、退出码、耗时与是否超时；结果保存为 `test` 来源。
- 测试进程启动前与结束后各计算一次有界工程树摘要（忽略 `.git`、`node_modules`、`dist`、`build`、敏感路径与符号链接；文件数/字节有上限，超限不产出摘要）。只有两次计算都成功且结果完全一致时，摘要才随 `test` 来源保存为可验证证据；测试改动了任何交付文件（源码、测试、fixture 或新文件）时报告 `workspace_changed`、不暴露摘要，并在输出与证据原因中说明。忽略的构建/缓存输出不影响摘要。项目测试代码仍可能写入工程内允许的路径；这不代表复核者拥有宿主写工具，但该次测试永远不能验证交付。
- 非零退出、超时、被杀或无法收敛都返回工具错误并附带来源 ID，模型不能把失败当成成功。宿主从不依据模型自述判定测试通过。

## 交付门禁

`engineering_report` 只在开发动作中有效。发布时服务端额外校验：

- 本次 Run 至少有一个实际 `diff` 来源，且每个被改动文件的最新 diff 都被报告引用；
- 每个被引用的最新 diff 的 SHA-256 仍与当前文件一致；
- 至少一个被引用的 `test` 来源退出码为 0、捕获时间不早于最后一次写入，且该测试开始前与结束后的工程树摘要都存在、彼此一致（证明测试没有改写交付树），发布时该摘要仍与当前工程树一致。仅凭测试结束后的摘要不能证明测试没有自改工程；测试改写过的树不满足该前提，永远不能被当作已验证交付；测试之后又发生的任何写入都需要更新的测试证据。

缺少任一证据时报告仍可交付，但必须至少有一条状态为 `insufficient_evidence` 的发现，且不能包含 `confirmed`/`disputed` 结论；空发现或无依据的“已完成”报告会被拒绝。分析动作没有写/测试，只报告静态结论。

## 已知限制

- `write_file` 是完整文件替换，单次最多 32 KiB。`patch_file` 只做一次精确唯一片段替换，完整文件最多 1 MiB；不支持多方块 patch、模糊上下文匹配或任意 diff 格式。
- 只为项目自带测试提供 `go test` 与 `npm test`；其他语言/测试器明确不支持，界面会写明。测试必须只依赖已安装的 shell 与工具链；调用 `git`、`python`、`osascript` 等其他系统程序会被进程白名单拒绝。
- 外部依赖必须已经存在于精确的模块/依赖缓存或项目内；测试进程禁止联网下载。
- 沙箱禁止所有网络（包括 loopback），需要本地端口的项目测试会失败。
- 工程树摘要忽略 `.git`、`node_modules`、`dist`、`build` 与敏感路径；超过 20000 个文件或 512 MiB 时不产出摘要，此时 `run_test` 结果不能作为最终交付验证（报告只能如实记为 `insufficient_evidence`）。
- macOS 已验证真实 `go test`/`npm test`、工程外读写、系统目录读取、网络连接与监听、敏感文件、AppleEvents/osascript 与 launchctl 执行等负例；直接调用 launchd Mach/XPC API 的原生代码未被验证可阻断（见上）。Linux 上 `run_test` 明确 blocked。
- `write_file` 的同进程写锁不约束外部进程；替换路径在最后一次哈希比对与 rename 之间仍有极小窗口，本实现不声称 CAS 或绝对防覆盖。
- 新建依赖同目录 hard-link 支持（本机 APFS/HFS 已验证）；不支持 hard-link 的文件系统上新建会失败关闭，不会退回可能覆盖并发文件的 rename。

## CLI 兼容

`write_file`、`patch_file` 与 `run_test` 是宿主 MCP 工具，不是 CLI 原生工具；Claude/Codex/Gemini 三个适配器通过同一 scoped MCP 桥暴露它们，因此不需要放宽任何 CLI 的 `Bash`/`Write`/`Edit` 权限，也没有新增品牌或模型绑定。现有安装、隔离参数与连接探测仍按原规则门禁；工程能力探测不到沙箱时只拒绝 `run_test`，不影响只读分析或其他工作方式。
