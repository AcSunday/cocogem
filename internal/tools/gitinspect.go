package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GitInspectInput is the deterministic read-only repository inspection. It never
// accepts an arbitrary git argument: the host composes a fixed argv with a
// hardened prefix and a bounded, validated path/filter.
type GitInspectInput struct {
	Subcommand string `json:"subcommand"`
	Path       string `json:"path"`
	Revision   string `json:"revision"`
	Limit      int    `json:"limit"`
}

const (
	gitInspectStatus = "status"
	gitInspectDiff   = "diff"
	gitInspectLog    = "log"
	gitInspectShow   = "show"

	// maxGitInspectOutputBytes bounds the observation; the model can page
	// further through a captured source when needed.
	maxGitInspectOutputBytes = 64 << 10
	// maxGitInspectLimit bounds log/show history so one call cannot stream a
	// whole repository.
	maxGitInspectLimit = 200
)

// gitRevisionPattern accepts a hash, a branch/tag name or a bounded range.
var gitRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./~^@-]{0,199}$`)

// sensitiveGitExcludes derives git pathspec exclusions from the shared
// sensitive-path policy, so git_inspect cannot drift from DeniedPath and
// SensitivePath. Every rule applies at the repository root and in any
// subdirectory, and ignores case like the policy does.
func sensitiveGitExcludes() []string {
	excludes := []string{}
	add := func(pattern string) {
		excludes = append(excludes, ":(exclude,icase)"+pattern, ":(exclude,icase)**/"+pattern)
	}
	for _, name := range sensitiveNames {
		add(name)
		add(name + "/**")
	}
	for _, prefix := range sensitivePrefixes {
		add(prefix + "*")
	}
	for _, suffix := range sensitiveSuffixes {
		add("*" + suffix)
	}
	return excludes
}

// gitInspectArgs composes the fixed argv for one subcommand. The harden prefix
// disables external diff drivers, textconv filters, fsmonitor helpers and
// repository hooks so a command cannot execute project code as a side effect.
func gitInspectArgs(in GitInspectInput, path string) ([]string, error) {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "core.pager=cat", "--no-pager"}
	sensitiveExcludes := sensitiveGitExcludes()
	switch in.Subcommand {
	case gitInspectStatus:
		args := append(prefix, "status", "--porcelain=v1", "--branch", "--untracked-files=all", "--no-renames", "--", path)
		return args, nil
	case gitInspectDiff:
		args := append(prefix, "diff", "--no-ext-diff", "--no-textconv", "--no-color")
		if revision := strings.TrimSpace(in.Revision); revision != "" {
			args = append(args, revision)
		}
		args = append(args, "--", path)
		args = append(args, sensitiveExcludes...)
		return args, nil
	case gitInspectLog:
		limit := in.Limit
		if limit == 0 {
			limit = 20
		}
		args := append(prefix, "log", "--no-ext-diff", "--no-textconv", "--no-color", "--format=%h %ad %s", "--date=short", "-n", itoa(limit))
		if revision := strings.TrimSpace(in.Revision); revision != "" {
			args = append(args, revision)
		}
		args = append(args, "--", path)
		return args, nil
	case gitInspectShow:
		revision := strings.TrimSpace(in.Revision)
		if revision == "" {
			revision = "HEAD"
		}
		args := append(prefix, "show", "--no-ext-diff", "--no-textconv", "--no-color", "--stat", "--format=fuller", revision)
		if path == "" {
			path = "."
		}
		args = append(args, "--", path)
		args = append(args, sensitiveExcludes...)
		return args, nil
	}
	return nil, errors.New("unsupported git_inspect subcommand: use status, diff, log or show")
}

// GitInspect runs one deterministic read-only git query inside root. It never
// writes the repository, never runs a hook and never reaches a network remote.
func GitInspect(ctx context.Context, root string, in GitInspectInput) (string, error) {
	in.Subcommand = strings.TrimSpace(strings.ToLower(in.Subcommand))
	if in.Subcommand == "" {
		in.Subcommand = gitInspectStatus
	}
	if in.Limit < 0 || in.Limit > maxGitInspectLimit {
		return "", errors.New("limit must be between 0 and 200")
	}
	if in.Revision != "" && !gitRevisionPattern.MatchString(in.Revision) {
		return "", errors.New("revision must be a bounded branch, tag or commit")
	}
	if root == "" {
		return "", errors.New("no source directory is bound")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("source directory is unavailable")
	}
	if _, err := os.Lstat(filepath.Join(resolvedRoot, ".git")); err != nil {
		return "", errors.New("git_inspect requires a git repository or linked worktree")
	}
	// The path is a repository-relative scope, never an absolute escape.
	path := strings.TrimSpace(in.Path)
	if path == "" {
		path = "."
	}
	if path != "." && (len(path) > 512 || !filepath.IsLocal(path) || strings.HasPrefix(path, "-")) {
		return "", errors.New("path must be a repository-relative path")
	}
	if path != "." && (DeniedPath(path) || SensitivePath(path)) {
		return "", errors.New("git_inspect refuses sensitive or credential paths")
	}
	args, err := gitInspectArgs(in, path)
	if err != nil {
		return "", err
	}
	tmpHome, err := os.MkdirTemp("", "cocogem-git-inspect-*")
	if err != nil {
		return "", errors.New("could not prepare git_inspect environment")
	}
	defer os.RemoveAll(tmpHome)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = resolvedRoot
	// The child receives a credential-free environment and never a user hook,
	// SSH agent or credential helper.
	cmd.Env = []string{
		"PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + tmpHome,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
		"GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false", "GIT_SSH_COMMAND=/bin/false",
		"LANG=C", "LC_ALL=C", "TZ=UTC",
	}
	output := &boundedBuffer{limit: maxGitInspectOutputBytes}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = 2e9
	if err := configureTestProcess(cmd); err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", errors.New("git_inspect could not start")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		terminateTestGroup(cmd)
		waitErr = <-done
	}
	_ = cleanupTestGroup(cmd)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if waitErr != nil {
		return "", errors.New("git_inspect failed: " + strings.TrimSpace(output.String()))
	}
	record := map[string]any{
		"subcommand": in.Subcommand, "path": path, "revision": in.Revision,
		"truncated": output.truncated, "output": output.String(),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", errors.New("could not encode the git observation")
	}
	return string(encoded), nil
}
