package project

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestWakeEventArrivingBeforeYieldIsNotLost(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, _, created, original := runningTaskFixture(t, db)
	early, err := service.WakeTask(t.Context(), "host", "early-1", "observed-before-yield")
	if err != nil || early.ID != "" {
		t.Fatalf("early event should only be recorded: %+v, %v", early, err)
	}
	wakeup, continuation, err := service.YieldRun(t.Context(), original.ID, "event", "cursor", "host", "early-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if continuation.ID == "" || continuation.ContinuedFromRun != original.ID || wakeup.Status != "consumed" || wakeup.ContinuationRunID != continuation.ID {
		t.Fatalf("early observation did not consume exact wait: wakeup=%+v run=%+v", wakeup, continuation)
	}
	replay, err := service.WakeTask(t.Context(), "host", "early-1", "a-different-replay-payload")
	if err != nil || replay.ID != continuation.ID {
		t.Fatalf("early event replay = %+v, %v", replay, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		task, err := tx.Task(created.Task.ID)
		if err != nil || task.Status != "ready" {
			t.Fatalf("task was not made ready: %+v, %v", task, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWakeDueUsesDurableDeadlineAndDoesNotDuplicate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, th, _, original := runningTaskFixture(t, db)
	if _, immediate, err := service.YieldRun(t.Context(), original.ID, "at:2026-11-04T12:00:00Z", "", "clock", "deadline-1", ""); err != nil || immediate.ID != "" {
		t.Fatalf("deadline yield = %+v, %v", immediate, err)
	}
	before, err := service.WakeDue(t.Context(), "2026-11-04T11:59:59Z")
	if err != nil || len(before) != 0 {
		t.Fatalf("deadline fired early: %+v, %v", before, err)
	}
	due, err := service.WakeDue(t.Context(), "2026-11-04T12:00:00Z")
	if err != nil || len(due) != 1 || due[0].ContinuedFromRun != original.ID {
		t.Fatalf("deadline continuation = %+v, %v", due, err)
	}
	replayed, err := service.WakeDue(t.Context(), "2026-11-04T12:00:01Z")
	if err != nil || len(replayed) != 0 {
		t.Fatalf("consumed deadline was pending again: %+v, %v", replayed, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		runs, err := tx.Runs(th.ID)
		if err != nil || len(runs) != 2 {
			t.Fatalf("deadline duplicated runs: %+v, %v", runs, err)
		}
		stored, err := tx.WakeupBySourceEvent("clock", "deadline-1")
		if err != nil || stored.Status != "consumed" || stored.SatisfiedBy != "deadline:2026-11-04T12:00:00Z" {
			t.Fatalf("deadline observation = %+v, %v", stored, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestYieldRejectsConcurrentBranches(t *testing.T) {
	db, service, _, original := setupRunningForYield(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveStep(w.Step{ID: "second-running-branch", RunID: original.ID, AgentID: "owner", Status: "running"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.YieldRun(t.Context(), original.ID, "event", "", "host", "concurrent", ""); !IsCode(err, "conflict") {
		t.Fatalf("concurrent yield = %v", err)
	}
}

func setupRunningForYield(t *testing.T) (*store.Store, *Service, w.Thread, w.Run) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	service, th, _, run := runningTaskFixture(t, db)
	return db, service, th, run
}
