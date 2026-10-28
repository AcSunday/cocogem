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
│   ├── model/             # 内部执行协议类型
│   ├── nativecli/         # 网页 CLI 执行、受限 MCP、兼容探测和进程生命周期
│   ├── proc/              # 进程身份、归属核实与受限终止
│   ├── skills/            # 单一源共享技能、索引、版本和按需读取
│   ├── store/             # SQLite、事务与迁移
│   ├── testutil/          # 仅测试的模型与网页 fixtures
│   ├── tools/             # 只读文件、公共网页与来源工具
│   ├── workflow/          # 路由、流程和运行调度
│   └── workspace/         # 领域类型与校验
├── api/                   # 生成的 OpenAPI
├── web/
│   ├── src/
│   │   ├── app/           # 页面状态和工作空间交互入口
│   │   ├── api/           # HTTP 客户端、测试与生成的 schema.ts
│   │   ├── components/    # Markdown、弹窗、消息和工具时间线
│   │   │   └── forms/     # 空间、成员、会话表单及测试
│   │   ├── lib/           # 共享展示文本与标识
│   │   ├── styles/        # CSS Modules 与类名解析
│   │   ├── test/          # 前端测试环境
│   │   └── main.tsx       # React 挂载
│   └── e2e/               # 浏览器端到端测试
├── scripts/               # 开发进程和第三方许可工具
├── examples/              # 可公开的人工评测资料
└── docs/                  # 产品、架构、验证及截图
```

CLI 使用 `go run ./cmd/cocogem`，Web 开发使用 `make dev`；任务执行直接调用本机已可用的 CLI，复用现有连接与模型配置，无需重新登录。
原先的 `go run .` 入口已迁移；已编译 CLI 的参数、退出码和工作目录中的
`.cocogem/` 会话文件格式保持兼容。

`make test` 包含原生 CLI 的进程集成测试；单独运行使用
`go test -race -tags=integration ./cmd/cocogem`。测试只使用临时替身进程，
不需要 CLI 登录或付费模型。

浏览器通过隔离的 Go 测试服务使用合成 fixtures；该服务仅由测试启动，
不打包进 Web 应用，也不作为运行模式提供。HTTP smoke 验证正式服务的
启动、工作空间持久化和备份恢复，不调用模型。

`dist/`、`web/dist/`、`web/node_modules/` 和浏览器测试输出由工具生成并被
Git 忽略。运行数据保存在配置目录，不能混入源码或提交到仓库。

前端测试与组件或客户端同目录。`api/openapi.yaml` 和
`web/src/api/schema.ts` 由 `make contract` 生成，请勿手工改写；提交前运行
`make check-contract` 验证漂移。前端 `web/go.mod` 仅用于阻止 Go 工具遍历
`node_modules`，不代表有独立的 Go 服务。
