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
// snapshot, honoring the candidate kind.
func (m *Manager) changedPaths(ctx context.Context, candidate w.WorkspaceExecution) ([]string, error) {
	if candidate.Kind == "git_worktree" {
		tracked, err := m.gitBinary(ctx, candidate.Path, "diff", "--name-only", "-z", "--no-ext-diff", "HEAD")
		if err != nil {
			return nil, err
		}
		untracked, err := m.gitBinary(ctx, candidate.Path, "ls-files", "-z", "--others", "--exclude-standard")
		if err != nil {
			return nil, err
		}
		out := append(splitNUL(tracked), splitNUL(untracked)...)
		return dedupe(out), nil
	}
	changed, err := diffTrees(candidate.Path, candidate.CanonicalRoot)
	if err != nil {
		return nil, err
	}
	return dedupe(changed), nil
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

// VerifyTree is the reviewer sandbox: an independent detached worktree derived
// from the author's exact candidate tree. It is created before dispatch and
// destroyed after the review, and the author's candidate digest is asserted
// unchanged afterwards.
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
	if author.Kind != "git_worktree" {
		return w.WorkspaceExecution{}, fmt.Errorf("%w: independent verification requires a git candidate", w.ErrConflict)
	}
	before, err := m.digestTree(author.Path)
	if err != nil {
		return w.WorkspaceExecution{}, err
	}
	// The reviewed digest is the author's exact current tree; the reviewer only
	// needs the bytes that exist right now, and DestroyVerifyTree asserts they
	// never changed while the review ran.
	path := filepath.Join(m.base, "verify-"+stepID)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return w.WorkspaceExecution{}, errors.New("verification directory already exists")
	}
	// A stash commit freezes the author's exact tracked state (including
	// uncommitted edits) so the reviewer sees the reviewed bytes, not the
	// author's later changes.
	tree, err := m.git(ctx, author.Path, "stash", "create")
	if err != nil {
		return w.WorkspaceExecution{}, fmt.Errorf("freeze author tree: %w", err)
	}
	if tree == "" {
		// A clean author tree is reviewable at HEAD.
		tree = "HEAD"
	}
	if _, err := m.git(ctx, author.CanonicalRoot, "worktree", "add", "--detach", "--quiet", "--", path, tree); err != nil {
		return w.WorkspaceExecution{}, fmt.Errorf("derive verification worktree: %w", err)
	}
	// Untracked author files are not part of a stash commit; copy them so the
	// reviewer can read every file the author actually has.
	if err := m.copyUntracked(ctx, author.Path, path); err != nil {
		_, _ = m.git(ctx, author.CanonicalRoot, "worktree", "remove", "--force", "--", path)
		return w.WorkspaceExecution{}, err
	}
	verification := w.WorkspaceExecution{
		ID: w.NewID(), WorkspaceID: author.WorkspaceID, ThreadID: author.ThreadID,
		TaskID: author.TaskID, Kind: "verify_dir", Path: path, CanonicalRoot: author.CanonicalRoot,
		BaseRevision: author.BaseRevision, TreeDigest: before, OwnerMemberID: author.OwnerMemberID,
		LeaseOwner: m.owner, LeaseEpoch: 1, Status: "active",
		Observation: "independent verification copy; destroyed after review",
		CreatedAt:   m.now().UTC().Format(time.RFC3339Nano), UpdatedAt: m.now().UTC().Format(time.RFC3339Nano),
	}
	// The verification directory is not a lease: it is a disposable copy keyed
	// by step identity and never persisted as a candidate scope.
	return verification, nil
}

// copyUntracked copies the non-ignored, non-sensitive untracked files of source
// into target without replaying tracked diffs.
func (m *Manager) copyUntracked(ctx context.Context, source, target string) error {
	untracked, err := m.gitBinary(ctx, source, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return fmt.Errorf("list untracked files: %w", err)
	}
	files, size := 0, int64(0)
	for _, name := range strings.Split(string(untracked), "\x00") {
		if name == "" || !filepath.IsLocal(name) {
			continue
		}
		info, err := os.Lstat(filepath.Join(source, name))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files++
		size += info.Size()
		if files > 20000 || size > 512<<20 || info.Size() > 32<<20 {
			return errors.New("untracked source files exceed isolated copy limits")
		}
		if err := copyRegularFile(filepath.Join(source, name), filepath.Join(target, name), info); err != nil {
			return err
		}
	}
	return nil
}

// DestroyVerifyTree removes a verification worktree and asserts the author
// candidate is byte-identical to the reviewed digest.
func (m *Manager) DestroyVerifyTree(ctx context.Context, verification w.WorkspaceExecution, authorID string) error {
	if _, err := m.git(ctx, verification.CanonicalRoot, "worktree", "remove", "--force", "--", verification.Path); err != nil {
		if removeErr := os.RemoveAll(verification.Path); removeErr != nil {
			return errors.Join(err, removeErr)
		}
	}
	if _, err := m.git(ctx, verification.CanonicalRoot, "worktree", "prune", "--expire", "now"); err != nil {
		return err
	}
	var author w.WorkspaceExecution
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		author, err = tx.Execution(authorID)
		return err
	}); err != nil {
		return err
	}
	after, err := m.digestTree(author.Path)
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
