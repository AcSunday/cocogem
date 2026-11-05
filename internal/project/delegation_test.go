package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestDelegateTaskBindsAttemptBudgetAndRetry(t *testing.T) {
	db, service, th := setup(t)
	parent := create(t, service, th)
	member := w.Agent{ID: "owner", WorkspaceID: th.WorkspaceID, DisplayName: "owner", Handle: "owner", Revision: 1, Tools: []string{"read"}}
	run := w.Run{ID: "delegate-parent-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: parent.Task.ID, TaskRevision: 1, Status: "running", CreatedAt: w.Now(), GrantActions: []string{"read", "collaborate"}}
	session := w.ExecutionSession{ID: "delegate-parent-session", TaskID: parent.Task.ID, MemberID: member.ID, Status: "fresh_only", ExposedRefs: []string{}, CreatedAt: w.Now(), UpdatedAt: w.Now()}
	session.ExposureDigest = exposureRefsDigest(session.ExposedRefs)
	attempt := w.Attempt{ID: "delegate-parent-attempt", RunID: run.ID, StepID: "delegate-parent-step", Number: 1, Status: "running", ExecutionSessionRef: session.ID, StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "delegate-parent", "delegate-parent-digest"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: attempt.StepID, RunID: run.ID, AgentID: member.ID, Status: "running"}); err != nil {
			return err
		}
		if err := tx.SaveSession(session); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	input := DelegateTaskInput{ParentTaskID: parent.Task.ID, ParentAttemptID: attempt.ID, OwnerMemberID: member.ID, Goal: "inspect a bounded child question", RequestID: "delegate-1"}
	first, err := service.DelegateTask(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Task.Task.ParentID != parent.Task.ID || first.Task.Budget.ID != parent.Budget.ID || !first.Run.AutomaticContinuation || !first.Run.BudgetReserved {
		t.Fatalf("delegation scope or budget = %+v", first)
	}
	if first.Task.Budget.ReservedRuns != 1 || first.Task.Budget.UsedContinuations != 0 {
		t.Fatalf("delegation created an independent ledger: %+v", first.Task.Budget)
	}
	replay, err := service.DelegateTask(t.Context(), input)
	if err != nil || replay.Task.Task.ID != first.Task.Task.ID || replay.Run.ID != first.Run.ID {
		t.Fatalf("delegation replay = %+v, %v", replay, err)
	}
	input.Goal = "different work"
	if _, err := service.DelegateTask(t.Context(), input); !IsCode(err, "conflict") {
		t.Fatalf("delegation request collision = %v", err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		runs, err := tx.Runs(first.Task.Task.ThreadID)
		if err != nil || len(runs) != 1 {
			t.Fatalf("delegated runs = %+v, %v", runs, err)
		}
		dependencies, err := tx.Dependencies(th.WorkspaceID)
		if err != nil || len(dependencies) != 1 || dependencies[0].ProducerTaskID != first.Task.Task.ID || dependencies[0].ConsumerTaskID != parent.Task.ID {
			t.Fatalf("return dependency = %+v, %v", dependencies, err)
		}
		account, err := tx.BudgetAccount(parent.Budget.ID)
		if err != nil || account.ReservedRuns != 1 {
			t.Fatalf("shared reservation = %+v, %v", account, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
