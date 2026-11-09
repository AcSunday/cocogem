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

func TestMemberRetryCopiesVerifiedCandidateWithoutRepeatingAuthor(t *testing.T) {
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
	run.Status, run.CreatedAt, run.ExecutionRef = "failed", w.Now(), first.ID
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.InsertRun(run, "source", "source") }); err != nil {
		t.Fatal(err)
	}
	retry := run
	retry.ID, retry.RetryOfStepID, retry.ContinuedFromRun = "retry", "failed-reviewer", run.ID
	second, err := manager.Prepare(t.Context(), retry)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(second.Path, "main.txt"))
	if err != nil || string(data) != "completed author's change" || second.Path == first.Path || second.Path == root {
		t.Fatalf("retry lost the author's exact candidate: body=%q candidate=%+v error=%v", data, second, err)
	}
	if err := os.WriteFile(filepath.Join(second.Path, "main.txt"), []byte("next correction"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(first.Path, "main.txt"))
	if err != nil || string(data) != "completed author's change" {
		t.Fatal("retry changed the historical candidate")
	}
	if err := manager.Finish(t.Context(), second.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "main.txt"), []byte("unverified edit"), 0600); err != nil {
		t.Fatal(err)
	}
	retry.ID = "changed-retry"
	if _, err := manager.Prepare(t.Context(), retry); err == nil {
		t.Fatal("retry accepted an altered historical candidate")
	}
}

func TestCanonicalRootLeaseRejectsParentChildAcrossConnections(t *testing.T) {
	db, root, run := executionFixture(t)
	second, err := store.Open(filepath.Join(filepath.Dir(root), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	base := t.TempDir()
	firstManager := New(db, filepath.Join(base, "one"), "owner-one")
	secondManager := New(second, filepath.Join(base, "two"), "owner-two")
	if _, err := firstManager.Prepare(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	child := run
	child.ID = "other-run"
	child.TaskID = "other-task"
	child.Workspace.Root = filepath.Join(root, "child")
	if err := os.Mkdir(child.Workspace.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := second.Write(t.Context(), func(tx *store.Tx) error {
		task := w.Task{ID: child.TaskID, WorkspaceID: child.WorkspaceID, ThreadID: child.ThreadID, RootTaskID: run.TaskID, BudgetAccountID: "budget", CollaborationScopeID: "scope", CurrentRevision: 1, Status: "ready", CreatedAt: w.Now(), UpdatedAt: w.Now()}
		if err := tx.InsertTask(task); err != nil {
			return err
		}
		return tx.InsertTaskRevision(w.TaskRevision{TaskID: task.ID, Revision: 1, Goal: "develop child", Digest: "child-digest", CreatedAt: w.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := secondManager.Prepare(t.Context(), child); !errors.Is(err, w.ErrConflict) {
		_ = second.Read(t.Context(), func(tx *store.Tx) error {
			items, _ := tx.Executions(run.WorkspaceID)
			t.Fatalf("child root lease conflict = %v; existing=%+v child canonical=%q", err, items, child.Workspace.Root)
			return nil
		})
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
