<div align="center">

# Cocogem

**把问题交给一个团队。**

让 Claude Code、Codex、Gemini 和 Pi 在同一个工作空间里讨论、研究、开发与复核。

[![CI](https://github.com/AcSunday/cocogem/actions/workflows/ci.yml/badge.svg)](https://github.com/AcSunday/cocogem/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26.10+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Node.js](https://img.shields.io/badge/Node.js-22.12+-339933?logo=node.js&logoColor=white)](https://nodejs.org/)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

[English](README.md) | **简体中文**

[快速开始](#快速开始) · [功能](#能做什么) · [产品文档](docs/product.md) · [参与贡献](CONTRIBUTING.md)

</div>

## 为什么做 Cocogem？

你已经有几个常用的 AI CLI，却还要在终端之间搬运上下文、转述意见、催下一位成员复核。任务越复杂，这些协调工作越容易淹没问题本身。

Cocogem 把它们放进同一个本地工作空间：你描述目标，成员分工、交接并检查彼此的结果，最后留下可回看的对话、来源和交付物。成员可以使用不同的 CLI 和模型，复用你已有的 API Key、服务地址、供应商及模型设置，无需额外的 Cocogem 模型密钥或重新登录。

## 看看界面

![Cocogem 协作会话](docs/screenshots/conversation-light.png)

<details>
<summary>深色与移动端</summary>

![深色模式](docs/screenshots/conversation-dark.png)

<img src="docs/screenshots/conversation-mobile.png" alt="移动端会话" width="320" />

</details>

界面示例使用合成资料，展示产品交互；不代表真实模型的研究或交付质量。

## 快速开始

准备 Go 1.26.10+（1.26 系列）、Node.js 22.12+（建议 24 LTS）和 npm，以及至少一个已能正常使用的本机 CLI。

```sh
git clone https://github.com/AcSunday/cocogem.git
cd cocogem
make setup
make dev
```

打开 [http://127.0.0.1:5173](http://127.0.0.1:5173)：

1. 首次使用时设置 12–128 字符的本地访问密码。
2. 在「设置 → 成员」创建成员，选择 CLI，模型留空或选择 `default` 即沿用 CLI 配置。
3. 创建工作空间，加入成员；需要分析或修改工程时绑定本地目录。
4. 新建会话，输入任务。用 `@` 指定成员，或用自然语言描述如何协作。

Enter 发送，Shift+Enter 换行；支持中文输入法确认和成员补全。本地访问密码只用于解锁网页，与 CLI 的模型凭据独立。

### 构建后运行

```sh
make build
./dist/cocogem-server
```

打开 [http://127.0.0.1:8741](http://127.0.0.1:8741)。运行时保留 `dist/cocogem-server` 与同目录的 `dist/web/`；无需 Node.js，仍需本机 CLI。

可用 `COCOGEM_DATA_DIR` 指定数据目录，用 `--allow-root /absolute/project` 限制可绑定的工程范围。代理、搜索、超时与备份配置见[产品文档](docs/product.md#配置与启动)。

## 能做什么

| 能力 | 使用方式 |
| --- | --- |
| 自由协作 | 用 `@` 或自然语言发起任务，协调者按需要直接回答、交接任务或选择内置方法 |
| 协作讨论 | 成员先独立思考，再交叉质疑、检查汇总，保留真实分歧 |
| Research | 检索和阅读资料，经过质疑、回应及复查后交付研究报告 |
| 工程分析与开发 | 分析模式保持只读；显式选择开发并授权后，修改、测试、独立复核和修复形成闭环 |
| 可复用的成员 | 全局成员可加入多个空间，分别配置职责、模型、执行选项、头像及工具授权 |
| 来源与交付物 | 查看捕获来源、确切报告版本、人工反馈，并导出 Markdown 或 JSON |
| 长任务与恢复 | 实时输出、停止、失败诊断和检查点恢复，保留已经完成的步骤与历史 |
| 日常工作空间 | 会话搜索、重命名、回收站，浅色、深色、暖色及跟随系统的外观 |

工程开发的测试工具目前依赖 macOS 沙箱；Linux 会明确阻止未验证的测试执行。只读分析与其他流程的权限、平台边界见[产品文档](docs/product.md#工具与执行边界)。

### CLI 接入

| CLI | 终端调用 | Web 接入方式 |
| --- | --- | --- |
| Claude Code | `claude` | stream-json、受限 MCP，按授权启用原生只读工具 |
| Codex | `codex` | exec JSON、受限 MCP，满足授权时启用原生联网 |
| Gemini CLI | `gemini` | stream-json、受限 MCP，文件与联网使用宿主工具 |
| Pi | `pi` | JSON、受限 MCP，关闭原生读写和 shell 工具 |

接入是否可用取决于安装版本的协议、隔离能力及实际模型连接；「已安装」不等于「连接已验证」。在「设置 → 服务」查看诊断和验证连接。

### 也可以只用终端

```sh
go run ./cmd/cocogem codex "记住这个项目叫 Cocogem"
go run ./cmd/cocogem codex --resume latest "项目叫什么？"
```

将 `codex` 换成 `claude`、`gemini` 或 `pi`；省略 CLI 名默认使用 Claude。终端会话记录保存在当前目录的 `.cocogem/`，与网页协作的隔离会话分开。参数、输出与恢复约定见[产品文档](docs/product.md#原生命令行)。

## 如何工作

```text
你的任务
   ↓
Web 工作空间 · 成员与路由 · 共享技能
   ↓
Go 服务 · 计划与权限 · 受限 MCP · 来源与检查点
   ↓
Claude Code / Codex / Gemini / Pi
   ↓
共享对话 · 独立复核 · 版本化交付物
```

服务使用 Go 标准库 HTTP 和 SQLite，前端使用 React、TypeScript 和 Vite。数据保存在本机，模型服务会收到执行任务所需的内容，搜索服务会收到查询；完整边界见 [SECURITY.md](SECURITY.md)。当前面向本地单用户，服务只允许监听回环地址。

## 参与开发

欢迎通过 [Issues](https://github.com/AcSunday/cocogem/issues) 反馈问题，或提交聚焦的 Pull Request。开始前请阅读[贡献指南](CONTRIBUTING.md)和[产品文档](docs/product.md)。

GitHub Actions 在 push 和 Pull Request 时只执行 `make test`。其他验证命令保留为本地入口。

```sh
make test             # Go、原生 CLI fixture、前端单元测试、Lint 与类型检查
make check-contract   # OpenAPI 与前端生成类型一致性
make e2e              # 构建、正式服务 smoke 与浏览器测试
make security         # 凭证检查与依赖漏洞扫描
```

首次运行浏览器测试，在 `web/` 下执行 `npx playwright install chromium`；也可用 `PLAYWRIGHT_CHANNEL=chrome make e2e` 使用已有 Chrome。

自动化默认使用临时进程与合成资料，不需要模型凭据。`make probe-cli` 和 `make probe-isolation` 可检查已安装 CLI 与本地假模型的协议及隔离行为；真实供应商调用需单独验证。

## 致谢与许可

感谢 [Clowder AI](https://github.com/zts212653/clowder-ai) 对多 Agent 协作、共享技能及项目介绍方式的启发，以及 [Cat Cafe Tutorials](https://github.com/zts212653/cat-cafe-tutorials) 的 CLI 入门示例。

采用 [MIT License](LICENSE)。第三方依赖许可见 [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt)。
