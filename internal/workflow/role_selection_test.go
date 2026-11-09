package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type roleSelectionModel struct {
	assignments map[string]string
	reason      string
	err         error
	calls       int
	request     model.Request
}

func (m *roleSelectionModel) Complete(_ context.Context, request model.Request, _ func(string) error) (model.Response, error) {
	m.calls++
	m.request = request
	if m.err != nil {
		return model.Response{}, m.err
	}
	payload, err := json.Marshal(map[string]any{"assignments": m.assignments, "reason": m.reason})
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: "assign", Name: "assign_roles", Input: payload}}}, nil
}

func roleSelectionAgents() []w.Agent {
	return []w.Agent{
		{ID: "coord", DisplayName: "协调", Handle: "coord", Executor: "claude", Model: "default", RoleDescription: "协调成员并安排工作", Tools: []string{"read_file", "publish_artifact", "write_file", "run_test"}},
		{ID: "researcher", DisplayName: "研究", Handle: "researcher", RoleDescription: "做资料调研和证据整理", Tools: []string{"read_file", "publish_artifact"}},
		{ID: "critic", DisplayName: "质疑", Handle: "critic", RoleDescription: "独立质疑结论和核对依据", Tools: []string{"read_file", "publish_artifact"}},
		{ID: "engineer", DisplayName: "工程", Handle: "engineer", RoleDescription: "负责工程实现和测试", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}},
		{ID: "reviewer", DisplayName: "复核", Handle: "reviewer", RoleDescription: "独立复核工程结果", Tools: []string{"read_file", "run_test", "publish_artifact"}},
	}
}

func TestSelectMissingRolesUsesResponsibilitiesAndExcludesCoordinator(t *testing.T) {
	agents := roleSelectionAgents()
	thread := w.Thread{AgentIDs: []string{"coord", "researcher", "critic", "engineer", "reviewer"}, CoordinatorID: "coord"}
	m := &roleSelectionModel{
		assignments: map[string]string{"researcher": "researcher", "critic": "critic"},
		reason:      "研究和质疑分别由职责匹配的成员承担",
	}
	got, reason, called := SelectMissingRoles(t.Context(), m, "research", []string{"researcher", "critic"}, map[string]string{"coordinator": "coord"}, "比较两份资料", thread, agents)
	if !called || m.calls != 1 || got["researcher"] != "researcher" || got["critic"] != "critic" || reason == "" {
		t.Fatalf("selection = %+v reason=%q calls=%d", got, reason, m.calls)
	}
	if len(m.request.Tools) != 1 || m.request.Tools[0].Name != "assign_roles" {
		t.Fatalf("role selection tools = %+v", m.request.Tools)
	}
	props := m.request.Tools[0].Schema["properties"].(map[string]any)["assignments"].(map[string]any)["properties"].(map[string]any)
	researcher := props["researcher"].(map[string]any)
	ids := researcher["enum"].([]string)
	if slices.Contains(ids, "coord") {
		t.Fatalf("coordinator was offered as a researcher candidate: %v", ids)
	}
	if !strings.Contains(m.request.Messages[0].Content[0].Text, "做资料调研和证据整理") {
		t.Fatal("role selection prompt lost the member responsibilities")
	}
}

func TestSelectMissingRolesAllowsCoordinatorOnlyAsLastResort(t *testing.T) {
	agents := roleSelectionAgents()
	thread := w.Thread{AgentIDs: []string{"coord", "reviewer"}, CoordinatorID: "coord"}
	m := &roleSelectionModel{assignments: map[string]string{"engineer": "coord"}, reason: "没有其他执行成员"}
	got, _, called := SelectMissingRoles(t.Context(), m, "engineering", []string{"engineer"}, map[string]string{"reviewer": "reviewer"}, "修复代码", thread, agents)
	if !called || got["engineer"] != "coord" {
		t.Fatalf("last-resort selection = %+v called=%v", got, called)
	}
	props := m.request.Tools[0].Schema["properties"].(map[string]any)["assignments"].(map[string]any)["properties"].(map[string]any)
	engineer := props["engineer"].(map[string]any)
	if ids := engineer["enum"].([]string); !slices.Equal(ids, []string{"coord"}) {
		t.Fatalf("engineer candidates = %v, want the coordinator as the only option", ids)
	}
}

func TestSelectMissingRolesRejectsInvalidOutput(t *testing.T) {
	agents := roleSelectionAgents()
	thread := w.Thread{AgentIDs: []string{"coord", "researcher", "critic"}, CoordinatorID: "coord"}
	m := &roleSelectionModel{assignments: map[string]string{"researcher": "outside", "critic": "critic"}, reason: "invalid"}
	if _, _, called := SelectMissingRoles(t.Context(), m, "research", []string{"researcher", "critic"}, map[string]string{"coordinator": "coord"}, "调研", thread, agents); called {
		t.Fatal("off-roster assignment was accepted")
	}
	m = &roleSelectionModel{err: errors.New("provider unavailable")}
	if _, _, called := SelectMissingRoles(t.Context(), m, "research", []string{"researcher", "critic"}, map[string]string{"coordinator": "coord"}, "调研", thread, agents); called {
		t.Fatal("model failure did not fall back")
	}
}
