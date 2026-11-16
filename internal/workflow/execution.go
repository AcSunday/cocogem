package workflow

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// prepareExecution performs filesystem work after the SQLite capacity admission.
// Legacy/unit engines without a Manager retain their direct-root behavior and
// are not evidence that production isolation was verified.
//
// The file exchange area is always the scope's IO directory, independent of the
// code candidate: uploaded attachments and collected deliverables can never
// pollute the code tree or its candidate diff.
func (e *Engine) prepareExecution(ctx context.Context, run w.Run) (w.Run, error) {
	if e.Executions == nil {
		return run, nil
	}
	if run.TaskID == "" && run.ThreadID == "" {
		return run, nil
	}
	scope := run.TaskID
	if scope == "" {
		scope = run.ThreadID
	}
	dir, err := e.Executions.IODirectory(scope)
	if err != nil {
		return run, err
	}
	prepared := run.TaskDirectory != ""
	run.TaskDirectory = dir
	baseline := run.OutputBaseline
	if !prepared {
		baseline = map[string]string{}
		entries, err := os.ReadDir(filepath.Join(dir, "outputs"))
		if err != nil {
			return run, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			digest, err := tools.OutputFingerprint(ctx, dir, filepath.Join("outputs", entry.Name()))
			if err != nil {
				return run, err
			}
			baseline[entry.Name()] = digest
		}
	}
	var sources []w.Source
	if err := e.Store.Read(ctx, func(tx *store.Tx) error { var err error; sources, err = tx.Sources(run.ThreadID); return err }); err != nil {
		return run, err
	}
	for _, source := range sources {
		if source.OriginalContent == "" || !slices.Contains(run.Input.SourceIDs, source.ID) {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(source.OriginalContent)
		if err != nil {
			return run, fmt.Errorf("decode original input: %w", err)
		}
		name := source.ID + "-" + filepath.Base(source.Title)
		if !filepath.IsLocal(name) {
			return run, fmt.Errorf("invalid input filename")
		}
		inputDir := filepath.Join(dir, "inputs")
		if err := os.MkdirAll(inputDir, 0700); err != nil {
			return run, err
		}
		if info, err := os.Lstat(inputDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return run, errors.New("task input directory is not a regular managed directory")
		}
		if _, err := os.Lstat(filepath.Join(inputDir, name)); err == nil {
			existing, err := tools.ReadBinary(ctx, inputDir, name, tools.MaxTaskFileBytes)
			if err != nil {
				return run, err
			}
			if contentDigest(string(existing)) != contentDigest(string(data)) {
				return run, errors.New("task input copy changed; original must be restored before continuation")
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return run, err
		}
		root, err := os.OpenRoot(inputDir)
		if err != nil {
			return run, err
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0400)
		if err == nil {
			_, err = file.Write(data)
			err = errors.Join(err, file.Close())
		}
		root.Close()
		if err != nil {
			return run, err
		}
	}
	if err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.TaskDirectory = dir
		current.OutputBaseline = baseline
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		return run, err
	}
	if !run.WriteBranch {
		return run, nil
	}
	candidate, err := e.Executions.Prepare(ctx, run)
	if err != nil {
		return run, err
	}
	run.ExecutionRef = candidate.ID
	run.ExecutionRoot = candidate.Path
	if err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return w.ErrConflict
		}
		current.ExecutionRef = candidate.ID
		current.ExecutionRoot = candidate.Path
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		if finishErr := e.Executions.Finish(context.WithoutCancel(ctx), candidate.ID); finishErr != nil {
			e.report(finishErr)
		}
		return run, err
	}
	return run, nil
}

// CandidateView is the API projection of one unapplied candidate: where it came
// from, what changed and whether the change is verified.
type CandidateView struct {
	Execution    w.WorkspaceExecution            `json:"execution"`
	Verification execution.CandidateVerification `json:"verification"`
}

// Candidates lists the unapplied candidate directories of one conversation.
func (e *Engine) Candidates(ctx context.Context, threadID string) ([]CandidateView, error) {
	out := []CandidateView{}
	if e.Executions == nil || threadID == "" {
		return out, nil
	}
	var executions []w.WorkspaceExecution
	var sources []w.Source
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		if executions, err = tx.ThreadExecutions(threadID); err != nil {
			return err
		}
		sources, err = tx.Sources(threadID)
		return err
	}); err != nil {
		return nil, err
	}
	for _, item := range executions {
		// Only an unapplied author candidate is user-visible; verification copies
		// are transient and discarded records are history.
		if item.Kind == "verify_dir" || (item.Status != "active" && item.Status != "completed" && item.Status != "uncertain") {
			continue
		}
		verdict, err := e.Executions.VerifyCandidate(ctx, item.ID, sources)
		if err != nil {
			verdict = execution.CandidateVerification{State: execution.CandidateUnverifiedMutation, Reason: err.Error()}
		}
		out = append(out, CandidateView{Execution: item, Verification: verdict})
	}
	return out, nil
}

// ApplyCandidate writes one candidate back to the bound project. It refuses to
// overwrite a file the user changed after the candidate was derived.
func (e *Engine) ApplyCandidate(ctx context.Context, threadID, executionID string) (w.WorkspaceExecution, error) {
	if e.Executions == nil {
		return w.WorkspaceExecution{}, errors.New("execution isolation is unavailable")
	}
	item, err := e.candidateOfThread(ctx, threadID, executionID)
	if err != nil {
		return w.WorkspaceExecution{}, err
	}
	return e.Executions.Apply(ctx, item.ID)
}

// DiscardCandidate destroys one candidate without touching the bound project.
func (e *Engine) DiscardCandidate(ctx context.Context, threadID, executionID string) (w.WorkspaceExecution, error) {
	if e.Executions == nil {
		return w.WorkspaceExecution{}, errors.New("execution isolation is unavailable")
	}
	item, err := e.candidateOfThread(ctx, threadID, executionID)
	if err != nil {
		return w.WorkspaceExecution{}, err
	}
	return e.Executions.Discard(ctx, item.ID)
}

func (e *Engine) candidateOfThread(ctx context.Context, threadID, executionID string) (w.WorkspaceExecution, error) {
	var item w.WorkspaceExecution
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		item, err = tx.Execution(executionID)
		return err
	}); err != nil {
		return item, err
	}
	if item.ThreadID != threadID || item.Kind == "verify_dir" {
		return item, w.ErrConflict
	}
	return item, nil
}

// PruneExecutions reclaims orphaned candidate directories at startup.
func (e *Engine) PruneExecutions(ctx context.Context) error {
	if e.Executions == nil {
		return nil
	}
	return e.Executions.Prune(ctx)
}

// ReleaseThreadExecutions removes every candidate directory and IO area of one
// conversation and prunes the bound repository metadata. It is used by permanent
// deletion, retention purge and workspace deletion, never by a soft delete.
func (e *Engine) ReleaseThreadExecutions(ctx context.Context, threadID string, roots []string) error {
	if e.Executions == nil || threadID == "" {
		return nil
	}
	var items []w.WorkspaceExecution
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		items, err = tx.ThreadExecutions(threadID)
		return err
	}); err != nil {
		return err
	}
	var releaseErr error
	for _, item := range items {
		if _, err := e.Executions.Discard(ctx, item.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if err := e.Executions.PruneRoot(ctx, root); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	if err := e.Executions.RemoveIO(threadID); err != nil {
		releaseErr = errors.Join(releaseErr, err)
	}
	return releaseErr
}

// ReleaseDeletedTaskExecutions reclaims the directories of tasks whose rows were
// just deleted. The caller captured the candidate records first, because after
// the aggregate deletion nothing in the database still names them.
func (e *Engine) ReleaseDeletedTaskExecutions(ctx context.Context, candidates []w.WorkspaceExecution, taskIDs, roots []string) error {
	if e.Executions == nil {
		return nil
	}
	var releaseErr error
	if err := e.Executions.RemoveDirectories(ctx, candidates); err != nil {
		releaseErr = errors.Join(releaseErr, err)
	}
	for _, taskID := range taskIDs {
		if taskID == "" {
			continue
		}
		if err := e.Executions.RemoveIO(taskID); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if err := e.Executions.PruneRoot(ctx, root); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	return releaseErr
}
