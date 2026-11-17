package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeGitMetadataIsDetectedAndDenied is the A03 regression: a linked
// worktree's own git dir is writable so git can lock its index, while the source
// repository's hooks, config and refs are present in the deny list.
func TestWorktreeGitMetadataIsDetectedAndDenied(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	worktree := filepath.Join(dir, "cand")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	run("init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "--", "main.txt")
	run("-c", "user.name=Cocogem Test", "-c", "user.email=cocogem@example.invalid", "commit", "--quiet", "-m", "baseline")
	run("worktree", "add", "--detach", "--quiet", worktree, "HEAD")

	gitDir, common := WorktreeMeta(worktree)
	if gitDir == "" || common == "" {
		t.Fatalf("worktree metadata was not recognized: gitDir=%q common=%q", gitDir, common)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "index")); err != nil {
		t.Fatalf("per-worktree index is missing: %v", err)
	}
	denies := GitMetadataDenies(worktree)
	for _, want := range []string{
		filepath.Join(common, "hooks"),
		filepath.Join(common, "config"),
		filepath.Join(common, "refs", "heads"),
	} {
		found := false
		for _, path := range denies {
			if path == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("source metadata %s is not denied: %v", want, denies)
		}
	}
	// The normal checkout has no linked worktree metadata.
	if gitDir, _ := WorktreeMeta(repo); gitDir != "" {
		t.Fatalf("a normal checkout reported worktree metadata: %s", gitDir)
	}
}
