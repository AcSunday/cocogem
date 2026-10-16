package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	if got := call("POST", "/api/v1/workspaces", map[string]string{"name": "x"}, map[string]string{"Origin": "https://evil.example"}).Code; got != 403 {
		t.Fatal("origin bypass", got)
	}
	rw = call("POST", "/api/v1/workspaces", map[string]string{"name": "研究空间"}, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var ws w.Workspace
	json.Unmarshal(rw.Body.Bytes(), &ws)
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
	if denied.Code != 400 || spy.calls.Load() != 0 || !strings.Contains(denied.Body.String(), "确认") {
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
}

type countModel struct{ calls atomic.Int32 }

func (m *countModel) Complete(context.Context, model.Request, func(string) error) (model.Response, error) {
	m.calls.Add(1)
	return model.Response{StopReason: "end_turn"}, nil
}
