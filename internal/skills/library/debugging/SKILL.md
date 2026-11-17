---
name: debugging
category: engineering
description: "Use when the root cause is unknown. Not for guessing a cache or changing several places at once. Output: four-step evidence (I/O boundary, working-path comparison, single-variable experiment, process/port/worktree check) naming the cause."
---

# 调试

先复现，再定位。禁止一次改多处碰运气，也禁止在没有证据时宣布"缓存问题"。

## 四步取证

1. **输入输出边界**：在真实入口记录实际输入、实际输出和第一处异常。不要从中间猜。
2. **正常路径对照**：在同一个仓库里找一个能正常工作的相似路径，对比差异点（配置、数据、权限、调用顺序）。
3. **单变量实验**：一次只改一个变量并保留结果。改完再改第二个，否则无法归因。
4. **实例核验**：检查进程、端口、worktree、锁文件和实际运行的二进制版本，确认调试的是正在失败的那个实例。

## 工具

- `git_inspect` 看真实 diff 与历史，`run_test(filter=...)` 把范围缩到单个用例。
- 页面问题用 `preview_page` 看真实渲染与控制台错误。
- 卡在环境时先确认隔离边界（沙箱、权限、网络），再怀疑业务代码。

## 收尾

写出根因、证据链、修复点和仍存在的限制。修复后用能覆盖该缺陷的测试证明，而不是"看起来好了"。
