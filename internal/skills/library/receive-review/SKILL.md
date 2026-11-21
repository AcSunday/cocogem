---
name: receive-review
category: quality
description: "Use when acting on review feedback. Not for arguing without evidence or fixing only the quoted line. Output: each point answered with code or test evidence, and every same-mode defect swept across the whole diff."
---

# 处理复核

复核意见是待核实的声明，不是命令。先判断它是否成立，再决定怎么改。

## 核实

- 用代码、测试、`git_inspect` 或实际运行结果验证每条意见。
- 成立：修，并附上覆盖该问题的测试或证据。
- 不成立：给出具体反证。对 YAGNI 式的过度设计要求可以做有据澄清，但不要用"以后可能需要"作为理由。
- 无法判断：说明缺什么信息，不要猜。

## 同型缺陷全量清扫

当同一份 diff 里出现两个或以上同类问题时，先提炼不变量（例如"所有外部输入都要校验"、"所有失败路径都要保留证据"），然后在整个 diff 范围内一次改完。只改被引用的那一行会留下同样的坑。

## 回复

逐条对应，写清改了什么、证据在哪、哪些没改及原因。不要把未处理的意见留成沉默。
