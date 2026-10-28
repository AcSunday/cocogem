package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestThreadRenameSurvivesDisabledMember(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(Config{Host: "local.test", WebDir: dir, AuthFile: filepath.Join(dir, "auth.json"), Models: []string{"default"}, Executors: []ExecutorInfo{
		{ID: "claude", Installed: true, Supported: true},
		{ID: "codex", Installed: true, Supported: true},
		{ID: "gemini", Installed: true, Supported: true},
	}}, db, nil)
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
	call("POST", "/auth/setup", PasswordInput{Password: "local-thread-rename"}, nil, 0, 200)
	var ws w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "rename"}, &ws, 0, 200)
	var members struct{ Items []w.Agent }
	call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, &members, 0, 200)
	if len(members.Items) != 3 {
		t.Fatalf("unexpected members: %+v", members.Items)
	}
	ids := []string{members.Items[0].ID, members.Items[1].ID, members.Items[2].ID}
	thread := w.Thread{Title: "旧标题", AgentIDs: ids, CoordinatorID: ids[1], Workflow: "research"}
	call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", thread, &thread, 0, 200)

	// Disabling the coordinator must not block a later title-only rename.
	memberPath := "/api/v1/workspaces/" + ws.ID + "/members/" + ids[1]
	call("PATCH", memberPath, w.WorkspaceMember{Enabled: false, Tools: []string{}}, nil, 1, 200)
	var beforeRename struct{ Items []w.Agent }
	call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, &beforeRename, 0, 200)

	var renamed w.Thread
	call("PATCH", "/api/v1/threads/"+thread.ID, map[string]string{"title": "新标题"}, &renamed, 1, 200)
	if renamed.Title != "新标题" || renamed.Revision != 2 || !reflect.DeepEqual(renamed.AgentIDs, ids) || renamed.CoordinatorID != ids[1] || renamed.Workflow != "research" {
		t.Fatalf("title-only update changed other fields: %+v", renamed)
	}
	if renamed.ID != thread.ID || renamed.WorkspaceID != ws.ID {
		t.Fatalf("title-only update changed identity: %+v", renamed)
	}
	var view ThreadView
	call("GET", "/api/v1/threads/"+thread.ID, nil, &view, 0, 200)
	if view.Thread.Title != "新标题" || view.Thread.Revision != 2 || !reflect.DeepEqual(view.Thread.AgentIDs, ids) || view.Thread.CoordinatorID != ids[1] || view.Thread.Workflow != "research" {
		t.Fatalf("rename was not persisted with its members: %+v", view.Thread)
	}
	var afterRename struct{ Items []w.Agent }
	call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, &afterRename, 0, 200)
	if !reflect.DeepEqual(beforeRename.Items, afterRename.Items) {
		t.Fatalf("rename mutated member roles or grants: %+v -> %+v", beforeRename.Items, afterRename.Items)
	}

	// A missing or stale If-Match still loses before any field is written.
	call("PATCH", "/api/v1/threads/"+thread.ID, map[string]string{"title": "无版本标题"}, nil, 0, 412)
	call("PATCH", "/api/v1/threads/"+thread.ID, map[string]string{"title": "过期标题"}, nil, 1, 412)
	call("GET", "/api/v1/threads/"+thread.ID, nil, &view, 0, 200)
	if view.Thread.Title != "新标题" || view.Thread.Revision != 2 {
		t.Fatalf("rejected rename wrote: %+v", view.Thread)
	}

	// Explicit member fields still run full validation, including an empty list.
	call("PATCH", "/api/v1/threads/"+thread.ID, map[string]any{"title": "新标题", "agent_ids": []string{}}, nil, 2, 400)
	call("PATCH", "/api/v1/threads/"+thread.ID, map[string]string{"title": "   "}, nil, 2, 400)
	call("PATCH", "/api/v1/threads/"+thread.ID, map[string]string{"title": strings.Repeat("长", 151)}, nil, 2, 400)
	call("GET", "/api/v1/threads/"+thread.ID, nil, &view, 0, 200)
	if view.Thread.Title != "新标题" || view.Thread.Revision != 2 {
		t.Fatalf("rejected edits were written: %+v", view.Thread)
	}

	// Legacy clients may still send a whole Thread object. Identity and version
	// fields are accepted but ignored; If-Match and the path stay authoritative.
	legacy := map[string]any{
		"id":             "forged-thread",
		"workspace_id":   "forged-workspace",
		"revision":       99,
		"updated_at":     "2000-01-01T00:00:00Z",
		"title":          "旧客户端标题",
		"agent_ids":      []string{ids[0], ids[2]},
		"coordinator_id": ids[0],
		"workflow":       "code_analysis",
	}
	var legacySaved w.Thread
	call("PATCH", "/api/v1/threads/"+thread.ID, legacy, &legacySaved, 2, 200)
	if legacySaved.ID != thread.ID || legacySaved.WorkspaceID != ws.ID || legacySaved.Revision != 3 || legacySaved.UpdatedAt == "2000-01-01T00:00:00Z" {
		t.Fatalf("legacy body changed identity or version: %+v", legacySaved)
	}
	if legacySaved.Title != "旧客户端标题" || legacySaved.CoordinatorID != ids[0] || legacySaved.Workflow != "code_analysis" || !reflect.DeepEqual(legacySaved.AgentIDs, []string{ids[0], ids[2]}) {
		t.Fatalf("legacy body edits were not applied: %+v", legacySaved)
	}
	call("GET", "/api/v1/threads/"+thread.ID, nil, &view, 0, 200)
	if view.Thread.ID != thread.ID || view.Thread.WorkspaceID != ws.ID || view.Thread.Revision != 3 || view.Thread.CoordinatorID != ids[0] {
		t.Fatalf("legacy body was not persisted with stable identity: %+v", view.Thread)
	}

	// Unknown fields are still rejected, legacy-shaped body or not.
	call("PATCH", "/api/v1/threads/"+thread.ID, map[string]any{"title": "未知字段", "bogus": true}, nil, 3, 400)
	call("GET", "/api/v1/threads/"+thread.ID, nil, &view, 0, 200)
	if view.Thread.Title != "旧客户端标题" || view.Thread.Revision != 3 {
		t.Fatalf("unknown-field request wrote: %+v", view.Thread)
	}
}
