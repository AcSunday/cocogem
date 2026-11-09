package httpapi

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestBootstrapFirstInputIsAtomicAndReplayable(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	api := newDeliveryAPI(t, root, db, nil)
	input := BootstrapInput{Title: "比较两种协作方案", Workflow: "research"}
	headers := map[string]string{"Idempotency-Key": "first-input-stable-key"}
	var first, replay BootstrapReply
	api.call("POST", "/api/v1/bootstrap", input, headers, 200, &first)
	api.call("POST", "/api/v1/bootstrap", input, headers, 200, &replay)
	if first.Workspace.ID != replay.Workspace.ID || first.Thread.ID != replay.Thread.ID {
		t.Fatalf("bootstrap replay created new identities: %+v / %+v", first, replay)
	}
	if first.Workspace.Name != "未命名项目" || first.Workspace.Root != "" || first.Workspace.ExecutionMode != w.ExecutionControlled {
		t.Fatalf("unsafe or blocking first project: %+v", first.Workspace)
	}
	if first.Thread.Title != input.Title || first.Thread.CoordinatorID == "" || len(first.Agents) == 0 {
		t.Fatalf("first input is not executable: %+v", first)
	}
	conflicting := input
	conflicting.Title = "另一个目标"
	api.call("POST", "/api/v1/bootstrap", conflicting, headers, 409, nil)
	var workspaces struct {
		Items []w.Workspace `json:"items"`
	}
	api.call("GET", "/api/v1/workspaces", nil, nil, 200, &workspaces)
	if len(workspaces.Items) != 1 {
		t.Fatalf("replay or conflict changed project count: %+v", workspaces.Items)
	}
}
