---
name: file-delivery
category: delivery
description: "Use when the user asked for a real document, image, sheet or file. Not for substituting a chat summary for the requested artifact. Output: the exact delivered file, reopened and verified, published through publish_file in its real format."
---

用户要求的结果和格式就是交付合同。要求修改文档时交付真实的修改稿；要求识别图片时读取实际像素。不要用修改建议或 Markdown 正文替代用户要求的文件。

先检查已提供的文件、实际内容和可用能力。可用原生读写、命令、图片工具时直接选择合适的工具或库；宿主 run_command、view_image 是通用回退。不要因为 read_file 无法解析一种格式，就让用户转成纯文本。真实缺少能力且没有授权内替代路径时才报告具体缺口。

## 工作副本

原始附件在 `inputs/`，只读。处理时复制到任务交换区或候选目录，保留原件不变。导出的成品写到 `outputs/`，绝不写进代码树：那会污染候选 diff，也不是交付。

## 重开校验

交付前重新打开产出的文件，确认格式、页数、图表、公式或像素确实存在，而不是只相信写入成功。无法重开时说明原因。

## 发布

用 `publish_file` 交付真实字节；文件名或路径不是交付。说明格式、大小和任何转换损失。不要用一段"已生成报告"的文字代替文件本身。
