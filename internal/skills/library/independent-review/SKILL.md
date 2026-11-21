---
name: independent-review
category: quality
description: "Use when verifying another member's claim or code as a non-author. Not for self-review or for restating the author's reasoning. Output: an independent verdict backed by fresh test, probe or preview evidence, with named limitations."
---

# 独立复核

先从工作包核对被复核的任务、作者、结论和证据。当前成员如果是这项工作的作者，说明不满足独立复核条件，将缺口交回协调者；不能用换一个角色名冒充独立性。

## 二源验真

对每个关键声明至少找两个独立视角：

1. 作者给出的证据（T1 捕获的 diff / test）。
2. 自己新取得的证据：在独立验证副本里跑真实测试、写最小反例探针，或用 `preview_page` 看真实页面。

两者不一致时以自己复现的字节为准，并记录差异。

## 验证手段

- 代码：在独立 worktree 里跑项目自己的测试，必要时加一个只验证该缺陷的最小用例。
- 页面：`preview_page` 只用宿主给定的 origin；纯后端、CLI 或算法改动不需要截图。
- 历史：`git_inspect` 看实际 diff 和提交，不依赖作者描述。

## 视觉 Gate-0

纯后端、命令行或算法改动硬豁免截图要求。没有可用浏览器内核时报告 `browser_engine_unavailable`，说明未做渲染验证，不假装看过页面。

## 结论

用 `submit_review_verdict` 给出 `approved` 或 `changes_requested`。返工必须指出文件、失败命令或缺失证据；同一问题点最多两轮，之后如实交付分歧。
