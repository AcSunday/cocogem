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
func ResolveRouting(ctx context.Context, m model.Model, input w.RunInput, thread w.Thread, agents []w.Agent) (Routing, error) {
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
		if hasNamedNegation(clean, agents) {
			return Routing{Decision: "clarify", Clarification: "正文包含对成员的否定，同时存在明确提及。请说明本次实际收件人。"}, nil
		}
		if hasNamedCondition(clean, agents) {
			return Routing{Decision: "clarify", Clarification: "正文包含针对成员的条件，同时存在明确提及。请直接选择实际收件人，或说明现在需要执行的任务。"}, nil
		}
		if len(input.Targets) > 0 && !sameIDs(explicit, input.Targets) {
			return r, errors.New("explicit mentions conflict with selected recipients")
		}
		r.Targets = explicit
		r.Workflow = "free"
		r.Assignments = nil
		r.Explanation = "按用户明确指定的成员执行"
		return r, nil
	}
	if len(input.Targets) > 0 {
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
		if input.Workflow != "" && input.Workflow != "free" {
			if input.Workflow == "discussion" {
				r.Targets = slices.Clone(thread.AgentIDs)
			}
			return r, nil
		}
		return Routing{Decision: "discuss", Workflow: "free", Targets: []string{thread.CoordinatorID}, Explanation: "没有明确收件人，交给会话协调者"}, nil
	}
	if m == nil {
		return Routing{}, errors.New("semantic routing requires a configured model")
	}
	timeout := 15 * time.Second
	if candidates[0].Executor != "" {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Avatars are presentation only and never enter model prompts.
	for i := range candidates {
		candidates[i].Avatar = ""
	}
	catalog, _ := json.Marshal(candidates)
	schema := map[string]any{"type": "object", "properties": map[string]any{"decision": map[string]any{"type": "string", "enum": []string{"assign", "discuss", "clarify"}}, "targets": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "workflow": map[string]any{"type": "string", "enum": []string{"free", "discussion", "research", "engineering"}}, "assignments": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "explanation": map[string]any{"type": "string"}, "clarification": map[string]any{"type": "string"}}, "required": []string{"decision", "targets", "workflow", "explanation"}, "additionalProperties": false}
	ids := []string{}
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	memberID := map[string]any{"type": "string", "enum": ids, "description": "Exact member UUID from the candidate catalog, never a name or task."}
	props := schema["properties"].(map[string]any)
	props["targets"] = map[string]any{"type": "array", "items": memberID, "maxItems": 4, "description": "Exact member UUIDs receiving work. For free collaboration, put all recipients here."}
	roles := map[string]any{}
	for _, role := range []string{"researcher", "critic", "coordinator", "engineer", "reviewer"} {
		roles[role] = memberID
	}
	props["assignments"] = map[string]any{"type": "object", "properties": roles, "additionalProperties": false, "description": "Built-in workflow role to member UUID only. Use an empty object for free collaboration. Never put task instructions here."}
	schema["properties"].(map[string]any)["evidence"] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"agent_id": map[string]any{"type": "string"}, "quote": map[string]any{"type": "string"}}, "required": []string{"agent_id", "quote"}, "additionalProperties": false}}
	request := model.Request{Model: candidates[0].Model, Executor: candidates[0].Executor, ContextSize: candidates[0].ContextSize, ThinkingLevel: candidates[0].ThinkingLevel, System: "Resolve only the current user's routing intent. Mentioning, quoting, negating, or hypothesizing about a member is not assigning work. A member named only in a past-failure, status or diagnostic record (for example \"失败成员是…\" or an executor/model line) is background context: never assign them; choose discuss unless the current input directly asks that member to do work. Choose discuss for ordinary discussion, clarify for uncertainty. Never invent IDs or participants. An assignment may target only named candidates. Explicit research roles may map to researcher/critic/coordinator; code and file work maps to engineering roles engineer/reviewer. Analysis of code or files is an in-flow behavior of engineering, not a separate workflow. Use engineering whenever the user asks for code or file work; use free for named independent opinions without a requested summary. Use discussion only for explicitly requested independent views followed by a named coordinator summary. Output exactly one routing_decision tool call; do not execute any task. Explanation and clarification must be Chinese.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Candidate members: " + string(catalog) + "\nCurrent user input:\n" + clean}}}}, MaxTokens: 1500, Tools: []model.Tool{{Name: "routing_decision", Description: "Propose routing only", Schema: schema}}}
	request.System += " Every assigned member requires evidence with its agent_id and an exact quote from the current input containing its name and task. Do not use paraphrases as quotes, and do not use a quote that only reports a past failure or status as an instruction."
	resp, err := m.Complete(ctx, request, func(string) error { return nil })
	if err != nil {
		return Routing{}, err
	}
	if len(resp.Blocks) != 1 || resp.Blocks[0].Type != "tool_use" || resp.Blocks[0].Name != "routing_decision" {
		return Routing{}, errors.New("invalid routing response")
	}
	r = Routing{}
	if err := json.Unmarshal(resp.Blocks[0].Input, &r); err != nil {
		return Routing{}, errors.New("invalid routing payload")
	}
	r.ModelCall = true
	r.UsageKnown = resp.UsageKnown
	r.InputTokens = resp.InputTokens
	r.OutputTokens = resp.OutputTokens
	if r.Decision == "discuss" {
		return reconcileRouting(r, input, thread, candidates, clean), nil
	}
	if r.Decision == "clarify" {
		return r, nil
	}
	if r.Decision != "assign" {
		return Routing{}, errors.New("unknown routing decision")
	}
	ids = append([]string{}, r.Targets...)
	for _, id := range r.Assignments {
		ids = append(ids, id)
	}
	verified := map[string]bool{}
	for _, id := range ids {
		if verified[id] {
			continue
		}
		verified[id] = hasVerifiableQuote(id, r.Evidence, clean, candidates)
	}
	targets := []string{}
	for _, id := range r.Targets {
		if verified[id] && !slices.Contains(targets, id) {
			targets = append(targets, id)
		}
	}
	assignments := map[string]string{}
	for role, id := range r.Assignments {
		if verified[id] {
			assignments[role] = id
		}
	}
	if len(targets)+len(assignments) == 0 {
		// A proposed dispatch without a verifiable user instruction has no
		// authority. Drop it and keep the message on its selected flow or the
		// coordinator so a model misread can never block the send.
		r.Decision, r.Workflow = "discuss", ""
		r.Targets, r.Assignments, r.Evidence = nil, nil, nil
		r = reconcileRouting(r, input, thread, candidates, clean)
		if r.Decision != "clarify" {
			if r.Explanation != "" {
				r.Explanation += "；"
			}
			r.Explanation += "模型提议的成员指派缺少可核实的正文原句，已按本轮没有新指派处理"
		}
		return r, nil
	}
	filtered := make([]w.RoutingEvidence, 0, len(r.Evidence))
	for _, evidence := range r.Evidence {
		if verified[evidence.AgentID] {
			filtered = append(filtered, evidence)
		}
	}
	r.Targets, r.Assignments, r.Evidence = targets, assignments, filtered
	if len(r.Assignments) == 0 {
		r.Assignments = nil
	}
	return reconcileRouting(r, input, thread, candidates, clean), nil
}

// hasVerifiableQuote reports whether one member's proposal cites an exact,
// present quote that actually contains that member's name or handle.
func hasVerifiableQuote(id string, evidence []w.RoutingEvidence, text string, candidates []w.Agent) bool {
	index := slices.IndexFunc(candidates, func(a w.Agent) bool { return a.ID == id })
	if index < 0 {
		return false
	}
	a := candidates[index]
	names := append([]string{a.DisplayName, a.Handle}, a.Aliases...)
	for _, entry := range evidence {
		if entry.AgentID != id || entry.Quote == "" || !strings.Contains(text, entry.Quote) {
			continue
		}
		for _, name := range names {
			if name != "" && strings.Contains(strings.ToLower(entry.Quote), strings.ToLower(name)) {
				return true
			}
		}
	}
	return false
}

func reconcileRouting(r Routing, input w.RunInput, thread w.Thread, candidates []w.Agent, text string) Routing {
	clarify := func(message string) Routing {
		r.Decision, r.Clarification = "clarify", message
		r.Targets, r.Assignments = nil, nil
		return r
	}
	chosenWorkflow := input.Workflow != "" && input.Workflow != "free"
	if r.Decision == "assign" {
		if hasNamedNegation(text, candidates) {
			return clarify("正文否定了成员参与，不能仅凭模型建议派发。请明确选择本次实际执行的成员。")
		}
		if hasNamedCondition(text, candidates) {
			return clarify("正文包含针对成员的条件，不能仅凭模型建议派发。请明确选择本次实际执行的成员。")
		}
	}
	roles := map[string][]string{"research": {"researcher", "critic", "coordinator"}, "engineering": {"engineer", "reviewer"}, "code_analysis": {"analyst", "reviewer"}, "discussion": {"coordinator"}}
	if r.Decision == "discuss" {
		targets := []string{thread.CoordinatorID}
		if chosenWorkflow {
			targets = slices.Clone(input.Targets)
			if input.Workflow == "discussion" && len(targets) == 0 {
				targets = slices.Clone(thread.AgentIDs)
			}
			for _, role := range roles[input.Workflow] {
				targets = append(targets, input.Assignments[role])
			}
		}
		// A negated named default recipient needs clarification, even if intent is ordinary discussion.
		targetMembers := slices.DeleteFunc(slices.Clone(candidates), func(a w.Agent) bool { return !slices.Contains(targets, a.ID) })
		if hasNamedNegation(text, targetMembers) {
			return clarify("正文否定了默认参与成员，请明确选择本次实际执行的成员。")
		}
		if chosenWorkflow {
			r.Workflow, r.Targets, r.Assignments = input.Workflow, input.Targets, input.Assignments
			if input.Workflow == "discussion" && len(r.Targets) == 0 {
				r.Targets = slices.Clone(thread.AgentIDs)
			}
			r.Explanation = "正文没有新的指派，使用已选流程与分工"
			return r
		}
		r.Targets, r.Workflow, r.Assignments = targets, "free", nil
		return r
	}
	return r
}

// A negation or condition only guards routing when it shares a clause with the
// member (or with a pronoun bound to a mentioned member). Unrelated constraints
// elsewhere in the message are not about the member.
func hasNamedNegation(text string, agents []w.Agent) bool {
	return hasMemberConstraint(text, agents, []string{"不要", "别让", "不用", "不让", "不需要", "don't", "do not", "must not"})
}

func hasNamedCondition(text string, agents []w.Agent) bool {
	return hasMemberConstraint(text, agents, []string{"如果", "假如", "假设", "是否让", "what if"})
}

func hasMemberConstraint(text string, agents []w.Agent, markers []string) bool {
	mentioned, _ := namedCandidates(text, agents)
	clauses := strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune("\n，。；,;.!?？！", r)
	})
	for _, clause := range clauses {
		lower := strings.ToLower(clause)
		marked := false
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				marked = true
				break
			}
		}
		if !marked {
			continue
		}
		if names, _ := namedCandidates(clause, agents); len(names) != 0 {
			return true
		}
		if len(mentioned) == 0 {
			continue
		}
		// 其他/其它 contain 他/它 but never refer to a member.
		pronounced := strings.NewReplacer("其他", "", "其它", "").Replace(lower)
		for _, reference := range []string{"他", "她", "让其", "由其", "其参与", "其继续", "该成员", "这个成员", "上述成员", " him", " her", " them", " their"} {
			if strings.Contains(pronounced, reference) {
				return true
			}
		}
	}
	return false
}

func sameIDs(a, b []string) bool {
	x := slices.Clone(a)
	y := slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
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
