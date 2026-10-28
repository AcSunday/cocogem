package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestEngineeringActionRequiresExplicitModeAndGrants(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(Config{Host: "local.test", WebDir: dir, AuthFile: filepath.Join(dir, "auth.json"), Models: []string{"default"}, ModelConfigured: true, Executors: []ExecutorInfo{{ID: "claude", Installed: true, Supported: true}}}, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path string, in, out any, revision, want int) {
		t.Helper()
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if revision > 0 {
			req.Header.Set("If-Match", strconv.Itoa(revision))
		}
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		if rw.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, rw.Code, want, rw.Body.String())
		}
		if path == "/auth/setup" {
			cookie = rw.Result().Cookies()[0]
			var session SessionReply
			if err := json.Unmarshal(rw.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			csrf = session.CSRFToken
		}
		if out != nil {
			if err := json.Unmarshal(rw.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	call("POST", "/auth/setup", PasswordInput{Password: "engineering-api-test"}, nil, 0, 200)
	var ws w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "engineering", Root: root}, &ws, 0, 200)
	var agents struct{ Items []w.Agent }
	call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, &agents, 0, 200)
	if len(agents.Items) < 2 {
		t.Fatal("expected initialized members")
	}
	engineeringTools := []string{"list_files", "read_file", "search_content", "read_source", "publish_artifact", "write_file", "run_test"}
	for i := 0; i < 2; i++ {
		agent := agents.Items[i]
		agent.Tools = engineeringTools
		var saved w.Agent
		call("PATCH", "/api/v1/agents/"+agent.ID, agent, &saved, agent.Revision, 200)
		if agent.Membership == nil {
			t.Fatal("initialized member has no workspace membership")
		}
		member := *agent.Membership
		member.Tools = engineeringTools
		call("PATCH", "/api/v1/workspaces/"+ws.ID+"/members/"+agent.ID, member, nil, member.Revision, 200)
	}
	var thread w.Thread
	call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "工程", AgentIDs: []string{agents.Items[0].ID, agents.Items[1].ID}, CoordinatorID: agents.Items[1].ID, Workflow: "engineering"}, &thread, 0, 200)
	assignments := map[string]string{"developer": agents.Items[0].ID, "reviewer": agents.Items[1].ID}
	analyzeAssignments := map[string]string{"analyst": agents.Items[0].ID, "reviewer": agents.Items[1].ID}
	preview := func(action string, roles map[string]string, want int) RoutingPreview {
		t.Helper()
		var out RoutingPreview
		call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", w.RunInput{Message: "修复 Add 并运行测试", Workflow: "engineering", EngineeringAction: action, Assignments: roles, ResearchScope: "given_sources"}, &out, 0, want)
		return out
	}
	develop := preview(w.EngineeringDevelop, assignments, 200)
	if develop.ProposalID == "" || develop.Routing.Workflow != "engineering" {
		t.Fatalf("develop preview = %+v", develop)
	}
	analyze := preview("", analyzeAssignments, 200)
	if analyze.ProposalID == "" {
		t.Fatal("analysis preview did not default to analyze")
	}
	// The action's applicable role subset rejects the other action's roles.
	call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", w.RunInput{Message: "修复", Workflow: "engineering", EngineeringAction: w.EngineeringDevelop, Assignments: analyzeAssignments}, nil, 0, 400)
	// An unknown action and a develop action on another workflow are rejected
	// before a proposal can be submitted.
	call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", w.RunInput{Message: "修复", Workflow: "engineering", EngineeringAction: "deploy", Assignments: assignments}, nil, 0, 400)
	call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", w.RunInput{Message: "修复", Workflow: "free", EngineeringAction: w.EngineeringDevelop}, nil, 0, 400)
	var clarified RoutingPreview
	call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", w.RunInput{Message: "@" + agents.Items[0].Handle + " 修复", Workflow: "engineering", EngineeringAction: w.EngineeringDevelop}, &clarified, 0, 200)
	if clarified.Routing.Clarification == "" {
		t.Fatalf("engineering mention conflict did not clarify: %+v", clarified)
	}

	req := httptest.NewRequest("POST", "http://local.test/api/v1/threads/"+thread.ID+"/runs", bytes.NewReader(mustJSON(t, w.RunInput{Message: "修复 Add 并运行测试", Workflow: "engineering", EngineeringAction: w.EngineeringDevelop, Assignments: assignments, ResearchScope: "given_sources", ProposalID: develop.ProposalID})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "engineering-test-key-0001")
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)
	if rw.Code != 202 {
		t.Fatalf("submit got %d: %s", rw.Code, rw.Body.String())
	}
	var run w.Run
	if err := json.Unmarshal(rw.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Input.EngineeringAction != w.EngineeringDevelop || run.Input.Workflow != "engineering" {
		t.Fatalf("persisted input = %+v", run.Input)
	}
	var view RunView
	call("GET", "/api/v1/runs/"+run.ID, nil, &view, 0, 200)
	roles := []string{}
	for _, step := range view.Steps {
		roles = append(roles, step.Role)
	}
	if !slices.Equal(roles, []string{"developer", "reviewer", "developer", "final_reviewer"}) {
		t.Fatalf("development plan roles = %v", roles)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
