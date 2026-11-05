package httpapi

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestProjectPolicyPatchesRequireCurrentRevision(t *testing.T) {
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
	api.call("PATCH", "/api/v1/workspaces/project/execution-policy", ExecutionPolicyInput{Mode: w.ExecutionFullAccess, SourceMessageID: "stale-mode"}, map[string]string{"If-Match": "9"}, 409, nil)
	api.call("PATCH", "/api/v1/workspaces/project/execution-policy", ExecutionPolicyInput{Mode: w.ExecutionFullAccess, SourceMessageID: "current-mode"}, map[string]string{"If-Match": "1"}, 200, nil)
	api.call("PATCH", "/api/v1/workspaces/project/budget-policy", BudgetPolicyInput{Override: &w.BudgetPolicyOverride{}}, map[string]string{"If-Match": "1"}, 409, nil)
	api.call("PATCH", "/api/v1/workspaces/project/budget-policy", BudgetPolicyInput{Override: &w.BudgetPolicyOverride{}}, map[string]string{"If-Match": "2"}, 200, nil)
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
