---
name: tdd
category: engineering
description: "Use when behaviour needs a trustworthy red signal. Not for pure documentation or a test that cannot fail for the expected reason. Output: a genuinely failing test, the smallest implementation, then a passing filtered run."
---

# 测试驱动

RED 是可信的失败信号，不是机械地新建一个文件。

## 什么算 RED

- 已有编译错误、类型错误或精准失败的测试，可以直接作为 RED。
- 新行为或无法复现的缺陷，先写出会因预期原因失败的测试，确认失败信息指向该原因，而不是别的错误。
- 纯文档、格式或注释改动免测；说明原因即可。

## 循环

1. 写最小测试，运行它，确认失败原因正确。
2. 写最小实现让它通过，不顺手加无关功能。
3. 运行覆盖范围内的测试，确认没有破坏既有行为。
4. 需要时重构，重构后再跑一次。

## 命令

用 `run_test(filter="TestName")` 把单测缩到秒级；`toolchain=auto` 让宿主按项目清单选工具链。失败时读真实输出，不要重复提交同一命令期待不同结果。

## 交付

说明哪个测试证明了这次改动、它是在最后一次写入之后运行的，以及哪些行为仍未覆盖。
