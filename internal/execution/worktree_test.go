package execution

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func gitRepo(t *testing.T) (string, func(dir string, args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	home := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + home,
			"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	return root, run
}

func gitExecutionFixture(t *testing.T) (*store.Store, string, w.Run, func(dir string, args ...string) string) {
	t.Helper()
	root, git := gitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(root, "init", "--quiet")
	git(root, "add", "--", "main.txt")
	git(root, "-c", "user.name=Cocogem Test", "-c", "user.email=cocogem@example.invalid", "commit", "--quiet", "-m", "baseline")

	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ws := w.Workspace{ID: "ws", Root: root, Revision: 1, CreatedAt: w.Now()}
	th := w.Thread{ID: "th", WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: "run", WorkspaceID: ws.ID, ThreadID: th.ID, Workspace: ws, WriteBranch: true}
	return db, root, run, git
}

// TestWorktreeSharesObjectsAndCarriesDirtyState is the A02 regression: deriving
// a candidate is a detached worktree (no clone) that also sees the user's
// uncommitted tracked and untracked files.
func TestWorktreeSharesObjectsAndCarriesDirtyState(t *testing.T) {
	db, root, run, git := gitExecutionFixture(t)
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("dirty tracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new-untracked.txt"), []byte("untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(root, "worktree", "list")
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Kind != "git_worktree" {
		t.Fatalf("candidate kind = %q, want git_worktree", candidate.Kind)
	}
	if data, err := os.ReadFile(filepath.Join(candidate.Path, "main.txt")); err != nil || string(data) != "dirty tracked\n" {
		t.Fatalf("candidate did not carry the dirty tracked file: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(candidate.Path, "new-untracked.txt")); err != nil || string(data) != "untracked\n" {
		t.Fatalf("candidate did not carry the untracked file: %q %v", data, err)
	}
	// The candidate shares the source object pool rather than cloning it.
	list := git(root, "worktree", "list", "--porcelain")
	if !strings.Contains(list, candidate.Path) {
		t.Fatalf("candidate is not registered as a worktree of the source:\n%s", list)
	}
	if data, err := os.ReadFile(filepath.Join(root, "main.txt")); err != nil || string(data) != "dirty tracked\n" {
		t.Fatalf("source changed while deriving: %q %v", data, err)
	}
	// Settling reclaims the worktree and prunes the source metadata.
	if _, err := manager.Discard(t.Context(), candidate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(candidate.Path); !os.IsNotExist(err) {
		t.Fatalf("discarded worktree was not removed: %v", err)
	}
	if list := git(root, "worktree", "list", "--porcelain"); strings.Contains(list, candidate.Path) {
		t.Fatalf("source kept zombie worktree metadata:\n%s", list)
	}
}

// TestPruneRemovesOrphanDirectories covers crash recovery: a derived directory
// with no live record is reclaimed.
func TestPruneRemovesOrphanDirectories(t *testing.T) {
	db, _, run := executionFixture(t)
	base := filepath.Join(t.TempDir(), "executions")
	manager := New(db, base, "owner")
	if _, err := os.Stat(manager.Base()); err != nil {
		if err := os.MkdirAll(manager.Base(), 0700); err != nil {
			t.Fatal(err)
		}
	}
	orphan := filepath.Join(manager.Base(), "orphan-candidate")
	if err := os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	live, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan directory survived prune: %v", err)
	}
	if _, err := os.Stat(live.Path); err != nil {
		t.Fatalf("live candidate was pruned: %v", err)
	}
}

// TestPruneKeepsCompletedCandidatesAwaitingApply guards the restart path: a
// finished candidate is exactly what the user still has to Apply or Discard, so
// startup pruning must never reclaim its directory.
func TestPruneKeepsCompletedCandidatesAwaitingApply(t *testing.T) {
	db, root, run := executionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("finished change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish(t.Context(), candidate.ID); err != nil {
		t.Fatal(err)
	}
	// The test is only meaningful if Finish really left the candidate completed.
	var settled w.WorkspaceExecution
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		settled, err = tx.Execution(candidate.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if settled.Status != "completed" {
		t.Fatalf("precondition: candidate status after Finish = %q, want completed", settled.Status)
	}
	// A server restart prunes with a fresh manager over the same data directory.
	restarted := New(db, filepath.Dir(manager.Base()), "restarted-owner")
	if restarted.Base() != manager.Base() {
		t.Fatalf("restarted manager points at %q, want the original %q", restarted.Base(), manager.Base())
	}
	if err := restarted.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(candidate.Path); err != nil {
		t.Fatalf("a completed candidate awaiting Apply was pruned: %v", err)
	}
	applied, err := manager.Apply(t.Context(), candidate.ID)
	if err != nil {
		t.Fatalf("apply after prune: %v", err)
	}
	if applied.Status != "applied" {
		t.Fatalf("apply status = %q", applied.Status)
	}
	if data, err := os.ReadFile(filepath.Join(root, "main.txt")); err != nil || string(data) != "finished change" {
		t.Fatalf("apply did not write back: %q %v", data, err)
	}
}

// TestVerifyTreeIsIndependentAndLeavesTheAuthorUntouched is the A07 regression.
func TestVerifyTreeIsIndependentAndLeavesTheAuthorUntouched(t *testing.T) {
	db, _, run, _ := gitExecutionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	author, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(author.Path, "main.txt"), []byte("author change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	authorDigest, err := manager.WorkspaceDigestOf(author.Path)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := manager.VerifyTree(t.Context(), author.ID, "step-review")
	if err != nil {
		t.Fatal(err)
	}
	if verification.Kind != "verify_dir" || verification.Path == author.Path {
		t.Fatalf("verification directory = %+v", verification)
	}
	if data, err := os.ReadFile(filepath.Join(verification.Path, "main.txt")); err != nil || string(data) != "author change\n" {
		t.Fatalf("verification copy does not match the author tree: %q %v", data, err)
	}
	// The reviewer's own writes never reach the author candidate.
	if err := os.WriteFile(filepath.Join(verification.Path, "main.txt"), []byte("reviewer probe\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.DestroyVerifyTree(t.Context(), verification, author.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(verification.Path); !os.IsNotExist(err) {
		t.Fatalf("verification directory was not destroyed: %v", err)
	}
	after, err := manager.WorkspaceDigestOf(author.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != authorDigest {
		t.Fatalf("author candidate changed during review: %s -> %s", authorDigest, after)
	}
	if data, err := os.ReadFile(filepath.Join(author.Path, "main.txt")); err != nil || string(data) != "author change\n" {
		t.Fatalf("reviewer wrote through to the author: %q %v", data, err)
	}
}

// TestVerifyCandidateNeedsACoveringTest is the A06 regression.
func TestVerifyCandidateNeedsACoveringTest(t *testing.T) {
	db, _, run, _ := gitExecutionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := manager.VerifyCandidate(t.Context(), candidate.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.State != CandidateUnchanged {
		t.Fatalf("untouched candidate = %+v", unchanged)
	}
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("mutation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	withoutEvidence, err := manager.VerifyCandidate(t.Context(), candidate.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withoutEvidence.State != CandidateUnverifiedMutation || !strings.Contains(withoutEvidence.Reason, "no captured diff evidence") {
		t.Fatalf("unverified mutation = %+v", withoutEvidence)
	}
	digest, err := manager.WorkspaceDigestOf(candidate.Path)
	if err != nil {
		t.Fatal(err)
	}
	sources := []w.Source{
		{ID: "diff-1", RunID: run.ID, Kind: "diff", Path: "main.txt", SHA256: "diff-digest", CapturedAt: "2026-11-16T10:00:00Z"},
		{ID: "test-1", RunID: run.ID, Kind: "test", ExitCode: 0, SHA256: digest, CapturedAt: "2026-11-16T10:01:00Z"},
	}
	verified, err := manager.VerifyCandidate(t.Context(), candidate.ID, sources)
	if err != nil {
		t.Fatal(err)
	}
	if verified.State != CandidateVerified || len(verified.DiffSourceIDs) != 1 || len(verified.TestSourceIDs) != 1 {
		t.Fatalf("verified candidate = %+v", verified)
	}
	// A mutation after the last test invalidates the evidence.
	if err := os.WriteFile(filepath.Join(candidate.Path, "extra.txt"), []byte("late\n"), 0600); err != nil {
		t.Fatal(err)
	}
	late, err := manager.VerifyCandidate(t.Context(), candidate.ID, sources)
	if err != nil {
		t.Fatal(err)
	}
	if late.State != CandidateUnverifiedMutation {
		t.Fatalf("late mutation = %+v", late)
	}
	_ = errors.Is
}

func TestCandidateAgentCommitSurvivesDiffApplyAndExportBranch(t *testing.T) {
	db, project, run, runGit := gitExecutionFixture(t)
	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an agent editing and running `git commit` inside its worktree.
	if err := os.WriteFile(filepath.Join(candidate.Path, "main.txt"), []byte("committed by agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(candidate.Path, "add", "main.txt")
	runGit(candidate.Path, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "agent commit")

	diff, err := manager.CandidateDiff(t.Context(), candidate.ID)
	if err != nil {
		t.Fatalf("CandidateDiff after agent git commit: %v", err)
	}
	if !strings.Contains(diff, "committed by agent") {
		t.Fatalf("CandidateDiff missed committed change: %q", diff)
	}
	branch, err := manager.ExportBranch(t.Context(), candidate.ID)
	if err != nil {
		t.Fatalf("ExportBranch: %v", err)
	}
	if !strings.HasPrefix(branch, "cocogem/cand-") {
		t.Fatalf("unexpected exported branch name: %q", branch)
	}
	if _, err := manager.Apply(t.Context(), candidate.ID); err != nil {
		t.Fatalf("Apply after agent git commit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(project, "main.txt"))
	if err != nil || string(data) != "committed by agent\n" {
		t.Fatalf("project file after Apply = %q, %v", data, err)
	}
}

func TestParallelNonOverlappingCandidatesApplyCleanlyAndExternalDeleteConflicts(t *testing.T) {
	db, project, runA, runGit := gitExecutionFixture(t)
	if err := os.WriteFile(filepath.Join(project, "a.txt"), []byte("a0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "b.txt"), []byte("b0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(project, "add", "a.txt", "b.txt")
	runGit(project, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "seed a and b")

	threadB := w.Thread{ID: "thread-b", WorkspaceID: runA.WorkspaceID, Title: "b", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(threadB) }); err != nil {
		t.Fatal(err)
	}
	runB := runA
	runB.ID = "run-b"
	runB.ThreadID = threadB.ID

	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candA, err := manager.Prepare(t.Context(), runA)
	if err != nil {
		t.Fatal(err)
	}
	candB, err := manager.Prepare(t.Context(), runB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candA.Path, "a.txt"), []byte("a1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candB.Path, "b.txt"), []byte("b1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(t.Context(), candA.ID); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	// Candidate B edited b.txt only; applying A (a.txt) must not falsely conflict B!
	if _, err := manager.Apply(t.Context(), candB.ID); err != nil {
		t.Fatalf("non-overlapping parallel candidate B falsely conflicted: %v", err)
	}

	// Now verify external deletion of an affected file triggers ErrApplyConflict.
	candC, err := manager.Prepare(t.Context(), runA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candC.Path, "a.txt"), []byte("a2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(project, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(t.Context(), candC.ID); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("external deletion apply error = %v, want ErrApplyConflict", err)
	}
}
