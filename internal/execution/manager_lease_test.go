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

func TestCanonicalRootLeaseHandlesRootAndParentChildPaths(t *testing.T) {
	tests := []struct {
		name         string
		existing     string
		candidate    string
		wantConflict bool
	}{
		{name: "root contains child", existing: "/", candidate: "/tmp/cocogem-lease-child", wantConflict: true},
		{name: "child conflicts with new root", existing: "/tmp/cocogem-lease-child", candidate: "/", wantConflict: true},
		{name: "root exact match", existing: "/", candidate: "/", wantConflict: true},
		{name: "parent contains child", existing: "/tmp/cocogem-lease", candidate: "/tmp/cocogem-lease/child", wantConflict: true},
		{name: "child conflicts with parent", existing: "/tmp/cocogem-lease/child", candidate: "/tmp/cocogem-lease", wantConflict: true},
		{name: "path prefix is not a child", existing: "/tmp/cocogem-lease", candidate: "/tmp/cocogem-lease-other", wantConflict: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _, run := executionFixture(t)
			err := db.Write(t.Context(), func(tx *store.Tx) error {
				now := time.Now().UTC()
				existing := w.WorkspaceExecution{
					ID: "existing", WorkspaceID: run.WorkspaceID, TaskID: run.TaskID,
					CanonicalRoot: tt.existing, LeaseOwner: "one", LeaseEpoch: 1,
					LeaseExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), Status: "active",
				}
				if err := tx.AcquireExecution(existing); err != nil {
					return err
				}
				candidate := existing
				candidate.ID = "candidate"
				candidate.CanonicalRoot = tt.candidate
				candidate.LeaseOwner = "two"
				return tx.AcquireExecution(candidate)
			})
			if tt.wantConflict && !errors.Is(err, w.ErrConflict) {
				t.Fatalf("lease conflict = %v, want ErrConflict", err)
			}
			if !tt.wantConflict && err != nil {
				t.Fatalf("independent roots conflicted: %v", err)
			}
		})
	}
}

func TestPrepareRejectsChangedRootForActiveTask(t *testing.T) {
	db, root, run := executionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	if _, err := manager.Prepare(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	otherRoot := filepath.Join(t.TempDir(), "other-source")
	if err := os.Mkdir(otherRoot, 0700); err != nil {
		t.Fatal(err)
	}
	changed := run
	changed.ID = "changed-root-run"
	changed.Workspace.Root = otherRoot
	if root == otherRoot {
		t.Fatal("test roots must differ")
	}
	if _, err := manager.Prepare(t.Context(), changed); !errors.Is(err, w.ErrConflict) || !strings.Contains(err.Error(), "another source root") {
		t.Fatalf("changed source root was not rejected: %v", err)
	}
}

func TestFileCopyRejectsSourceMutation(t *testing.T) {
	db, root, run := executionFixture(t)
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
	db, _, run := executionFixture(t)
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
	db, _, run := executionFixture(t)
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
