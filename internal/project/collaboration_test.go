package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestA2AIdempotencyAndDisclosureBoundary(t *testing.T) {
	db, service, th := setup(t)
	sender := create(t, service, th)
	targetThread := w.Thread{ID: "a2a-target-thread", WorkspaceID: th.WorkspaceID, CoordinatorID: "owner", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(targetThread) }); err != nil {
		t.Fatal(err)
	}
	target, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: targetThread.ID, OwnerMemberID: "owner",
		Goal: "independent target", SourceMessageID: "target-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: "a2a-source-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: sender.Task.ID, Status: "succeeded", CreatedAt: w.Now()}
	privateSource := w.Source{ID: "a2a-private-source", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: run.ID, Kind: "file", Content: "private evidence", Visibility: "private"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, "a2a-source-key", "a2a-source-hash"); err != nil {
			return err
		}
		return tx.SaveSource(privateSource)
	}); err != nil {
		t.Fatal(err)
	}
	input := SendTaskMessageInput{
		SenderTaskID: sender.Task.ID, TargetTaskID: target.Task.ID, Kind: "question",
		Body: "what does the evidence show?", RequestID: "a2a-request-1", ContextRefs: []string{privateSource.ID},
	}
	if _, err := service.SendTaskMessage(t.Context(), input); !IsCode(err, "disclosure_denied") {
		t.Fatalf("private A2A body was allowed to cross scopes: %v", err)
	}
	childThread := w.Thread{ID: "a2a-child-thread", WorkspaceID: th.WorkspaceID, CoordinatorID: "owner", Internal: true, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(childThread) }); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ParentID: sender.Task.ID, ThreadID: childThread.ID,
		OwnerMemberID: "owner", Goal: "child target", SourceMessageID: "child-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	input.ContextRefs = nil
	input.TargetTaskID = child.Task.ID
	first, err := service.SendTaskMessage(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.SendTaskMessage(t.Context(), input)
	if err != nil || replay.Message.ID != first.Message.ID || len(replay.Receipts) != 1 || replay.Receipts[0].Status != "queued" {
		t.Fatalf("A2A replay = %+v, %v", replay, err)
	}
	input.Body = "changed request"
	if _, err := service.SendTaskMessage(t.Context(), input); !IsCode(err, "conflict") {
		t.Fatalf("A2A request ID accepted different content: %v", err)
	}
	receipt, err := service.RecordReceipt(t.Context(), first.Message.ID, "presented", "", "")
	if err != nil || receipt.Status != "presentation_unknown" {
		t.Fatalf("unproven presentation = %+v, %v", receipt, err)
	}
	receipt, err = service.RecordReceipt(t.Context(), first.Message.ID, "handled", "linked_response", "answer-message-1")
	if err != nil || receipt.Status != "handled" || receipt.ResponseRef != "answer-message-1" {
		t.Fatalf("handled receipt = %+v, %v", receipt, err)
	}
}

func TestA2ABodyInheritsOnlyVerifiableCurrentExposure(t *testing.T) {
	db, service, th := setup(t)
	sender := create(t, service, th)
	targetThread := w.Thread{ID: "derived-target-thread", WorkspaceID: th.WorkspaceID, CoordinatorID: "owner", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(targetThread) }); err != nil {
		t.Fatal(err)
	}
	target, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: targetThread.ID, OwnerMemberID: "owner",
		Goal: "independent recipient", SourceMessageID: "derived-target-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: "derived-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: sender.Task.ID, TaskRevision: 1, Status: "succeeded", CreatedAt: w.Now()}
	privateSource := w.Source{ID: "derived-private", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: run.ID, Kind: "file", Content: "secret", Visibility: "private"}
	session := w.ExecutionSession{
		ID: "derived-session", TaskID: sender.Task.ID, MemberID: "owner", Status: "fresh_only",
		ExposedRefs: []string{"source_id:derived-private:digest"}, CreatedAt: w.Now(), UpdatedAt: w.Now(),
	}
	session.ExposureDigest = exposureRefsDigest(session.ExposedRefs)
	attempt := w.Attempt{ID: "derived-attempt", RunID: run.ID, StepID: "derived-step", Number: 1, Status: "succeeded", ExecutionSessionRef: session.ID, StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, "derived-run-key", "derived-run-digest"); err != nil {
			return err
		}
		if err := tx.SaveSource(privateSource); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: attempt.StepID, RunID: run.ID, AgentID: "owner", Status: "succeeded"}); err != nil {
			return err
		}
		if err := tx.SaveSession(session); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	input := SendTaskMessageInput{
		SenderTaskID: sender.Task.ID, SenderAttemptID: attempt.ID, TargetTaskID: target.Task.ID,
		Kind: "result", Body: "a derived answer without explicit references", RequestID: "derived-private-request",
	}
	if _, err := service.SendTaskMessage(t.Context(), input); !IsCode(err, "disclosure_denied") {
		t.Fatalf("private derived body crossed tasks: %v", err)
	}
	input.SenderAttemptID = "forged-attempt"
	if _, err := service.SendTaskMessage(t.Context(), input); !IsCode(err, "disclosure_denied") {
		t.Fatalf("forged attempt was accepted: %v", err)
	}
	input.SenderAttemptID = attempt.ID
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Session(session.ID)
		if err != nil {
			return err
		}
		current.VisibilityEpoch = 1
		return tx.SaveSession(current)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendTaskMessage(t.Context(), input); !IsCode(err, "authority_revoked") {
		t.Fatalf("stale visibility epoch was accepted: %v", err)
	}
	if _, err := service.ReviseTask(t.Context(), sender.Task.ID, 1, ReviseTaskInput{
		Goal: "new revision", Acceptance: sender.Revision.Acceptance, AllowedActions: sender.Revision.AllowedActions,
		SourceMessageID: "derived-new-revision", AcceptedBy: "user",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendTaskMessage(t.Context(), input); !IsCode(err, "disclosure_denied") {
		t.Fatalf("old task revision attempt was accepted: %v", err)
	}
}
