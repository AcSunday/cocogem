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
