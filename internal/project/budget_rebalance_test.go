package project

import (
	"fmt"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestBudgetReductionReallocatesQueuedReservations(t *testing.T) {
	db, service, rootThread := setup(t)
	root := create(t, service, rootThread)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(root.Budget.ID)
		if err != nil {
			return err
		}
		account.UsedContinuations = 5
		account.Revision++
		return tx.SaveBudgetAccount(account, account.Revision-1)
	}); err != nil {
		t.Fatal(err)
	}
	children := make([]TaskView, 5)
	for i := range children {
		thread := w.Thread{ID: fmt.Sprintf("budget-child-thread-%d", i), WorkspaceID: rootThread.WorkspaceID, CoordinatorID: "owner", Internal: true, Revision: 1, UpdatedAt: w.Now()}
		if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(thread) }); err != nil {
			t.Fatal(err)
		}
		child, err := service.CreateTask(t.Context(), CreateTaskInput{
			WorkspaceID: rootThread.WorkspaceID, ParentID: root.Task.ID, ThreadID: thread.ID, OwnerMemberID: "owner",
			Goal: fmt.Sprintf("queued child %d", i), AllowedActions: []string{"read"}, SourceMessageID: "budget-create", AcceptedBy: "user", Priority: i,
		})
		if err != nil {
			t.Fatal(err)
		}
		children[i] = child
		run := w.Run{ID: fmt.Sprintf("budget-rebalance-run-%d", i), WorkspaceID: rootThread.WorkspaceID, ThreadID: thread.ID, TaskID: child.Task.ID, TaskRevision: 1, Status: "queued", CreatedAt: fmt.Sprintf("2026-11-04T00:00:0%dZ", i), AutomaticContinuation: true}
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			if err := tx.ReserveTaskRun(&run); err != nil {
				return err
			}
			return tx.InsertRun(run, run.ID, run.ID+"-digest")
		}); err != nil {
			t.Fatal(err)
		}
	}
	account, err := service.Task(t.Context(), root.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy := account.Budget.Policy
	policy.AutoContinuations = 8
	adjusted, err := service.AdjustBudget(t.Context(), root.Task.ID, account.Budget.Revision, policy)
	if err != nil || adjusted.UsedContinuations != 5 || adjusted.ReservedRuns != 3 || adjusted.RemainingContinuations() != 0 {
		t.Fatalf("reduced account = %+v, %v", adjusted, err)
	}
	for i, child := range children {
		view, err := service.Task(t.Context(), child.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i >= 2 && view.Task.Status != "ready" {
			t.Fatalf("priority child %d did not retain capacity: %+v", i, view)
		}
		if i < 2 && (view.Task.Status != "waiting" || view.Task.WaitingReason != "budget") {
			t.Fatalf("lower-priority child %d did not wait: %+v", i, view.Task)
		}
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		for i := range children {
			run, err := tx.Run(fmt.Sprintf("budget-rebalance-run-%d", i))
			if err != nil {
				return err
			}
			if run.ReservationReleaseReason != "reservation_released_due_to_policy" {
				t.Fatalf("missing release audit on %s: %+v", run.ID, run)
			}
			events, err := tx.Events(run.ID, 0)
			if err != nil || len(events) != 1 || events[0].Type != "reservation_released_due_to_policy" {
				t.Fatalf("release events for %s = %+v, %v", run.ID, events, err)
			}
			if run.BudgetReserved != (i >= 2) {
				t.Fatalf("reassigned reservation for %s = %+v", run.ID, run)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	policy.AutoContinuations = 10
	expanded, err := service.AdjustBudget(t.Context(), root.Task.ID, adjusted.Revision, policy)
	if err != nil || expanded.ReservedRuns != 5 {
		t.Fatalf("expanded account = %+v, %v", expanded, err)
	}
	for _, child := range children {
		view, err := service.Task(t.Context(), child.Task.ID)
		if err != nil || view.Task.Status != "ready" {
			t.Fatalf("sibling was not awakened by expansion: %+v, %v", view.Task, err)
		}
	}
}
