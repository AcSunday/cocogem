# 项目结构

根目录保留构建配置、模块清单和开源入口文档。命令入口放在 `cmd/`，
应用实现放在 `internal/`，测试与被测代码同目录；首版不提供公共 Go SDK。

```text
cocogem/
├── cmd/
│   ├── cocogem/           # 原生 Claude/Codex/Gemini CLI 入口
│   ├── cocogem-server/    # Web 服务入口
│   ├── contract/          # OpenAPI 生成
│   ├── checksecrets/      # 已跟踪文件的凭证检查
│   ├── smoke/             # 进程、HTTP、备份恢复验证
│   └── release/           # 本地发布候选包
├── internal/
│   ├── cli/               # 原生 CLI 参数、事件、进程、会话及测试
│   ├── agent/             # 有界工具循环
│   ├── httpapi/           # REST、SSE、本地认证与契约
│   ├── model/             # 模型协议适配与离线模型
│   ├── store/             # SQLite、事务与迁移
│   ├── tools/             # 只读文件、公共网页与来源工具
│   ├── workflow/          # 路由、流程和运行调度
│   └── workspace/         # 领域类型与校验
├── api/                   # 生成的 OpenAPI
├── web/                   # React 前端与浏览器测试
├── scripts/               # 开发进程和第三方许可工具
├── examples/              # 可公开的演示资料
└── docs/                  # 产品、架构、验证及截图
```

CLI 使用 `go run ./cmd/cocogem`，Web 演示使用 `make demo`。
原先的 `go run .` 入口已迁移；已编译 CLI 的参数、退出码和工作目录中的
`.cocogem/` 会话文件格式保持兼容。

`make test` 包含原生 CLI 的进程集成测试；单独运行使用
`go test -race -tags=integration ./cmd/cocogem`。测试只使用临时替身进程，
不需要 CLI 登录或付费模型。

`dist/`、`web/dist/`、`web/node_modules/` 和浏览器测试输出由工具生成并被
Git 忽略。运行数据保存在配置目录，不能混入源码或提交到仓库。
