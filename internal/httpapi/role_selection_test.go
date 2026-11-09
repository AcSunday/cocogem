package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type fixedRoleModel struct {
	assignments map[string]string
	err         error
	calls       int
}

func (m *fixedRoleModel) Complete(_ context.Context, request model.Request, _ func(string) error) (model.Response, error) {
	m.calls++
	if m.err != nil {
		return model.Response{}, m.err
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "assign_roles" {
		return model.Response{}, errors.New("unexpected role selection request")
	}
	payload, err := json.Marshal(map[string]any{"assignments": m.assignments, "reason": "按职责选择"})
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: "assign", Name: "assign_roles", Input: payload}}}, nil
}

func TestApplySettingsDefaultsWithModelFillsOnlyOpenRoles(t *testing.T) {
	coord := w.Agent{ID: "coord", DisplayName: "协调", Handle: "coord", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	reader := w.Agent{ID: "reader", DisplayName: "资料", Handle: "reader", RoleDescription: "负责资料整理", Tools: []string{"read_file", "publish_artifact"}}
	writer := w.Agent{ID: "writer", DisplayName: "工程", Handle: "writer", RoleDescription: "负责工程实现和测试", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	reviewer := w.Agent{ID: "reviewer", DisplayName: "复核", Handle: "reviewer", RoleDescription: "独立复核", Tools: []string{"read_file", "run_test", "publish_artifact"}}
	agents := []w.Agent{coord, reader, writer, reviewer}
	thread := w.Thread{AgentIDs: []string{"coord", "reader", "writer", "reviewer"}, CoordinatorID: "coord"}
	settings := w.Settings{Assignments: map[string]map[string]string{"engineering": {"reviewer": "reviewer"}}}
	in := w.RunInput{Message: "修复代码并运行测试", Workflow: "engineering"}
	fallback, err := applySettingsDefaults(in, thread, agents, settings)
	if err != nil {
		t.Fatal(err)
	}
	// The deterministic resolver would pick the first non-coordinator as the
	// engineer; the coordinator's responsibility-based choice must win.
	m := &fixedRoleModel{assignments: map[string]string{"engineer": "writer"}}
	server := &Server{config: Config{Model: m}}
	got, reason := server.applySettingsDefaultsWithModel(t.Context(), in, thread, agents, settings, fallback)
	if m.calls != 1 || got.Assignments["engineer"] != "writer" || got.Assignments["reviewer"] != "reviewer" || reason == "" {
		t.Fatalf("selected = %+v reason=%q calls=%d", got.Assignments, reason, m.calls)
	}
	if got.Assignments["engineer"] == fallback.Assignments["engineer"] {
		t.Fatal("test fixture did not exercise a different responsibility-based choice")
	}

	// A configured engineer leaves no open role, so no second model call is made.
	settings.Assignments["engineering"]["engineer"] = "reader"
	fallback, err = applySettingsDefaults(in, thread, agents, settings)
	if err != nil {
		t.Fatal(err)
	}
	got, reason = server.applySettingsDefaultsWithModel(t.Context(), in, thread, agents, settings, fallback)
	if m.calls != 1 || got.Assignments["engineer"] != "reader" || reason != "" {
		t.Fatalf("fixed role selected = %+v reason=%q calls=%d", got.Assignments, reason, m.calls)
	}
}

func TestApplySettingsDefaultsWithModelFallsBackOnFailure(t *testing.T) {
	coord := w.Agent{ID: "coord", DisplayName: "协调", Handle: "coord", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	reader := w.Agent{ID: "reader", DisplayName: "资料", Handle: "reader", Tools: []string{"read_file", "publish_artifact"}}
	writer := w.Agent{ID: "writer", DisplayName: "工程", Handle: "writer", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	reviewer := w.Agent{ID: "reviewer", DisplayName: "复核", Handle: "reviewer", Tools: []string{"read_file", "run_test", "publish_artifact"}}
	agents := []w.Agent{coord, reader, writer, reviewer}
	thread := w.Thread{AgentIDs: []string{"coord", "reader", "writer", "reviewer"}, CoordinatorID: "coord"}
	settings := w.Settings{Assignments: map[string]map[string]string{"engineering": {"reviewer": "reviewer"}}}
	in := w.RunInput{Message: "修复代码并运行测试", Workflow: "engineering"}
	fallback, err := applySettingsDefaults(in, thread, agents, settings)
	if err != nil {
		t.Fatal(err)
	}
	m := &fixedRoleModel{err: errors.New("provider unavailable")}
	server := &Server{config: Config{Model: m}}
	got, reason := server.applySettingsDefaultsWithModel(t.Context(), in, thread, agents, settings, fallback)
	if m.calls != 1 || got.Assignments["engineer"] != fallback.Assignments["engineer"] || reason == "" {
		t.Fatalf("fallback = %+v reason=%q calls=%d", got.Assignments, reason, m.calls)
	}
}

func TestApplySettingsDefaultsWithModelRejectsCoordinatorWorkerWhenAlternativeExists(t *testing.T) {
	coord := w.Agent{ID: "coord", DisplayName: "协调", Handle: "coord", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	writer := w.Agent{ID: "writer", DisplayName: "工程", Handle: "writer", RoleDescription: "负责工程实现", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	reviewer := w.Agent{ID: "reviewer", DisplayName: "复核", Handle: "reviewer", Tools: []string{"read_file", "run_test", "publish_artifact"}}
	agents := []w.Agent{coord, writer, reviewer}
	thread := w.Thread{AgentIDs: []string{"coord", "writer", "reviewer"}, CoordinatorID: "coord"}
	settings := w.Settings{Assignments: map[string]map[string]string{"engineering": {"reviewer": "reviewer"}}}
	in := w.RunInput{Message: "修复代码并运行测试", Workflow: "engineering"}
	fallback, err := applySettingsDefaults(in, thread, agents, settings)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Assignments["engineer"] != "writer" {
		t.Fatalf("deterministic fallback engineer = %s", fallback.Assignments["engineer"])
	}
	m := &fixedRoleModel{assignments: map[string]string{"engineer": "coord"}}
	server := &Server{config: Config{Model: m}}
	got, reason := server.applySettingsDefaultsWithModel(t.Context(), in, thread, agents, settings, fallback)
	if m.calls != 1 || got.Assignments["engineer"] != "writer" || reason == "" {
		t.Fatalf("coordinator worker = %+v reason=%q calls=%d", got.Assignments, reason, m.calls)
	}
}

func TestApplySettingsDefaultsWithModelKeepsCoordinatorAsLastResort(t *testing.T) {
	coord := w.Agent{ID: "coord", DisplayName: "协调", Handle: "coord", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	reviewer := w.Agent{ID: "reviewer", DisplayName: "复核", Handle: "reviewer", Tools: []string{"read_file", "run_test", "publish_artifact"}}
	agents := []w.Agent{coord, reviewer}
	thread := w.Thread{AgentIDs: []string{"coord", "reviewer"}, CoordinatorID: "coord"}
	settings := w.Settings{Assignments: map[string]map[string]string{"engineering": {"reviewer": "reviewer"}}}
	in := w.RunInput{Message: "修复代码并运行测试", Workflow: "engineering"}
	fallback, err := applySettingsDefaults(in, thread, agents, settings)
	if err != nil {
		t.Fatal(err)
	}
	m := &fixedRoleModel{assignments: map[string]string{"engineer": "coord"}}
	server := &Server{config: Config{Model: m}}
	got, reason := server.applySettingsDefaultsWithModel(t.Context(), in, thread, agents, settings, fallback)
	if m.calls != 1 || got.Assignments["engineer"] != "coord" || got.Assignments["reviewer"] != "reviewer" || reason == "" {
		t.Fatalf("last resort = %+v reason=%q calls=%d", got.Assignments, reason, m.calls)
	}
}

func TestPreviewUsesCoordinatorRoleSelectionForOpenEngineeringRole(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &fixedRoleModel{assignments: map[string]string{"engineer": ""}}
	api := newDeliveryAPIWithModel(t, dir, db, nil, m)
	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "职责组队", Root: dir}, nil, 200, &ws)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	if len(members.Items) < 3 {
		t.Fatal("expected initialized members")
	}
	coordinator := members.Items[0]
	writer := members.Items[1]
	reviewer := members.Items[2]
	var current w.Settings
	api.call("GET", "/api/v1/settings", nil, nil, 200, &current)
	api.call("PATCH", "/api/v1/settings", SettingsInput{Assignments: assignments(map[string]map[string]string{
		"engineering": {"reviewer": reviewer.ID},
	})}, map[string]string{"If-Match": strconv.Itoa(current.Revision)}, 200, nil)
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "工程职责组队", AgentIDs: []string{coordinator.ID, writer.ID, reviewer.ID}, CoordinatorID: coordinator.ID, Workflow: "engineering"}, nil, 200, &thread)
	m.assignments = map[string]string{"engineer": writer.ID}
	var preview RoutingPreview
	api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", w.RunInput{Message: "修复代码并运行测试", Workflow: "engineering"}, nil, 200, &preview)
	if preview.ProposalID == "" || preview.Routing.Assignments["engineer"] != writer.ID || preview.Routing.Assignments["reviewer"] != reviewer.ID {
		t.Fatalf("responsibility preview = %+v", preview)
	}
	if !strings.Contains(preview.Routing.Explanation, "按职责选择") {
		t.Fatalf("preview explanation lost the selection reason: %q", preview.Routing.Explanation)
	}
	if m.calls != 1 {
		t.Fatalf("role selection calls = %d", m.calls)
	}
}
