<div align="center">

# Cocogem

**Give your task a team.**

Let Claude Code, Codex, Gemini, and Pi discuss, research, develop, and review in one workspace.

[![CI](https://github.com/AcSunday/cocogem/actions/workflows/ci.yml/badge.svg)](https://github.com/AcSunday/cocogem/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26.10+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Node.js](https://img.shields.io/badge/Node.js-22.12+-339933?logo=node.js&logoColor=white)](https://nodejs.org/)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

**English** | [简体中文](README.zh-CN.md)

[Quick Start](#quick-start) · [Features](#what-it-does) · [Product Guide](docs/product.md) · [Contributing](CONTRIBUTING.md)

</div>

## Why Cocogem?

You already have a few AI CLIs you rely on, but you still move context between terminals, relay opinions, and ask the next agent to review the work. The more complex the task, the more time goes into coordination.

Cocogem brings them into one local workspace. Describe your goal; members divide the work, hand off tasks, and check each other's results. Conversations, sources, and deliverables stay available for review. Each member can use a different CLI and model, reusing your existing API keys, endpoints, providers, and model settings without an extra Cocogem model key or a new login.

## See It in Action

![Cocogem collaboration workspace](docs/screenshots/conversation-light.png)

<details>
<summary>Dark mode and mobile</summary>

![Dark mode](docs/screenshots/conversation-dark.png)

<img src="docs/screenshots/conversation-mobile.png" alt="Mobile conversation" width="320" />

</details>

These screenshots use synthetic data to demonstrate the interface. They do not establish the quality of real model research or deliverables.

## Quick Start

You need Go 1.26.10+ within the 1.26 series, Node.js 22.12+ (24 LTS recommended), npm, and at least one working local agent CLI.

```sh
git clone https://github.com/AcSunday/cocogem.git
cd cocogem
make setup
make dev
```

Open [http://127.0.0.1:5173](http://127.0.0.1:5173):

1. Set a local access password of 12–128 characters on first use.
2. Describe your goal directly. The first input creates an unnamed project, a conversation and editable default member profiles from your already-configured CLIs; no wizard is required.
3. Research needs no project directory. The first engineering task asks for a local directory, and that binds read access only.
4. Use `@` or natural language to address members, or let the coordinator choose. Durable tasks, goal revisions, budgets and completion records continue across sessions and restarts.

Enter sends; Shift+Enter inserts a newline. Member completion and IME confirmation are supported. The local access password unlocks the web interface independently of the CLI's model credentials.

### Run a Build

```sh
make build
./dist/cocogem-server
```

Open [http://127.0.0.1:8741](http://127.0.0.1:8741). Keep `dist/cocogem-server` alongside `dist/web/`. Node.js is not required at runtime; the local agent CLIs are still required.

Use `COCOGEM_DATA_DIR` to choose the data directory and `--allow-root /absolute/project` to restrict workspace directories. Proxy, search, timeout, and backup settings are covered in the [product guide (Chinese)](docs/product.md#配置与启动).

## What It Does

| Capability | How it works |
| --- | --- |
| Projects and durable tasks | The project overview shows attention items, active tasks and recent accepted outcomes; goal revisions keep their history, and delivery is distinct from acceptance |
| Free collaboration | Use `@` or natural language; the coordinator answers, hands off tasks, or selects a built-in workflow as needed |
| Team discussion | Members form independent views, challenge each other, and check the synthesis while preserving disagreements |
| Research | Search and read sources, then challenge, respond, and recheck before delivering a research report |
| Engineering analysis and development | Analysis stays read-only; explicitly authorized development combines changes, tests, independent review, and fixes |
| Reusable members | Share global profiles across workspaces, with separate roles, models, execution options, avatars, and tool grants |
| Sources and deliverables | Inspect captured sources, exact report versions, and human feedback; export Markdown or JSON |
| Long tasks and recovery | Stream output, run independent tasks in parallel, stop tasks, inspect failures, retry failed runs, wait and wake durably, and resume from checkpoints while retaining completed steps and history |
| Collaboration budget | Root tasks share configurable 20/4/2/3/2 defaults, with project overrides, versioned task adjustments and explicit waiting at exhaustion |
| Everyday workspace tools | Search, rename, and restore conversations; choose light, dark, warm, or system appearance |

The development test tool currently requires the macOS sandbox. Linux blocks test execution while its sandbox remains unverified. See the [product guide (Chinese)](docs/product.md#工具与执行边界) for permissions and platform boundaries.

### Supported CLIs

| CLI | Command | Web integration |
| --- | --- | --- |
| Claude Code | `claude` | stream-json, scoped MCP, member-granted native read tools; explicit full access can also grant native write/shell within task scope |
| Codex | `codex` | exec JSON, scoped MCP, and native web access when authorized |
| Gemini CLI | `gemini` | stream-json and scoped MCP; host tools handle files and web access |
| Pi | `pi` | JSON and scoped MCP; native file and shell tools stay disabled |
| OpenCode | `opencode` | v2.2.5+ live text deltas and scoped MCP through a private server; model thinking variants are detected automatically |

Availability depends on the installed CLI's protocol, isolation capabilities, and actual model connection. An installed CLI does not imply a verified connection. Open Settings → Service (设置 → 服务) for diagnostics and connection verification.

### Terminal Mode

```sh
go run ./cmd/cocogem codex "Remember that this project is called Cocogem"
go run ./cmd/cocogem codex --resume latest "What is the project called?"
```

Replace `codex` with `claude`, `gemini`, `pi`, or `opencode`. Omitting the CLI name selects Claude. Terminal session references live in `.cocogem/` under the current directory, separately from isolated web collaboration sessions. See the [product guide (Chinese)](docs/product.md#原生命令行) for arguments, output, and session recovery.

## How It Works

```text
Your task
   ↓
Web workspace · Members and routing · Shared skills
   ↓
Go service · Plans and permissions · Scoped MCP · Sources and checkpoints
   ↓
Claude Code / Codex / Gemini / Pi
   ↓
Shared conversations · Independent review · Versioned deliverables
```

The backend uses Go's standard library HTTP server and SQLite; the frontend uses React, TypeScript, and Vite. Data is stored locally. Model services receive the content needed for a task, and search services receive queries. See [SECURITY.md](SECURITY.md) for the full boundaries. Cocogem currently serves a single local user and only listens on loopback addresses.

## Contributing

Report problems through [Issues](https://github.com/AcSunday/cocogem/issues), or send a focused pull request. Read the [contribution guide](CONTRIBUTING.md) and [product guide (Chinese)](docs/product.md) before you start.

GitHub Actions runs only `make test` on pushes and pull requests. Other local validation commands are listed below.

```sh
make test             # Go, native CLI fixtures, frontend unit tests, lint, and type checks
make check-contract   # OpenAPI and generated frontend type consistency
make e2e              # Build, production service smoke, and browser tests
make verify-redesign  # First-question, durable task, steering and budget journey
make security         # Credential checks and dependency vulnerability scans
```

Before your first browser test, run `npx playwright install chromium` in `web/`, or use an installed Chrome with `PLAYWRIGHT_CHANNEL=chrome make e2e`.

Automated tests use temporary processes and synthetic data without model credentials. `make probe-cli` and `make probe-isolation` check installed CLIs against a local fake model for protocol and isolation behavior. Real provider calls require separate verification.

## Acknowledgments and License

Thanks to [Clowder AI](https://github.com/zts212653/clowder-ai) for inspiration on multi-agent collaboration, shared skills, and project presentation, and to [Cat Cafe Tutorials](https://github.com/zts212653/cat-cafe-tutorials) for the introductory CLI examples.

Licensed under the [MIT License](LICENSE). Dependency licenses are listed in [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).
