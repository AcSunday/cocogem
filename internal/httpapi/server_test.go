package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestAuthenticatedWorkspaceToResearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600)
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	engine := &workflow.Engine{Store: db, AllowedModels: []string{"demo"}, Model: model.Demo{}, Web: tools.NewWeb("", true), Demo: true}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	s, err := New(Config{Host: "local.test", WebDir: dir, Models: []string{"demo"}, Demo: true, Model: model.Demo{}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path string, value any, headers map[string]string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(value)
		req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		return rw
	}
	if got := call("GET", "/api/v1/system", nil, nil).Code; got != 401 {
		t.Fatal(got)
	}
	rw := call("POST", "/bootstrap", map[string]string{"token": s.BootstrapToken()}, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	cookie = rw.Result().Cookies()[0]
	var auth map[string]string
	json.Unmarshal(rw.Body.Bytes(), &auth)
	csrf = auth["csrf_token"]
	if reused := call("POST", "/bootstrap", map[string]string{"token": "used"}, nil); reused.Code != 401 {
		t.Fatal("bootstrap token reuse accepted")
	}
	oldCSRF := csrf
	csrf = "incorrect"
	if denied := call("POST", "/api/v1/workspaces", map[string]string{"name": "bad"}, nil); denied.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	csrf = oldCSRF
	validateResponse(t, "/bootstrap", "post", 200, rw.Body.Bytes())
	systemResponse := call("GET", "/api/v1/system", nil, nil)
	validateResponse(t, "/api/v1/system", "get", 200, systemResponse.Body.Bytes())
	if got := call("POST", "/api/v1/workspaces", map[string]string{"name": "x"}, map[string]string{"Origin": "https://evil.example"}).Code; got != 403 {
		t.Fatal("origin bypass", got)
	}
	rw = call("POST", "/api/v1/workspaces", map[string]string{"name": "研究空间"}, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var ws w.Workspace
	json.Unmarshal(rw.Body.Bytes(), &ws)
	validateResponse(t, "/api/v1/workspaces", "post", 200, rw.Body.Bytes())
	rw = call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil)
	var members struct {
		Items []w.Agent `json:"items"`
	}
	json.Unmarshal(rw.Body.Bytes(), &members)
	if len(members.Items) != 3 {
		t.Fatal(rw.Body.String())
	}
	a := members.Items
	a[0].DisplayName = "布布"
	rw = call("PATCH", "/api/v1/agents/"+a[0].ID, a[0], map[string]string{"If-Match": "1"})
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	if got := call("PATCH", "/api/v1/agents/"+a[0].ID, a[0], map[string]string{"If-Match": "1"}).Code; got != 412 {
		t.Fatal("stale edit accepted", got)
	}
	th := w.Thread{Title: "研究", AgentIDs: []string{a[0].ID, a[1].ID, a[2].ID}, CoordinatorID: a[2].ID, Workflow: "research"}
	rw = call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", th, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	json.Unmarshal(rw.Body.Bytes(), &th)
	s.config.Demo = false
	spy := &countModel{}
	previousModel := s.config.Model
	s.config.Model = spy
	denied := call("POST", "/api/v1/threads/"+th.ID+"/routing-preview", w.RunInput{Message: "布布帮我研究一下"}, nil)
	if denied.Code != 403 || spy.calls.Load() != 0 || !strings.Contains(denied.Body.String(), "确认") {
		t.Fatal("routing bypassed data consent", denied.Body.String(), spy.calls.Load())
	}
	s.config.Demo = true
	s.config.Model = previousModel
	in := w.RunInput{Message: "比较协作方法", Workflow: "research", Assignments: map[string]string{"researcher": a[0].ID, "critic": a[1].ID, "coordinator": a[2].ID}}
	rw = call("POST", "/api/v1/threads/"+th.ID+"/routing-preview", in, nil)
	var preview struct {
		ID string `json:"proposal_id"`
	}
	json.Unmarshal(rw.Body.Bytes(), &preview)
	in.ProposalID = preview.ID
	rw = call("POST", "/api/v1/threads/"+th.ID+"/runs", in, map[string]string{"Idempotency-Key": "test-idempotency-key"})
	if rw.Code != 202 {
		t.Fatal(rw.Body.String())
	}
	var run w.Run
	json.Unmarshal(rw.Body.Bytes(), &run)
	validateResponse(t, "/api/v1/threads/{id}/runs", "post", 202, rw.Body.Bytes())
	replay := call("POST", "/api/v1/threads/"+th.ID+"/runs", in, map[string]string{"Idempotency-Key": "test-idempotency-key"})
	var duplicate w.Run
	json.Unmarshal(replay.Body.Bytes(), &duplicate)
	if duplicate.ID != run.ID {
		t.Fatal("duplicate dispatch")
	}
	deadline := time.After(5 * time.Second)
	for {
		wake := db.Changed()
		var settled w.Run
		db.Read(ctx, func(tx *store.Tx) error { var err error; settled, err = tx.Run(run.ID); return err })
		if w.Terminal(settled.Status) {
			if settled.Status != "succeeded" {
				t.Fatal(settled.Error)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("run did not settle")
		case <-wake:
		}
	}
	rw = call("GET", "/api/v1/runs/"+run.ID+"/events", nil, nil)
	if !strings.Contains(rw.Body.String(), "run_finished") {
		t.Fatal("SSE terminal missing")
	}
	var eventBodies []string
	lastID := ""
	for _, line := range strings.Split(rw.Body.String(), "\n") {
		if strings.HasPrefix(line, "id: ") {
			lastID = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "data: ") {
			eventBodies = append(eventBodies, strings.TrimPrefix(line, "data: "))
		}
	}
	if len(eventBodies) == 0 {
		t.Fatal("no persisted event")
	}
	var eventDoc map[string]any
	if err := json.Unmarshal([]byte(eventBodies[len(eventBodies)-1]), &eventDoc); err != nil {
		t.Fatal(err)
	}
	if eventDoc["type"] != "run_finished" || eventDoc["schema_version"] != float64(1) {
		t.Fatal(eventDoc)
	}
	afterTerminal := call("GET", "/api/v1/runs/"+run.ID+"/events", nil, map[string]string{"Last-Event-ID": lastID})
	if afterTerminal.Body.Len() != 0 {
		t.Fatal("terminal event replay ignored cursor")
	}
	rw = call("GET", "/api/v1/threads/"+th.ID+"/artifacts", nil, nil)
	var artifacts struct {
		Items []w.Artifact `json:"items"`
	}
	json.Unmarshal(rw.Body.Bytes(), &artifacts)
	if len(artifacts.Items) != 1 {
		t.Fatal(rw.Body.String())
	}
	artifact := artifacts.Items[0]
	rw = call("GET", "/api/v1/artifacts/"+artifact.ID+"/versions/1/export", nil, nil)
	b, _ := io.ReadAll(rw.Result().Body)
	if !strings.Contains(string(b), "离线演示") {
		t.Fatal("demo export not labeled")
	}

	// Deleting a conversation removes its run history; deleting the workspace removes the rest.
	rw = call("DELETE", "/api/v1/threads/"+th.ID, nil, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	validateResponse(t, "/api/v1/threads/{id}", "delete", 200, rw.Body.Bytes())
	if got := call("GET", "/api/v1/threads/"+th.ID, nil, nil).Code; got != 404 {
		t.Fatal("deleted thread still readable", got)
	}
	if got := call("GET", "/api/v1/artifacts/"+artifact.ID+"/versions/1", nil, nil).Code; got != 404 {
		t.Fatal("deleted thread kept its artifact", got)
	}
	if got := call("GET", "/api/v1/runs/"+run.ID, nil, nil).Code; got != 404 {
		t.Fatal("deleted thread kept its run", got)
	}
	if got := call("DELETE", "/api/v1/threads/"+th.ID, nil, nil).Code; got != 404 {
		t.Fatal("repeated delete", got)
	}
	rw = call("DELETE", "/api/v1/workspaces/"+ws.ID, nil, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var spaces struct {
		Items []w.Workspace `json:"items"`
	}
	json.Unmarshal(call("GET", "/api/v1/workspaces", nil, nil).Body.Bytes(), &spaces)
	if len(spaces.Items) != 0 {
		t.Fatal("workspace survived deletion")
	}
}

func TestDeleteRefusesActiveRun(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := w.Workspace{ID: w.NewID(), Name: "x", Revision: 1}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: th.ID, Status: "running", CreatedAt: w.Now()}
	if err := db.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		return tx.InsertRun(run, "key", "hash")
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: db, proposals: map[string]proposal{}}
	for _, path := range []string{"/api/v1/threads/" + th.ID, "/api/v1/workspaces/" + ws.ID} {
		req := httptest.NewRequest("DELETE", path, nil)
		rw := httptest.NewRecorder()
		mux := http.NewServeMux()
		mux.HandleFunc("DELETE /api/v1/threads/{id}", s.deleteThread)
		mux.HandleFunc("DELETE /api/v1/workspaces/{id}", s.deleteWorkspace)
		mux.ServeHTTP(rw, req)
		if rw.Code != 409 {
			t.Fatal(path, rw.Code, rw.Body.String())
		}
	}
}

type countModel struct{ calls atomic.Int32 }

func (m *countModel) Complete(context.Context, model.Request, func(string) error) (model.Response, error) {
	m.calls.Add(1)
	return model.Response{StopReason: "end_turn"}, nil
}

func TestPaginationUsesOpaqueBoundedCursor(t *testing.T) {
	r := httptest.NewRequest("GET", "http://local.test/?limit=2", nil)
	page, err := paginate(r, []int{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	cursor := page["next_cursor"].(string)
	if cursor == "2" || cursor == "" {
		t.Fatal("cursor not opaque")
	}
	r.URL.RawQuery = "limit=2&cursor=" + url.QueryEscape(cursor)
	next, err := paginate(r, []int{1, 2, 3})
	if err != nil || len(next["items"].([]int)) != 1 || next["next_cursor"] != "" {
		t.Fatal(next, err)
	}
	r.URL.RawQuery = "limit=999"
	if _, err := paginate(r, []int{}); err == nil {
		t.Fatal("unbounded limit accepted")
	}
}

func TestExecutorCapabilitiesFailClosed(t *testing.T) {
	s := &Server{config: Config{Executors: []ExecutorInfo{
		{ID: "claude", Installed: true, Supported: true},
		{ID: "codex", Installed: true, Supported: false, Reason: "restricted adapter unavailable"},
		{ID: "gemini", Installed: false},
	}}}
	for _, tc := range []struct {
		name     string
		executor string
		wantErr  bool
	}{
		{"supported", "claude", false},
		{"missing explicit migration", "", true},
		{"unknown", "invented", true},
		{"unsupported", "codex", true},
		{"not installed", "gemini", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.validateExecutor(tc.executor); (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
