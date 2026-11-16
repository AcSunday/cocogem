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

// TestDynamicDispatchWithoutPresetWorkflow is the A01/A05 API regression: sending
// a message resolves recipients deterministically, with no routing-preview round
// trip and no preset workflow on either the request or the persisted run.
func TestDynamicDispatchWithoutPresetWorkflow(t *testing.T) {
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
	var thread w.Thread
	call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "协作", AgentIDs: []string{agents.Items[0].ID, agents.Items[1].ID}, CoordinatorID: agents.Items[1].ID}, &thread, 0, 200)

	// A single explicit recipient produces one writable step.
	var single w.Run
	postRun := func(input w.RunInput, out any) {
		t.Helper()
		b, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "http://local.test/api/v1/threads/"+thread.ID+"/runs", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "dynamic-dispatch-"+w.NewID())
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", csrf)
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		if rw.Code != 202 {
			t.Fatalf("submit got %d: %s", rw.Code, rw.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(rw.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	postRun(w.RunInput{Message: "@" + agents.Items[0].Handle + " 修复 Add 并运行测试", ResearchScope: "given_sources"}, &single)
	if len(single.Input.Targets) != 1 || single.Input.Targets[0] != agents.Items[0].ID {
		t.Fatalf("explicit recipient was not resolved deterministically: %+v", single.Input)
	}
	var singleView RunView
	call("GET", "/api/v1/runs/"+single.ID, nil, &singleView, 0, 200)
	if len(singleView.Steps) != 1 || singleView.Steps[0].Role != "responder" || singleView.Steps[0].ReadOnly {
		t.Fatalf("single-recipient plan = %+v", singleView.Steps)
	}
	// The retired preset workflow must not survive anywhere on the wire.
	encoded, err := json.Marshal(single)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"workflow"`, `"assignments"`, `"selected_workflow"`, `"proposal_id"`} {
		if bytes.Contains(encoded, []byte(banned)) {
			t.Fatalf("persisted run still exposes %s: %s", banned, encoded)
		}
	}

	// Two explicit recipients fan out to two independent read-only steps on a
	// second conversation (one active run per conversation).
	var secondThread w.Thread
	call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "并行", AgentIDs: []string{agents.Items[0].ID, agents.Items[1].ID}, CoordinatorID: agents.Items[1].ID}, &secondThread, 0, 200)
	var parallel w.Run
	postParallel := func(input w.RunInput, out any) {
		t.Helper()
		b, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "http://local.test/api/v1/threads/"+secondThread.ID+"/runs", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "dynamic-dispatch-"+w.NewID())
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", csrf)
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		if rw.Code != 202 {
			t.Fatalf("submit got %d: %s", rw.Code, rw.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(rw.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	postParallel(w.RunInput{Message: "@" + agents.Items[0].Handle + " @" + agents.Items[1].Handle + " 各自评估", ResearchScope: "given_sources"}, &parallel)
	var parallelView RunView
	call("GET", "/api/v1/runs/"+parallel.ID, nil, &parallelView, 0, 200)
	if len(parallelView.Steps) != 2 {
		t.Fatalf("parallel plan = %+v", parallelView.Steps)
	}
	for _, step := range parallelView.Steps {
		if !step.ReadOnly || len(step.DependsOn) != 0 {
			t.Fatalf("parallel blind step = %+v", step)
		}
	}
	if !slices.ContainsFunc(agents.Items, func(a w.Agent) bool { return a.ID == parallelView.Steps[0].AgentID }) {
		t.Fatalf("unknown step member: %+v", parallelView.Steps[0])
	}
}
