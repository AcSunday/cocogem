---
name: quality-gate
category: quality
description: "Use when finishing or submitting work. Not for declaring done on an unrun entry point or a TODO tail. Output: the original goal re-read, a real entry dogfood with one edge case, leftovers swept, and worktree hygiene confirmed."
---

# 交付前自检

在说"完成"之前，逐项过一遍。任何一项不满足就补齐或明确说明。

## 回读目标

重新读用户原话与已接受的验收条件，逐条对照实际产出。核心诉求被留在 `TODO`、`后续再做` 或"下一阶段"里，就不算完成。

## 真实入口 Dogfood

从用户真正会用的入口跑一遍，而不是只跑单元测试。至少覆盖一个异常或空状态边界：空输入、缺文件、无权限、网络失败或重复提交。记录实际命令和结果。

## 环境卫生

- 跑过的测试是否改写了工作树？候选是否仍然干净？
- 是否留下了调试打印、临时脚本、生成文件或未使用的导入？
- 交付的字节是否真的存在，能否重新打开？

## 证据

列出：改了哪些文件、哪个测试或预览证明它工作、还有什么限制。没有证据的"已完成"不算通过。
