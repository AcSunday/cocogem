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
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestDeleteGlobalAgentRemovesWorkspaceLinksAtomically(t *testing.T) {
	server, _, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	var workspace w.Workspace
	response := apiCall(server, http.MethodPost, "/api/v1/workspaces", WorkspaceInput{Name: "删除成员"}, cookie, csrf, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("create workspace: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}
	var globals struct{ Items []w.Agent }
	response = apiCall(server, http.MethodGet, "/api/v1/agents", nil, cookie, csrf, nil)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &globals) != nil || len(globals.Items) != 3 {
		t.Fatalf("global agents: %d %s", response.Code, response.Body.String())
	}
	victim := globals.Items[0]
	path := "/api/v1/agents/" + victim.ID
	if got := apiCall(server, http.MethodDelete, path, nil, cookie, csrf, nil); got.Code != http.StatusPreconditionFailed {
		t.Fatalf("delete without If-Match: %d %s", got.Code, got.Body.String())
	}
	response = apiCall(server, http.MethodDelete, path, nil, cookie, csrf, map[string]string{"If-Match": strconv.Itoa(victim.Revision)})
	if response.Code != http.StatusOK {
		t.Fatalf("delete agent: %d %s", response.Code, response.Body.String())
	}
	var reply DeleteReply
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil || reply.ID != victim.ID {
		t.Fatalf("delete reply: %+v %v", reply, err)
	}
	response = apiCall(server, http.MethodGet, "/api/v1/agents", nil, cookie, csrf, nil)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &globals) != nil || len(globals.Items) != 2 {
		t.Fatalf("deleted profile still listed: %d %s", response.Code, response.Body.String())
	}
	var linked struct{ Items []w.Agent }
	response = apiCall(server, http.MethodGet, "/api/v1/workspaces/"+workspace.ID+"/agents", nil, cookie, csrf, nil)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &linked) != nil || len(linked.Items) != 2 {
		t.Fatalf("workspace link survived deletion: %d %s", response.Code, response.Body.String())
	}
	if got := apiCall(server, http.MethodDelete, path, nil, cookie, csrf, map[string]string{"If-Match": "1"}); got.Code != http.StatusNotFound {
		t.Fatalf("repeat delete: %d %s", got.Code, got.Body.String())
	}
}

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
	// Enablement is enforced; the legacy workspace tool array is audit data and
	// no longer strips the profile's inherited capabilities.
	if !effective.Disabled || len(effective.Tools) != len(agent.Tools) || effective.Membership == nil || effective.Membership.Revision != 2 {
		t.Fatal("local enablement not applied", effective)
	}
	call("PATCH", memberPath, grant, nil, 1, 412)
	call("GET", "/api/v1/workspaces/"+second.ID+"/agents", nil, &linked, 0, 200)
	if linked.Items[0].Disabled || len(linked.Items[0].Tools) != len(agent.Tools) {
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

// TestMemberProfileWithoutToolsIsUsable is the retirement acceptance at the API
// boundary: a new global profile with no tools is accepted, the runtime catalog
// applies to it, and an old restricted profile keeps its history while still
// running with the standard catalog.
func TestMemberProfileWithoutToolsIsUsable(t *testing.T) {
	server, _, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	response := apiCall(server, http.MethodPost, "/api/v1/agents", w.Agent{
		DisplayName: "无工具成员", Handle: "no-tools", Aliases: []string{}, Model: "default",
	}, cookie, csrf, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("create member without tools: %d %s", response.Code, response.Body.String())
	}
	var created w.Agent
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || len(created.Tools) != 0 {
		t.Fatalf("new profile = %+v", created)
	}
	// It can be edited without sending a tool array, and a legacy profile keeps
	// its historical array untouched.
	created.DisplayName = "无工具成员改名"
	response = apiCall(server, http.MethodPatch, "/api/v1/agents/"+created.ID, created, cookie, csrf,
		map[string]string{"If-Match": strconv.Itoa(created.Revision)})
	if response.Code != http.StatusOK {
		t.Fatalf("edit member without tools: %d %s", response.Code, response.Body.String())
	}
	var edited w.Agent
	if err := json.Unmarshal(response.Body.Bytes(), &edited); err != nil {
		t.Fatal(err)
	}
	if edited.DisplayName != created.DisplayName || len(edited.Tools) != 0 {
		t.Fatalf("edited profile = %+v", edited)
	}
	// The runtime catalog, not the profile array, decides what a member may run.
	if len(w.StandardToolCatalog()) == 0 {
		t.Fatal("runtime catalog is empty")
	}
	for _, tool := range []string{"read_file", "write_file", "run_test", "publish_artifact", "run_command"} {
		if !slices.Contains(w.StandardToolCatalog(), tool) {
			t.Fatalf("runtime catalog lacks %s", tool)
		}
	}
}
