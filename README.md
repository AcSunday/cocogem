# Cocogem

Cocogem 的目标是一个由 Go 驱动、带交互式 Web 前端的多 Agent 协作工作空间，支持协作讨论、Research 和代码分析。

[v0.1 产品与实现规格](docs/product-v0.1.md)定义功能、交互和验收依据；[实现与验证进度](docs/development-progress.md)记录实际状态。

## Web 工作空间

已实现本地工作空间、可命名的 Agent 成员、共享会话、自然语言指派、有限协作流程、只读工具、来源捕获和版本化报告。页面支持实时记录、取消、历史重放、报告导出和人工标记。支持协作讨论、Research 和代码分析。

[真实 CLI 协作验收与修正记录](docs/alignment-review.md)区分隔离测试、原生协议测试和实际模型运行，并保留历史页面截图。

开发需要 Go 1.26.10+、Node.js 22.12+（建议 24 LTS）与 npm。发布包运行无需 Node.js。

```sh
make setup
make dev
```

打开开发页面 `http://127.0.0.1:5173`，首次使用时设置并确认工作空间密码；之后输入该密码解锁。任务直接调用本机已可用的 CLI，复用其现有连接与模型配置，无需重新登录或为 Cocogem 另配模型密钥；CLI 不可用时仍可查看历史和设置。

```sh
make test
make build
make e2e
```

`make e2e` 首次需要在 `web/` 下运行 `npx playwright install chromium`。已有 Chrome 时可以使用 `PLAYWRIGHT_CHANNEL=chrome make e2e`。浏览器自动化通过仅供测试的隔离 fixture 服务验证 HTTP/SSE 和用户流程；smoke 验证正式服务启动、工作空间持久化和备份恢复，均不调用真实供应商。

`make probe-cli` 使用已安装的 Claude/Codex/Gemini 对本地假模型端点验证会话初始化、受限 MCP 工具清单、必需工具、工具调用和终态，不需要账户凭证；`make probe-isolation` 进一步验证生产隔离判定（help 只作为候选，必须实际完成受限协议/工具探测才算通过）。它们不证明真实模型质量。真实 Claude/Codex 崩溃/重启恢复记录、Gemini 的未通过原因见[长任务恢复证据](docs/evidence/restart-recovery-20261026.json)。

发布包使用本机 CLI，无需为 Cocogem 额外设置模型 API Key：

```sh
make build
# 直接复用本机 CLI 的现有连接配置，无需重新登录。
# 启动后在网页成员设置选择 CLI，模型默认沿用 CLI 默认值。
./dist/cocogem-server
# 可选：BRAVE_SEARCH_API_KEY，仅用于在线发现资料
# 需要代理访问公共网页时：COCOGEM_WEB_PROXY=env（复用 HTTPS_PROXY）、system（复用 macOS 系统代理）或显式代理地址
# make dev 未设置时自动使用 system；COCOGEM_WEB_PROXY= 强制直连
```

在“创建/编辑工作空间”中点击“浏览目录”，可逐层进入文件夹、返回上级，点击“选择此目录”后保存工作空间即可生效；取消不会更改原绑定。也可在“本地资料目录 → 输入目录路径…”中填写完整路径，支持 `~/`，或选“暂不绑定”取消绑定。保存时检查存在和可读，并保存真实绝对路径。可选启动参数 `--allow-root /absolute/path/to/public-sources`（可重复）将可浏览及绑定范围限制为这些目录及其子目录；不设置时从本机主目录开始浏览，由已解锁的本机用户选择。协作运行期间不能更改目录绑定。

网页在“服务与设置”中分别显示 CLI 安装、版本、调用方式、工具隔离状态和
按 CLI/模型记录的连接验证时间。安装和版本探测不能替代一次真实调用。
适配按实际参数、MCP 工具清单握手、会话初始化和终止事件判断，不再依赖
固定版本白名单；版本仅用于诊断、兼容规则和已知不兼容拦截。每次执行尝试
和重启恢复前都会重新检查程序身份（路径、大小、修改时间和 inode），CLI
在运行期间升级或被替换后不会复用旧结论。无法确认关键隔离能力时阻止执行
并保留检查点，不会放宽原生工具、hooks、插件或外部 MCP 权限。
成员的 `default` 使用本机 CLI 的模型/供应商连接配置；可显式指定其它可用模型。
适配与实际验收的差异见[对齐审查](docs/alignment-review.md)。

每个网页步骤使用新 CLI 会话与隔离目录，Go 提供其所需的共享消息和确切
上游产物；不复用本机交互会话的 `latest`。CLI 管理模型和工具循环，通过
本步专用 MCP 调用 Go 的只读工具。CLI 原生只读工具只从成员现有授权派生
（`Read`/`Glob`/`Grep` 及发现范围下的 `WebSearch`/`WebFetch`），不新增开关；
普通 shell、编辑/写文件、内置任务/技能工具、外部 MCP 和扩展不会因接入 CLI
自动获准。原生联网由 CLI/供应商网络策略负责，并非 Go 的 URL/DNS/SSRF 保证；
配置的公共网页代理只传给启用原生联网的子进程，并保留 loopback MCP 直连。
Codex 的合并 web 工具在两项联网授权同时存在时启用，并声明 Responses web 能力；上游不支持时显示实际错误，
Gemini 因 stream-json 不暴露实际工具结果正文而保持 MCP 联网能力缺口。
CLI 的认证和模型访问仍由对应供应商管理。宿主仅按白名单转交本机连接配置；不会继承其中的 hooks、插件、外部 MCP 或通用工具权限。

旧 API 模式创建的成员需要在成员设置明确选择 CLI，历史记录保留；不会
静默迁移旧模型 ID。`.env.example` 仅有配置说明，程序不自动加载 `.env`。

默认监听 `127.0.0.1:8741`，首版拒绝公网监听。首次打开页面设置 12–128 字符的工作空间密码，之后通过密码解锁；服务重启后密码保留，浏览器需重新解锁。无需终端令牌或特殊启动链接。本机 CLI 继续复用现有配置。

密码以随机盐和 PBKDF2-HMAC-SHA256（600,000 次迭代）派生值保存在数据目录的 `auth.json`，文件权限为 0600；不保存明文。会话采用 HttpOnly、SameSite=Strict cookie，24 小时过期；变更请求继续校验 CSRF、Host 和 Origin。连续 5 次密码错误暂停尝试 1 分钟。“服务与设置”可锁定当前浏览器，后台协作继续执行。

备份和恢复时包含 `auth.json`。忘记密码时，停止服务并确认本机数据目录后，可将 `auth.json` 移到安全的备份位置，再启动服务重新设置；工作空间数据库保留。此操作仅限可信的本机文件访问，页面不提供免密码重置。

数据默认位于 `os.UserConfigDir()/cocogem`，可通过 `COCOGEM_DATA_DIR` 指定。停止服务后整体备份该目录，恢复时停止服务再复制回原位置；不要只复制正在使用的 SQLite 主文件而遗漏 WAL。历史捕获可能包含资料正文，应按敏感本地数据管理。

模型和工具执行有时间、来源大小和队列限制。长时间任务默认不设单次协作或单步骤总期限（`COCOGEM_RUN_TIMEOUT`/`COCOGEM_STEP_TIMEOUT` 为 `0` 表示关闭）；等待首个有效响应默认 10 分钟、无进展默认 15 分钟且只发出可见告警，不会仅因暂时没有文本输出就停止工具执行。通过 `COCOGEM_FIRST_RESPONSE_TIMEOUT`、`COCOGEM_STALL_TIMEOUT` 显式设为 `0` 可关闭对应检测，负数或非法值会使服务拒绝启动；`COCOGEM_STALL_ACTION=stop` 才会终止停滞步骤。等待工具、等待上游成员和真正的停滞分别标记。 长正文分页、显式恢复及本轮真实模型验收见[长任务实现与验收](docs/long-task-implementation.md)。

服务停止或异常退出后，新的执行尝试可从未完成步骤继续：已完成步骤不重复执行，历史消息、来源和产物不被覆盖；失败委派仍可回传给协调者处理。恢复前会重新核对 CLI 程序身份、工具隔离探测、权限范围和实际解析模型/上游连接；默认模型、供应商或服务地址变化时保留检查点并要求新 Run，不会静默切换。正在执行的尝试、CLI 版本/程序身份、原生会话 ID 和进程归属独立保存；服务重启时先核实旧进程归属，只终止本平台记录且身份匹配的进程。无法核实的结果保留为“待核实”并要求人工确认，不会盲目重跑；用户取消的协作不会自动复活。页面提供“继续执行”以显式恢复被能力检查阻塞的检查点。

在线发现资料优先使用 `BRAVE_SEARCH_API_KEY`；未配置时默认使用无密钥公共搜索回退（Bing RSS，失败后 DuckDuckGo HTML），可用 `COCOGEM_SEARCH_PROVIDER=auto|brave|bing|duckduckgo|public|off` 控制。未配置或搜索全部失败时会明确报告，不用模型记忆冒充网页证据。搜索查询会发送给对应搜索服务。不能访问登录网页、执行 shell、修改文件或上传 Office/PDF。自然语言路由仍可能误判；页面提供解释与明确选人入口，歧义时不会执行。

真实 CLI/搜索 smoke、平台证据及远端 CI 状态见[验证进度](docs/development-progress.md)，工具链升级与扫描结果见[安全扫描记录](docs/security-scan.md)。本地功能测试通过不等于公开发布验收完成。

## 开源开发

仓库：[AcSunday/cocogem](https://github.com/AcSunday/cocogem)。代码采用 [MIT](LICENSE)，贡献流程见 [CONTRIBUTING.md](CONTRIBUTING.md)，安全边界见 [SECURITY.md](SECURITY.md)。

运行 `make contract` 生成 OpenAPI 与前端类型，`make check-contract` 检查漂移；`make security` 执行漏洞扫描，`make release` 只生成本地候选包和 SHA-256，不推送、不发布。参考 Clowder 的架构思路，未复制其品牌资产。

## 当前可运行的 CLI 原型

对应[原作业](https://github.com/zts212653/cat-cafe-tutorials/blob/main/docs/lessons/01-homework.md)：启动 Claude、Codex 或 Gemini CLI、逐行读取 NDJSON、打印回复文本、处理子进程退出。只使用 Go 标准库，无需第三方 Go 依赖。

## 成员与技能协作

全局成员可以复用于多个工作空间，空间单独设置启用与工具权限。自由协作通过本轮成员名册和受控 MCP 工具交接任务，再由平台回传结果；共享技能只描述角色和方法，不绑定具体成员。配置、边界与验证命令见[协作说明](docs/collaboration.md)。

## 运行

需要 Go 1.26.10+，以及可直接调用的本机 CLI：Claude Code（`claude --version`）、Codex（`codex --version`）或 Gemini（`gemini --version`）。沿用 CLI 已有的 API Key、服务地址、供应商和模型配置，无需额外登录或另配模型密钥。

默认使用 Claude：

```sh
go run ./cmd/cocogem "你好，请用一句话介绍自己"
```

也可以显式选择 CLI（选择参数放在提示词前）：

```sh
go run ./cmd/cocogem claude "你好，请用一句话介绍自己"
go run ./cmd/cocogem codex "你好，请用一句话介绍自己"
go run ./cmd/cocogem gemini "你好，请用一句话介绍自己"
```

## 恢复会话

每次成功调用后，程序会把会话 ID 打印到 stderr，并在当前目录的 `.cocogem/<cli>.json` 保存该 CLI 最近一次成功会话。默认仍然新建会话。

```sh
go run ./cmd/cocogem codex "记住我的项目叫 cocogem"
go run ./cmd/cocogem codex --resume latest "我的项目叫什么？"

go run ./cmd/cocogem claude "记住数字 42"
go run ./cmd/cocogem claude --resume latest "刚才的数字是多少？"

go run ./cmd/cocogem gemini "记住口令 blue-cat"
go run ./cmd/cocogem gemini --resume latest "刚才的口令是什么？"
```

也可以指定原生 CLI 会话 UUID：

```sh
go run ./cmd/cocogem codex --resume 12345678-1234-1234-1234-123456789abc "继续这个会话"
```

`--resume` 放在提示词之前。`latest` 只读取本目录、本 CLI 的记录；没有记录或记录损坏时会报错。恢复失败也会报错，不自动新建会话。Claude、Codex、Gemini 的会话不能互换，恢复时返回的会话 ID 必须与请求一致。

`.cocogem/` 只保存 CLI 名称和会话 ID；聊天历史仍由各 CLI 保存。如果原生 CLI 删除了历史、启用了临时会话，或换了账号/工作目录，记录的 ID 可能无法恢复。文件以 0600 权限创建，通过临时文件加重命名更新，调用失败时不覆盖上次成功记录。

## 统一 Go 接口

三个 CLI 共用同一个调用函数：

```go
func Invoke(cli string, request Request, output io.Writer) (Result, error)
```

仓库内的 Go 代码可以导入 `github.com/AcSunday/cocogem/internal/cli`；流式回复写入 `output`，返回值提供真实会话 ID：

```go
first, err := cli.Invoke("codex", cli.Request{Prompt: "记住项目名 cocogem"}, os.Stdout)
if err != nil {
    return err
}
_, err = cli.Invoke("codex", cli.Request{
    Prompt:    "我的项目叫什么？",
    SessionID: first.SessionID,
}, os.Stdout)
return err
```

把 `"codex"` 换成 `"claude"` 或 `"gemini"` 即可。`Request.SessionID` 留空时新建会话。Go 接口不读写 `.cocogem/`，调用方可以自行保存 ID；命令行入口负责本地记录。

## 编译和文件结构

也可以编译为可执行文件：

```sh
go build -o /tmp/cocogem ./cmd/cocogem
/tmp/cocogem "你好，请用一句话介绍自己"
/tmp/cocogem codex "你好，请用一句话介绍自己"
/tmp/cocogem gemini "你好，请用一句话介绍自己"
```

CLI 入口位于 `cmd/cocogem/main.go`，调用逻辑、事件解析及会话记录位于
`internal/cli/`，测试与对应实现放在同一目录。目录职责见[项目结构](docs/project-layout.md)。

目录调整后，原来的 `go run .` 改为 `go run ./cmd/cocogem`；编译后的
`cocogem` 参数、输出、退出码和 `.cocogem/` 会话记录格式保持兼容。

## 对应关系

| 作业中的 Node.js | Go 实现 |
| --- | --- |
| `child_process.spawn()` | `exec.Command()` + `cmd.Start()` |
| 读取子进程 stdout | `cmd.StdoutPipe()` |
| `readline` 逐行读取 | `bufio.Scanner` |
| `JSON.parse()` | `json.Unmarshal()` |
| 监听进程退出 | `cmd.Wait()` |

Claude 调用相当于：

```sh
claude -p --output-format stream-json --verbose -- "你的问题"
```

提示词作为独立参数传入，不经过 shell 拼接；`--` 也防止提示词被误当成 CLI 选项。

Codex 调用相当于：

```sh
printf '%s' "你的问题" | codex exec --json --skip-git-repo-check --sandbox read-only -
```

Go 通过 `cmd.Stdin` 直接传入提示词，不执行上面的 shell 管道。这样即使提示词是 `-` 或包含特殊字符，也能按原文传入。`--skip-git-repo-check` 允许 CLI 在非 Git 资料目录运行；Codex 使用只读沙箱。

Gemini 调用相当于：

```sh
gemini --output-format stream-json --approval-mode plan --prompt="你的问题"
```

Gemini 使用 plan 模式。若本机开启了目录信任检查，需要先在交互式 `gemini` 中确认当前目录。

统一的 `runCLI()` 负责子进程生命周期，三个 CLI 文件分别解析自己的事件：

- Claude：打印 `assistant.message.content` 中 `type` 为 `text` 的块，跳过 system、result 等事件。
- Codex：打印 `item.completed` 中 `item.type` 为 `agent_message` 的 `item.text`，跳过中间更新、推理和工具事件；遇到 `turn.failed` 时返回错误。
- Gemini：连续写出 `message` 中 `role` 为 `assistant` 的 `content` 增量，在成功的 `result` 事件后换行；错误结果返回错误。

会话 ID 分别来自 Claude 的 `system/init.session_id`、Codex 的 `thread.started.thread_id`、Gemini 的 `init.session_id`。恢复时使用 `claude --resume ID`、`codex exec resume ID`、`gemini --resume ID`；Codex 的只读沙箱参数仍在 `exec` 层显式设置。

Claude/Codex 每条完整回复后换行，Gemini 的增量片段直接拼接。Codex 的协议参见 [OpenAI 官方文档](https://learn.chatgpt.com/docs/non-interactive-mode#make-output-machine-readable)。

Scanner 负责处理分多次到达的数据和一次到达的多行 JSON；单行上限为 4 MiB，超过时明确报错。stderr 直接显示 CLI 的诊断信息。参数错误返回退出码 2，运行错误返回 1，成功返回 0；不原样透传子进程的退出码。

## 验证

```sh
go test ./...
go vet ./...
```

测试不调用模型，覆盖参数解析、统一接口校验、三种 CLI 的事件和会话 ID、增量输出、长行、失败事件、读写失败、进程退出，以及本地记录的权限、隔离、损坏和更新失败。

## 范围

Claude/Codex 按完整消息输出，Gemini 按增量片段输出。没有加入重试或超时；每个目录、每种 CLI 只记录最近一次成功会话。连接和模型选择沿用本机 CLI 配置；Claude 的工具权限沿用本机配置，Codex 显式使用只读沙箱，Gemini 使用 plan 模式。
