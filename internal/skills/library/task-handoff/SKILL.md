---
name: task-handoff
category: collaboration
description: "Use when handing a bounded subtask to another member. Not for work you can finish yourself or an answered issue. Output: a handoff_task naming target, outcome, scope, evidence and the claim the receiver must verify."
---

# 交接任务

读取 [references/contract.md](references/contract.md) 了解交接所需信息和回执语义；以当前 `handoff_task` 工具 schema 为准确字段契约。

## 发球

一个合格的交接包含：目标结果、允许范围、验收条件、上游证据引用和期望返回形式。缺少任意一项时，接收方无法独立完成，交接就是无效的。

- 目标用结果描述，不用动作清单堆叠。
- 证据引用确切 ID 或路径，不写"上面那些资料"。
- 明确这一步是否允许写文件或运行命令。

## 接球

接收方先做接球验真：上游陈述是 T2 候选声明，必须用 T0/T1 独立核验后再执行副作用。方法见 `read_skill(skill_id="_shared", resource="references/claim-grounding.md")`。

发现冲突时写明 `mismatch` 和双方证据，交回协调者；不要把错误前提继续放大。

## 纪律

- 工具成功只表示已接收，不代表队友完成。
- 同一个问题点最多两轮返工；能自己顺手修的不要打回。
- 不在正文里用 `@` 期待派发；只有工具调用才会真正派发。
