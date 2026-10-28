# 长任务执行实现与验收

日期：2026-10-27（Asia/Shanghai）。实施依据：[长任务技术方案](long-task-budget-review.md)。定位和验收标准以 [product-v0.1.md](product-v0.1.md) 为准。

本文记录本次实现、独立审阅和验证边界。P0/P1 的本次实现和审阅闭环已完成，自动化门禁与真实长任务验收均通过；P2 原生旧 session resume 继续保留为未实现。

## 交付范围

| 项目 | 实现后的行为 | 代价与保留的边界 |
| --- | --- | --- |
| Run / Step 总时长 | 默认 `0`，健康长任务没有平台绝对时长截止；正期限仍可配置 | 长 Run 可能长期占用唯一 FIFO worker；不会自动压低费用 |
| 累计工具数与输出 | 删除 native 的 60 次工具拒绝及其强停、累计 32MiB / 65536 events / 256KiB text 强停 | 仍保留单工具 20s、64KiB 观察/参数、4MiB 单帧、100000-byte 初始宿主输入、12/32/4 委派图边界，以及原有每 Step 最多 4 次产物提交纠正 |
| 活动判断 | 真实新增文本、已验证 reasoning 元数据、工具活动刷新；init/status/usage/空白/协议回放不刷新 | 不评价研究价值；合法重复读取仍可继续，持续输出的无效循环需要用户干预 |
| 首响应与静默 | 默认首响应 10m；静默 15m 告警，显式 `stop` 才停止；显式 `0` 关闭对应检测 | 默认阈值是产品策略，尚无本地分布证明其最优 |
| 长正文 | UTF-8 安全的最多 8KiB 不可变块，最多 64KiB 预览；100ms 或块满时提交 | 正文、元数据、事件同事务；磁盘和事件历史仍会增长，不自动删证据 |
| 完整取回 | HTTP 正文快照分页、会话消息分页、事件历史分页；模型 `read_context` / `read_source` 每页最多 8KiB 原文、64KiB wire | 游标绑定对象/版本/位置；分页不能突破冻结授权，调用时重验权限 |
| 结构化证据 | `read_context field=artifact_json` 取回完整 findings / 来源 / limitations；交接提供预览、确切引用和内容摘要 | 展示预览不等于读过全文；来源捕获本身截断时继续明确标记 |
| 检查点与状态 | liveness stop 为 interrupted；context overflow 为 failed Attempt + interrupted Step；真实失败按逻辑 Step 聚合 | 多错误保留详情；兄弟执行和写入先收敛，再提交 Run 终态；用户先提交的取消优先 |
| 显式恢复 | 新 Attempt 使用 fresh 隔离会话，保存原 Attempt、正文、来源和未发布 draft，重新校验授权与执行身份 | 没有自动 provider retry；同计划且无新内容的重复 fresh overflow 拒绝机械重播 |
| 用量与界面 | known / partial / unknown，保留已知 subtotal；持久化每个 Attempt 的静默告警 | 未知用量不填 0；不能据此承诺实时精确金额 |

`internal/cli/` 原生入口未改；`internal/agent/loop.go` 保留原有 12 轮 / 60 工具 / 100KB 行为，仅给 usage 事件增加单轮统计语义。未新增依赖。

## 独立审阅促成的修复

Codex 按实施前完整工作树快照审阅本次增量，并把复现用例交回本机 Claude Code 修改。没有以脏工作树的 HEAD 差异冒充本次改动。

- **追加期间的全文游标**：最初绑定当前正文长度，新增输出会令旧游标失效。改为固定 chunk highwater 快照，原阅读可继续，用户主动切换到最新正文。
- **旧 inline 正文与长输出**：时间线在 SQL 中投影有界预览；模型分页直接读取正文窗口并流式计算摘要，避免先在 Go 中重组完整 chunk 正文。
- **协议活动与格式保真**：用协议 item/block 身份去重，保留合法相同内容与空白；空白不刷新活动。reasoning 只发送合法的活动元数据，不保存推理正文。
- **工具契约一致性**：真实模型验收发现 Research 的工具 schema 允许 `confirmed` 而后端拒绝，且错误未列合法值，导致报告重试失败。按产物类型统一 schema 与 validator 的合法状态，并给出可纠正的反馈；保留该失败样例，不用先前成功样例覆盖。
- **错误并发**：取消不吞掉独立鉴权/网络/协议失败；多个失败步骤保留每条详情；context overflow 不被宽泛字符串猜测误分类。
- **进程生命周期**：正常 leader 退出也检查同组子进程；取消 callback 必须 join，不能在函数返回后再信号旧 PID；无法确认归属时保留 uncertain。
- **落库一致性**：分块、已提交字节数、预览和事件同事务；预览在 UTF-8 前缀边界停止，持久写入失败停止执行；publish 被接受后立即保存原 Attempt draft，成功后才正式发布；恢复不会自动发布上次草稿，成员需根据原始证据重新提交。
- **旧任务采用新策略**：缺少旧策略及明确 deadline 时，从一次性 adoption 起应用当前正期限，历史开始时间不改；已有绝对 deadline 不延长，重启或 Resume 不重新领时间。
- **恢复拦截**：输入计划摘要覆盖实际职责、技能、工具权限、来源坐标/内容和执行身份；checkpoint 不把 UUID、版本、通知或重复 capture 计为新增内容。
- **前端有界与刷新**：最近事件窗口 500 条；正文和历史事件按页替换，分页游标保留有界窗口；过期请求不能写回其它 Run；并行 Step 的活动只能清除自己的告警。

## 验证记录

所有验收只用临时目录和合成资料，不迁移或改写用户正在使用的数据库。

| 最终代码自动化门禁 | 结果 |
| --- | --- |
| `make contract` 连续两次 | OpenAPI 与前端类型 hash 一致 |
| `make test` | 通过：脚本检查、gofmt、Go vet、全仓 race/shuffle、原生 CLI integration、前端 lint/typecheck、17 文件 112 单测 |
| `PLAYWRIGHT_CHANNEL=chrome make e2e` | 新构建后 7 项浏览器 E2E 通过，含长正文尾证据、>500 事件历史分页、跨 Run 过期响应、协作和取消 |
| `make e2e` 中正式服务 smoke | HTTP、进程停止、持久化备份恢复通过；没有模型调用 |
| `make probe-cli` | Claude 2.1.311 / Codex 0.164.0 / Gemini 0.65.0 已安装二进制工具协议探测通过 |
| `make probe-isolation` | 三 CLI 对本地合成端点的隔离验证通过 |
| `go run ./cmd/checksecrets` | 通过；另按相同规则扫描当前未跟踪文本文件，无命中 |

自动化还覆盖：三 parser 超过累计 32MiB / 70000 events 不强停、4MiB 单帧仍拒绝、324KB 中文正文逐字还原、100 次重复同参工具调用后仍可 publish、冻结/采用期限、权限撤销、fresh session 身份、并行取消/多错误聚合、draft 重提及 no-progress 重播拦截。

真实 CLI 探测通过与真实供应商成功是两件事：本机二进制对本地合成端点的工具清单和隔离验证，只证明适配协议。真实模型验收另列，不将 fake 作为 live。

已执行的真实样例：

- Claude Code 2.1.311，复用原有 `deepseek-flash[1M]`：65 个独立 500 行文件窗口，研究成员累计 73 次宿主工具调用，研究、复核和汇总均分页取得预览之外的来源尾部。3 个独立 fresh session、3 个步骤成功，189.56 秒完成；研究报告保留否定条件、日期、两个价格及单位、冲突和证据不足。一次无效的 publish 请求经契约错误反馈后修正。
- 复核产物主动指出研究者未分页看完所有 capture 却声称完整核实的过强表述，要求降低断言强度；这支持本次样例的证据边界，不能推出通用 research 质量已证明。
- Claude 协调 / Codex 研究 / Codex 独立复核的混合协作样例成功，早期 64.51 秒，最终集成后重跑 91.81 秒；均为 5 个步骤、2 次交接与 2 次协调者后续调用、3 位成员实际读取技能。产物为 note，后续 Research 状态修正不改变其合法状态。
- 采用 Gemini 复核的混合样例失败：现有上游拒绝 Gemini 原生 API 协议。未改变凭据、端点、provider 或模型，也未把另一 CLI 成功记为 Gemini 成功。

上述样例先于最后集成修复。集成后真实重跑曾因 Research schema / validator 不一致失败（364.44 秒；65 窗口及各角色尾部读取已通过，coordinator 的产物契约未通过）；修复后在最终代码上重新验收通过：

- **167.34 秒，3 个步骤和 3 个 fresh session 全部成功**；65 个独立文件窗口捕获、研究成员累计 71 次宿主工具调用；critic 与 coordinator 完成已提交上下文读取，并实际分页到来源尾部。
- coordinator 的 `research_report` 一次提交成功，合法状态为 supported / disputed / insufficient_evidence。
- Codex 逐段检查最终报告的正文和 findings：保留第 32498 行否定条件、2026-10-27、3.25 USD/kg 与 4.10 USD/kg、样本基础未知、日期语义不明和口径差异；没有把条件价格升级为已证实的无条件结论。
- 报告明确保留“65 次 capture 不代表模型逐字看完所有 capture”的覆盖性争议。平台记录证明调用与捕获，不能用调用数替代内容充分性。
- 最后修正后再次完整运行 `make test` 与新构建的 `make e2e`，112 项前端单测及 7 项 Chrome E2E 继续全部通过。源码 hash 在真实验收期间未变化。

脱敏验收元数据见 [long-task-20261027.json](evidence/long-task-20261027.json)。包含成功和失败样例及其原因；不包含凭据、供应商原始响应、真实用户资料、数据库或本地 CLI 配置。

真实验收可重现命令：

```bash
COCOGEM_CHECK_LIVE_LONG_TASK=1 COCOGEM_LIVE_LONG_TASK_CLI=claude \
  go test -count=1 -v -run '^TestLiveLongTaskEvidence$' -timeout=13m ./internal/httpapi
COCOGEM_CHECK_LIVE_COLLABORATION=1 COCOGEM_LIVE_REVIEWER_CLI=codex \
  go test -count=1 -v -run '^TestLiveMixedCLICollaboration$' -timeout=15m ./internal/httpapi
```

这些命令调用现有配置的真实模型，可能消耗额度；常规 `make test` 默认跳过该显式 opt-in。最终报告的独立审阅使用了仅增加合成报告导出的测试 overlay，未改变输入、断言、生产代码或 CLI/provider 配置。

## 交付与已知边界

- P0/P1 以本次代码和验收为准；P2 的原生旧 session resume 尚未实现。fresh Attempt 是从持久化上下文开启新隔离会话，不能称为恢复原生旧会话。
- 默认不限累计时长/次数会增加存储、费用和队列等待；单 worker 调度保留。没有预算评分、p75/p90 自动截止、自动摘要替换证据或自动重试框架。
- 进程归属检查针对记录的进程组；自行脱离该组的后代不在此证明范围。native adapter 仍严格限制可执行工具。
- 固定内存的近期协议 item 去重不能辨认远超缓存窗口的任意旧帧回放；这依赖当前 CLI 流协议的有序追加语义，不构建持久 per-frame 索引。
- macOS 本机验证与合成行为测试不能代替 Linux 实机、未来 CLI 版本、长达数日运行或普遍内容质量评测。
- 工作树原有大量未提交工作已保留。本次未 commit、push 或替换当前常驻服务；构建产物只用于隔离验收。
