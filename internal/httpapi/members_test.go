package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestGlobalMembersCanBeReusedAndLocallyRestricted(t *testing.T) {
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
	call("POST", "/auth/setup", PasswordInput{Password: "local-members-test"}, nil, 0, 200)
	var first, second w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "one"}, &first, 0, 200)
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "two"}, &second, 0, 200)
	var globals, linked struct{ Items []w.Agent }
	call("GET", "/api/v1/agents", nil, &globals, 0, 200)
	call("GET", "/api/v1/workspaces/"+second.ID+"/agents", nil, &linked, 0, 200)
	if len(globals.Items) != 3 || len(linked.Items) != 3 {
		t.Fatalf("workspaces duplicated global members: %+v %+v", globals, linked)
	}
	for i, executor := range []string{"claude", "codex", "gemini"} {
		if globals.Items[i].ID != linked.Items[i].ID || globals.Items[i].Executor != executor || globals.Items[i].WorkspaceID != "" || globals.Items[i].Membership != nil {
			t.Fatalf("global binding not reused: %+v %+v", globals.Items[i], linked.Items[i])
		}
	}
	agent := globals.Items[0]
	agent.DisplayName, agent.Capabilities = "可复用成员", []string{"资料核验"}
	call("PATCH", "/api/v1/agents/"+agent.ID, agent, &agent, 1, 200)
	call("GET", "/api/v1/workspaces/"+second.ID+"/agents", nil, &linked, 0, 200)
	if linked.Items[0].DisplayName != agent.DisplayName || linked.Items[0].Revision != 2 || len(linked.Items[0].Capabilities) != 1 {
		t.Fatal("global edit did not reach second workspace", linked.Items[0])
	}
	memberPath := "/api/v1/workspaces/" + first.ID + "/members/" + agent.ID
	grant := w.WorkspaceMember{Enabled: false, Tools: []string{"read_file"}}
	var effective w.Agent
	call("PATCH", memberPath, grant, &effective, 1, 200)
	if !effective.Disabled || len(effective.Tools) != 1 || effective.Membership == nil || effective.Membership.Revision != 2 {
		t.Fatal("local grants not applied", effective)
	}
	call("PATCH", memberPath, grant, nil, 1, 412)
	call("GET", "/api/v1/workspaces/"+second.ID+"/agents", nil, &linked, 0, 200)
	if linked.Items[0].Disabled || len(linked.Items[0].Tools) <= 1 {
		t.Fatal("workspace restriction leaked to another workspace", linked.Items[0])
	}
	call("DELETE", memberPath, nil, nil, 1, 412)
	call("DELETE", memberPath, nil, nil, 2, 200)
	call("GET", "/api/v1/agents", nil, &globals, 0, 200)
	if len(globals.Items) != 3 {
		t.Fatal("unlink deleted the global profile")
	}
	grant.Enabled = true
	call("PUT", memberPath, grant, &effective, 0, 200)
	call("PUT", memberPath, grant, nil, 0, 409)
	duplicate := agent
	duplicate.ID, duplicate.Revision = "", 0
	call("POST", "/api/v1/agents", duplicate, &duplicate, 0, 200)
	call("PUT", "/api/v1/workspaces/"+first.ID+"/members/"+duplicate.ID, grant, nil, 0, 400)
	call("DELETE", "/api/v1/workspaces/"+first.ID, nil, nil, 0, 200)
	call("GET", "/api/v1/agents", nil, &globals, 0, 200)
	if len(globals.Items) != 4 {
		t.Fatal("workspace deletion deleted reusable global profiles")
	}
}
