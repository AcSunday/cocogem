package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestRunViewAttemptsAndExplicitResume(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	engine := &workflow.Engine{Store: db, AllowedModels: []string{"default"}, Model: testutil.Model{}, Web: testutil.Web{}}
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	server, err := New(Config{Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, ModelConfigured: true, Model: testutil.Model{}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path string, value any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(value)
		req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		return rw
	}
	rw := call("POST", "/auth/setup", PasswordInput{Password: "local-test-password"})
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	cookie = rw.Result().Cookies()[0]
	var auth map[string]string
	json.Unmarshal(rw.Body.Bytes(), &auth)
	csrf = auth["csrf_token"]
	rw = call("POST", "/api/v1/workspaces", map[string]string{"name": "恢复空间"})
	var ws w.Workspace
	json.Unmarshal(rw.Body.Bytes(), &ws)
	rw = call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil)
	var members struct{ Items []w.Agent }
	json.Unmarshal(rw.Body.Bytes(), &members)
	agent := members.Items[0]
	thread := w.Thread{Title: "恢复会话", AgentIDs: []string{agent.ID}, CoordinatorID: agent.ID, Workflow: "free"}
	rw = call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", thread)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	json.Unmarshal(rw.Body.Bytes(), &thread)

	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "interrupted", CreatedAt: w.Now(), FinishedAt: w.Now(), Agents: []w.Agent{agent}, CoordinatorID: agent.ID, Input: w.RunInput{Message: "继续给定任务", Workflow: "free", Targets: []string{agent.ID}, ResearchScope: "given_sources"}}
	step := w.Step{ID: w.NewID(), RunID: run.ID, AgentID: agent.ID, AgentName: agent.DisplayName, Role: "responder", Instruction: "Answer the task", Status: "interrupted", Error: "capability_blocked: claude: missing --restricted"}
	attempt := w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: step.ID, Number: 1, Status: "blocked", Reason: "initial", Error: step.Error, StartedAt: w.Now(), FinishedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, w.NewID(), "resume"); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Role: "user", Content: run.Input.Message, Status: "completed", CreatedAt: w.Now()})
	}); err != nil {
		t.Fatal(err)
	}

	rw = call("GET", "/api/v1/runs/"+run.ID, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var view RunView
	if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Attempts) != 1 || view.Attempts[0].Status != "blocked" {
		t.Fatalf("attempts missing from run view: %+v", view)
	}
	validateResponse(t, "/api/v1/runs/{id}", "get", 200, rw.Body.Bytes())

	resumeReply := call("POST", "/api/v1/runs/"+run.ID+"/resume", map[string]any{})
	if resumeReply.Code != 202 {
		t.Fatalf("resume status=%d body=%s", resumeReply.Code, resumeReply.Body.String())
	}
	validateResponse(t, "/api/v1/runs/{id}/resume", "post", 202, resumeReply.Body.Bytes())
	var final w.Run
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		rw = call("GET", "/api/v1/runs/"+run.ID, nil)
		if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if w.Terminal(view.Run.Status) {
			final = view.Run
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final.Status != "succeeded" || final.RecoveryCount != 1 {
		t.Fatalf("resume did not continue: %+v", final)
	}
	if len(view.Attempts) < 2 {
		t.Fatalf("resume did not create a new attempt: %+v", view.Attempts)
	}
}

func TestResumeRejectsCanceledRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	engine := &workflow.Engine{Store: db, AllowedModels: []string{"default"}, Model: testutil.Model{}}
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	server, err := New(Config{Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, ModelConfigured: true, Model: testutil.Model{}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path string, value any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(value)
		req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		return rw
	}
	rw := call("POST", "/auth/setup", PasswordInput{Password: "local-test-password"})
	cookie = rw.Result().Cookies()[0]
	var auth map[string]string
	json.Unmarshal(rw.Body.Bytes(), &auth)
	csrf = auth["csrf_token"]
	rw = call("POST", "/api/v1/workspaces", map[string]string{"name": "取消空间"})
	var ws w.Workspace
	json.Unmarshal(rw.Body.Bytes(), &ws)
	rw = call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil)
	var members struct{ Items []w.Agent }
	json.Unmarshal(rw.Body.Bytes(), &members)
	agent := members.Items[0]
	thread := w.Thread{Title: "取消会话", AgentIDs: []string{agent.ID}, CoordinatorID: agent.ID, Workflow: "free"}
	rw = call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", thread)
	json.Unmarshal(rw.Body.Bytes(), &thread)
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "canceled", CreatedAt: w.Now(), FinishedAt: w.Now(), Agents: []w.Agent{agent}, Input: w.RunInput{Message: "停止", Workflow: "free", Targets: []string{agent.ID}}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.InsertRun(run, w.NewID(), "canceled") }); err != nil {
		t.Fatal(err)
	}
	if got := call("POST", "/api/v1/runs/"+run.ID+"/resume", map[string]any{}); got.Code != 409 {
		t.Fatalf("canceled run resumed: %d %s", got.Code, got.Body.String())
	}
}

func TestDeleteRefusesAliveOwnedProcess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	engine := &workflow.Engine{Store: db, AllowedModels: []string{"default"}, Model: testutil.Model{}}
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	server, err := New(Config{Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, ModelConfigured: true, Model: testutil.Model{}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path string, value any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(value)
		req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		return rw
	}
	rw := call("POST", "/auth/setup", PasswordInput{Password: "local-test-password"})
	cookie = rw.Result().Cookies()[0]
	var auth map[string]string
	json.Unmarshal(rw.Body.Bytes(), &auth)
	csrf = auth["csrf_token"]
	rw = call("POST", "/api/v1/workspaces", map[string]string{"name": "进程空间"})
	var ws w.Workspace
	json.Unmarshal(rw.Body.Bytes(), &ws)
	rw = call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil)
	var members struct{ Items []w.Agent }
	json.Unmarshal(rw.Body.Bytes(), &members)
	agent := members.Items[0]
	thread := w.Thread{Title: "进程会话", AgentIDs: []string{agent.ID}, CoordinatorID: agent.ID, Workflow: "free"}
	rw = call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", thread)
	json.Unmarshal(rw.Body.Bytes(), &thread)
	self, err := proc.Inspect(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "interrupted", CreatedAt: w.Now(), FinishedAt: w.Now(), Agents: []w.Agent{agent}, Input: w.RunInput{Message: "pending", Workflow: "free", Targets: []string{agent.ID}}}
	step := w.Step{ID: w.NewID(), RunID: run.ID, AgentID: agent.ID, AgentName: agent.DisplayName, Role: "responder", Status: "interrupted", Instruction: "pending"}
	attempt := w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: step.ID, Number: 1, Status: "uncertain", StartedAt: w.Now(), Process: &w.ProcessRef{PID: self.PID, PGID: self.PGID, Start: self.Start}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, w.NewID(), "owned"); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	if got := call("DELETE", "/api/v1/threads/"+thread.ID, map[string]any{}); got.Code != 409 {
		t.Fatalf("live ownership deleted: %d %s", got.Code, got.Body.String())
	}
	attempt.Process = &w.ProcessRef{PID: 999999, PGID: 999999, Start: "Mon Jan  1 00:00:00 2001"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveAttempt(attempt) }); err != nil {
		t.Fatal(err)
	}
	if got := call("DELETE", "/api/v1/threads/"+thread.ID, map[string]any{}); got.Code != 200 {
		t.Fatalf("dead ownership blocked deletion: %d %s", got.Code, got.Body.String())
	}
}
