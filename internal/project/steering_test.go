package project

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestInboxPersistsIdempotentReceipt(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	input := SubmitInboxInput{
		SourceMessageID: "message-inbox", ExpectedRevision: 1, Kind: "supplement",
		Body: "保留 24 小时，不能新增 Redis", ApplicationPolicy: "next_boundary", IdempotencyKey: "inbox-request-1",
	}
	first, err := service.SubmitInbox(t.Context(), created.Task.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.SubmitInbox(t.Context(), created.Task.ID, input)
	if err != nil || replayed != first {
		t.Fatalf("inbox replay = %+v, first = %+v, err = %v", replayed, first, err)
	}
	conflicting := input
	conflicting.Body = "不同请求"
	if _, err := service.SubmitInbox(t.Context(), created.Task.ID, conflicting); !IsCode(err, "conflict") {
		t.Fatalf("same key with another body = %v", err)
	}
	stale := input
	stale.IdempotencyKey = "stale-request"
	stale.ExpectedRevision = 2
	if _, err := service.SubmitInbox(t.Context(), created.Task.ID, stale); !IsCode(err, "revision_conflict") {
		t.Fatalf("stale inbox revision = %v", err)
	}
}

func TestInboxRestartAndControlRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ws := w.Workspace{ID: "restart-ws", Name: "重启项目", Revision: 1, CreatedAt: w.Now()}
	th := w.Thread{ID: "restart-thread", WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now(), CoordinatorID: "owner"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	service := New(db)
	created, err := service.CreateTask(t.Context(), CreateTaskInput{WorkspaceID: ws.ID, ThreadID: th.ID, OwnerMemberID: "owner", Goal: "跨重启继续", SourceMessageID: "root", AcceptedBy: "user"})
	if err != nil {
		t.Fatal(err)
	}
	input := SubmitInboxInput{SourceMessageID: "supplement", ExpectedRevision: 1, Body: "新增约束", IdempotencyKey: "restart-inbox"}
	if _, err := service.SubmitInbox(t.Context(), created.Task.ID, input); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service = New(db)
	items, err := service.Inbox(t.Context(), created.Task.ID)
	if err != nil || len(items) != 1 || items[0].Status != "pending" {
		t.Fatalf("restarted inbox = %+v, %v", items, err)
	}
	if _, err := service.TaskAction(t.Context(), created.Task.ID, "pause", "pause-before-inbox"); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "paused-inbox"
	if _, err := service.SubmitInbox(t.Context(), created.Task.ID, input); !IsCode(err, "authority_revoked") {
		t.Fatalf("paused task inbox = %v", err)
	}
}

func TestHundredthRevisionUsesOnlyCurrentRequirements(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	for revision := 1; revision < 100; revision++ {
		if _, err := service.ReviseTask(t.Context(), created.Task.ID, revision, ReviseTaskInput{
			Goal: "当前有效目标", NonGoals: []string{"旧目标不得复活"}, SourceMessageID: "revision", AcceptedBy: "user",
			Acceptance: created.Revision.Acceptance, AllowedActions: created.Revision.AllowedActions,
		}); err != nil {
			t.Fatalf("revision %d: %v", revision+1, err)
		}
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.CurrentRevision != 100 || view.Revision.Goal != "当前有效目标" {
		t.Fatalf("current requirements = %+v", view)
	}
	revisions, err := service.Revisions(t.Context(), created.Task.ID)
	if err != nil || len(revisions) != 100 || revisions[0].Revision != 100 || revisions[99].Goal != created.Revision.Goal {
		t.Fatalf("immutable revision history was lost: len=%d err=%v", len(revisions), err)
	}
	otherThread := w.Thread{ID: "another-session", WorkspaceID: th.WorkspaceID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(otherThread) }); err != nil {
		t.Fatal(err)
	}
	// A new service/session resolves the same durable objective without a native session.
	crossSession, err := New(db).Task(t.Context(), created.Task.ID)
	if err != nil || crossSession.Revision.Digest != view.Revision.Digest {
		t.Fatalf("cross-session task = %+v, %v", crossSession, err)
	}
}

func TestExternalCompletionCannotForgeHostObservation(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	_, err := service.RecordCompletion(t.Context(), created.Task.ID, RecordCompletionInput{
		EvaluationGeneration: 1, InputSnapshotDigest: created.Revision.Digest,
		SubjectDigest: "candidate", CandidateRefs: []string{"candidate@1"},
		Results:       []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{"test@1"}}},
		JudgeMemberID: "reviewer", EvidenceProducer: "host_observed",
	})
	if !IsCode(err, "invalid_request") {
		t.Fatalf("forged host observation = %v", err)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil || view.Task.Status == "completed" {
		t.Fatalf("forged observation completed task: %+v, %v", view.Task, err)
	}
}
