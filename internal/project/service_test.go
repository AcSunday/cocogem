package project

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func setup(t *testing.T) (*store.Store, *Service, w.Thread) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ws := w.Workspace{ID: "ws", Name: "项目", Revision: 1, CreatedAt: w.Now(), ExecutionMode: w.ExecutionControlled}
	th := w.Thread{ID: "th", WorkspaceID: ws.ID, Title: "任务", CoordinatorID: "owner", AgentIDs: []string{"owner"}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	return db, New(db), th
}

func create(t *testing.T, service *Service, th w.Thread) TaskView {
	t.Helper()
	view, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: th.ID, OwnerMemberID: "owner",
		Goal: "实现并验证幂等创建", AllowedActions: []string{"read", "write", "run_test"},
		SourceMessageID: "message-1", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestTaskRevisionKeepsHistoryAndRejectsStaleCompletion(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	if created.Budget.Policy != w.DefaultBudgetPolicy() {
		t.Fatalf("budget defaults = %+v", created.Budget.Policy)
	}
	revised, err := service.ReviseTask(t.Context(), created.Task.ID, 1, ReviseTaskInput{
		Goal: "实现幂等创建且不得新增 Redis", Scope: []string{"orders"},
		Acceptance: created.Revision.Acceptance, AllowedActions: created.Revision.AllowedActions,
		SourceMessageID: "message-20", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Task.CurrentRevision != 2 || revised.Task.EvaluationGeneration != 2 {
		t.Fatalf("revision projection = %+v", revised.Task)
	}
	old, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Revision.Digest == created.Revision.Digest {
		t.Fatal("goal digest did not change")
	}
	_, err = service.RecordCompletion(t.Context(), created.Task.ID, RecordCompletionInput{
		EvaluationGeneration: 1, InputSnapshotDigest: created.Revision.Digest,
		Results:       []w.CriterionResult{{CriterionID: "outcome", Result: "passed"}},
		JudgeMemberID: "reviewer", EvidenceProducer: "host_observed",
	})
	if !IsCode(err, "evidence_stale") {
		t.Fatalf("stale completion error = %v", err)
	}
}

func TestCompletionIsSeparateFromRunDelivery(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	if created.Task.Status == "completed" {
		t.Fatal("task was completed by creation")
	}
	record, err := service.RecordHostCompletion(t.Context(), created.Task.ID, RecordCompletionInput{
		EvaluationGeneration: 1, InputSnapshotDigest: created.Revision.Digest,
		SubjectDigest: "tree-v1", CandidateRefs: []string{"candidate@1"},
		Results:       []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{"test@1"}}},
		JudgeMemberID: "reviewer", EvidenceProducer: "host_observed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Validity != "current" {
		t.Fatalf("record validity = %q", record.Validity)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.Status != "completed" || view.Task.EvaluationStatus != "current" {
		t.Fatalf("completion projection = %+v", view.Task)
	}
}

func TestBudgetSharedAcrossRevisionAndModeChanges(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	account, err := service.ReserveRun(t.Context(), created.Task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedRuns != 1 || account.RemainingContinuations() != 19 {
		t.Fatalf("reservation = %+v", account)
	}
	if _, err := service.SetExecutionMode(t.Context(), th.WorkspaceID, w.ExecutionFullAccess, "message-mode", 1); err != nil {
		t.Fatal(err)
	}
	lower := account.Policy
	lower.AutoContinuations = 1
	adjusted, err := service.AdjustBudget(t.Context(), created.Task.ID, account.Revision, lower)
	if err != nil {
		t.Fatal(err)
	}
	if adjusted.ReservedRuns != 1 || adjusted.RemainingContinuations() != 0 {
		t.Fatalf("budget adjustment erased reservation: %+v", adjusted)
	}
	_, err = service.ReserveRun(t.Context(), created.Task.ID, true)
	if !IsCode(err, "budget_exhausted") {
		t.Fatalf("exhaustion error = %v", err)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.Status != "waiting" || view.Task.WaitingReason != "budget" {
		t.Fatalf("waiting projection = %+v", view.Task)
	}
}

func TestTaskActionsOnlyControlOwnedTree(t *testing.T) {
	db, service, th := setup(t)
	root := create(t, service, th)
	otherThread := w.Thread{ID: "other", WorkspaceID: th.WorkspaceID, Title: "独立", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(otherThread) }); err != nil {
		t.Fatal(err)
	}
	independent := create(t, service, otherThread)
	if _, err := service.TaskAction(t.Context(), root.Task.ID, "cancel", "cancel-1"); err != nil {
		t.Fatal(err)
	}
	other, err := service.Task(t.Context(), independent.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.Task.Status != "ready" {
		t.Fatalf("independent task was canceled: %+v", other.Task)
	}
	_, err = service.ReserveRun(t.Context(), root.Task.ID, true)
	if !IsCode(err, "authority_revoked") {
		t.Fatalf("canceled root admitted work: %v", err)
	}
}

func TestTaskActionReceiptAndChildControlBoundary(t *testing.T) {
	db, service, th := setup(t)
	root := create(t, service, th)
	childThread := w.Thread{ID: "child-thread", WorkspaceID: th.WorkspaceID, Internal: true, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(childThread) }); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ParentID: root.Task.ID, ThreadID: childThread.ID,
		OwnerMemberID: "coder", Goal: "实现子任务", SourceMessageID: "delegation-1", AcceptedBy: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.TaskAction(t.Context(), child.Task.ID, "pause", "pause-child")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.TaskAction(t.Context(), child.Task.ID, "pause", "pause-child")
	if err != nil || replayed.RootControlEpoch != paused.RootControlEpoch {
		t.Fatalf("action replay advanced epoch: first=%+v replay=%+v err=%v", paused, replayed, err)
	}
	if _, err := service.TaskAction(t.Context(), child.Task.ID, "cancel", "pause-child"); !IsCode(err, "conflict") {
		t.Fatalf("reused action key error = %v", err)
	}
	parent, err := service.Task(t.Context(), root.Task.ID)
	if err != nil || parent.Task.Status != "ready" {
		t.Fatalf("child pause changed its parent: %+v err=%v", parent.Task, err)
	}
	if canceled, err := service.TaskAction(t.Context(), root.Task.ID, "cancel", "cancel-root"); err != nil || canceled.Status != "canceled" {
		t.Fatalf("root cancellation = %+v, %v", canceled, err)
	}
	descendant, err := service.Task(t.Context(), child.Task.ID)
	if err != nil || descendant.Task.Status != "canceled" {
		t.Fatalf("owned descendant was not canceled: %+v, %v", descendant.Task, err)
	}
}
