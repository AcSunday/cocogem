# Cocogem 上下文工程重构设计

状态：**历史提案；后续评审与目标决策已合并到 [redesign.md](redesign.md)**

实现入口为 [项目协作内核重构设计](redesign.md)，尤其第 5–7 节。本文正文保留用于追溯；首条消息固定、scope 与 ContextPlan 的关系、失败轮次排除等冲突项，以重构设计的已确认决策为准，不另行按本文旧提案实现。
日期：2026-11-03
范围：会话上下文的选择、预算、压缩、检索、角色视图和可观测性
不包含：具体代码实现、真实 CLI/供应商验收、跨会话长期记忆

本文记录最初提交评审的设计，不代表当前已经实现的产品行为；后续接受的目标与修订以 `docs/redesign.md` 为准。当前行为仍以 `docs/product.md` 和仓库代码为准。

## 评审摘要

- 不再把“共享历史”作为唯一上下文来源，改为 `ContextPlan` 统一组装。
- 固定核心（首条用户消息、pin、成功交付）优先；最近原文、旧轮次摘要、检索结果分层使用。
- 预算按模型窗口/成员预算动态计算，不使用固定 60%。
- 每个步骤记录 Context Manifest，用户可以查看成员实际看到了什么、为什么。
- 先做确定性 ThreadBrief 和可观测性，再引入 LLM 摘要；避免一上来就增加成本和幻觉风险。

---

## 1. 背景

Cocogem 现在会把同一会话中最近若干轮已提交消息作为共享历史注入每个成员，并按成员 `context_size` 分配 token 预算。这个方案解决了“不要无限增长”和“不要超过模型窗口”的问题，但仍然是：

> 固定预算 + 最近优先 + 单条消息前缀截断。

它不能很好回答以下问题：

- 早先的用户约束、已确认决策是否还在？
- 一份很长的报告，结论在末尾，前缀预览是否把结论丢了？
- 旧消息和当前任务相关，但因为时间久被淘汰了怎么办？
- 每个成员看到的上下文不同，用户如何知道差异？
- 失败轮次的中间内容是否会被误当成已验证结论？
- 1M 窗口的模型是否真的应该填满 1M？

本设计把“历史截断”升级为“上下文组装”：按优先级、模型预算、角色范围、摘要和检索共同决定每次调用看到什么。

---

## 2. 当前基线

### 2.1 已有能力

- 消息、产物、来源、Run 快照和交付记录都是不可变历史；摘要不会覆盖原文。
- `recentHistory` 选择最近的完整轮次；最新失败轮次中已完成的消息可以进入共享历史。
- 每个步骤只读取自己的冻结范围：共享历史、依赖步骤、显式引用和角色可见的产物。
- `read_context` / `read_source` 支持按确切 ID 分页读取完整正文。
- `context_size` 是成员级估算 token 预算；未设置时使用全局共享历史默认值。
- 当前共享历史默认：128 条消息 / 64,000 估算 tokens；成员设置 `context_size` 时，历史预算取其 60%。
- 单条消息预览默认约 4K tokens，大窗口成员最高约 32K tokens。
- 独立观点、交叉质疑、复核、最终交付的角色隔离已经存在。

### 2.2 主要缺口

| 缺口 | 后果 |
| --- | --- |
| 固定 60% 历史占比 | 不同任务的实际提示构成差异很大，容易浪费预算或挤掉必需内容 |
| 最近优先 | 早期约束、决策和产物引用会被淘汰 |
| 前缀截断 | 长报告的结论、限制和反例通常在末尾，容易被丢掉 |
| 没有滚动摘要 | 旧轮次只能整体丢弃，不能保留决策和未决问题 |
| 没有检索 | 与当前任务相关但时间久远的消息无法被找回 |
| 没有上下文清单 | 用户无法知道某个成员实际看到了什么、为什么被排除 |
| 没有模型窗口感知 | `context_size` 可能大于模型真实窗口，或远小于模型能力 |
| 失败轮次语义不明确 | 中间内容可能被当作最终结论 |
| 没有成本预算 | 大窗口 × 多成员 × 多步骤会快速放大费用和延迟 |

---

## 3. 研究结论

### 3.1 关键发现

1. **上下文长度不等于有效上下文。** RULER 发现很多宣称 32K 以上的模型在 32K 时已经明显退化；Chroma 的 Context Rot 报告显示，随着输入增长，模型对信息的可靠使用会下降。填满 1M 不等于用好 1M。
2. **位置很重要。** Lost in the Middle 观察到关键信息位于长上下文中部时更容易被忽略，开头和结尾通常更好。因此“固定核心放开头、最近对话放结尾”比简单追加更合理。
3. **压缩和结构化笔记是主流做法。** Anthropic 建议使用 compaction、结构化笔记、子 Agent 和 just-in-time 检索，目标是“最小的高信号 token 集合”，而不是最大上下文。
4. **分层记忆优于全量历史。** MemGPT/Letta 用核心内存 + archival memory + 工具分页；Generative Agents 用 memory stream + recency/importance/relevance 检索 + reflection。
5. **滚动窗口 + 摘要已被工程验证。** OpenHands Condenser 保留头部系统提示和尾部最近事件，压缩中间历史；LangGraph、CrewAI、OpenAI Agents SDK 也都提供 trim/summarize/session 机制。
6. **多 Agent 需要共享事实，但不需要人人全量。** MetaGPT 使用共享 message pool + 按角色 publish-subscribe，结构化产物比逐字对话更适合跨角色传递。
7. **成本和缓存是上下文设计的一部分。** Manus 强调稳定前缀、KV-cache 命中率、把大结果放文件系统、用 todo recitation 把目标带回注意力范围。
8. **摘要必须可追溯。** 摘要本身可能出错，因此必须保留来源消息/产物 ID，并明确标记为派生内容，不能当作证据。

### 3.2 参考对照

| 来源 | 核心做法 | 本设计借鉴 |
| --- | --- | --- |
| [Anthropic: Effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) | compaction、结构化笔记、子 Agent、just-in-time 检索 | 分层上下文、动态压缩、按需读取 |
| [MemGPT](https://arxiv.org/abs/2310.08560) / [Letta](https://docs.letta.com/concepts/letta) | 核心内存 + archival memory；工具分页 | 固定状态与归档历史分离 |
| [Generative Agents](https://arxiv.org/abs/2304.03442) | recency + importance + relevance 检索；reflection | 旧消息检索评分，不只看时间 |
| [OpenHands Condenser](https://docs.openhands.dev/sdk/arch/condenser) | 保留头尾，摘要中间；阈值触发；注意缓存失效 | 压缩策略和触发时机 |
| [LangGraph memory](https://docs.langchain.com/oss/python/langgraph/memory) | trim / delete / summarize；短期 thread + 长期 store | 标准的分层记忆接口 |
| [CrewAI Memory](https://docs.crewai.com/en/concepts/memory) | 语义、recency、importance 综合评分；返回匹配原因 | 检索评分与可解释性 |
| [MetaGPT](https://arxiv.org/abs/2308.00352) | 共享 message pool + 角色 publish-subscribe；结构化文档 | Run Brief + 角色视图 |
| [Mem0](https://arxiv.org/abs/2504.19413) / [Zep](https://arxiv.org/abs/2501.13956) | 事实抽取、consolidation、时序知识图谱 | 结构化记忆、时间有效性、来源 |
| [Manus context engineering](https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus) | 稳定前缀、KV-cache、文件系统外置、目标 recitation | 缓存友好布局、大结果外置、目标复述 |
| [Lost in the Middle](https://arxiv.org/abs/2307.03172) | 长上下文中部信息更容易丢失 | 核心约束放开头，当前任务放结尾 |
| [RULER](https://arxiv.org/abs/2404.06654) / [Context Rot](https://research.trychroma.com/context-rot) | 宣称窗口 ≠ 有效窗口；输入越长越不可靠 | 动态预算、评测有效上下文 |

> Mem0/Zep 的收益数字来自厂商论文和基准，方向可借鉴，但不直接作为 Cocogem 的效果承诺。

---

## 4. 目标与非目标

### 4.1 目标

1. 用一个可解释的 `ContextPlan` 决定每次模型调用看到什么。
2. 固定核心状态（任务、约束、决策、未决问题、产物引用）不因时间久远而丢失。
3. 最近原文、旧轮次摘要、检索结果各司其职，而不是互相替代。
4. 预算按模型窗口和实际提示构成动态计算，不再使用固定百分比。
5. 每个步骤的上下文保持角色隔离，不泄露独立观点或未授权内容。
6. 摘要和检索结果可追溯到原始消息、产物和来源。
7. 用户能看到“这个成员看到了什么、为什么”。
8. 不覆盖、不改写原始历史；所有派生内容都可重建。

### 4.2 非目标

- 不在本阶段实现跨会话的全局个人记忆。
- 不引入外部向量数据库或云端记忆服务。
- 不改变现有工具权限、成员授权和沙箱边界。
- 不自动把摘要升级为“证据”或“交付”。
- 不承诺真实供应商 token 数与估算完全一致。

---

## 5. 目标架构

### 5.1 五层上下文

| 层 | 内容 | 默认策略 |
| --- | --- | --- |
| P0 Pinned State | 当前任务、首条用户消息、用户 pin、权限边界、已确认交付引用；摘要阶段可加入决策和未决问题 | 总是包含，不可静默淘汰 |
| P1 Required Scope | 当前步骤依赖的确切上游消息、产物版本、来源 ID、角色指令 | 必需，优先保留 |
| P2 Recent Window | 最近若干轮原始消息和当前 Run 状态 | 优先保留，按预算截断 |
| P3 Episodic Summary | 更早轮次的结构化摘要：决策、约束、分歧、开放问题、产物引用 | 超预算时使用 |
| P4 Retrieval | 从旧消息、产物、来源中检索出的相关片段 | 按任务和角色选择性使用 |
| P5 On-demand | 完整消息、报告、文件、网页正文 | 不自动注入，通过工具读取 |

### 5.1.1 ThreadBrief 的确定性构建

确定性 ThreadBrief 不调用 LLM，按以下顺序构建：

1. 当前任务与参与者。
2. 首条用户消息（通常包含原始目标和约束）。
3. 用户显式 pin 的消息、产物版本和来源。
4. 最近成功 Run 的交付消息和 `产物ID@版本号`。
5. 最近失败/中断 Run 的状态标记。
6. 其余用户消息放入 P2 的高优先子集，先于普通 assistant 历史填充，直到 pinned/用户消息预算用完。

没有 LLM 摘要时，**不能保证任意一条早期用户约束都会自动保留**。因此：

- 首条用户消息、用户 pin 和成功交付是确定性保障。
- 其它早期用户约束需要用户 pin，或在 Phase 3 由摘要/约束账本保留。
- 如果用户消息层超出 pinned/用户消息预算，manifest 必须明确列出被排除的 user 消息，并提示用户 pin 或提高预算。

这一点必须在 UI 和产品文档中诚实表达，不能制造“所有早期内容都不会丢”的错觉。

### 5.2 Context Assembler 流程

```text
Thread history / artifacts / sources / pins
                  │
                  ▼
          ThreadBrief（确定性）
                  │
      ┌───────────┼───────────┐
      ▼           ▼           ▼
 Recent raw   Summaries   Retrieval index
      └───────────┼───────────┘
                  ▼
          Context Planner
   （角色、依赖、预算、优先级）
                  │
                  ▼
            ContextPlan
                  │
                  ▼
        Prompt + Context Manifest
```

每次步骤派发前生成一个 `ContextPlan`，并在调用前做一次 token 预算校验。计划包含：

- 使用了哪些消息/产物/来源/摘要
- 每项的 token 估算
- 被排除的项和原因
- 使用的模型窗口、预留 token、压缩策略
- 可复现的 digest

### 5.3 预算与优先级

#### 预算公式

```text
effectiveWindow = min(模型真实窗口, 成员显式 context_size)
reserved = 系统提示 + 工具定义 + 输出预留 + 安全余量
available = effectiveWindow - reserved
```

- 模型窗口优先使用 CLI/模型元数据。
- 拿不到时使用显式 `context_size`。
- 两者都没有时使用经过校准的保守默认值，并记录“窗口未知”。
- 不使用固定的 60% 历史占比。

#### 优先级与淘汰

```text
P0 Pinned State          → 永不静默淘汰
P1 Required Scope        → 永不静默淘汰；放不下则失败并说明原因
P2 Recent Window         → 从最旧开始淘汰
P3 Episodic Summary      → 先减少摘要范围，再淘汰
P4 Retrieval             → 先淘汰
P5 On-demand             → 不占自动预算
```

如果 P0/P1 本身超过可用预算，返回明确的 `context_budget_exhausted` 错误，而不是悄悄截断后继续执行。错误应告诉用户：需要缩小任务、减少必需引用、提高成员预算，或改用可分页读取的方式。

#### 预算分组

建议在配置中支持以下上限，但默认值由实际窗口计算：

| 类别 | 说明 |
| --- | --- |
| `pinned_tokens` | 固定状态上限 |
| `user_history_tokens` | 首条之外的用户消息上限 |
| `required_tokens` | 必需交接/产物上限 |
| `recent_tokens` | 最近原文上限 |
| `summary_tokens` | 摘要上限 |
| `retrieval_tokens` | 检索结果上限 |

这些是“保护性上限”，不是固定比例；实际分配按优先级动态调整。

#### 示例

一个 100 轮会话，首轮用户写“只读分析，不要改代码”：

```text
P0 首条用户消息 + 当前任务 + 用户 pin + 最近交付引用
P2 最近 6 轮原始对话
P3 第 2–80 轮的结构化摘要（决策/约束/未决问题）
P4 检索到的第 12 轮产物 `artifact@v2`
P5 被排除的其余 60 轮，只保留 ID 和 read_context 入口
```

Manifest 会记录：为什么第 12 轮被检索、摘要覆盖哪些来源、哪些消息被排除、预算剩余多少。

### 5.4 压缩与摘要

#### 触发时机

- 只在 **Run 边界** 或 **步骤边界** 检查，不在模型调用中途压缩。
- 达到有效预算的高水位（建议默认 75%，可配置）时触发。
- 压缩会改变前缀，破坏 KV-cache；因此不要每一步都重写历史。

#### 压缩策略

参考 OpenHands Condenser：

1. 保留头部：系统提示、用户约束、已确认决策。
2. 保留尾部：最近原文、当前任务。
3. 摘要中间：将较早轮次压缩成结构化摘要。
4. 摘要保留来源消息 ID、产物版本和来源 ID。
5. 原文永远不删除；摘要只是派生的视图。

#### 摘要内容

建议固定字段，避免自由发挥：

```json
{
  "decisions": [],
  "constraints": [],
  "open_questions": [],
  "disagreements": [],
  "artifact_refs": [],
  "source_ids": [],
  "failed_or_interrupted": []
}
```

摘要必须带：

- `source_message_ids`
- `source_artifact_refs`
- `generated_by_model`
- `created_at`
- `digest`
- `version`

摘要标记为“生成内容，不是证据”。模型需要原文时，仍通过 `read_context` 读取。

#### 引入 LLM 摘要前的保守做法

在引入 LLM 摘要之前，可以只做 **确定性 ThreadBrief**，不调用模型：

- 最近用户消息
- 最近成功 Run 的交付消息和产物引用
- 用户显式 pin 的消息/产物
- 失败/中断标记
- 当前任务和参与者

这样可以先解决首条用户消息、pin 和成功交付引用丢失的问题，再决定是否引入摘要模型。

### 5.5 检索

第一阶段使用 SQLite FTS5，不引入外部向量库：

- 消息、产物标题/正文、来源标题/路径/URL 建立索引。
- 中文可使用 FTS5 `trigram` tokenizer；若环境不支持，回退到受限的 `LIKE` 检索。
- 查询由当前任务、步骤指令、角色和依赖组成。
- 评分建议：`BM25 + recency + importance + pinned`。
- 结果必须带确切消息/产物/来源 ID。
- 只检索当前 thread；权限和步骤 scope 仍然生效。
- 检索结果按 token 预算截断，并记录到 manifest。

后续可以增加本地 embedding，但必须是可选能力，且不能把原文或用户数据发送到外部服务。

### 5.6 多 Agent 角色视图

- 每个 Run 生成一份共享的 `RunBrief`：任务、约束、计划、已交付产物引用、未决问题。
- 每个步骤从 `RunBrief` 派生自己的 `ContextPlan`，再叠加角色和依赖范围。
- 独立观点步骤仍然看不到其他成员的独立观点。
- 复核者只获得被复核对象和必要上游。
- 最终交付者获得完整的依赖集合。
- `ContextManifest` 记录每个成员实际看到的差异。

这样既保证基本前提一致，又不会让所有成员拿全量历史。

### 5.7 失败和重试

建议区分：

| 来源 | 默认行为 |
| --- | --- |
| 用户消息 | 正常进入历史 |
| 成功轮次的最终交付 | 进入历史，带交付引用 |
| 失败轮次的已完成中间消息 | 普通追问默认不进入；重试时进入并明确标注“未验证中间记录” |
| 失败消息正文 | 永不进入 |
| 被中断的步骤 | 只作为诊断上下文，不作为证据 |

重试时应把来源 Run ID、失败状态和中间内容标记写入 manifest，避免模型把草稿当成最终结果。

### 5.8 Prompt 布局与缓存

建议顺序：

```text
1. 稳定的 system / policy / tool 定义
2. ThreadBrief / P0 固定状态
3. Episodic summaries（标记为派生）
4. Retrieved context（标记来源和 ID）
5. P1 精确上游交接
6. P2 最近原文（最新在后）
7. 当前任务和步骤指令（最后）
```

理由：

- 固定核心放开头，当前任务放结尾，降低 Lost in the Middle 风险。
- 稳定的 system/tool 前缀有利于 KV-cache。
- 摘要和检索只在边界变化，不要在每一步重新排序。
- 当前任务放在最后，符合“最近注意力”原则。

### 5.9 安全与可信

每个上下文项标记来源类型：

- `policy`：系统/产品策略
- `user`：用户输入
- `model_output`：成员回复
- `summary`：派生摘要
- `source`：文件/网页/工具观察
- `failed_intermediate`：失败轮次中间记录

规则：

- 摘要、成员回复和来源都不能覆盖系统策略或扩大权限。
- 摘要不能自动升级为证据；引用必须回到 source ID。
- 失败轮次中间内容默认不作为事实。
- 检索结果不能突破 thread、step、角色和权限范围。
- Context Manifest 记录来源和信任级别，便于审计。

---

## 6. 数据模型

### 6.1 `thread_pins`

用户显式固定的消息或产物，优先级高于普通历史。

```text
id            TEXT PRIMARY KEY
thread_id     TEXT NOT NULL
kind          TEXT NOT NULL        -- message | artifact | source
ref_id        TEXT NOT NULL
note          TEXT
created_at    TEXT NOT NULL
```

唯一约束：`(thread_id, kind, ref_id)`。`kind=artifact` 时 `ref_id` 使用 `产物ID@版本号`，固定确切版本，而不是“最新版本”。

### 6.2 `context_summaries`

```text
id                  TEXT PRIMARY KEY
thread_id           TEXT NOT NULL
from_message_id     TEXT NOT NULL
to_message_id       TEXT NOT NULL
from_run_id         TEXT NOT NULL
to_run_id           TEXT NOT NULL
source_message_ids  TEXT NOT NULL   -- JSON array
source_artifact_refs TEXT NOT NULL  -- JSON array
content             TEXT NOT NULL   -- structured JSON + rendered text
version             INTEGER NOT NULL
digest              TEXT NOT NULL
generated_by_model  TEXT
created_at          TEXT NOT NULL
```

特点：

- 只追加，不覆盖旧摘要。
- 覆盖范围不能与当前最近窗口重叠。
- 原文变更时摘要不重写；通过 digest 和来源校验发现不一致。
- 线程永久删除时级联清理。

### 6.3 `context_manifests`

```text
id                  TEXT PRIMARY KEY
run_id              TEXT NOT NULL
step_id             TEXT NOT NULL
attempt_id          TEXT NOT NULL
policy_version      TEXT NOT NULL
model_window        INTEGER NOT NULL
context_budget      INTEGER NOT NULL
reserved_tokens     INTEGER NOT NULL
estimated_tokens    INTEGER NOT NULL
included            TEXT NOT NULL   -- JSON
excluded            TEXT NOT NULL   -- JSON
summary_ids         TEXT NOT NULL   -- JSON array
retrieval_ids       TEXT NOT NULL   -- JSON array
digest              TEXT NOT NULL
created_at          TEXT NOT NULL
```

Manifest 应支持：

- “为什么这条消息被排除”
- “为什么这条旧消息被检索进来”
- “本次使用了哪个模型窗口/预算策略”
- “估算 token 与实际 usage 的偏差”

Manifest 只保存 ID、token 数、原因和 digest，不复制消息/产物正文，避免重复存储和泄露。

### 6.4 保留现有对象

- `messages`、`artifacts`、`sources`、`runs`、`steps`、`attempts` 不重写。
- `Run` 和 `Attempt` 增加可选的 manifest 引用。
- 旧 Run 没有 summary/manifest 时，使用现有共享历史路径作为兼容回退。

---

## 7. API 与 UI

### 7.1 API 建议

| 接口 | 用途 |
| --- | --- |
| `GET /api/v1/runs/{id}/context` | 查看某个 attempt 的 Context Manifest |
| `GET /api/v1/threads/{id}/context-state` | 查看当前 ThreadBrief、摘要和 pin 摘要 |
| `POST /api/v1/threads/{id}/pins` | 固定消息/产物/来源 |
| `DELETE /api/v1/threads/{id}/pins/{pinID}` | 取消固定 |

Manifest 可以在 `RunView` 中返回摘要，在专门接口返回完整 included/excluded 明细。

### 7.2 UI 建议

- 在 Run/步骤面板增加“上下文”页签。
- 显示：
  - 固定状态
  - 最近原文条数和 token
  - 摘要覆盖范围
  - 检索命中项
  - 被排除项及原因
  - 估算 token 和实际 usage
- 允许用户 pin/unpin 关键消息或产物。
- 对失败轮次中间内容显示“未验证”标签。

---

## 8. 实施阶段

### 8.1 代码落点（参考）

| 现有位置 | 改造方向 |
| --- | --- |
| `internal/workflow/context.go` | `recentHistory` 保留为回退；新增 `ThreadBrief`、`ContextPlanner`、优先级和预算逻辑 |
| `internal/workflow/engine.go` | 派发前生成 `ContextPlan`；`groupHistory` 改为上下文快照；`planDigestFor` 使用同一计划 |
| `internal/workflow/request.go` | 按 ContextPlan 组装 prompt；替换固定共享历史循环 |
| `internal/workflow/context_tools.go` | `read_context` 继续作为按需读取；scope 来源改为 ContextPlan；manifest 记录检索/摘要 |
| `internal/store/schema.sql` | 只追加 pins/summaries/manifests 表及索引 |
| `internal/httpapi/server.go` / `contracts.go` | 新上下文查询和 pin 接口；保持旧字段兼容 |
| `web/src/` | 上下文检查器、pin/unpin、未验证标签 |
| `api/openapi.yaml` / `web/src/api/schema.ts` | 由 `make contract` 生成，不手改 |

每阶段结束都要同步 API、OpenAPI、生成类型、fixtures 和行为测试。

### Phase 1：可观测 + 动态预算 + 头尾预览

- 增加 Context Manifest。
- 记录实际 `input_tokens` 与估算偏差。
- 长消息预览改为头尾保留，而不是只保留前缀。
- 预算从固定 60% 改为按模型窗口/显式 `context_size` 动态分配。
- 保留现有共享历史逻辑作为回退。

验收重点：不改变现有角色隔离；旧行为可回退；manifest 可解释。

### Phase 2：确定性 ThreadBrief + 用户 pin

- 生成确定性 ThreadBrief：首条用户消息、用户 pin、成功交付、失败状态、产物引用。
- 增加 pin 表和 UI。
- ThreadBrief 作为 P0 总是注入。
- 不引入 LLM 摘要。

验收重点：首条用户消息、pin 和成功交付不丢失；用户可以固定关键消息；P0 不被淘汰。

### Phase 3：摘要 + 压缩

- 增加 `context_summaries`。
- 在阈值处压缩中间轮次，保留头尾。
- 摘要必须带来源和 digest。
- 默认只在 Run/步骤边界触发，避免缓存抖动。

验收重点：摘要可追溯；原历史不改写；压缩后任务成功率不下降。

### Phase 4：检索 + 上下文检查器

- SQLite FTS5/trigram 索引消息、产物、来源。
- 按任务/角色检索，使用 BM25 + recency + importance。
- UI 展示 manifest 和检索原因。
- 支持用户 pin 覆盖检索排序。

验收重点：旧但相关的信息能被找回；检索不越权；结果可解释。

### Phase 5：模型窗口和成本

- 优先读取 CLI/模型真实窗口。
- 按 provider 做 token 计数校准。
- 增加每次 Run 的 token/成本预算。
- 优化稳定前缀和 prompt cache。

验收重点：预算与真实窗口一致；成本和延迟可观测。

---

## 9. 测试与验收

### 9.1 单元测试

- 预算分配：P0/P1 不被淘汰；低优先级先淘汰。
- 固定比例移除后，不同窗口/角色得到不同计划。
- 头尾预览：长消息保留结论和开头。
- 摘要覆盖范围不与最近窗口重叠。
- 摘要 digest 与来源不一致时拒绝使用。
- 失败轮次中间内容默认排除，重试时标记。
- Manifest 的 included/excluded 可复现。
- FTS 检索只返回当前 thread 和授权范围。
- 角色隔离：独立观点不泄露兄弟回复。

### 9.2 集成测试

- 长会话第 1 轮写入关键约束，经过 100 轮和多次压缩后，约束仍在 P0。
- 长报告结论在末尾，预览和摘要都能保留结论索引。
- 旧产物被检索到后，引用确切版本。
- 任何一次请求都不超过显式 `context_size`。
- 必需上下文放不下时，在调用前失败并给出可操作错误。
- 重试/恢复/并行/SSE 行为不受影响。
- 旧 Run 没有 manifest/summary 时仍能正常查看和恢复。

### 9.3 前端测试

- 上下文面板显示 included/excluded、token 和原因。
- pin/unpin 操作生效并在刷新后保留。
- 失败轮次中间内容有“未验证”标识。
- 旧运行没有 manifest 时显示兼容提示。

### 9.4 验收标准

- 用户能回答“这个成员看到了什么、为什么没看到某条”。
- 关键约束（首条用户消息、用户 pin、成功交付、已摘要约束）在长会话中不因时间久远丢失；被排除的其它用户消息必须出现在 manifest。
- 摘要/检索内容不会冒充证据或交付。
- 不因固定百分比导致必需交接被挤掉。
- 预算失败在模型调用前发生，不浪费一次真实请求。
- 现有权限、角色隔离、原文不可变和恢复语义保持不变。

---

## 10. 迁移与兼容

- 新表只追加，不重写旧表。
- 旧 Run/Attempt 没有 manifest 时使用现有共享历史逻辑。
- 摘要惰性生成，不为历史会话回填。
- `context_size` 字段保留，单位继续是估算 tokens；新增预算策略不改变它的外部含义。
- 现有 `COCOGEM_HISTORY_MESSAGES` / `COCOGEM_HISTORY_TOKENS` 保留为回退配置；新配置项在评审后确定。
- API 新增字段保持可选；旧客户端不受影响。
- 删除 thread 时级联清理 pins/summaries/manifests；永久删除语义不变。

---

## 11. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 摘要幻觉 | 保留来源 ID；摘要不是证据；可追溯到原文 |
| 检索漏掉关键内容 | 固定 P0/P1；用户 pin；评测长会话约束保留 |
| token 估算偏差 | 运行后记录实际 usage；按 provider 校准；保守估算 |
| 压缩破坏缓存 | 只在边界压缩；稳定前缀；不每步重排 |
| 预算过紧导致失败 | 明确 `context_budget_exhausted`；建议提高预算或减少引用 |
| 成本上升 | 每次 Run 成本预算；摘要使用低成本模型；缓存 |
| 角色视图不一致 | 共享 RunBrief + manifest diff |
| 旧行为回归 | Phase 1 保留回退；逐阶段开关 |

---

## 12. 需要评审确认的问题

1. 摘要由谁生成？
   - 使用协调者成员
   - 使用单独的低成本成员/模型
   - 只做确定性摘要，在引入 LLM 摘要前不调用模型
2. 默认压缩阈值用 75% 是否合适？
3. 在引入摘要前，是否只做确定性 ThreadBrief，不做 LLM 摘要？
4. 检索先做 FTS5/trigram，还是直接引入本地 embedding？
5. 失败轮次中间内容默认排除，还是默认带“未验证”标签？
6. 早期用户约束是否需要自动抽取成“约束账本”？如果做，由谁生成、如何校验来源？
7. 用户 pin 的入口放在消息卡片、产物面板，还是两者都放？
8. Context Manifest 放在 RunView 内，还是只提供独立接口？
9. 必需上下文放不下时，是否允许降级为“预览 + 强制 read_context”，还是直接失败？
10. 是否保留 `COCOGEM_HISTORY_*` 作为回退，还是逐步废弃？
11. 是否需要跨会话长期记忆？如果需要，应作为独立设计，不与本方案混合。

---

## 附录：研究来源

- Anthropic, *Effective context engineering for AI agents*: <https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents>
- Anthropic, *Managing context on the Claude Developer Platform*: <https://www.anthropic.com/news/context-management>
- Chroma, *Context Rot: How Increasing Input Tokens Impacts LLM Performance*: <https://research.trychroma.com/context-rot>
- Hsieh et al., *RULER: What's the Real Context Size of Your Long-Context Language Models?*: <https://arxiv.org/abs/2404.06654>
- Liu et al., *Lost in the Middle: How Language Models Use Long Contexts*: <https://arxiv.org/abs/2307.03172>
- Packer et al., *MemGPT: Towards LLMs as Operating Systems*: <https://arxiv.org/abs/2310.08560>
- Park et al., *Generative Agents: Interactive Simulacra of Human Behavior*: <https://arxiv.org/abs/2304.03442>
- Chhikara et al., *Mem0: Building Production-Ready AI Agents with Scalable Long-Term Memory*: <https://arxiv.org/abs/2504.19413>
- Rasmussen et al., *Zep: A Temporal Knowledge Graph Architecture for Agent Memory*: <https://arxiv.org/abs/2501.13956>
- OpenHands, *Condenser*: <https://docs.openhands.dev/sdk/arch/condenser>
- LangChain, *Memory overview*: <https://docs.langchain.com/oss/python/langgraph/memory>
- CrewAI, *Memory*: <https://docs.crewai.com/en/concepts/memory>
- Hong et al., *MetaGPT: Meta Programming for A Multi-Agent Collaborative Framework*: <https://arxiv.org/abs/2308.00352>
- Manus, *Context Engineering for AI Agents: Lessons from Building Manus*: <https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus>
- OpenAI Agents SDK, *Sessions*: <https://openai.github.io/openai-agents-python/sessions/>
