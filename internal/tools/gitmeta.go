package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// WorktreeMeta returns the source repository's per-worktree git metadata
// directory when root is a linked worktree, plus the repository's shared
// metadata directory. A normal checkout returns empty values.
//
// A linked worktree's `.git` is a file containing `gitdir: <path>`; git needs to
// lock and update that per-worktree index while reading objects and refs from the
// shared repository. The sandbox must allow exactly that, while still denying
// hooks, config, refs/heads and packed-refs so a command can never rewrite the
// user's branches or inject a hook.
func WorktreeMeta(root string) (worktreeGitDir, commonGitDir string) {
	info, err := os.Lstat(filepath.Join(root, ".git"))
	if err != nil || info.IsDir() {
		return "", ""
	}
	data, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil || len(data) > 4096 {
		return "", ""
	}
	line := strings.TrimSpace(string(data))
	value, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", ""
	}
	gitDir := strings.TrimSpace(value)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if resolved, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = resolved
	}
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		return "", ""
	}
	common := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil && len(data) <= 4096 {
		common = strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		common = filepath.Clean(common)
	}
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		common = resolved
	}
	return gitDir, common
}

// GitMetadataDenies lists the source repository paths a sandboxed command must
// never write: hooks, repository config, branch refs and the packed refs file.
// Paths are emitted unconditionally (even if packed-refs or config.worktree do
// not exist yet) so a command cannot create them either.
func GitMetadataDenies(root string) []string {
	_, common := WorktreeMeta(root)
	if common == "" {
		// Also protect a normal checkout's .git directory when running directly.
		gitPath := filepath.Join(root, ".git")
		if info, err := os.Lstat(gitPath); err == nil && info.IsDir() {
			if resolved, err := filepath.EvalSymlinks(gitPath); err == nil {
				common = resolved
			} else {
				common = gitPath
			}
		}
	}
	if common == "" {
		return nil
	}
	return []string{
		filepath.Join(common, "hooks"),
		filepath.Join(common, "config"),
		filepath.Join(common, "config.worktree"),
		filepath.Join(common, "refs", "heads"),
		filepath.Join(common, "packed-refs"),
	}
}
