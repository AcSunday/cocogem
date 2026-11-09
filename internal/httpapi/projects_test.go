package httpapi

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestWorkspacePolicyRoutesAreRetired pins the API收口: the old per-workspace
// execution mode, budget override and file-execution routes no longer exist and
// cannot change a running permission. The global settings endpoint is the only
// write path.
func TestWorkspacePolicyRoutesAreRetired(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveWorkspace(w.Workspace{ID: "project", Name: "项目", Revision: 1})
	}); err != nil {
		t.Fatal(err)
	}
	api := newDeliveryAPI(t, root, db, nil)
	for _, route := range []struct {
		method string
		path   string
	}{
		{"PATCH", "/api/v1/workspaces/project/execution-policy"},
		{"PATCH", "/api/v1/workspaces/project/budget-policy"},
		{"POST", "/api/v1/workspaces/project/file-execution"},
		{"GET", "/api/v1/workspaces/project/grants"},
		{"POST", "/api/v1/workspaces/project/grants/member/revoke"},
	} {
		api.call(route.method, route.path, map[string]any{}, map[string]string{"If-Match": "1"}, 404, nil)
	}
	// The global settings route still accepts a permission change with CAS.
	var settings w.Settings
	api.call("GET", "/api/v1/settings", nil, nil, 200, &settings)
	var updated w.Settings
	api.call("PATCH", "/api/v1/settings", SettingsInput{ExecutionMode: ptr(w.ExecutionFullAccess)}, map[string]string{"If-Match": fmt.Sprint(settings.Revision)}, 200, &updated)
	if updated.ExecutionMode != w.ExecutionFullAccess || updated.PermissionEpoch != settings.PermissionEpoch+1 {
		t.Fatalf("global permission change = %+v", updated)
	}
	api.call("PATCH", "/api/v1/settings", SettingsInput{ExecutionMode: ptr(w.ExecutionControlled)}, map[string]string{"If-Match": fmt.Sprint(settings.Revision)}, 412, nil)
}

func TestProjectInboxHTTPReceiptAndEvidenceBoundary(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(w.Workspace{ID: "project", Name: "项目", Revision: 1}); err != nil {
			return err
		}
		return tx.SaveThread(w.Thread{ID: "thread", WorkspaceID: "project", Revision: 1, UpdatedAt: w.Now(), CoordinatorID: "owner"})
	}); err != nil {
		t.Fatal(err)
	}
	api := newDeliveryAPI(t, root, db, nil)
	var created project.TaskView
	api.call("POST", "/api/v1/workspaces/project/tasks", project.CreateTaskInput{
		ThreadID: "thread", OwnerMemberID: "owner", Goal: "保存持久目标",
		SourceMessageID: "message", AcceptedBy: "user",
	}, nil, 200, &created)
	input := project.SubmitInboxInput{SourceMessageID: "inbox-message", ExpectedRevision: 1, Body: "补充验收条件", IdempotencyKey: "inbox-key"}
	var first, replay w.InboxItem
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/inbox", input, nil, 200, &first)
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/inbox", input, nil, 200, &replay)
	if first.ID != replay.ID || first.Status != "pending" {
		t.Fatalf("HTTP inbox replay = %+v / %+v", first, replay)
	}
	var listed InboxList
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/inbox", nil, nil, 200, &listed)
	if len(listed.Items) != 1 {
		t.Fatalf("persistent inbox = %+v", listed.Items)
	}
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/completions", project.RecordCompletionInput{
		EvaluationGeneration: 1, InputSnapshotDigest: created.Revision.Digest,
		SubjectDigest: "candidate", CandidateRefs: []string{"candidate@1"},
		Results:       []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{"test@1"}}},
		JudgeMemberID: "reviewer", EvidenceProducer: "host_observed",
	}, nil, 400, nil)
}

// setGlobalMode changes the one global permission policy through its only
// write path. Tests must not use a retired per-workspace route.
func setGlobalMode(t *testing.T, api *deliveryAPI, mode string) {
	t.Helper()
	var settings w.Settings
	api.call("GET", "/api/v1/settings", nil, nil, 200, &settings)
	api.call("PATCH", "/api/v1/settings", SettingsInput{ExecutionMode: &mode}, map[string]string{"If-Match": fmt.Sprint(settings.Revision)}, 200, nil)
}
