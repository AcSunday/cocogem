package execution

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func executionFixture(t *testing.T) (*store.Store, string, w.Run) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := filepath.Join(dir, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	ws := w.Workspace{ID: "ws", Root: root, Revision: 1, CreatedAt: w.Now()}
	th := w.Thread{ID: "th", WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		task := w.Task{ID: "task", WorkspaceID: ws.ID, ThreadID: th.ID, RootTaskID: "task", BudgetAccountID: "budget", CollaborationScopeID: "scope", CurrentRevision: 1, Status: "ready", CreatedAt: w.Now(), UpdatedAt: w.Now()}
		if err := tx.InsertTask(task); err != nil {
			return err
		}
		revision := w.TaskRevision{TaskID: task.ID, Revision: 1, Goal: "develop", Digest: "digest", CreatedAt: w.Now()}
		if err := tx.InsertTaskRevision(revision); err != nil {
			return err
		}
		return tx.InsertBudgetAccount(w.BudgetAccount{ID: "budget", RootTaskID: task.ID, Revision: 1, Policy: w.DefaultBudgetPolicy(), UpdatedAt: w.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: "run", WorkspaceID: ws.ID, ThreadID: th.ID, TaskID: "task", Workspace: ws, WriteBranch: true}
	return db, root, run
}

func TestFileCopyIsIndependentAndSkipsSecrets(t *testing.T) {
	db, root, run := executionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Kind != "file_copy" || candidate.Path == root {
		t.Fatalf("candidate = %+v", candidate)
	}
	if _, err := os.Stat(filepath.Join(candidate.Path, ".env")); !os.IsNotExist(err) {
		t.Fatalf("secret entered candidate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, "main.txt"))
	if err != nil || string(original) != "baseline" {
		t.Fatalf("source changed: %q, %v", original, err)
	}
	if err := manager.Finish(t.Context(), candidate.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		finished, err := tx.Execution(candidate.ID)
		if err != nil {
			return err
		}
		if finished.Status != "completed" || finished.TreeDigest == candidate.TreeDigest {
			t.Fatalf("tree reconciliation = %+v", finished)
		}
		effect, err := tx.EffectByKey("task", "execution:"+candidate.ID)
		if err != nil || effect.BeforeDigest == effect.AfterDigest || effect.Status != "applied" {
			t.Fatalf("effect = %+v, %v", effect, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestMemberRetryContinuesTheSameCandidate is the A26 regression: a member retry
// on the same scope keeps the exact unapplied candidate instead of copying a new
// one, so the half-finished work of the failing step is still on disk.
func TestMemberRetryContinuesTheSameCandidate(t *testing.T) {
	db, root, run := executionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	first, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "main.txt"), []byte("completed author's change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	retry := run
	retry.ID, retry.RetryOfStepID, retry.ContinuedFromRun = "retry", "failed-reviewer", run.ID
	second, err := manager.Prepare(t.Context(), retry)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(second.Path, "main.txt"))
	if err != nil || string(data) != "completed author's change" {
		t.Fatalf("retry lost the author's exact candidate: body=%q error=%v", data, err)
	}
	if second.ID != first.ID || second.Path != first.Path {
		t.Fatalf("retry forked a new candidate: first=%s/%s second=%s/%s", first.ID, first.Path, second.ID, second.Path)
	}
	if data, err := os.ReadFile(filepath.Join(root, "main.txt")); err != nil || string(data) != "baseline" {
		t.Fatalf("candidate wrote through to the project: %q %v", data, err)
	}
}

// TestApplyWritesBackAndDiscardKeepsTheProjectClean is the A04 regression.
func TestApplyWritesBackAndDiscardKeepsTheProjectClean(t *testing.T) {
	db, root, run := executionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("applied change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "added.txt"), []byte("new file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish(t.Context(), candidate.ID); err != nil {
		t.Fatal(err)
	}
	applied, err := manager.Apply(t.Context(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != "applied" {
		t.Fatalf("apply status = %q", applied.Status)
	}
	if data, err := os.ReadFile(filepath.Join(root, "main.txt")); err != nil || string(data) != "applied change" {
		t.Fatalf("apply did not write back: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "added.txt")); err != nil || string(data) != "new file" {
		t.Fatalf("apply did not copy the new file: %q %v", data, err)
	}
	if _, err := os.Stat(applied.Path); !os.IsNotExist(err) {
		t.Fatalf("applied candidate directory was not reclaimed: %v", err)
	}

	second, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == applied.ID {
		t.Fatal("applied candidate was reused instead of deriving a fresh one")
	}
	if data, err := os.ReadFile(filepath.Join(second.Path, "main.txt")); err != nil || string(data) != "applied change" {
		t.Fatalf("fresh candidate did not derive from the applied state: %q %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(second.Path, "main.txt"), []byte("discarded change"), 0600); err != nil {
		t.Fatal(err)
	}
	discarded, err := manager.Discard(t.Context(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if discarded.Status != "discarded" {
		t.Fatalf("discard status = %q", discarded.Status)
	}
	if data, err := os.ReadFile(filepath.Join(root, "main.txt")); err != nil || string(data) != "applied change" {
		t.Fatalf("discard changed the project: %q %v", data, err)
	}
	if _, err := os.Stat(discarded.Path); !os.IsNotExist(err) {
		t.Fatalf("discarded candidate directory was not reclaimed: %v", err)
	}
}

// TestApplyRefusesToOverwriteAnExternalEdit is the A04 conflict guard.
func TestApplyRefusesToOverwriteAnExternalEdit(t *testing.T) {
	db, root, run := executionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("candidate change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish(t.Context(), candidate.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("external edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(t.Context(), candidate.ID); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("apply overwrote an external edit: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "main.txt")); err != nil || string(data) != "external edit" {
		t.Fatalf("conflicting apply changed the project: %q %v", data, err)
	}
}

func TestExpiredLeaseRequiresQuarantineNotTakeover(t *testing.T) {
	db, _, run := executionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	now := time.Now()
	manager.now = func() time.Time { return now }
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	otherRun := run
	otherRun.ID = "retry"
	if _, err := manager.Prepare(t.Context(), otherRun); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("expired lease was silently taken over: %+v, %v", candidate, err)
	}
}

func TestConcurrentManagersHaveOneWinner(t *testing.T) {
	db, _, run := executionFixture(t)
	// Both managers share the exact database through separate service owners;
	// SQL acquisition is still exercised under concurrent admission.
	base := t.TempDir()
	managers := []*Manager{New(db, filepath.Join(base, "one"), "one"), New(db, filepath.Join(base, "two"), "two")}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, manager := range managers {
		wg.Add(1)
		go func(m *Manager) {
			defer wg.Done()
			other := run
			other.ID = w.NewID()
			_, err := m.Prepare(t.Context(), other)
			results <- err
		}(manager)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, w.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}
