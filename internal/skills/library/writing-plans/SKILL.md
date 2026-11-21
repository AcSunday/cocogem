---
name: writing-plans
category: engineering
description: "Use when planning a change before implementation. Not for a trivial or already-decided edit. Output: an aligned plan naming lifecycle owners, transitions, recovery and invariants, plus the project's real build and test commands."
---

# 编写方案

方案的价值在于让实现者不必重新猜边界。先回读原始目标与验收条件，再写步骤。

## 端到端对齐

从用户的第一句诉求出发，逐条标注方案覆盖到哪里。遗漏的诉求要么补进方案，要么明确告知不做及原因。

## 状态对象门禁

任何有生命周期的对象（任务、会话、候选、授权、连接）都必须回答：

- **所有者**：谁创建、谁可以修改、谁可以销毁。
- **转移事件**：哪些事件驱动状态变化，非法转移如何处理。
- **恢复**：进程崩溃或超时后从哪个持久记录恢复。
- **并发**：两个执行者同时推进时谁赢，如何避免双写。
- **不变量**：任何时刻都必须成立的条件，例如"未应用前源目录字节不变"。

缺任意一项时，方案还没有到可实现的程度。

## 防线代价核算

每加一层兜底、重试或开关都要说明它防的是什么缺陷、代价是什么。三层兜底往往说明根因没找到。

## 真实命令

通过项目清单或实际执行确认构建与测试命令，不要凭印象写 `make test`。在方案里给出可复制的确切命令，并标注哪一个是真正的验证命令。
