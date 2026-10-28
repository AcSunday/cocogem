// Package testutil contains collaborators for automated tests only.
package testutil

import (
	"context"

	"encoding/json"
	"github.com/AcSunday/cocogem/internal/model"
	"regexp"
	"strings"
	"time"
)

// Model exercises tool and streaming contracts without a provider call.
type Model struct{ Delay time.Duration }

// Sources are the bundled public fixtures; each role reads a different one.
var Sources = []string{
	"https://example.org/cocogem/research",
	"https://example.org/cocogem/limits",
	"https://example.org/cocogem/cases",
}

var fixtureRole = regexp.MustCompile(`role=(\w+)`)
var fixtureHandle = regexp.MustCompile(`\(@([^)]+)\)`)

// fixtureReplies gives every step role its own voice so the fixture shows real division of labor.
var fixtureReplies = map[string][]string{
	"researcher":  {"**研究发现**（自动化测试样例）\n\n1. 多 Agent 协作适合可拆分、需要交叉核对的任务，例如资料调研与代码审查。\n2. 已读资料指出，成员需要稳定身份、明确来源和有限步骤，否则结论难以追溯。\n3. 不确定性：示例资料没有给出量化收益，效果仍需真实任务验证。"},
	"critic":      {"**复核意见**（自动化测试样例）\n\n- 同意“任务可拆分”这一前提，但研究结论缺少反例：简单问题拆给多人反而更慢。\n- 资料只说明了机制，没有证明质量提升，应标为“证据不足”。\n- 建议补充：协作成本（上下文传递、等待）与单 Agent 的对比。"},
	"coordinator": {"**汇总**（自动化测试样例）\n\n研究与复核的共识：多 Agent 协作在可拆分、需交叉核对的任务上更可信。\n\n保留分歧：收益是否大于协作成本尚无证据。报告已发布到“产物”面板。"},
	"analyst":     {"**代码分析**（自动化测试样例）\n\n示例仓库把成员身份（稳定 ID 与 handle）和当前任务职责分开保存：改名不会影响历史记录，同一成员可以在不同步骤承担不同角色。"},
	"reviewer":    {"**独立复核**（自动化测试样例）\n\n已重新读取相关行，确认“身份与职责分离”的结论成立。未执行任何代码，运行时行为没有验证。复核报告已发布。"},
	"discussant": {
		"**观点：值得用**（自动化测试样例）\n\n协作能让不同角色各自专注，交叉核对减少单点错误。适合研究、审查这类需要多视角的问题。",
		"**观点：要谨慎**（自动化测试样例）\n\n协作会带来上下文传递和等待成本；问题简单时，一个成员直接回答更快、更省。",
		"**观点：看场景**（自动化测试样例）\n\n关键是任务能否拆分、结果能否核对。可以先单人试做，遇到分歧或证据不足再引入更多成员。",
	},
	"synthesizer": {"**综合草案**（自动化测试样例）\n\n共识：可拆分、需核对的任务适合协作。\n\n保留分歧：协作收益是否大于传递与等待成本，尚无证据。\n\n待核实：不同成员对“简单任务”的边界判断。"},
	"checker": {
		`{"status":"confirmed","correction":"草案准确表示了我的观点，保留简单任务不一定需要多人的分歧。"}`,
		`{"status":"disputed","correction":"请补充上下文传递成本这一限制。"}`,
		`{"status":"disputed","correction":"我主张按任务判断，而不是一律多人协作；请修正措辞。"}`,
	},
	"responder": {
		"**回复**（自动化测试样例）\n\n我从资料角度看：先明确问题范围和可用来源，再决定是否需要其他成员复核。",
		"**回复**（自动化测试样例）\n\n我从质疑角度看：请先说明判断标准，否则不同成员的结论无法比较。",
		"**回复**（自动化测试样例）\n\n我从组织角度看：可以把问题拆成调研、复核、汇总三步，由不同成员分别完成。",
	},
}

// fixtureVoice picks a stable per-member variant so parallel discussants differ.
func fixtureVoice(system string) int {
	m := fixtureHandle.FindStringSubmatch(system)
	if m == nil {
		return 0
	}
	switch m[1] {
	case "ragdoll":
		return 0
	case "maine":
		return 1
	case "orange":
		return 2
	}
	sum := 0
	for _, b := range []byte(m[1]) {
		sum += int(b)
	}
	return sum
}

func fixtureWait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Complete selects fixed tool actions based on its explicit fixture context.
func (d Model) Complete(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
	for _, tool := range r.Tools {
		if tool.Name == "routing_decision" {
			return route(r)
		}
	}
	delay := d.Delay
	if delay == 0 {
		delay = 70 * time.Millisecond
	}
	if err := fixtureWait(ctx, delay); err != nil {
		return model.Response{}, err
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
	tool := func(name string, input any) model.Response {
		b, _ := json.Marshal(input)
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: "fixture-" + name, Name: name, Input: b}}}
	}
	allowed := func(name string) bool {
		for _, t := range r.Tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	role := "responder"
	if m := fixtureRole.FindStringSubmatch(r.System); m != nil {
		role = m[1]
	}
	voice := fixtureVoice(r.System)
	code := strings.Contains(r.System, "workflow=code_analysis")
	if code && allowed("read_file") && !used["read_file"] {
		input := map[string]any{"path": "README.md"}
		if role == "reviewer" {
			input["start_line"], input["end_line"] = 3, 4
		}
		return tool("read_file", input), nil
	}
	if !code && allowed("fetch_url") && !used["fetch_url"] {
		url := Sources[voice%len(Sources)]
		switch role {
		case "researcher":
			url = Sources[0]
		case "critic":
			url = Sources[1]
		case "coordinator":
			url = Sources[2]
		}
		return tool("fetch_url", map[string]any{"url": url}), nil
	}
	if allowed("publish_artifact") && !used["publish_artifact"] && (role == "coordinator" || role == "reviewer") {
		return tool("publish_artifact", fixtureArtifact(r.System, code, sourceIDs)), nil
	}
	replies := fixtureReplies[role]
	if len(replies) == 0 {
		replies = fixtureReplies["responder"]
	}
	text := replies[voice%len(replies)]
	// Stream in short segments so the page shows progress rather than a finished block.
	runes := []rune(text)
	const segments = 6
	for i := range segments {
		chunk := string(runes[i*len(runes)/segments : (i+1)*len(runes)/segments])
		if err := emit(chunk); err != nil {
			return model.Response{}, err
		}
		if i < segments-1 {
			if err := fixtureWait(ctx, delay/4); err != nil {
				return model.Response{}, err
			}
		}
	}
	return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: text}}, UsageKnown: false}, nil
}

func fixtureArtifact(system string, code bool, sourceIDs []string) map[string]any {
	kind, title := "note", "自动化测试样例：讨论纪要"
	body := "# 自动化测试样例：讨论纪要\n\n## 各方观点\n- 值得用：分工与交叉核对减少单点错误。\n- 要谨慎：协作有传递与等待成本。\n- 看场景：先判断任务能否拆分、结果能否核对。\n\n## 共识\n可拆分、需核对的任务适合协作。\n\n## 保留分歧\n收益是否大于成本，没有一致结论。\n\n## 限制\n自动化测试样例，未调用真实模型。"
	findings := []any{}
	if strings.Contains(system, "workflow=research") {
		kind, title = "research_report", "自动化测试样例：多 Agent 协作研究报告"
		body = "# 自动化测试样例：多 Agent 协作研究报告\n\n## 问题与范围\n多 Agent 协作适合哪些场景，有哪些证据与限制。仅使用随包的三份示例资料。\n\n## 方法\n研究成员读取方法资料，复核成员读取限制资料并质疑，汇总成员补充案例资料。\n\n## 发现\n1. 可拆分、需要交叉核对的任务（调研、审查）适合协作。\n2. 稳定身份、来源记录和有限步骤是结论可追溯的前提。\n\n## 分歧与限制\n复核成员指出：资料只说明机制，没有证明质量提升；简单任务协作反而更慢。本次使用确定性模型，不能证明真实模型质量。\n\n## 下一步\n用真实 CLI 跑一次公开样例，对比单 Agent 与多 Agent 的耗时和准确度。"
		if len(sourceIDs) > 0 {
			findings = append(findings, map[string]any{"title": "可拆分任务适合协作", "detail": "案例资料显示，调研与审查类任务拆分后可以交叉核对。", "status": "supported", "source_ids": sourceIDs[:1]})
		}
		findings = append(findings, map[string]any{"title": "质量提升尚无证据", "detail": "示例资料没有量化对比，需要真实任务验证。", "status": "insufficient_evidence", "source_ids": []string{}})
	}
	if code {
		kind, title = "code_analysis", "自动化测试样例：身份与职责分离复核"
		body = "# 自动化测试样例：身份与职责分离复核\n\n## 范围\n示例仓库 README。\n\n## 方法\n分析成员读取全文，复核成员独立重读第 3–4 行。\n\n## 发现\n成员身份与当前任务职责分开保存，改名不影响历史记录。\n\n## 限制\n只读分析，未执行代码，运行时行为未验证。"
		if len(sourceIDs) > 0 {
			findings = append(findings, map[string]any{"title": "身份与职责分离", "detail": "README 说明稳定身份与显示名、模型设置相互独立。", "status": "confirmed", "severity": "low", "source_ids": sourceIDs[:1]})
		}
	}
	return map[string]any{"type": kind, "title": title, "body": body, "findings": findings, "source_ids": sourceIDs, "limitations": []string{"自动化测试样例，未调用真实模型或在线搜索。"}}
}
