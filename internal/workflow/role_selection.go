package workflow

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

const roleSelectionReasonLimit = 500

type roleSelectionDecision struct {
	Assignments map[string]string `json:"assignments"`
	Reason      string            `json:"reason"`
}

// SelectMissingRoles asks the conversation coordinator to fill only the open
// roles from the runtime roster. Fixed roles and user assignments are never
// changed. The coordinator is removed from worker-role candidates whenever
// another eligible member exists, so it can only fill a worker role when nobody
// else can.
func SelectMissingRoles(ctx context.Context, m model.Model, workflow string, open []string, fixed map[string]string, message string, thread w.Thread, agents []w.Agent) (map[string]string, string, bool) {
	if len(open) == 0 {
		return nil, "", false
	}
	roster := roleSelectionRoster(thread, agents)
	if len(roster) == 0 || m == nil {
		return nil, "", false
	}
	candidates := map[string][]string{}
	requested := make([]string, 0, len(open))
	for _, role := range open {
		ids := roleCandidates(workflow, role, roster, fixed, thread.CoordinatorID)
		if len(ids) == 0 {
			continue
		}
		candidates[role] = ids
		requested = append(requested, role)
	}
	if len(requested) == 0 {
		return nil, "", false
	}
	agent, ok := roleSelectionAgent(roster, thread.CoordinatorID)
	if !ok {
		return nil, "", false
	}
	timeout := 15 * time.Second
	if agent.Executor != "" {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	catalog, _ := json.Marshal(roleCatalog(roster))
	fixedJSON, _ := json.Marshal(fixed)
	contractsJSON, _ := json.Marshal(roleContracts(workflow))
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"assignments": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"required":             slices.Clone(requested),
				"additionalProperties": false,
			},
			"reason": map[string]any{"type": "string", "maxLength": roleSelectionReasonLimit},
		},
		"required":             []string{"assignments", "reason"},
		"additionalProperties": false,
	}
	assignmentProperties := schema["properties"].(map[string]any)["assignments"].(map[string]any)["properties"].(map[string]any)
	for _, role := range requested {
		assignmentProperties[role] = map[string]any{
			"type":        "string",
			"enum":        candidates[role],
			"description": roleSelectionDescription(workflow, role),
		}
	}
	request := model.Request{
		Model:         agent.Model,
		Executor:      agent.Executor,
		ContextSize:   agent.ContextSize,
		ThinkingLevel: agent.ThinkingLevel,
		System:        "Choose only the open roles for this collaboration plan. Use the runtime roster's responsibilities, capabilities and actual tool grants. Fixed roles are immutable and user assignments always win. The host already removed the conversation coordinator from worker-role candidate lists whenever another eligible member exists; do not try to assign it there. Respect exclusive roles and required tools. Output exactly one assign_roles tool call with an exact member ID for every open role. Do not execute the task. Reason must be Chinese.",
		Messages:      []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Runtime roster: " + string(catalog) + "\nRole contracts: " + string(contractsJSON) + "\nFixed assignments: " + string(fixedJSON) + "\nOpen roles: " + strings.Join(requested, ", ") + "\nCurrent user task:\n" + message}}}},
		MaxTokens:     1200,
		Tools: []model.Tool{{
			Name:        "assign_roles",
			Description: "Assign exact runtime member IDs to the open collaboration roles",
			Schema:      schema,
		}},
	}
	resp, err := m.Complete(ctx, request, func(string) error { return nil })
	if err != nil {
		return nil, "", false
	}
	if len(resp.Blocks) != 1 || resp.Blocks[0].Type != "tool_use" || resp.Blocks[0].Name != "assign_roles" {
		return nil, "", false
	}
	var decision roleSelectionDecision
	if err := json.Unmarshal(resp.Blocks[0].Input, &decision); err != nil {
		return nil, "", false
	}
	out := map[string]string{}
	for role, id := range decision.Assignments {
		allowed, ok := candidates[role]
		if !ok || !slices.Contains(allowed, id) {
			return nil, "", false
		}
		out[role] = id
	}
	for _, role := range requested {
		if out[role] == "" {
			return nil, "", false
		}
	}
	merged := map[string]string{}
	for role, id := range fixed {
		merged[role] = id
	}
	for role, id := range out {
		merged[role] = id
	}
	for role, id := range out {
		for otherRole, otherID := range merged {
			if otherRole != role && otherID == id && w.RolesConflict(workflow, role, otherRole) {
				return nil, "", false
			}
		}
	}
	reason := strings.TrimSpace(decision.Reason)
	if utf8.RuneCountInString(reason) > roleSelectionReasonLimit {
		reason = string([]rune(reason)[:roleSelectionReasonLimit])
	}
	return out, reason, true
}

func roleSelectionRoster(thread w.Thread, agents []w.Agent) []w.Agent {
	roster := make([]w.Agent, 0, len(thread.AgentIDs))
	for _, id := range thread.AgentIDs {
		index := slices.IndexFunc(agents, func(a w.Agent) bool { return a.ID == id })
		if index < 0 || agents[index].Disabled {
			continue
		}
		roster = append(roster, agents[index])
	}
	if len(roster) == 0 {
		for _, agent := range agents {
			if !agent.Disabled {
				roster = append(roster, agent)
			}
		}
	}
	return roster
}

func roleCandidates(workflow, role string, roster []w.Agent, fixed map[string]string, coordinatorID string) []string {
	ids := make([]string, 0, len(roster))
	for _, agent := range roster {
		if agent.Disabled || !roleToolsSatisfied(workflow, role, agent) {
			continue
		}
		conflict := false
		for otherRole, otherID := range fixed {
			if otherID == agent.ID && w.RolesConflict(workflow, role, otherRole) {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		ids = append(ids, agent.ID)
	}
	if role != "coordinator" {
		others := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == coordinatorID })
		if len(others) > 0 {
			ids = others
		}
	}
	return ids
}

// roleToolsSatisfied checks the runtime catalog, never a retired member tool
// array: every enabled member can publish, so a legacy restriction cannot exclude
// a qualified member from a role.
func roleToolsSatisfied(workflow, role string, _ w.Agent) bool {
	catalog := w.StandardToolCatalog()
	for _, tool := range w.RoleTools(workflow, "", role) {
		if !slices.Contains(catalog, tool) {
			return false
		}
	}
	return true
}

func roleSelectionDescription(workflow, role string) string {
	required := w.RoleTools(workflow, "", role)
	if len(required) == 0 {
		return "Exact runtime member ID. Use the member's responsibility and granted tools."
	}
	return "Exact runtime member ID. Required tools: " + strings.Join(required, ", ")
}

func roleContracts(workflow string) map[string]string {
	switch workflow {
	case "research":
		return map[string]string{
			"researcher":  "调研问题、收集并核对证据，明确说明不确定性和限制",
			"critic":      "独立阅读来源，质疑日期、证据、假设、遗漏替代方案和不成立的结论",
			"coordinator": "汇总研究、质疑、回应和复查，交付保留分歧与来源的研究报告",
		}
	case "engineering":
		return map[string]string{
			"engineer": "按任务使用已授权工具执行；有写/测试权限时修改并验证，否则保持只读",
			"reviewer": "独立读取文件与证据，确认或质疑结果；有测试权限时可独立复测，只发布最终工程报告",
		}
	case "code_analysis":
		return map[string]string{
			"analyst":  "只读分析代码和请求的行为，给出带文件证据的发现",
			"reviewer": "独立复核发现，确认或质疑并交付代码分析报告",
		}
	case "discussion":
		return map[string]string{
			"coordinator": "汇总独立观点和交叉质疑，保留真实分歧并交付讨论纪要",
		}
	}
	return nil
}

func roleSelectionAgent(roster []w.Agent, coordinatorID string) (w.Agent, bool) {
	for _, agent := range roster {
		if agent.ID == coordinatorID {
			return agent, true
		}
	}
	for _, agent := range roster {
		if agent.Executor != "" || agent.Model != "" {
			return agent, true
		}
	}
	if len(roster) == 0 {
		return w.Agent{}, false
	}
	return roster[0], true
}

func roleCatalog(roster []w.Agent) []map[string]any {
	out := make([]map[string]any, 0, len(roster))
	for _, agent := range roster {
		out = append(out, map[string]any{
			"id":               agent.ID,
			"display_name":     agent.DisplayName,
			"handle":           agent.Handle,
			"role_description": agent.RoleDescription,
			"capabilities":     slices.Clone(agent.Capabilities),
			"tools":            w.StandardToolCatalog(),
		})
	}
	return out
}
