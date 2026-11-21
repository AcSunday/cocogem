---
name: task-orchestration
category: collaboration
description: "Use when a task needs delegation, review or synthesis across members. Not for a direct answer one member finishes alone. Output: a lane (solo, relay review, durable parallel task) plus the exact roster and a decidable delivery condition."
---

# 编排任务

先确定用户要解决的问题、允许范围和可判定的交付条件，再决定是否需要其他成员。能直接完成的简单任务不必增加交接。

## 车道

读取 [共享参考：车道选择](../_shared/references/lane-selection.md)（通过 `read_skill`，`skill_id="_shared"`，`resource="references/lane-selection.md"`），按可逆性、影响面、证据可得性、异构性和持续性五个轴判断：

- 局部、可逆、证据充分 → 单人直达。
- 有风险但边界清楚 → `handoff_task(mode="relay")` 交给执行者，由执行者直接找独立复核者。
- 跨阶段、可并行、需持续跟踪 → 升级为持久任务，用 `delegate` 并行子任务。

## 名册

只从本轮实际可用的名册里选人，按职责、能力和实际工具授权匹配，不按名字或 CLI 品牌推断角色。用户明确点名的成员优先。没有合适成员时说明缺口，不要虚构一个。

## 交付条件

在交接前写清：要产出什么、如何判定完成、允许改哪些范围、需要什么证据。条件不可判定时先向用户澄清，而不是把模糊目标发出去。
