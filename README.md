# Cocogem

Cocogem 的目标是一个由 Go 驱动、带交互式 Web 前端的多 Agent 协作工作空间，支持协作讨论、Research 和代码分析。

[v0.1 产品与实现规格](docs/product-v0.1.md)定义了第一版的功能范围、页面交互、工程规范与分阶段验收。当前代码已经实现下方 CLI 原型；Web 工作空间按文档逐步实现。

## 当前可运行的 CLI 原型

对应[原作业](https://github.com/zts212653/cat-cafe-tutorials/blob/main/docs/lessons/01-homework.md)：启动 Claude、Codex 或 Gemini CLI、逐行读取 NDJSON、打印回复文本、处理子进程退出。只使用 Go 标准库，无需第三方 Go 依赖。

## 运行

需要 Go 1.22+，以及你要使用的、已经安装并登录的 CLI：Claude Code（`claude --version`）、Codex（`codex --version`）或 Gemini（`gemini --version`）。Claude 未登录时运行 `claude`；Codex 未登录时运行 `codex login`；Gemini 未登录时运行 `gemini` 完成认证。

默认使用 Claude：

```sh
go run . "你好，请用一句话介绍自己"
```

也可以显式选择 CLI（选择参数放在提示词前）：

```sh
go run . claude "你好，请用一句话介绍自己"
go run . codex "你好，请用一句话介绍自己"
go run . gemini "你好，请用一句话介绍自己"
```

## 恢复会话

每次成功调用后，程序会把会话 ID 打印到 stderr，并在当前目录的 `.cocogem/<cli>.json` 保存该 CLI 最近一次成功会话。默认仍然新建会话。

```sh
go run . codex "记住我的项目叫 cocogem"
go run . codex --resume latest "我的项目叫什么？"

go run . claude "记住数字 42"
go run . claude --resume latest "刚才的数字是多少？"

go run . gemini "记住口令 blue-cat"
go run . gemini --resume latest "刚才的口令是什么？"
```

也可以指定原生 CLI 会话 UUID：

```sh
go run . codex --resume 12345678-1234-1234-1234-123456789abc "继续这个会话"
```

`--resume` 放在提示词之前。`latest` 只读取本目录、本 CLI 的记录；没有记录或记录损坏时会报错。恢复失败也会报错，不自动新建会话。Claude、Codex、Gemini 的会话不能互换，恢复时返回的会话 ID 必须与请求一致。

`.cocogem/` 只保存 CLI 名称和会话 ID；聊天历史仍由各 CLI 保存。如果原生 CLI 删除了历史、启用了临时会话，或换了账号/工作目录，记录的 ID 可能无法恢复。文件以 0600 权限创建，通过临时文件加重命名更新，调用失败时不覆盖上次成功记录。

## 统一 Go 接口

三个 CLI 共用同一个调用函数：

```go
func Invoke(cli string, request Request, output io.Writer) (Result, error)
```

在当前 `main` 包内可以这样调用；流式回复写入 `output`，返回值提供真实会话 ID：

```go
first, err := Invoke("codex", Request{Prompt: "记住项目名 cocogem"}, os.Stdout)
if err != nil {
    return err
}
_, err = Invoke("codex", Request{
    Prompt:    "我的项目叫什么？",
    SessionID: first.SessionID,
}, os.Stdout)
return err
```

把 `"codex"` 换成 `"claude"` 或 `"gemini"` 即可。`Request.SessionID` 留空时新建会话。Go 接口不读写 `.cocogem/`，调用方可以自行保存 ID；命令行入口负责本地记录。

## 编译和文件结构

也可以编译为可执行文件：

```sh
go build -o /tmp/cocogem .
/tmp/cocogem "你好，请用一句话介绍自己"
/tmp/cocogem codex "你好，请用一句话介绍自己"
/tmp/cocogem gemini "你好，请用一句话介绍自己"
```

文件按职责分开：

- `main.go`：参数解析和 CLI 选择。
- `invoke.go`：统一请求、结果、参数校验和调用接口。
- `session.go`：会话 ID 校验和本地会话记录。
- `runner.go`：子进程生命周期和通用 NDJSON 逐行读取。
- `claude.go`：Claude 命令和事件解析。
- `codex.go`：Codex 命令和事件解析。
- `gemini.go`：Gemini 命令和事件解析。

拆分后需要用 `go run .` 编译整个包，不能再只运行一个 `.go` 文件。

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

Go 通过 `cmd.Stdin` 直接传入提示词，不执行上面的 shell 管道。这样即使提示词是 `-` 或包含特殊字符，也能按原文传入。`--skip-git-repo-check` 使这个示例能在当前这样的非 Git 目录运行；Codex 使用只读沙箱。

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

Claude/Codex 按完整消息输出，Gemini 按增量片段输出。没有加入重试或超时；每个目录、每种 CLI 只记录最近一次成功会话。登录和模型选择沿用本机 CLI 配置；Claude 的工具权限沿用本机配置，Codex 显式使用只读沙箱，Gemini 使用 plan 模式。
