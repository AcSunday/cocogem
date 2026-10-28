# 产品对齐审查与修正 — 2026-10-19

对齐依据是用户的原始目标：使用 Go 复刻 Clowder 的本机 CLI 多 Agent 协作
工作空间，同时支持讨论、Research 和代码分析，并能通过页面实际操作。
后续文档不能用自己新增的 API 优先假设覆盖这个目标。

本文保留 2026-10-19 的历史验收口径；其中离线演示运行模式现已移除，当前
自动化仅保留隔离测试 fixtures。公开合成资料现位于
[`examples/research-scenario.md`](../examples/research-scenario.md)。

## 已确认的偏差及修正

| 问题与触发 | 修正 | 回归依据 |
| --- | --- | --- |
| 网页只调 Anthropic API；已登录的本机 CLI 没有接入 | 网页、语义路由与连接验证统一接本机 CLI；成员显式选择执行器和默认/指定模型 | nativecli 进程/MCP 测试、HTTP/契约测试 |
| 隔离配置时误丢本机 API Key、供应商、地址和默认模型，转而尝试过期 OAuth | 按白名单复用本机连接配置，hooks/工具仍隔离；不要求另配 Key 或改登录方式 | 配置白名单、secret 不进 argv/日志、本机默认模型回归 |
| 终端原型只验证退出码和会话，不能承接网页取消、权限与产物 | 网页使用独立的有界 nativecli 执行器；限定工具桥、确认协议终态、取消进程组、记录独立会话；终端原型保持兼容 | 断流、会话漂移、非法工具、取消、持久化失败负例 |
| 默认讨论把工作空间成员全拉进两人成员会话 | 默认只使用当前会话启用成员；只有明确选择/点名才增加成员 | routing 单测和两成员浏览器回归 |
| 会话保存 Research 等默认流程，但刷新后变成自由协作 | 按会话初始化工作方式，保留本轮手动选择 | 切换/刷新浏览器回归 |
| 语义决策 schema 允许把任务文字写进角色成员 ID | 工具 schema 限定候选成员 UUID 和既有角色，非法建议由 CLI 在工具循环内修正 | 路由 schema 回归及实际自然语言指派联调 |
| 选了流程后忽略正文明确指派，或否定协调者后仍唤醒它 | 流程与正文冲突须澄清；默认接收人被否定时不执行；显式目标仍优先 | 真/演示路由的否定、流程和分工冲突负例 |
| 上游只发布结构化产物时，下游没收到正文与发现 | 持久化并传递精确交接包，包含消息、产物版本、发现和来源ID；页面可展开核对 | helper 与 Engine 产物独立交接测试 |
| 历史预算可能留旧回复丢最新补充 | 按最近完整轮次选取，当前问题与必需上游输入单独保留 | 历史预算回归 |
| 成员连改使用旧 revision，创建后再次保存重复 POST | 表单接收服务端保存后的成员 ID/revision | 连续编辑与创建组件/浏览器回归 |
| 停用角色、移除协调者后 UI 展示与提交 ID 不一致 | 从启用会话成员生成角色；失效值重新选择 | 角色默认值与协调者组件测试 |
| 成员同名难区分；历史工具标签随改名改变 | 选择界面附 handle；历史工具使用 Run 配置快照 | 身份标签组件与改名浏览器回归 |
| 成员常用职责容易与本次流程角色混淆 | 当前步骤角色和指令显式优先于成员背景职责与旧角色 | 模型上下文约束、真实样例有限评估 |
| 研究/质疑中间步骤可提前发布正式 report | 中间步骤只可发布 note；正式报告限指定汇总/复核步骤，工具 schema 和持久化前校验一致 | 中间产物类型负例及真实混合运行发现 |
| 原始 CLI/模型错误被后续“依赖跳过”覆盖 | 保留实际根因，跳过状态不覆盖它 | 原生失败与后续跳过回归 |
| 工具权限在排队后撤销，仍按旧快照派发 | 调度前比较当前权限，撤销则停止派发 | queued tool revocation 回归 |

## 设计选择及限制

- 身份、名称、职责、执行 CLI 和模型分别保存。相同 CLI 可以运行多个不同
  成员，但不能据此声称完成了跨模型复核。
- 本版仍是有限协作：独立讨论后汇总、研究后质疑汇总、代码分析后复核。
  临时自主委派、循环返工和任意流程图没有实现，继续明确列入后续范围。
- 网页继续使用受控只读工具；接入 CLI 不会自动开放 shell、写文件或本机
  所有 MCP。无法限制工具的 CLI 版本必须显示不支持并阻止启动。
- 旧 API 成员需要明确选择 CLI，历史消息、来源、产物和成员 ID 不重写。
  `.cocogem/<cli>.json` 不作为网页多成员会话存储。
- 离线演示仍有明确标识。浏览器 E2E 的演示通过只证明交互与流程，不证明
  实际模型可以登录、推理、质疑或产生合格报告。

## 实际页面验收状态

2026-10-19 已通过实际网页“服务与设置”发起本机 CLI 调用。首次 Claude
验证返回 OAuth 过期，但用户说明其本机实际使用 API Key。核对后确认是
适配器的配置隔离误丢了已有 API 连接配置，不能据此要求用户重新登录。

修复连接字段白名单后，网页复用本机配置的 Claude、Codex、Gemini 真实
连接验证均已成功，无需重新登录或在 Cocogem 另配密钥。

三名独立 Claude 成员的真实 Research 已完成：Run
`9b006294-e855-415d-bcaa-0fb752bb4154`，`demo=false`，三次不同原生
session ID；7 次成功宿主工具调用、2 次交接，发布 2 份 note 和 1 份
research_report。资料为本仓库公开合成示例 `research-scenario.md`，没有
访问外部网页。质疑成员补出连续时长、周末开放等证据缺口，最终报告保留
这些异议；这只是单个公开样例，不说明多 Agent 普遍优于单 Agent。

跨 CLI 的实际页面 Research 也已完成：Run
`aa1d4f82-5f0f-4914-aab6-bf66bbeba8b5` 为 Codex 研究、Gemini 质疑、
Codex 汇总，三步分别使用独立会话；两个中间 note 和一份最终 report
成功发布。三种 CLI 的同一轮尝试保留了实际上游 502 失败，不能把
Claude/Codex/Gemini 全三端同轮成功写为已验证。

自然语言派发 Run `8e8a2d38-4896-4def-8f37-557c9cd4d7aa` 实际输入
“缅因猫帮我读取……”，未选成员、未用艾特，由 Codex 执行语义决策，
只调用指定缅因猫一个成员并完成；路由和执行用量分别保存。

取消 Run `aff05a77-affb-4ff7-97c4-29278b07f532` 在真实 Codex 会话
初始化后由页面停止，状态收敛为 canceled；已记录的进程 PID/进程组
55655 在停止后已不存在。取消前的可见回复保留，没有新正式产物。

可审阅的脱敏结果见 [验收摘要](evidence/native-cli-acceptance.json)，页面
截图见 [真实 Research](screenshots/native-cli-research.png) 和
[自然语言指派](screenshots/native-natural-routing.png)。这些样例证明
链路与交接可用；模型角色遵循、研究完整性和结论正确率仍需更多任务评估。

本机二进制另有不调用真实供应商的测试：Codex 0.162.1/Gemini 0.64.0
连接本地假模型，触发真实 MCP 工具与原生续轮，并检查实际工具注册表。
这是当时的版本记录；当前探测版本、命令和结果见
[长任务恢复证据](evidence/restart-recovery-20261026.json)。
Codex 本机配置的供应商、默认模型、Bearer 和附加 header 也有独立本地
假服务回归，测试不使用用户真实凭证。

后续样例应继续补充：运行的 CLI/版本/模型、各成员独立会话、输入与确切
交接、实际来源读取、最终产物及保留的异议、取消与重启结果。数据仅使用可
公开样例，截图和仓库不得包含登录信息或私有资料。

## 2026-10-28：原生只读工具与联网能力证据

本轮按“成员现有授权派生原生只读工具、平台 MCP 补充”实施，没有新增授权项、
模式或 consent gate。结论按实际探针证据区分：

- Claude 2.1.312：隔离 cwd + `--add-dir` 工作区根 + `--restricted` 下，
  原生 `Read` 成功；工作区外路径、经符号链接越界和 `.env` 在 cwd=工作区根
  时被拒绝。后续复测发现 `permissions.deny` 不覆盖 `--add-dir` 根（同一
  设置下 `.env` 可被读取），因此实现改为文件步骤以工作区根为 cwd，并设置
  `CLAUDE_CODE_DISABLE_CLAUDE_MDS=1`；安装版回归确认公共文件可读、`.env`/
  越界/符号链接被拒绝且工作区与父目录 `CLAUDE.md` 均未进入请求。
- Claude 真实连接（既有 provider/model）：不使用系统代理时 `WebFetch` 返回
  domain safe-check 错误；同一 provider/model 在既有系统代理下 7.2 秒成功
  读取 DeBank 官方文档并返回摘要。本地假模型下另确认真实 CLI 的
  `WebSearch` 以 `tool_use`/字符串 `tool_result` 返回搜索摘要与链接提示，
  因此只按 `native_search` 线索保存。该证据只说明原生联网可经既有代理工作，
  不是对全部站点、搜索质量或三端等价性的验收。
- Codex 0.164.0 真实连接：`web_search="live"` 且 provider 声明
  `supports_standalone_web_search` 时，stream-json 的 `web_search` item 提供
  query/action/results 字段；单次探针曾返回可保存的 snippets/URL，`open_page`
  只返回“Total lines”一类元数据。正式 case
  `b73f4ac8-4285-4c58-a973-5c482f00ce23` succeeded（116.46 秒）也确实调用了
  原生 web，但两条 `item.completed` 都只有 `action: other` 且没有 `results`，
  没有来源；模型如实写明缺失后改用 MCP 搜索/取页完成 note。因此原生输出可能
  只有 metadata 或为空，空不作来源；旧单次探针只证明调用路径，不作为正式产品
  的原生成功保存。工具调用本身确实 completed，不伪称失败。Codex 原生结果仍只
  保存为 `native_search` 线索，不冒充已读原文。
- Gemini 0.65.0：本地假模型端点下可见 `google_web_search`/`web_fetch` 的
  `tool_use` 参数与 `tool_result` 事件，但 `tool_result.output` 只有
  “Content processed from prompt.” 一类状态文本，实际处理内容不进入
  stream-json。无法保真保存结果，故 Gemini 保持 MCP 联网并记录能力缺口。

以上真实调用来自开发机既有 CLI 配置，未改 provider/model/key，未关闭 TLS；
Gemini 与 Codex 的本地假端点实验不冒充真实 provider 验收。真实三端 UI
最终验收结果见文末及 [实施记录](native-tools-implementation.md)。

### 独立复审反例修正

Codex 独立 overlay 反例复核后，本轮保留原有原生优先方向并修复：

- 配置目录与工作区根均按物理路径比较；不同符号链接别名指向同一位置时，
  原生文件工具降级为 MCP，配置 marker 不进入可读边界。历史探针显示通用
  绝对通配 `//**` deny 语法不可靠；产品对配置目录使用具体绝对路径规则，
  且以上述根内降级为强边界，不把 deny parser 当唯一执行者。
- 空白原生输出不作证据；带真实 `file` 元数据的空文件单独保留。
- `list_sources` 以实际 UTF-8/JSON 字节分页（≤64 KiB，最多 20 条），cursor
  使用实际 offset，所有后续条目可取回；旧 `kind=web` 搜索页仍按 lead 处理。
- 撤销/停用/解绑会取消正在运行的 native step，取消后的原生观察不落库；
  普通改名不取消。
- Claude `Read` 保留 CLI 的 `tool_use_result.file` 范围与分段事实，缺元数据
  时标记截断，不承诺全文；宿主不重读文件。存在 `content` 字段时直接采用该
  实际返回正文（即使更短或为空），不以字符串长度猜测完整性。
- 三端 Provider 的运行期文件（Claude MCP/settings、Codex catalog 与
  `CODEX_HOME`、Gemini settings/policies）统一位于本轮临时目录的 `.cocogem`
  子目录：Claude 原生 deny 与宿主 MCP `tools.DeniedPath` 同时拒绝该组件，
  资料根覆盖临时目录（如 `/`）时 MCP 文件后备也读不到私有配置；根内降级
  逻辑保留为第二层防护。
- 原生联网环境合并 `NO_PROXY`/`no_proxy`；Codex 正式 catalog 不再声称仅
  使用 MCP 工具。
- 撤权取消使用有界 `WithoutCancel`，不随请求断开丢失；原生事件只带坐标元数据，
  CLI 返回的 `file.content` 只进入有界来源正文；`list_sources` 每页最多转换
  20 条候选后再按实际 JSON 字节收缩。
- Codex `web_search` 在收到同一 item 的 `item.completed` 前保持 pending，
  `turn.completed` 遇到未完成调用按协议失败关闭；精确重放不会重新打开已完成项。
  仅 `item.completed`、没有 `item.started` 的流仍合法（CLI 有时不发 started）。

真实证据仍以本机既有 CLI 配置为准：Codex `open_page` 只提供行数/引用元数据，
继续只保存为 `native_search` lead；Gemini 仍因结果正文不可得保持 MCP 联网。
Codex 正式产品 case 已 succeeded，但本次原生 web 没有可保存来源，不能据此宣称
原生搜索保存通过。此处为隔离快照的中间结论，最终主工作区与 DeBank 验收
见文末；初步双成员文件分析记录不代替最终复验。

### 原生优先：实际工具名、fallback 说明与局限

DeBank 原问题 case `58cfdff5-0393-4fd2-9df8-7327c7c82dff` 已 succeeded
（355.56 秒），`native_adapter` 实际暴露 `Read/Glob/Grep/WebSearch/WebFetch`，
但该次执行 `native_tools=[]`、MCP 调用 52 次：只注册原生工具不等于用户看到的
“原生优先”已生效。最小根因是工具选择提示不一致——旧系统提示只泛泛写
“Prefer them”，而 MCP `web_search`/`fetch_url` 的描述与 research 指令仍按默认
主路径反复点名，模型因此一直走能直接返回 `source_id` 的 MCP 路径。

本轮只在 `buildRequest` 做最小修正，没有新策略层、开关、API 字段或凭据路径：

- 系统提示列出本步实际启用的原生工具名，并给出实际 MCP fallback 映射
  （如 `read_file<-Read`、`fetch_url<-WebFetch`；Codex 合并工具为
  `web_search/fetch_url<-web_search`），要求先调用原生工具。
- 仅这些被映射的 MCP 工具描述改为 fallback-only：原生工具失败（引用确切错误）
  或无可使用内容后才调用。`read_source`、`list_sources`、`publish_artifact`、
  协作工具及没有原生能力的步骤保持原说明；`given_sources` 无原生 web 时
  web 工具说明保持正常，只标注实际启用的文件原生工具。
- `web_discovery`/`auto` 的研究指令按是否启用原生 web 分支：有原生 web 时
  先原生搜索/读取，fallback 仅用于失败或无可用内容；没有原生能力时保持原有
  `web_search`/`fetch_url` 流程。共享 skill 文本未改，仍按运行期能力而非品牌
  选择工具。
- 局限：这是提示与工具描述层面的引导，不是强制边界。MCP 桥仍随时接受被映射
  的 fallback 调用，因为原生失败必须可恢复；模型若忽略说明仍可先用 MCP。
  因此真实“自然先调原生”必须由不带提示的原问题复验证明，不能以本仓库的
  假模型/描述断言代替。

同一轮修复了原生 `tool_finished` 无 `duration_ms` 时 ToolTimeline 显示
“undefined ms” 的可见缺陷：paired 结果复用孤立 finished 的
`typeof === "number"` 判断，不伪造 0 ms；新增原生 started+finished 无时长
的组件回归，未改布局、滚动、折叠或 polling。

### 最终独立验收

DeBank 原问题原文复验 `416323bc-2ba5-428d-8932-da702a695742` succeeded，
293.16 秒，18 次 WebSearch + 31 次 WebFetch，49 条原生观察、1 份纪要；引用
的 20 个 native_web 来源 ID 均有效，未以搜索线索单独支撑 supported，产物
自动保留原生摘要限制。双成员文件复验 120.44 秒 succeeded，文件尾部反证
与 2 份产物保留；Codex 仍如前述只证明原生调用完成与 MCP 恢复，不夸大保存。

合回主工作区后全量检查通过：Go vet/race/shuffle/integration、前端 122 项，
Chrome E2E 7/7，契约生成哈希稳定、密钥扫描与安装版 Claude 文件边界通过。
当前主工作区保留并行 UI 修正，因此隔离快照的 3 项 UI 基线失败最终不再复现。
详情见 [实施与验收](native-tools-implementation.md) 及
[脱敏证据](evidence/native-tools-20261028.json)。未写入真实用户库，未 commit、
push 或发布，研究事实未作完整独立审计。
