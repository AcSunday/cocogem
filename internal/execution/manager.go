// Package execution owns candidate worktrees and durable scope leases. It never
// runs project commands and never claims that a directory lease is an OS sandbox.
package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

const leaseTTL = 2 * time.Minute

// gitEnvironment keeps every worktree command hermetic: no system or global
// user config (e.g. diff.noprefix), no credential prompt, and no hooks can run.
func gitEnvironment() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// Manager serializes lease ownership in SQLite, never with a process-local map.
type Manager struct {
	store             *store.Store
	base              string
	owner             string
	now               func() time.Time
	digestTree        func(string) (string, error)
	heartbeatInterval time.Duration
}

func New(db *store.Store, base, owner string) *Manager {
	if owner == "" {
		owner = w.NewID()
	}
	return &Manager{
		store: db,
		// All derived worktrees live under one private directory so a user's
		// project and its parent are never polluted.
		base: filepath.Join(base, "worktrees"), owner: owner, now: time.Now,
		digestTree: tools.WorkspaceDigest, heartbeatInterval: leaseTTL / 3,
	}
}

// Base is the private directory holding every derived execution directory.
func (m *Manager) Base() string { return m.base }

// TaskDirectory persists task files (attachments and collected outputs) outside
// the code tree, so an office deliverable can never pollute a candidate diff.
func (m *Manager) TaskDirectory(taskID string) (string, error) {
	return m.IO("task-files", taskID)
}

// IODirectory returns the per-scope file exchange area
// ($COCOGEM_DATA_DIR/io/<thread|task>/{inputs,outputs}).
func (m *Manager) IODirectory(scopeID string) (string, error) {
	return m.IO("io", scopeID)
}

func (m *Manager) IO(kind, id string) (string, error) {
	if id == "" || !filepath.IsLocal(id) || filepath.Base(id) != id {
		return "", errors.New("invalid scope identity")
	}
	dir := filepath.Join(filepath.Dir(m.base), kind, id)
	for _, child := range []string{"inputs", "outputs"} {
		if err := os.MkdirAll(filepath.Join(dir, child), 0700); err != nil {
			return "", err
		}
	}
	resolved, err := canonical(dir)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func canonical(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = gitEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// isGitWorktree reports whether root is a git repository with a usable commit.
// A freshly initialized repository without any commit has an unborn HEAD and
// falls back to a bounded file copy.
func isGitWorktree(ctx context.Context, root string) bool {
	if _, err := os.Lstat(filepath.Join(root, ".git")); err != nil {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "rev-parse", "--verify", "--quiet", "HEAD")
	cmd.Dir = root
	cmd.Env = gitEnvironment()
	return cmd.Run() == nil
}

// Prepare returns the live candidate for this scope and root, or derives a new
// one. A scope keeps one unapplied candidate across turns; applying or
// discarding it settles the record so the next write derives a fresh worktree.
func (m *Manager) Prepare(ctx context.Context, run w.Run) (w.WorkspaceExecution, error) {
	if run.Workspace.Root == "" {
		return w.WorkspaceExecution{}, errors.New("execution requires a bound source root")
	}
	scope := run.TaskID
	if scope == "" {
		scope = run.ThreadID
	}
	if scope == "" {
		return w.WorkspaceExecution{}, errors.New("execution requires a task or conversation")
	}
	root, err := canonical(run.Workspace.Root)
	if err != nil {
		return w.WorkspaceExecution{}, err
	}
	if err := os.MkdirAll(m.base, 0700); err != nil {
		return w.WorkspaceExecution{}, err
	}
	base, err := canonical(m.base)
	if err != nil {
		return w.WorkspaceExecution{}, err
	}
	if info, err := os.Lstat(base); err != nil || !info.IsDir() {
		return w.WorkspaceExecution{}, errors.New("execution base is not a private directory")
	}
	now := m.now().UTC()
	candidate := w.WorkspaceExecution{
		ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, TaskID: run.TaskID,
		CanonicalRoot: root, Path: filepath.Join(base, scope+"-"+run.ID),
		OwnerMemberID: run.CoordinatorID, LeaseOwner: m.owner, LeaseEpoch: 1,
		LeaseExpiresAt: now.Add(leaseTTL).Format(time.RFC3339Nano), Status: "active",
		Observation: "candidate_tree_only; external_effects=unknown",
		CreatedAt:   now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
	}
	err = m.store.Write(ctx, func(tx *store.Tx) error {
		existing, err := tx.Executions(run.WorkspaceID)
		if err != nil {
			return err
		}
		for _, item := range existing {
			// A completed candidate is still unapplied work: the next turn keeps
			// evolving it. Only applied/discarded records end continuity.
			if (item.Status != "active" && item.Status != "completed") || item.CanonicalRoot != root || !sameScope(item, scope) {
				continue
			}
			if item.Status == "completed" {
				candidate = item
				candidate.Status = "active"
				// The lease owner is regenerated on every server start, so a
				// finished candidate is taken over by the current manager.
				candidate.LeaseOwner = m.owner
				candidate.LeaseEpoch = item.LeaseEpoch + 1
				candidate.LeaseExpiresAt = now.Add(leaseTTL).Format(time.RFC3339Nano)
				candidate.UpdatedAt = now.Format(time.RFC3339Nano)
				return tx.TakeOverCompletedExecution(candidate, item.LeaseEpoch)
			}
			expires, parseErr := time.Parse(time.RFC3339Nano, item.LeaseExpiresAt)
			if item.LeaseOwner != m.owner || parseErr != nil || !expires.After(now) {
				return fmt.Errorf("%w: prior execution owner has not been verified stopped; quarantine is required", w.ErrConflict)
			}
			candidate = item
			candidate.LeaseEpoch = item.LeaseEpoch + 1
			candidate.LeaseExpiresAt = now.Add(leaseTTL).Format(time.RFC3339Nano)
			candidate.UpdatedAt = now.Format(time.RFC3339Nano)
			return tx.SaveExecutionEpoch(candidate, item.LeaseEpoch)
		}
		return tx.AcquireExecution(candidate)
	})
	if err != nil {
		return candidate, err
	}
	if candidate.TreeDigest != "" {
		return candidate, nil
	}
	if err := m.materialize(ctx, root, &candidate); err != nil {
		_ = m.store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
			candidate.Status = "failed"
			candidate.UpdatedAt = m.now().UTC().Format(time.RFC3339Nano)
			return tx.SaveExecutionEpoch(candidate, candidate.LeaseEpoch)
		})
		return candidate, err
	}
	return candidate, nil
}

// sameScope reports whether one execution record belongs to the given owning
// scope. A task-scoped record is matched by task, a thread-scoped one by thread.
func sameScope(item w.WorkspaceExecution, scope string) bool {
	if item.TaskID != "" {
		return item.TaskID == scope
	}
	return item.ThreadID == scope
}

// materialize derives the candidate directory from the current source tree.
func (m *Manager) materialize(ctx context.Context, root string, candidate *w.WorkspaceExecution) error {
	if _, err := os.Stat(candidate.Path); !os.IsNotExist(err) {
		return errors.New("candidate path already exists")
	}
	if isGitWorktree(ctx, root) {
		if err := m.materializeWorktree(ctx, root, candidate); err != nil {
			return err
		}
	} else if err := m.materializeCopy(ctx, root, candidate); err != nil {
		return err
	}
	resolved, err := canonical(candidate.Path)
	if err != nil {
		return err
	}
	candidate.Path = resolved
	digest, err := m.digestTree(candidate.Path)
	if err != nil {
		return err
	}
	candidate.TreeDigest = digest
	candidate.UpdatedAt = m.now().UTC().Format(time.RFC3339Nano)
	return m.store.Write(ctx, func(tx *store.Tx) error { return tx.SaveExecutionEpoch(*candidate, candidate.LeaseEpoch) })
}

// materializeWorktree uses `git worktree add --detach`, which shares the source
// object pool and is therefore milliseconds even for a large repository, then
// synchronizes the user's uncommitted tracked and untracked changes so the
// candidate sees the same working state.
func (m *Manager) materializeWorktree(ctx context.Context, root string, candidate *w.WorkspaceExecution) error {
	candidate.Kind = "git_worktree"
	base, err := m.git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read base commit: %w", err)
	}
	candidate.BaseRevision = base
	if err := os.MkdirAll(filepath.Dir(candidate.Path), 0700); err != nil {
		return err
	}
	if _, err := m.git(ctx, root, "worktree", "add", "--detach", "--quiet", "--", candidate.Path, base); err != nil {
		return fmt.Errorf("derive candidate worktree: %w", err)
	}
	if err := m.syncDirtyWorkspace(ctx, root, candidate.Path); err != nil {
		// A failed synchronization must not leave a half-populated candidate.
		_, _ = m.git(ctx, root, "worktree", "remove", "--force", "--", candidate.Path)
		return err
	}
	snapshotOID, err := m.snapshotWorktreeCommit(ctx, candidate.Path, base)
	if err != nil {
		_, _ = m.git(ctx, root, "worktree", "remove", "--force", "--", candidate.Path)
		return fmt.Errorf("snapshot candidate baseline: %w", err)
	}
	candidate.BaseSnapshotOID = snapshotOID
	candidate.BaseTreeDigest, err = m.digestTree(candidate.Path)
	if err != nil {
		return err
	}
	return nil
}

// snapshotWorktreeCommit captures the exact current working tree (tracked +
// non-ignored untracked files) as an unreferenced git commit using a temporary
// GIT_INDEX_FILE, without mutating HEAD or the worktree's own index.
func (m *Manager) snapshotWorktreeCommit(ctx context.Context, dir, parentOID string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "cocogem-git-index-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	indexPath := filepath.Join(tmpDir, "index")
	runWithIndex := func(args ...string) (string, error) {
		full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "user.name=Cocogem", "-c", "user.email=cocogem@local.invalid"}, args...)
		cmd := exec.CommandContext(ctx, "git", full...)
		cmd.Dir = dir
		cmd.Env = append(gitEnvironment(), "GIT_INDEX_FILE="+indexPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := runWithIndex("add", "-A"); err != nil {
		return "", err
	}
	treeOID, err := runWithIndex("write-tree")
	if err != nil {
		return "", err
	}
	commitArgs := []string{"commit-tree", treeOID, "-m", "cocogem candidate snapshot"}
	if parentOID != "" {
		commitArgs = []string{"commit-tree", treeOID, "-p", parentOID, "-m", "cocogem candidate snapshot"}
	}
	return runWithIndex(commitArgs...)
}

// syncDirtyWorkspace replays the user's uncommitted tracked changes and copies
// their non-ignored, non-sensitive untracked files into the candidate.
func (m *Manager) syncDirtyWorkspace(ctx context.Context, root, target string) error {
	diff, err := m.gitBinary(ctx, root, "diff", "--binary", "--no-color", "--no-ext-diff", "HEAD")
	if err != nil {
		return fmt.Errorf("read uncommitted changes: %w", err)
	}
	if len(diff) > 0 {
		cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "apply", "--whitespace=nowarn", "--binary", "-")
		cmd.Dir = target
		cmd.Env = gitEnvironment()
		cmd.Stdin = strings.NewReader(string(diff))
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("apply uncommitted changes: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	untracked, err := m.gitBinary(ctx, root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return fmt.Errorf("list untracked files: %w", err)
	}
	files, size := 0, int64(0)
	for _, name := range strings.Split(string(untracked), "\x00") {
		if name == "" {
			continue
		}
		if !filepath.IsLocal(name) || tools.DeniedPath(name) {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files++
		size += info.Size()
		if files > 20000 || size > 512<<20 || info.Size() > 32<<20 {
			return errors.New("untracked source files exceed isolated copy limits")
		}
		if err := copyRegularFile(filepath.Join(root, name), filepath.Join(target, name), info); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) gitBinary(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = gitEnvironment()
	return cmd.Output()
}

// materializeCopy is the bounded fallback for a non-git directory or an unborn
// HEAD repository; it snapshots regular files only.
func (m *Manager) materializeCopy(ctx context.Context, root string, candidate *w.WorkspaceExecution) error {
	candidate.Kind = "file_copy"
	before, err := m.digestTree(root)
	if err != nil {
		return fmt.Errorf("digest source before copy: %w", err)
	}
	if err := copyTree(ctx, root, candidate.Path); err != nil {
		return err
	}
	after, err := m.digestTree(root)
	if err != nil {
		return fmt.Errorf("digest source after copy: %w", err)
	}
	if before != after {
		return errors.New("source tree changed during isolated copy")
	}
	candidate.BaseTreeDigest = before
	candidate.BaseFiles, err = digestFiles(candidate.Path)
	if err != nil {
		return err
	}
	return nil
}

// Renew advances the lease epoch so late owners cannot renew or write back.
func (m *Manager) Renew(ctx context.Context, id string, expectedEpoch int) (w.WorkspaceExecution, error) {
	var renewed w.WorkspaceExecution
	err := m.store.Write(ctx, func(tx *store.Tx) error {
		candidate, err := tx.Execution(id)
		if err != nil {
			return err
		}
		if candidate.Status != "active" || candidate.LeaseOwner != m.owner || candidate.LeaseEpoch != expectedEpoch {
			return w.ErrConflict
		}
		now := m.now().UTC()
		expires, err := time.Parse(time.RFC3339Nano, candidate.LeaseExpiresAt)
		if err != nil || !expires.After(now) {
			return fmt.Errorf("%w: expired execution lease requires quarantine", w.ErrConflict)
		}
		candidate.LeaseEpoch++
		candidate.LeaseExpiresAt = now.Add(leaseTTL).Format(time.RFC3339Nano)
		candidate.UpdatedAt = now.Format(time.RFC3339Nano)
		if err := tx.SaveExecutionEpoch(candidate, expectedEpoch); err != nil {
			return err
		}
		renewed = candidate
		return nil
	})
	return renewed, err
}

// Heartbeat renews one observed epoch until the returned stop function is called.
func (m *Manager) Heartbeat(ctx context.Context, id string, report func(error)) (func(), error) {
	var epoch int
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		candidate, err := tx.Execution(id)
		if err != nil {
			return err
		}
		if candidate.Status != "active" || candidate.LeaseOwner != m.owner {
			return w.ErrConflict
		}
		epoch = candidate.LeaseEpoch
		return nil
	}); err != nil {
		return nil, err
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	interval := m.heartbeatInterval
	if interval <= 0 {
		interval = leaseTTL / 3
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				renewed, err := m.Renew(heartbeatCtx, id, epoch)
				if err != nil {
					if heartbeatCtx.Err() == nil {
						if report != nil {
							report(err)
						}
						if errors.Is(err, w.ErrConflict) {
							return
						}
					}
					continue
				}
				epoch = renewed.LeaseEpoch
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}, nil
}

// Finish records before/after candidate tree evidence and releases the lease.
// The candidate directory is kept so the next turn continues the same work.
func (m *Manager) Finish(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	var candidate w.WorkspaceExecution
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		current, err := tx.Execution(id)
		if err != nil {
			return err
		}
		if current.Status != "active" {
			return nil
		}
		if current.LeaseOwner != m.owner {
			return w.ErrConflict
		}
		candidate = current
		return nil
	}); err != nil {
		return err
	}
	if candidate.ID == "" {
		return nil
	}
	after, digestErr := m.digestTree(candidate.Path)
	if digestErr != nil {
		candidate.Status = "uncertain"
		candidate.UpdatedAt = m.now().UTC().Format(time.RFC3339Nano)
		saveErr := m.store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
			return tx.SaveExecutionEpoch(candidate, candidate.LeaseEpoch)
		})
		return errors.Join(fmt.Errorf("digest candidate tree: %w", digestErr), saveErr)
	}
	before := candidate.TreeDigest
	candidate.TreeDigest = after
	candidate.Status = "completed"
	candidate.UpdatedAt = m.now().UTC().Format(time.RFC3339Nano)
	if candidate.TaskID != "" {
		return m.store.Write(ctx, func(tx *store.Tx) error {
			if err := tx.SaveExecutionEpoch(candidate, candidate.LeaseEpoch); err != nil {
				return err
			}
			effect := w.EffectRecord{ID: w.NewID(), TaskID: candidate.TaskID, IdempotencyKey: "execution:" + candidate.ID,
				RequestDigest: digest(before), Status: "applied", BeforeDigest: before, AfterDigest: after,
				Observation: "candidate tree reconciled; integration and external side effects are not observed", CreatedAt: candidate.CreatedAt, UpdatedAt: candidate.UpdatedAt}
			if previous, err := tx.EffectByKey(candidate.TaskID, effect.IdempotencyKey); err == nil {
				// The record covers the candidate's whole change from its baseline.
				// A later turn extends it only when it starts exactly where the
				// previous one ended; any other start means the tree changed outside
				// a run, which stays a conflict.
				if previous.AfterDigest == before {
					previous.AfterDigest = after
					previous.Status = "applied"
					previous.UpdatedAt = candidate.UpdatedAt
					return tx.SaveEffect(previous)
				}
				if previous.RequestDigest != effect.RequestDigest {
					return w.ErrConflict
				}
				return nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			return tx.SaveEffect(effect)
		})
	}
	return m.store.Write(ctx, func(tx *store.Tx) error { return tx.SaveExecutionEpoch(candidate, candidate.LeaseEpoch) })
}

// ErrApplyConflict reports that the bound project changed after the candidate
// was derived, so writing it back would overwrite an external edit.
var ErrApplyConflict = errors.New("execution: the project changed after this candidate was derived")

// Apply writes the candidate back to the bound project. It refuses to overwrite
// a file the user changed since the candidate was derived, then removes the
// worktree so the next write derives a clean candidate from the live tree.
func (m *Manager) Apply(ctx context.Context, id string) (w.WorkspaceExecution, error) {
	var candidate w.WorkspaceExecution
	var running bool
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		candidate, err = tx.Execution(id)
		if err != nil {
			return err
		}
		running, err = candidateInUse(tx, candidate.ID)
		return err
	}); err != nil {
		return candidate, err
	}
	if running {
		return candidate, fmt.Errorf("%w: candidate is locked while a run is active", w.ErrConflict)
	}
	if candidate.Status != "completed" && candidate.Status != "active" && candidate.Status != "uncertain" {
		return candidate, fmt.Errorf("%w: candidate is not applicable", w.ErrConflict)
	}
	if candidate.Kind == "git_worktree" {
		return m.applyWorktree(ctx, candidate)
	}
	return m.applyCopy(ctx, candidate)
}

// candidateBaseRef returns the authoritative baseline commit for diffing and
// conflict checks, preferring BaseSnapshotOID over BaseRevision and HEAD.
func candidateBaseRef(candidate w.WorkspaceExecution) string {
	if candidate.BaseSnapshotOID != "" {
		return candidate.BaseSnapshotOID
	}
	if candidate.BaseRevision != "" {
		return candidate.BaseRevision
	}
	return "HEAD"
}

// worktreeChangedPaths returns all workspace-relative paths modified, added, or
// deleted between the candidate's baseline snapshot and its current working tree
// (surviving any git commits made inside the candidate worktree).
func (m *Manager) worktreeChangedPaths(ctx context.Context, candidate w.WorkspaceExecution) ([]string, error) {
	baseRef := candidateBaseRef(candidate)
	currentOID, err := m.snapshotWorktreeCommit(ctx, candidate.Path, baseRef)
	if err != nil {
		return nil, fmt.Errorf("snapshot candidate tree: %w", err)
	}
	changed, err := m.gitBinary(ctx, candidate.Path, "diff", "--name-only", "--no-ext-diff", "-z", baseRef, currentOID)
	if err != nil {
		return nil, fmt.Errorf("list candidate changes: %w", err)
	}
	return dedupe(splitNUL(changed)), nil
}

// CandidateDiff returns a bounded unified diff of the candidate against its
// derived baseline so users can review changes before clicking Apply.
func (m *Manager) CandidateDiff(ctx context.Context, id string) (string, error) {
	var candidate w.WorkspaceExecution
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		candidate, err = tx.Execution(id)
		return err
	}); err != nil {
		return "", err
	}
	if candidate.Path == "" {
		return "", nil
	}
	if candidate.Kind == "git_worktree" {
		baseRef := candidateBaseRef(candidate)
		currentOID, err := m.snapshotWorktreeCommit(ctx, candidate.Path, baseRef)
		if err != nil {
			return "", fmt.Errorf("snapshot candidate diff: %w", err)
		}
		raw, err := m.gitBinary(ctx, candidate.Path, "diff", "--no-ext-diff", "--no-color", baseRef, currentOID)
		if err != nil {
			return "", fmt.Errorf("read candidate diff: %w", err)
		}
		const maxDiff = 256 << 10
		if len(raw) > maxDiff {
			return string(raw[:maxDiff]) + "\n... (diff truncated at 256 KiB)\n", nil
		}
		return string(raw), nil
	}
	changed, err := m.copyChangedPaths(candidate)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, name := range changed {
		out.WriteString("diff --git a/" + name + " b/" + name + "\n")
		candPath := filepath.Join(candidate.Path, name)
		if _, err := os.Lstat(candPath); os.IsNotExist(err) {
			out.WriteString("deleted file: " + name + "\n")
		} else if _, existed := candidate.BaseFiles[name]; !existed {
			out.WriteString("new file: " + name + "\n")
		} else {
			out.WriteString("modified file: " + name + "\n")
		}
	}
	return out.String(), nil
}

// ExportBranch creates a local Git branch (`cocogem/cand-<shortID>`) in the
// source repository pointing at the candidate's exact snapshot without switching
// the user's current branch or touching their working tree. This provides a
// safe escape hatch whenever Apply reports a 409 conflict.
func (m *Manager) ExportBranch(ctx context.Context, id string) (string, error) {
	var candidate w.WorkspaceExecution
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		candidate, err = tx.Execution(id)
		return err
	}); err != nil {
		return "", err
	}
	if candidate.Kind != "git_worktree" {
		return "", fmt.Errorf("%w: exporting a git branch requires a git worktree candidate", w.ErrConflict)
	}
	if candidate.Status != "completed" && candidate.Status != "active" && candidate.Status != "uncertain" {
		return "", fmt.Errorf("%w: candidate is no longer active", w.ErrConflict)
	}
	baseRef := candidateBaseRef(candidate)
	commitOID, err := m.snapshotWorktreeCommit(ctx, candidate.Path, baseRef)
	if err != nil {
		return "", fmt.Errorf("snapshot candidate for export: %w", err)
	}
	shortID := candidate.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	branch := "cocogem/cand-" + shortID
	if _, err := m.git(ctx, candidate.CanonicalRoot, "branch", "-f", branch, commitOID); err != nil {
		return "", fmt.Errorf("create local branch %s: %w", branch, err)
	}
	return branch, nil
}

func (m *Manager) applyWorktree(ctx context.Context, candidate w.WorkspaceExecution) (w.WorkspaceExecution, error) {
	names, err := m.worktreeChangedPaths(ctx, candidate)
	if err != nil {
		return candidate, err
	}
	baseRef := candidateBaseRef(candidate)
	conflicts := []string{}
	for _, name := range names {
		if !filepath.IsLocal(name) {
			continue
		}
		if !m.worktreeTargetUnchanged(ctx, candidate, baseRef, name) {
			conflicts = append(conflicts, name)
		}
	}
	if len(conflicts) > 0 {
		return candidate, fmt.Errorf("%w: %s", ErrApplyConflict, strings.Join(conflicts, ", "))
	}
	if err := m.copyCandidateFilesAtomic(candidate, names); err != nil {
		return candidate, err
	}
	return m.settle(ctx, candidate, "applied", true)
}

// worktreeTargetUnchanged checks whether the live file at CanonicalRoot/<name>
// still matches the candidate's derived baseline (BaseSnapshotOID). Unrelated
// files edited in CanonicalRoot (or applied by another parallel child task) are
// never compared, while external edits AND external deletions of affected files
// are accurately detected as conflicts.
func (m *Manager) worktreeTargetUnchanged(ctx context.Context, candidate w.WorkspaceExecution, baseRef, name string) bool {
	baseBytes, baseErr := m.gitBinary(ctx, candidate.Path, "show", baseRef+":"+filepath.ToSlash(name))
	existedInBase := baseErr == nil

	targetPath := filepath.Join(candidate.CanonicalRoot, name)
	projectBytes, readErr := os.ReadFile(targetPath)
	if os.IsNotExist(readErr) {
		// Safe only if the file did not exist in the baseline either (candidate
		// created a brand-new file). If it existed in baseline, the user deleted
		// it externally — flag as conflict instead of silently resurrecting.
		return !existedInBase
	}
	if readErr != nil {
		return false
	}
	if !existedInBase {
		// Candidate created a new file, and a file now exists at that path in the
		// project: safe only if the bytes already match the candidate.
		candBytes, candErr := os.ReadFile(filepath.Join(candidate.Path, name))
		return candErr == nil && string(projectBytes) == string(candBytes)
	}
	return string(projectBytes) == string(baseBytes)
}

// copyCandidateFilesAtomic validates all paths first and stages writes via
// temporary files + os.Rename so a mid-loop validation/I/O error does not leave
// the user's repository half-mutated.
func (m *Manager) copyCandidateFilesAtomic(candidate w.WorkspaceExecution, names []string) error {
	type plannedWrite struct {
		source string
		target string
		mode   os.FileMode
		remove bool
	}
	plan := make([]plannedWrite, 0, len(names))
	for _, name := range names {
		if !filepath.IsLocal(name) {
			continue
		}
		source := filepath.Join(candidate.Path, name)
		target := filepath.Join(candidate.CanonicalRoot, name)
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			plan = append(plan, plannedWrite{target: target, remove: true})
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("candidate file %s is not a regular file", name)
		}
		plan = append(plan, plannedWrite{source: source, target: target, mode: info.Mode().Perm() & 0777})
	}
	// Stage all file contents into temporary sibling files before renaming.
	type stagedFile struct {
		tmp    string
		target string
	}
	staged := make([]stagedFile, 0, len(plan))
	cleanupStaged := func() {
		for _, s := range staged {
			_ = os.Remove(s.tmp)
		}
	}
	for _, item := range plan {
		if item.remove {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(item.target), 0700); err != nil {
			cleanupStaged()
			return err
		}
		tmpFile, err := os.CreateTemp(filepath.Dir(item.target), ".cocogem-apply-*")
		if err != nil {
			cleanupStaged()
			return err
		}
		tmpName := tmpFile.Name()
		input, err := os.Open(item.source)
		if err != nil {
			tmpFile.Close()
			_ = os.Remove(tmpName)
			cleanupStaged()
			return err
		}
		_, copyErr := io.Copy(tmpFile, input)
		chmodErr := tmpFile.Chmod(item.mode)
		closeErr := errors.Join(input.Close(), tmpFile.Close())
		if err := errors.Join(copyErr, chmodErr, closeErr); err != nil {
			_ = os.Remove(tmpName)
			cleanupStaged()
			return err
		}
		staged = append(staged, stagedFile{tmp: tmpName, target: item.target})
	}
	for _, s := range staged {
		if err := os.Rename(s.tmp, s.target); err != nil {
			cleanupStaged()
			return err
		}
	}
	for _, item := range plan {
		if item.remove {
			if err := os.Remove(item.target); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// copyChangedPaths lists files added, modified, or deleted in a file_copy
// candidate relative to candidate.BaseFiles (ignoring unrelated user edits in
// CanonicalRoot).
func (m *Manager) copyChangedPaths(candidate w.WorkspaceExecution) ([]string, error) {
	current, err := digestFiles(candidate.Path)
	if err != nil {
		return nil, err
	}
	changed := []string{}
	for name, digest := range current {
		if base, ok := candidate.BaseFiles[name]; !ok || base != digest {
			changed = append(changed, name)
		}
	}
	for name := range candidate.BaseFiles {
		if _, ok := current[name]; !ok {
			changed = append(changed, name)
		}
	}
	return dedupe(changed), nil
}

// applyCopy writes a bounded file-copy candidate back to a non-git directory.
func (m *Manager) applyCopy(ctx context.Context, candidate w.WorkspaceExecution) (w.WorkspaceExecution, error) {
	changed, err := m.copyChangedPaths(candidate)
	if err != nil {
		return candidate, err
	}
	conflicts := []string{}
	for _, name := range changed {
		base, tracked := candidate.BaseFiles[name]
		current, readErr := fileDigest(filepath.Join(candidate.CanonicalRoot, name))
		if readErr != nil {
			return candidate, readErr
		}
		candidateDigest, candidateErr := fileDigest(filepath.Join(candidate.Path, name))
		if candidateErr != nil {
			return candidate, candidateErr
		}
		switch {
		case current == candidateDigest:
			// Already in the candidate's state; nothing to overwrite.
		case tracked && base == current:
			// Unchanged since the candidate was derived.
		case !tracked && current == "":
			// A new file that does not exist in the project yet.
		default:
			conflicts = append(conflicts, name)
		}
	}
	if len(conflicts) > 0 {
		return candidate, fmt.Errorf("%w: %s", ErrApplyConflict, strings.Join(conflicts, ", "))
	}
	if err := m.copyCandidateFilesAtomic(candidate, changed); err != nil {
		return candidate, err
	}
	return m.settle(ctx, candidate, "applied", true)
}

// Discard removes the candidate without touching the bound project. Like Apply
// it is refused while an active run still uses the candidate, so a user cannot
// delete a worktree that a native process is writing to. Host reclamation on
// permanent delete, purge and workspace deletion does not go through here.
func (m *Manager) Discard(ctx context.Context, id string) (w.WorkspaceExecution, error) {
	var candidate w.WorkspaceExecution
	var running bool
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		candidate, err = tx.Execution(id)
		if err != nil {
			return err
		}
		running, err = candidateInUse(tx, candidate.ID)
		return err
	}); err != nil {
		return candidate, err
	}
	if running {
		return candidate, fmt.Errorf("%w: candidate is locked while a run is active", w.ErrConflict)
	}
	return m.settle(ctx, candidate, "discarded", true)
}

// candidateInUse reports whether a queued, running or canceling run still owns
// the candidate. Apply and Discard share this single lock.
func candidateInUse(tx *store.Tx, id string) (bool, error) {
	active, err := tx.Active()
	if err != nil {
		return false, err
	}
	for _, run := range active {
		if run.ExecutionRef == id {
			return true, nil
		}
	}
	return false, nil
}

// settle marks the record terminal and reclaims the derived directory.
func (m *Manager) settle(ctx context.Context, candidate w.WorkspaceExecution, status string, remove bool) (w.WorkspaceExecution, error) {
	if remove {
		if err := m.removeDirectory(ctx, candidate); err != nil {
			return candidate, err
		}
	}
	candidate.Status = status
	candidate.LeaseExpiresAt = m.now().UTC().Format(time.RFC3339Nano)
	candidate.UpdatedAt = m.now().UTC().Format(time.RFC3339Nano)
	if err := m.store.Write(ctx, func(tx *store.Tx) error { return tx.SaveExecution(candidate) }); err != nil {
		return candidate, err
	}
	return candidate, nil
}

// removeDirectory reclaims the derived directory and prunes the source
// repository's worktree metadata so no zombie handle survives.
func (m *Manager) removeDirectory(ctx context.Context, candidate w.WorkspaceExecution) error {
	if candidate.Path == "" {
		return nil
	}
	if candidate.Kind == "git_worktree" {
		if _, err := m.git(ctx, candidate.CanonicalRoot, "worktree", "remove", "--force", "--", candidate.Path); err != nil {
			// A missing directory is already reclaimed; prune below.
			if removeErr := os.RemoveAll(candidate.Path); removeErr != nil {
				return errors.Join(err, removeErr)
			}
		}
		if _, err := m.git(ctx, candidate.CanonicalRoot, "worktree", "prune", "--expire", "now"); err != nil {
			return err
		}
		return nil
	}
	if err := os.RemoveAll(candidate.Path); err != nil {
		return err
	}
	return nil
}

// Prune removes derived directories for terminal records and every orphan
// directory that no live record claims.
func (m *Manager) Prune(ctx context.Context) error {
	var known map[string]bool
	err := m.store.Read(ctx, func(tx *store.Tx) error {
		all, err := tx.ActiveExecutions()
		if err != nil {
			return err
		}
		known = make(map[string]bool, len(all))
		for _, item := range all {
			known[filepath.Clean(item.Path)] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	resolvedBase, err := canonical(m.base)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing was ever derived in this data directory.
			return nil
		}
		return err
	}
	entries, err := os.ReadDir(resolvedBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	roots := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		full := filepath.Join(resolvedBase, entry.Name())
		if known[filepath.Clean(full)] {
			continue
		}
		// Inspect the orphan worktree's .git pointer before deleting it so we can
		// prune the source repository even when no active candidates remain there.
		if _, commonGit := tools.WorktreeMeta(full); commonGit != "" {
			if filepath.Base(commonGit) == ".git" {
				roots[filepath.Dir(commonGit)] = true
			}
		}
		if err := os.RemoveAll(full); err != nil {
			return err
		}
	}
	// Prune stale metadata in every repository that still has a live candidate or
	// bound workspace root.
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		all, err := tx.ActiveExecutions()
		if err != nil {
			return err
		}
		for _, item := range all {
			if item.CanonicalRoot != "" {
				roots[item.CanonicalRoot] = true
			}
		}
		if workspaces, err := tx.Workspaces(); err == nil {
			for _, ws := range workspaces {
				if ws.Root != "" {
					roots[ws.Root] = true
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for root := range roots {
		if _, err := m.git(ctx, root, "worktree", "prune", "--expire", "now"); err != nil {
			continue
		}
	}
	return nil
}

func splitNUL(value []byte) []string {
	out := []string{}
	for _, item := range strings.Split(string(value), "\x00") {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func copyRegularFile(source, target string, info os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm()&0777)
	if err != nil {
		input.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := errors.Join(input.Close(), output.Close())
	return errors.Join(copyErr, closeErr)
}

func copyTree(ctx context.Context, source, target string) error {
	files, size := 0, int64(0)
	return filepath.WalkDir(source, func(path string, item fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.Mkdir(target, 0700)
		}
		if tools.DeniedPath(relative) {
			if item.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 {
			return nil
		}
		destination := filepath.Join(target, relative)
		if item.IsDir() {
			return os.Mkdir(destination, 0700)
		}
		info, err := item.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		files++
		size += info.Size()
		if files > 20000 || size > 512<<20 || info.Size() > 32<<20 {
			return errors.New("source exceeds isolated copy limits")
		}
		return copyRegularFile(path, destination, info)
	})
}

// diffTrees lists files whose bytes differ between two directory trees.
func diffTrees(candidate, project string) ([]string, error) {
	deletedChange := map[string]bool{}
	err := filepath.WalkDir(candidate, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(candidate, path)
		if err != nil {
			return err
		}
		info, err := item.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		candidateBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		projectBytes, err := os.ReadFile(filepath.Join(project, relative))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if string(candidateBytes) != string(projectBytes) {
			deletedChange[relative] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A candidate-side file copy cannot represent a deletion without a manifest.
	out := make([]string, 0, len(deletedChange))
	for name := range deletedChange {
		out = append(out, name)
	}
	return out, nil
}

// SnapshotDigest provides a stable canonical digest for execution metadata tests.
func SnapshotDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return digest(string(encoded))
}

// digestFiles records each regular file's content digest so an apply can prove
// the bound project copy was not edited after the snapshot was derived.
func digestFiles(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := item.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		value, err := fileDigest(path)
		if err != nil {
			return err
		}
		out[relative] = value
		return nil
	})
	return out, err
}

// fileDigest hashes one file's exact bytes; a missing file is the empty digest.
func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return digest(string(data)), nil
}

// RemoveIO deletes one scope's file exchange area.
func (m *Manager) RemoveIO(scopeID string) error {
	if scopeID == "" || !filepath.IsLocal(scopeID) || filepath.Base(scopeID) != scopeID {
		return errors.New("invalid scope identity")
	}
	return os.RemoveAll(filepath.Join(filepath.Dir(m.base), "io", scopeID))
}

// PruneRoot removes stale worktree metadata in one bound repository.
func (m *Manager) PruneRoot(ctx context.Context, root string) error {
	if _, err := os.Lstat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	_, err := m.git(ctx, root, "worktree", "prune", "--expire", "now")
	return err
}

// RemoveDirectories reclaims derived directories whose durable records are
// about to be or were just deleted, plus any orphan left under the private base.
func (m *Manager) RemoveDirectories(ctx context.Context, candidates []w.WorkspaceExecution) error {
	var releaseErr error
	for _, candidate := range candidates {
		if err := m.removeDirectory(ctx, candidate); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	return releaseErr
}
