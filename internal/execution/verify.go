package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Verification states of one candidate.
const (
	// CandidateVerified means every changed file has diff evidence and the last
	// successful test ran after the last write, against this exact tree.
	CandidateVerified = "verified"
	// CandidateUnverifiedMutation means the tree changed without a passing check
	// covering the change, so it must not be presented as verified work.
	CandidateUnverifiedMutation = "unverified_mutation"
	// CandidateUnchanged means the candidate still matches its derived snapshot.
	CandidateUnchanged = "unchanged"
)

// CandidateVerification is the settlement-time verdict for one candidate: the
// changed paths, their captured diff evidence and the last passing test.
type CandidateVerification struct {
	State string `json:"state"`
	// ChangedPaths are the workspace-relative paths that differ from the derived
	// snapshot.
	ChangedPaths []string `json:"changed_paths,omitempty"`
	// DiffSourceIDs cite the captured diff evidence per changed path.
	DiffSourceIDs []string `json:"diff_source_ids,omitempty"`
	// TestSourceIDs cite passing test evidence captured after the last write.
	TestSourceIDs []string `json:"test_source_ids,omitempty"`
	// TreeDigest is the exact candidate tree the verdict was computed against.
	TreeDigest string `json:"tree_digest,omitempty"`
	// Reason explains a non-verified verdict in plain words.
	Reason string `json:"reason,omitempty"`
	// HygieneHints are non-blocking observations about the candidate diff,
	// such as stacked fallback branches. They never change the verdict.
	HygieneHints []string `json:"hygiene_hints,omitempty"`
}

// VerifyCandidate computes the settlement verdict for one execution record. It
// never runs commands: it compares the derived snapshot with the current tree
// and checks whether captured evidence covers every change.
func (m *Manager) VerifyCandidate(ctx context.Context, id string, sources []w.Source) (CandidateVerification, error) {
	var candidate w.WorkspaceExecution
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		candidate, err = tx.Execution(id)
		return err
	}); err != nil {
		return CandidateVerification{}, err
	}
	current, err := m.digestTree(candidate.Path)
	if errors.Is(err, tools.ErrDigestTooLarge) && candidate.Kind == "git_worktree" {
		if treeOID, treeErr := m.snapshotWorktreeTree(ctx, candidate.Path); treeErr == nil {
			current, err = "git-tree:"+treeOID, nil
		}
	}
	if err != nil {
		return CandidateVerification{}, fmt.Errorf("digest candidate tree: %w", err)
	}
	verdict := CandidateVerification{TreeDigest: current}
	if candidate.BaseTreeDigest != "" && current == candidate.BaseTreeDigest {
		verdict.State = CandidateUnchanged
		return verdict, nil
	}
	changed, err := m.changedPaths(ctx, candidate)
	if err != nil {
		return CandidateVerification{}, err
	}
	verdict.ChangedPaths = changed
	if len(changed) == 0 {
		verdict.State = CandidateUnchanged
		return verdict, nil
	}
	diffByPath := map[string]w.Source{}
	lastWrite := ""
	successfulTests := []w.Source{}
	for _, source := range sources {
		if source.RunID == "" {
			continue
		}
		switch source.Kind {
		case "diff":
			if source.Path == "" || source.SHA256 == "" {
				continue
			}
			if previous, ok := diffByPath[source.Path]; !ok || source.CapturedAt > previous.CapturedAt {
				diffByPath[source.Path] = source
			}
			if source.CapturedAt > lastWrite {
				lastWrite = source.CapturedAt
			}
		case "test":
			if source.ExitCode == 0 && !source.TimedOut && source.SHA256 == current {
				successfulTests = append(successfulTests, source)
			}
		}
	}
	for _, path := range changed {
		source, ok := diffByPath[path]
		if !ok {
			verdict.State = CandidateUnverifiedMutation
			verdict.Reason = "changed file has no captured diff evidence: " + path
			return verdict, nil
		}
		verdict.DiffSourceIDs = append(verdict.DiffSourceIDs, source.ID)
	}
	if len(successfulTests) == 0 {
		verdict.State = CandidateUnverifiedMutation
		verdict.Reason = "no passing test captured after the last write against this tree"
		return verdict, nil
	}
	for _, source := range successfulTests {
		if source.CapturedAt >= lastWrite {
			verdict.TestSourceIDs = append(verdict.TestSourceIDs, source.ID)
		}
	}
	if len(verdict.TestSourceIDs) == 0 {
		verdict.State = CandidateUnverifiedMutation
		verdict.Reason = "the passing test predates the last write"
		return verdict, nil
	}
	verdict.State = CandidateVerified
	return verdict, nil
}

// changedPaths lists the workspace-relative paths that differ from the derived
// snapshot, honoring the candidate kind (and surviving agent git commits).
func (m *Manager) changedPaths(ctx context.Context, candidate w.WorkspaceExecution) ([]string, error) {
	if candidate.Kind == "git_worktree" {
		return m.worktreeChangedPaths(ctx, candidate)
	}
	return m.copyChangedPaths(candidate)
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// VerifyTree is the reviewer sandbox: an independent detached worktree (or
// bounded file copy for non-git projects) derived from the author's exact
// candidate tree. Uncommitted changes stay as working-tree diffs so `git diff`
// and `git_inspect` show the author's changes directly.
func (m *Manager) VerifyTree(ctx context.Context, authorID, stepID string) (w.WorkspaceExecution, error) {
	var author w.WorkspaceExecution
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		author, err = tx.Execution(authorID)
		return err
	}); err != nil {
		return w.WorkspaceExecution{}, err
	}
	if author.Path == "" {
		return w.WorkspaceExecution{}, errors.New("author candidate is unavailable")
	}
	return m.verifyFromRoot(ctx, author.Path, author.CanonicalRoot, author.WorkspaceID, author.ThreadID, author.TaskID, author.OwnerMemberID, author.Kind == "git_worktree", stepID)
}

// VerifyDirectRoot materializes a disposable reviewer sandbox from a bound
// project root directly (used when independent review runs on an ordinary
// direct-write conversation without a Candidate worktree).
func (m *Manager) VerifyDirectRoot(ctx context.Context, root, workspaceID, threadID, taskID, ownerMemberID, stepID string) (w.WorkspaceExecution, error) {
	if root == "" {
		return w.WorkspaceExecution{}, errors.New("source directory is unavailable for independent review")
	}
	resolved, err := canonical(root)
	if err != nil {
		return w.WorkspaceExecution{}, err
	}
	isGit := isGitWorktree(ctx, resolved)
	return m.verifyFromRoot(ctx, resolved, resolved, workspaceID, threadID, taskID, ownerMemberID, isGit, stepID)
}

func (m *Manager) verifyFromRoot(ctx context.Context, sourcePath, canonicalRoot, workspaceID, threadID, taskID, ownerMemberID string, isGit bool, stepID string) (w.WorkspaceExecution, error) {
	before, err := m.digestTree(sourcePath)
	if errors.Is(err, tools.ErrDigestTooLarge) && isGit {
		if treeOID, treeErr := m.snapshotWorktreeTree(ctx, sourcePath); treeErr == nil {
			before, err = "git-tree:"+treeOID, nil
		}
	}
	if err != nil {
		return w.WorkspaceExecution{}, err
	}
	path := filepath.Join(m.base, "verify-"+stepID)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return w.WorkspaceExecution{}, errors.New("verification directory already exists")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return w.WorkspaceExecution{}, err
	}
	baseRev := ""
	if isGit {
		headRev, err := m.git(ctx, sourcePath, "rev-parse", "HEAD")
		if err != nil {
			return w.WorkspaceExecution{}, fmt.Errorf("read reviewed HEAD: %w", err)
		}
		baseRev = headRev
		if _, err := m.git(ctx, canonicalRoot, "worktree", "add", "--detach", "--quiet", "--", path, headRev); err != nil {
			return w.WorkspaceExecution{}, fmt.Errorf("derive verification worktree: %w", err)
		}
		// Replay uncommitted tracked & untracked changes as working-tree diffs so
		// `git status`, `git diff`, and `git_inspect` show the author's edits.
		if err := m.syncDirtyWorkspace(ctx, sourcePath, path); err != nil {
			_, _ = m.git(ctx, canonicalRoot, "worktree", "remove", "--force", "--", path)
			return w.WorkspaceExecution{}, err
		}
	} else {
		if err := copyTree(ctx, sourcePath, path); err != nil {
			_ = os.RemoveAll(path)
			return w.WorkspaceExecution{}, err
		}
	}
	verification := w.WorkspaceExecution{
		ID: w.NewID(), WorkspaceID: workspaceID, ThreadID: threadID,
		TaskID: taskID, Kind: "verify_dir", Path: path, CanonicalRoot: canonicalRoot,
		BaseRevision: baseRev, TreeDigest: before, OwnerMemberID: ownerMemberID,
		LeaseOwner: m.owner, LeaseEpoch: 1, Status: "active",
		Observation: "independent verification copy; destroyed after review",
		CreatedAt:   m.now().UTC().Format(time.RFC3339Nano), UpdatedAt: m.now().UTC().Format(time.RFC3339Nano),
	}
	return verification, nil
}

// DestroyVerifyTree removes a verification worktree (or copy) and asserts the
// reviewed tree is byte-identical to the reviewed digest.
func (m *Manager) DestroyVerifyTree(ctx context.Context, verification w.WorkspaceExecution, authorID string) error {
	if verification.BaseRevision != "" && verification.CanonicalRoot != "" {
		if _, err := m.git(ctx, verification.CanonicalRoot, "worktree", "remove", "--force", "--", verification.Path); err != nil {
			if removeErr := os.RemoveAll(verification.Path); removeErr != nil {
				return errors.Join(err, removeErr)
			}
		}
		_, _ = m.git(ctx, verification.CanonicalRoot, "worktree", "prune", "--expire", "now")
	} else {
		if err := os.RemoveAll(verification.Path); err != nil {
			return err
		}
	}
	reviewedPath := verification.CanonicalRoot
	if authorID != "" {
		var author w.WorkspaceExecution
		if err := m.store.Read(ctx, func(tx *store.Tx) error {
			var err error
			author, err = tx.Execution(authorID)
			return err
		}); err != nil {
			return err
		}
		reviewedPath = author.Path
	}
	if reviewedPath == "" {
		return nil
	}
	var after string
	var err error
	if strings.HasPrefix(verification.TreeDigest, "git-tree:") {
		if treeOID, treeErr := m.snapshotWorktreeTree(ctx, reviewedPath); treeErr == nil {
			after = "git-tree:" + treeOID
		} else {
			err = treeErr
		}
	} else {
		after, err = m.digestTree(reviewedPath)
		if errors.Is(err, tools.ErrDigestTooLarge) && verification.BaseRevision != "" {
			if treeOID, treeErr := m.snapshotWorktreeTree(ctx, reviewedPath); treeErr == nil {
				after, err = "git-tree:"+treeOID, nil
			}
		}
	}
	if err != nil {
		return err
	}
	if verification.TreeDigest != "" && after != verification.TreeDigest {
		return errors.New("the reviewed candidate changed while the reviewer was running")
	}
	return nil
}

// WorkspaceDigestOf exposes the tree digest of one candidate path.
func (m *Manager) WorkspaceDigestOf(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty candidate path")
	}
	return m.digestTree(path)
}

// EnsureSafe removes an execution directory that escaped the private base.
func (m *Manager) EnsureSafe(path string) error {
	base, err := canonical(m.base)
	if err != nil {
		return err
	}
	resolved, err := canonical(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(base, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return errors.New("execution directory escaped the private base")
	}
	return nil
}
