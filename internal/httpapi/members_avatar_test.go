package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func validAvatarPNG(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 0x2f, G: 0x81, B: 0x9d, A: 0xff})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
}

func TestMemberAvatarUploadLegacyPreservationAndClear(t *testing.T) {
	s, db, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	call := func(method, path string, in, out any, revision int, want int) {
		t.Helper()
		var headers map[string]string
		if revision > 0 {
			headers = map[string]string{"If-Match": strconv.Itoa(revision)}
		}
		rw := apiCall(s, method, path, in, cookie, csrf, headers)
		if rw.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, rw.Code, want, rw.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(rw.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var workspace w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "avatar"}, &workspace, 0, 200)

	avatar := validAvatarPNG(t)
	agent := w.Agent{DisplayName: "avatar", Handle: "avatar", Executor: "claude", Model: "default", ContextSize: 50000, ThinkingLevel: "high", Tools: []string{}, Avatar: avatar}
	var created w.Agent
	call("POST", "/api/v1/agents", agent, &created, 0, 200)
	if created.Avatar != avatar || created.ContextSize != 50000 || created.ThinkingLevel != "high" {
		t.Fatalf("execution options or avatar not persisted: %+v", created)
	}

	agent.Avatar = "🦊"
	call("POST", "/api/v1/agents", agent, nil, 0, 400)

	// A legacy emoji already stored may survive unrelated edits...
	legacy := w.Agent{ID: w.NewID(), DisplayName: "legacy", Handle: "legacy", Model: "default", Tools: []string{}, Avatar: "🐼", Revision: 1}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveAgent(legacy) }); err != nil {
		t.Fatal(err)
	}
	legacy.RoleDescription = "updated"
	call("PATCH", "/api/v1/agents/"+legacy.ID, legacy, nil, legacy.Revision, 200)
	// ...but cannot be replaced by another emoji.
	legacy.Avatar = "🐯"
	call("PATCH", "/api/v1/agents/"+legacy.ID, legacy, nil, legacy.Revision+1, 400)
	// Clearing it is allowed and leaves the rest of the profile intact.
	legacy.Avatar = ""
	legacy.Revision++
	var cleared w.Agent
	call("PATCH", "/api/v1/agents/"+legacy.ID, legacy, &cleared, legacy.Revision, 200)
	if cleared.Avatar != "" || cleared.RoleDescription != "updated" {
		t.Fatalf("avatar clear damaged the profile: %+v", cleared)
	}

	// The same global member is reusable by another workspace.
	link := w.WorkspaceMember{Enabled: true, Tools: []string{"read_source"}, Revision: 1}
	call("PUT", "/api/v1/workspaces/"+workspace.ID+"/members/"+created.ID, link, nil, 0, 200)
	var linked struct {
		Items []w.Agent `json:"items"`
	}
	call("GET", "/api/v1/workspaces/"+workspace.ID+"/agents", nil, &linked, 0, 200)
	found := false
	for _, item := range linked.Items {
		if item.ID == created.ID && item.Avatar == avatar && item.ContextSize == 50000 && item.ThinkingLevel == "high" {
			found = true
		}
	}
	if !found {
		t.Fatalf("global profile or avatar lost in workspace link: %+v", linked.Items)
	}
}

func TestBuiltInCatAvatarUniquenessAndDefaults(t *testing.T) {
	s, _, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	call := func(method, path string, in, out any, revision int, want int) {
		t.Helper()
		var headers map[string]string
		if revision > 0 {
			headers = map[string]string{"If-Match": strconv.Itoa(revision)}
		}
		rw := apiCall(s, method, path, in, cookie, csrf, headers)
		if rw.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, rw.Code, want, rw.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(rw.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}

	var workspace w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "cat-studio"}, &workspace, 0, 200)

	var initial struct {
		Items []w.Agent `json:"items"`
	}
	call("GET", "/api/v1/workspaces/"+workspace.ID+"/agents", nil, &initial, 0, 200)
	wantDefaults := map[string]string{"ragdoll": "cat:ragdoll", "maine": "cat:maine", "orange": "cat:orange"}
	for _, agent := range initial.Items {
		if want, ok := wantDefaults[agent.Handle]; ok && agent.Avatar != want {
			t.Fatalf("default agent %s avatar=%q want %q", agent.Handle, agent.Avatar, want)
		}
	}

	// Creating a member with an already-used cat preset fails with 409.
	dup := w.Agent{DisplayName: "另一只橘猫", Handle: "orange2", Executor: "claude", Model: "default", Avatar: "cat:orange"}
	call("POST", "/api/v1/agents", dup, nil, 0, 409)

	// Unused cat preset succeeds.
	siamese := w.Agent{DisplayName: "暹罗猫", Handle: "siamese", Executor: "claude", Model: "default", Avatar: "cat:siamese"}
	var created w.Agent
	call("POST", "/api/v1/agents", siamese, &created, 0, 200)
	if created.Avatar != "cat:siamese" {
		t.Fatalf("created avatar=%q want cat:siamese", created.Avatar)
	}

	// Updating unrelated fields while keeping own cat preset succeeds.
	created.RoleDescription = "专注协议与边界核查"
	var updated w.Agent
	call("PATCH", "/api/v1/agents/"+created.ID, created, &updated, created.Revision, 200)
	if updated.Avatar != "cat:siamese" {
		t.Fatalf("updated avatar=%q want cat:siamese", updated.Avatar)
	}

	// Switching to a preset held by another member fails with 409.
	updated.Avatar = "cat:ragdoll"
	call("PATCH", "/api/v1/agents/"+created.ID, updated, nil, updated.Revision, 409)

	// Deleting the member frees the cat preset for a new profile.
	call("DELETE", "/api/v1/agents/"+created.ID, nil, nil, updated.Revision, 200)
	var recreated w.Agent
	call("POST", "/api/v1/agents", siamese, &recreated, 0, 200)
	if recreated.Avatar != "cat:siamese" {
		t.Fatalf("recreated avatar=%q want cat:siamese", recreated.Avatar)
	}
}
