package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

var fenced = regexp.MustCompile("(?s)```.*?```|`[^`]*`|“[^”\\n]*”|「[^」\\n]*」|\"[^\"\\n]*\"|(?m)^\\s*>.*$")
var mention = regexp.MustCompile(`(?:^|[\s，。,:：])@([a-z][a-z0-9-]{1,31})\b`)

// Routing is a proposed exact dispatch, not authority to execute host tools.
type Routing = w.Routing

// ResolveRouting uses deterministic explicit recipients before optional semantic parsing.
func ResolveRouting(ctx context.Context, m model.Model, demo bool, input w.RunInput, thread w.Thread, agents []w.Agent) (Routing, error) {
	r := Routing{Decision: "assign", Targets: input.Targets, Workflow: input.Workflow, Assignments: input.Assignments, Explanation: "使用明确选择的成员与流程"}
	clean := fenced.ReplaceAllString(input.Message, "")
	byHandle := map[string]w.Agent{}
	for _, a := range agents {
		byHandle[a.Handle] = a
	}
	explicit := []string{}
	for _, match := range mention.FindAllStringSubmatch(clean, -1) {
		a, ok := byHandle[match[1]]
		if !ok || a.Disabled {
			return Routing{Decision: "clarify", Clarification: "成员 @" + match[1] + " 不可用，请选择有效成员。"}, nil
		}
		if !slices.Contains(explicit, a.ID) {
			explicit = append(explicit, a.ID)
		}
	}
	if len(explicit) > 0 {
		for _, constraint := range []string{"不要", "别让", "如果", "假如", "假设", "是否让", "don't", "do not", "what if"} {
			if strings.Contains(strings.ToLower(clean), constraint) {
				return Routing{Decision: "clarify", Clarification: "正文包含否定或条件，同时存在明确提及。请直接选择实际收件人，或说明现在需要执行的任务。"}, nil
			}
		}
		if len(input.Targets) > 0 && !sameIDs(explicit, input.Targets) {
			return r, errors.New("explicit mentions conflict with selected recipients")
		}
		if input.Workflow != "" && input.Workflow != "free" {
			return Routing{Decision: "clarify", Clarification: "明确收件人与已选流程冲突，请选择自由协作或移除明确收件人。"}, nil
		}
		r.Targets = explicit
		r.Workflow = "free"
		return r, nil
	}
	if len(input.Targets) > 0 || input.Workflow != "" && input.Workflow != "free" {
		if r.Workflow == "" {
			r.Workflow = "free"
		}
		return r, nil
	}
	candidates, ambiguous := namedCandidates(clean, agents)
	if ambiguous {
		return Routing{Decision: "clarify", Clarification: "称呼对应多个成员，请选择确切的成员。"}, nil
	}
	if len(candidates) == 0 {
		return Routing{Decision: "discuss", Workflow: "free", Targets: []string{thread.CoordinatorID}, Explanation: "没有明确收件人，交给会话协调者"}, nil
	}
	if demo {
		return demoRouting(clean, thread, candidates), nil
	}
	if m == nil {
		return Routing{}, errors.New("semantic routing requires a configured model")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	catalog, _ := json.Marshal(candidates)
	schema := map[string]any{"type": "object", "properties": map[string]any{"decision": map[string]any{"type": "string", "enum": []string{"assign", "discuss", "clarify"}}, "targets": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "workflow": map[string]any{"type": "string", "enum": []string{"free", "research", "code_analysis"}}, "assignments": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "explanation": map[string]any{"type": "string"}, "clarification": map[string]any{"type": "string"}}, "required": []string{"decision", "targets", "workflow", "explanation"}, "additionalProperties": false}
	schema["properties"].(map[string]any)["evidence"] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"agent_id": map[string]any{"type": "string"}, "quote": map[string]any{"type": "string"}}, "required": []string{"agent_id", "quote"}, "additionalProperties": false}}
	request := model.Request{Model: candidates[0].Model, System: "Resolve only the current user's routing intent. Mentioning, quoting, negating, or hypothesizing about a member is not assigning work. Choose discuss for ordinary discussion, clarify for uncertainty. Never invent IDs or participants. An assignment may target only named candidates. Explicit research roles may map to researcher/critic/coordinator; code roles to analyst/reviewer. Output exactly one routing_decision tool call; do not execute any task. Explanation and clarification must be Chinese.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Candidate members: " + string(catalog) + "\nCurrent user input:\n" + clean}}}}, MaxTokens: 1500, Tools: []model.Tool{{Name: "routing_decision", Description: "Propose routing only", Schema: schema}}}
	request.System += " Every assigned member requires evidence with its agent_id and an exact quote from the current input containing its name and task. Do not use paraphrases as quotes."
	resp, err := m.Complete(ctx, request, func(string) error { return nil })
	if err != nil {
		return Routing{}, err
	}
	if len(resp.Blocks) != 1 || resp.Blocks[0].Type != "tool_use" || resp.Blocks[0].Name != "routing_decision" {
		return Routing{}, errors.New("invalid routing response")
	}
	if err := json.Unmarshal(resp.Blocks[0].Input, &r); err != nil {
		return Routing{}, errors.New("invalid routing payload")
	}
	r.ModelCall = true
	r.UsageKnown = resp.UsageKnown
	r.InputTokens = resp.InputTokens
	r.OutputTokens = resp.OutputTokens
	if r.Decision == "discuss" {
		r.Targets = []string{thread.CoordinatorID}
		r.Workflow = "free"
		r.Assignments = nil
		return r, nil
	}
	if r.Decision == "clarify" {
		return r, nil
	}
	if r.Decision != "assign" {
		return Routing{}, errors.New("unknown routing decision")
	}
	ids := append([]string{}, r.Targets...)
	for _, id := range r.Assignments {
		ids = append(ids, id)
	}
	for _, id := range ids {
		if !slices.ContainsFunc(candidates, func(a w.Agent) bool { return a.ID == id }) {
			return Routing{}, errors.New("routing proposed an unnamed member")
		}
		matched := false
		for _, evidence := range r.Evidence {
			if evidence.AgentID != id || evidence.Quote == "" || !strings.Contains(clean, evidence.Quote) {
				continue
			}
			a := candidates[slices.IndexFunc(candidates, func(a w.Agent) bool { return a.ID == id })]
			for _, name := range append([]string{a.DisplayName, a.Handle}, a.Aliases...) {
				if strings.Contains(strings.ToLower(evidence.Quote), strings.ToLower(name)) {
					matched = true
				}
			}
		}
		if !matched {
			return Routing{}, errors.New("routing assignment lacks an exact user instruction quote")
		}
	}
	if _, err := Plan("preview", w.RunInput{Workflow: r.Workflow, Targets: r.Targets, Assignments: r.Assignments}, agents); err != nil {
		return Routing{}, err
	}
	return r, nil
}

func sameIDs(a, b []string) bool {
	x := slices.Clone(a)
	y := slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
func demoRouting(text string, thread w.Thread, candidates []w.Agent) Routing {
	r := Routing{Decision: "discuss", Workflow: "free", Targets: []string{thread.CoordinatorID}, Explanation: "离线示例路由：普通讨论交给协调者"}
	for _, word := range []string{"不要", "如果", "假如", "我觉得", "怎么样", "会有什么"} {
		if strings.Contains(text, word) {
			return r
		}
	}
	assigned := false
	for _, word := range []string{"帮我", "请", "研究", "调查", "查资料", "复核", "汇总", "分别"} {
		if strings.Contains(text, word) {
			assigned = true
		}
	}
	if !assigned {
		return r
	}
	r.Decision = "assign"
	r.Targets = nil
	r.Explanation = "离线示例路由：按明确出现的成员称呼指派"
	for _, a := range candidates {
		r.Targets = append(r.Targets, a.ID)
	}
	if len(candidates) == 3 && strings.Contains(text, "复核") && strings.Contains(text, "汇总") {
		r.Workflow = "research"
		r.Targets = nil
		r.Assignments = map[string]string{"researcher": candidates[0].ID, "critic": candidates[1].ID, "coordinator": candidates[2].ID}
	}
	return r
}

// namedCandidates matches normalized names at the longest non-overlapping span.
func namedCandidates(input string, agents []w.Agent) ([]w.Agent, bool) {
	text := strings.ToLower(input)
	owners := map[string][]w.Agent{}
	for _, agent := range agents {
		if agent.Disabled {
			continue
		}
		for _, name := range append([]string{agent.DisplayName, agent.Handle}, agent.Aliases...) {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			if !slices.ContainsFunc(owners[key], func(a w.Agent) bool { return a.ID == agent.ID }) {
				owners[key] = append(owners[key], agent)
			}
		}
	}
	candidates := []w.Agent{}
	ambiguous := false
	asciiWord := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-' }
	for offset := 0; offset < len(text); {
		longest := ""
		for name := range owners {
			if len(name) <= len(longest) || !strings.HasPrefix(text[offset:], name) {
				continue
			}
			end := offset + len(name)
			if asciiWord(name[0]) && offset > 0 && asciiWord(text[offset-1]) {
				continue
			}
			if asciiWord(name[len(name)-1]) && end < len(text) && asciiWord(text[end]) {
				continue
			}
			longest = name
		}
		if longest == "" {
			offset++
			continue
		}
		matches := owners[longest]
		if len(matches) > 1 {
			ambiguous = true
		}
		for _, agent := range matches {
			if !slices.ContainsFunc(candidates, func(a w.Agent) bool { return a.ID == agent.ID }) {
				candidates = append(candidates, agent)
			}
		}
		offset += len(longest)
	}
	return candidates, ambiguous
}
