package model

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Demo is a deterministic model fixture that still exercises real host tools.
type Demo struct{ Delay time.Duration }

// Complete selects fixed tool actions based on its explicit fixture context.
func (d Demo) Complete(ctx context.Context, r Request, emit func(string) error) (Response, error) {
	delay := d.Delay
	if delay == 0 {
		delay = 70 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-timer.C:
	}
	used := map[string]bool{}
	sourceIDs := []string{}
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if b.Type == "tool_use" {
				used[b.Name] = true
			}
			if b.Type == "tool_result" {
				var value map[string]any
				if json.Unmarshal([]byte(b.Content), &value) == nil {
					if id, ok := value["id"].(string); ok {
						sourceIDs = append(sourceIDs, id)
					}
				}
			}
		}
	}
	tool := func(name string, input any) Response {
		b, _ := json.Marshal(input)
		return Response{StopReason: "tool_use", Blocks: []Block{{Type: "tool_use", ID: "demo-" + name, Name: name, Input: b}}}
	}
	allowed := func(name string) bool {
		for _, t := range r.Tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	code := strings.Contains(r.System, "workflow=code_analysis")
	if code && allowed("read_file") && !used["read_file"] {
		return tool("read_file", map[string]any{"path": "README.md"}), nil
	}
	if !code && allowed("fetch_url") && !used["fetch_url"] {
		return tool("fetch_url", map[string]any{"url": "https://example.org/cocogem/research"}), nil
	}
	if allowed("publish_artifact") && !used["publish_artifact"] {
		kind := "note"
		if strings.Contains(r.System, "workflow=research") {
			kind = "research_report"
		}
		if code {
			kind = "code_analysis"
		}
		body := "# 离线演示结果\n\n## 问题与范围\n本轮演示有限步骤、来源捕获与成员协作。\n\n## 方法\n通过正式工具读取随包示例。\n\n## 发现\n系统应区分成员身份、当前职责与模型。\n\n## 分歧与限制\n本次使用确定性模型，不能证明真实模型质量；研究结论仍需独立验证。\n\n## 下一步\n配置真实服务后运行公开样例。"
		return tool("publish_artifact", map[string]any{"type": kind, "title": "离线演示：协作报告", "body": body, "findings": []any{}, "source_ids": sourceIDs, "limitations": []string{"离线演示，未调用真实模型或在线搜索。"}}), nil
	}
	text := "离线演示：已完成本步。已有资料支持明确分工；复核意见保留证据不足与真实效果尚未验证的限制。"
	runes := []rune(text)
	for _, chunk := range []string{string(runes[:len(runes)/2]), string(runes[len(runes)/2:])} {
		if err := emit(chunk); err != nil {
			return Response{}, err
		}
	}
	return Response{StopReason: "end_turn", Blocks: []Block{{Type: "text", Text: text}}, UsageKnown: false}, nil
}
