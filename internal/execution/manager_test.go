package execution

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
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

// TestApplyAndDiscardAreLockedWhileARunUsesTheCandidate keeps a user from
// destroying or publishing a worktree that an active run is still writing.
func TestApplyAndDiscardAreLockedWhileARunUsesTheCandidate(t *testing.T) {
	db, root, run := executionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("in progress"), 0600); err != nil {
		t.Fatal(err)
	}
	active := w.Run{ID: "active-run", WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, TaskID: run.TaskID, Status: "running", ExecutionRef: candidate.ID, CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.InsertRun(active, "lock-key", "lock-hash") }); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(t.Context(), candidate.ID); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("apply during a run = %v, want a conflict", err)
	}
	if _, err := manager.Discard(t.Context(), candidate.ID); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("discard during a run = %v, want a conflict", err)
	}
	if _, err := os.Stat(candidate.Path); err != nil {
		t.Fatalf("a locked candidate lost its directory: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "main.txt")); err != nil || string(data) != "baseline" {
		t.Fatalf("a locked apply changed the project: %q %v", data, err)
	}
}

// TestRestartedManagerTakesOverACompletedCandidate covers the restart path: the
// server generates a new lease owner on every start, and a finished candidate
// that is still waiting for Apply must be usable by the next write turn.
func TestRestartedManagerTakesOverACompletedCandidate(t *testing.T) {
	db, root, run := executionFixture(t)
	data := filepath.Join(t.TempDir(), "executions")
	first := New(db, data, "owner-before-restart")
	candidate, err := first.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("first turn"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := first.Finish(t.Context(), candidate.ID); err != nil {
		t.Fatal(err)
	}
	second := New(db, data, "owner-after-restart")
	reused, err := second.Prepare(t.Context(), run)
	if err != nil {
		t.Fatalf("prepare after restart: %v", err)
	}
	if reused.ID != candidate.ID {
		t.Fatalf("restart derived a new candidate %q instead of continuing %q", reused.ID, candidate.ID)
	}
	if reused.LeaseOwner != "owner-after-restart" {
		t.Fatalf("reused candidate lease owner = %q, want the restarted manager", reused.LeaseOwner)
	}
	if err := os.WriteFile(filepath.Join(reused.Path, "main.txt"), []byte("second turn"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := second.Finish(t.Context(), reused.ID); err != nil {
		t.Fatalf("finish after restart: %v", err)
	}
	// The effect record describes the candidate's whole change from its baseline,
	// so a later turn extends it instead of conflicting with the first turn.
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		finished, err := tx.Execution(reused.ID)
		if err != nil {
			return err
		}
		effect, err := tx.EffectByKey("task", "execution:"+reused.ID)
		if err != nil {
			return err
		}
		if effect.BeforeDigest != candidate.TreeDigest {
			t.Fatalf("effect before digest = %q, want the baseline %q", effect.BeforeDigest, candidate.TreeDigest)
		}
		if effect.AfterDigest != finished.TreeDigest || effect.AfterDigest == effect.BeforeDigest {
			t.Fatalf("effect after digest = %q, want the latest tree %q", effect.AfterDigest, finished.TreeDigest)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	applied, err := second.Apply(t.Context(), reused.ID)
	if err != nil {
		t.Fatalf("apply after restart: %v", err)
	}
	if applied.Status != "applied" {
		t.Fatalf("apply status = %q", applied.Status)
	}
	if data, err := os.ReadFile(filepath.Join(root, "main.txt")); err != nil || string(data) != "second turn" {
		t.Fatalf("apply did not write back: %q %v", data, err)
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

func TestMaterializeCopyCleansPartialDirectoryOnFailure(t *testing.T) {
	db, root, run := executionFixture(t)
	// A single 33 MiB sparse file exceeds the 32 MiB per-file copy limit.
	big := filepath.Join(root, "large.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(33 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("Prepare error = %v, want ErrSourceTooLarge", err)
	}
	if _, statErr := os.Stat(candidate.Path); !os.IsNotExist(statErr) {
		t.Fatalf("partial candidate directory leaked on disk: %s (%v)", candidate.Path, statErr)
	}
}

func TestLargeGitWorktreeFallsBackToGitTreeDigest(t *testing.T) {
	db, repo, run, _ := gitExecutionFixture(t)
	run.WriteBranch = true
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	manager.digestTree = func(string) (string, error) {
		return "", tools.ErrDigestTooLarge
	}
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatalf("Prepare large git worktree: %v", err)
	}
	if candidate.Kind != "git_worktree" || candidate.TreeDigest == "" || candidate.BaseTreeDigest == "" {
		t.Fatalf("unexpected candidate metadata: %+v", candidate)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("updated in large repo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish(t.Context(), candidate.ID); err != nil {
		t.Fatalf("Finish large git worktree: %v", err)
	}
	verify, err := manager.VerifyTree(t.Context(), candidate.ID, "review-step")
	if err != nil {
		t.Fatalf("VerifyTree large git worktree: %v", err)
	}
	if err := manager.DestroyVerifyTree(t.Context(), verify, candidate.ID); err != nil {
		t.Fatalf("DestroyVerifyTree large git worktree: %v", err)
	}
	applied, err := manager.Apply(t.Context(), candidate.ID)
	if err != nil {
		t.Fatalf("Apply large git worktree: %v", err)
	}
	if applied.Status != "applied" {
		t.Fatalf("applied status = %q", applied.Status)
	}
	got, err := os.ReadFile(filepath.Join(repo, "main.txt"))
	if err != nil || string(got) != "updated in large repo\n" {
		t.Fatalf("applied content = %q (%v)", got, err)
	}
}
