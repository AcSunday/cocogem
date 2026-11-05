package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCloneUsesIndependentCandidateAndPreservesSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	db, root, run := executionFixture(t)
	home := t.TempDir()
	git := func(dir string, args ...string) string {
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
	git(root, "init", "--quiet")
	git(root, "add", "--", "main.txt")
	git(root, "-c", "user.name=Cocogem Test", "-c", "user.email=cocogem@example.invalid", "commit", "--quiet", "-m", "baseline")
	base := git(root, "rev-parse", "HEAD")
	before := snapshotRegularFiles(t, root)

	manager := New(db, filepath.Join(t.TempDir(), "executions"), "owner")
	candidate, err := manager.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Kind != "git_clone" || candidate.BaseRevision != base {
		t.Fatalf("candidate base = %+v, want %s", candidate, base)
	}
	gitInfo, err := os.Lstat(filepath.Join(candidate.Path, ".git"))
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("candidate .git is not an independent directory: %v, %v", gitInfo, err)
	}
	if filepath.Clean(candidate.Path) == filepath.Clean(root) {
		t.Fatal("candidate reused the source directory")
	}

	sourceFiles := regularFileInfo(t, root)
	candidateFiles := regularFileInfo(t, candidate.Path)
	for sourcePath, sourceInfo := range sourceFiles {
		for candidatePath, candidateInfo := range candidateFiles {
			if os.SameFile(sourceInfo, candidateInfo) {
				t.Fatalf("hard-linked source and candidate files: %s and %s", sourcePath, candidatePath)
			}
		}
	}
	statusBefore := git(root, "status", "--porcelain")

	if err := os.WriteFile(filepath.Join(candidate.Path, "candidate.txt"), []byte("candidate only"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "candidate.txt")); !os.IsNotExist(err) {
		t.Fatalf("candidate file leaked into source: %v", err)
	}
	if got := git(root, "rev-parse", "HEAD"); got != base {
		t.Fatalf("source HEAD changed: %s -> %s", base, got)
	}
	if got := git(root, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("source worktree changed: %q -> %q", statusBefore, got)
	}
	after := snapshotRegularFiles(t, root)
	for path, sum := range before {
		if path == filepath.ToSlash(filepath.Join(".git", "index")) {
			continue
		}
		if after[path] != sum {
			t.Fatalf("source file changed during clone: %s", path)
		}
	}
}

func snapshotRegularFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() || item.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = hex.EncodeToString(hash.Sum(nil))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func regularFileInfo(t *testing.T, root string) map[string]os.FileInfo {
	t.Helper()
	result := map[string]os.FileInfo{}
	if err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() || item.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			result[path] = info
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
