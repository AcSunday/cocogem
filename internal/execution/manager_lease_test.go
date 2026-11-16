package execution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func leaseFixture(t *testing.T) (*store.Store, string, w.Run) {
	t.Helper()
	db, root, run := executionFixture(t)
	return db, root, run
}

// TestActiveCandidateIsUniquePerScopeAndRoot is the A02 regression: a plain
// conversation and a durable task may each hold a candidate worktree on the
// same bound project, while one owning scope never forks a second live
// candidate for one root.
func TestActiveCandidateIsUniquePerScopeAndRoot(t *testing.T) {
	db, root, run := leaseFixture(t)
	now := time.Now().UTC()
	base := w.WorkspaceExecution{
		WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, CanonicalRoot: root,
		LeaseOwner: "one", LeaseEpoch: 1,
		LeaseExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), Status: "active",
	}
	err := db.Write(t.Context(), func(tx *store.Tx) error {
		taskScoped := base
		taskScoped.ID, taskScoped.TaskID = "task-scoped", run.TaskID
		if err := tx.AcquireExecution(taskScoped); err != nil {
			return err
		}
		threadScoped := base
		threadScoped.ID = "thread-scoped"
		if err := tx.AcquireExecution(threadScoped); err != nil {
			t.Fatalf("thread-scoped candidate rejected: %v", err)
		}
		taskDuplicate := taskScoped
		taskDuplicate.ID = "task-duplicate"
		if err := tx.AcquireExecution(taskDuplicate); !errors.Is(err, w.ErrConflict) {
			t.Fatalf("same-task candidate conflict = %v, want ErrConflict", err)
		}
		threadDuplicate := threadScoped
		threadDuplicate.ID = "thread-duplicate"
		if err := tx.AcquireExecution(threadDuplicate); !errors.Is(err, w.ErrConflict) {
			t.Fatalf("same-thread candidate conflict = %v, want ErrConflict", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPrepareRejectsASecondLiveCandidateForOneScope keeps the lease rule that a
// scope cannot silently take over an unverified owner, and that the same owner
// continues the existing candidate instead of forking a new one.
func TestPrepareRejectsASecondLiveCandidateForOneScope(t *testing.T) {
	db, root, run := leaseFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	first, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	// A different process owner observes the live candidate as unverified.
	other := New(db, filepath.Join(t.TempDir(), "executions"), "other-owner")
	if _, err := other.Prepare(t.Context(), run); !errors.Is(err, w.ErrConflict) || !strings.Contains(err.Error(), "quarantine") {
		t.Fatalf("second live candidate = %v", err)
	}
	again, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Path != first.Path {
		t.Fatalf("continuity broke: first=%s/%s again=%s/%s", first.ID, first.Path, again.ID, again.Path)
	}
	if _, err := os.Stat(filepath.Join(root, "main.txt")); err != nil {
		t.Fatalf("source file disappeared: %v", err)
	}
}

func TestFileCopyRejectsSourceMutation(t *testing.T) {
	db, root, run := leaseFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	canonicalRoot, err := canonical(root)
	if err != nil {
		t.Fatal(err)
	}
	realDigest := manager.digestTree
	sourceReads := 0
	manager.digestTree = func(path string) (string, error) {
		if path == canonicalRoot {
			sourceReads++
			if sourceReads == 2 {
				if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("changed during copy"), 0600); err != nil {
					return "", err
				}
			}
		}
		return realDigest(path)
	}
	candidate, err := manager.Prepare(t.Context(), run)
	if err == nil || !strings.Contains(err.Error(), "source tree changed during isolated copy") {
		t.Fatalf("unstable source baseline = %+v, %v", candidate, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Execution(candidate.ID)
		if err != nil {
			return err
		}
		if current.Status != "failed" {
			t.Fatalf("failed copy status = %q", current.Status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFinishDigestFailureMarksUncertainOutsideTransaction(t *testing.T) {
	db, _, run := leaseFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	manager.digestTree = func(string) (string, error) {
		if err := db.Read(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Execution(candidate.ID)
			return err
		}); err != nil {
			return "", err
		}
		return "", errors.New("injected digest failure")
	}
	if err := manager.Finish(t.Context(), candidate.ID); err == nil || !strings.Contains(err.Error(), "injected digest failure") {
		t.Fatalf("digest failure = %v", err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Execution(candidate.ID)
		if err != nil {
			return err
		}
		if current.Status != "uncertain" {
			t.Fatalf("execution status = %q", current.Status)
		}
		if _, err := tx.EffectByKey(run.TaskID, "execution:"+candidate.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("uncertain execution gained applied effect: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatStopsAndRejectsStaleEpoch(t *testing.T) {
	db, _, run := leaseFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	manager.heartbeatInterval = 5 * time.Millisecond
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	heartbeatErrors := make(chan error, 1)
	stop, err := manager.Heartbeat(t.Context(), candidate.ID, func(err error) {
		select {
		case heartbeatErrors <- err:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	var current w.WorkspaceExecution
	deadline := time.Now().Add(2 * time.Second)
	for current.LeaseEpoch <= candidate.LeaseEpoch && time.Now().Before(deadline) {
		if err := db.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			current, err = tx.Execution(candidate.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if current.LeaseEpoch <= candidate.LeaseEpoch {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if current.LeaseEpoch <= candidate.LeaseEpoch {
		t.Fatal("heartbeat did not renew the lease")
	}
	if _, err := manager.Renew(t.Context(), candidate.ID, candidate.LeaseEpoch); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("stale epoch renewed lease: %v", err)
	}
	stale := candidate
	stale.Status = "completed"
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveExecutionEpoch(stale, stale.LeaseEpoch)
	}); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("stale epoch wrote execution: %v", err)
	}
	stop()
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Execution(candidate.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stoppedEpoch := current.LeaseEpoch
	time.Sleep(30 * time.Millisecond)
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Execution(candidate.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if current.LeaseEpoch != stoppedEpoch {
		t.Fatalf("heartbeat continued after stop: epoch %d -> %d", stoppedEpoch, current.LeaseEpoch)
	}
	select {
	case err := <-heartbeatErrors:
		t.Fatalf("heartbeat error: %v", err)
	default:
	}
	if err := manager.Finish(t.Context(), candidate.ID); err != nil {
		t.Fatal(err)
	}
}
