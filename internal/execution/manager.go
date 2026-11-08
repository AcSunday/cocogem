// Package execution owns candidate directories and durable root leases. It
// never runs project commands and never claims that a directory lease is an OS sandbox.
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
		store: db, base: base, owner: owner, now: time.Now,
		digestTree: tools.WorkspaceDigest, heartbeatInterval: leaseTTL / 3,
	}
}

// TaskDirectory persists outputs across execution segments without copying a whole user directory.
func (m *Manager) TaskDirectory(taskID string) (string, error) {
	if taskID == "" || !filepath.IsLocal(taskID) || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid task identity")
	}
	dir := filepath.Join(m.base, "task-files", taskID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "outputs"), 0700); err != nil {
		return "", err
	}
	resolved, err := canonical(dir)
	if err != nil {
		return "", err
	}
	base, err := canonical(m.base)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(base, resolved); err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("task directory escaped execution storage")
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

// Prepare returns an existing live lease for this exact Run or creates a new
// isolated candidate. Expired leases never get taken over implicitly.
func (m *Manager) Prepare(ctx context.Context, run w.Run) (w.WorkspaceExecution, error) {
	if run.TaskID == "" || run.Workspace.Root == "" {
		return w.WorkspaceExecution{}, errors.New("execution requires a task and source root")
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
	var retrySource *w.WorkspaceExecution
	candidate := w.WorkspaceExecution{
		ID: w.NewID(), WorkspaceID: run.WorkspaceID, TaskID: run.TaskID, CanonicalRoot: root,
		Path: filepath.Join(base, run.TaskID+"-"+run.ID), OwnerMemberID: run.CoordinatorID,
		LeaseOwner: m.owner, LeaseEpoch: 1, LeaseExpiresAt: now.Add(leaseTTL).Format(time.RFC3339Nano),
		Status: "active", Observation: "candidate_tree_only; external_effects=unknown", CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
	}
	if err := m.store.Write(ctx, func(tx *store.Tx) error {
		if run.RetryOfStepID != "" && run.ContinuedFromRun != "" {
			previous, err := tx.Run(run.ContinuedFromRun)
			if err != nil {
				return err
			}
			original, err := tx.Execution(previous.ExecutionRef)
			if err != nil || original.Status != "completed" || original.TaskID != run.TaskID || original.CanonicalRoot != root || previous.WorkspaceID != run.WorkspaceID || previous.ThreadID != run.ThreadID {
				return errors.New("原候选执行目录尚未核实或已不可用，无法只重试该成员")
			}
			retrySource = &original
		}
		existing, err := tx.Executions(run.WorkspaceID)
		if err != nil {
			return err
		}
		for _, item := range existing {
			if item.TaskID != run.TaskID || item.Status != "active" {
				continue
			}
			if item.CanonicalRoot != root {
				return fmt.Errorf("%w: task already has an active execution for another source root", w.ErrConflict)
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
	}); err != nil {
		return candidate, err
	}
	if candidate.TreeDigest != "" {
		return candidate, nil
	}
	var materializeErr error
	if retrySource != nil {
		materializeErr = m.materializeRetry(ctx, *retrySource, &candidate)
	} else {
		materializeErr = m.materialize(ctx, root, &candidate)
	}
	if materializeErr != nil {
		_ = m.store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
			candidate.Status = "failed"
			candidate.UpdatedAt = m.now().UTC().Format(time.RFC3339Nano)
			return tx.SaveExecutionEpoch(candidate, candidate.LeaseEpoch)
		})
		return candidate, materializeErr
	}
	return candidate, nil
}

func (m *Manager) materializeRetry(ctx context.Context, source w.WorkspaceExecution, candidate *w.WorkspaceExecution) error {
	before, err := m.digestTree(source.Path)
	if err != nil || before != source.TreeDigest {
		return errors.New("原候选执行目录已变化，无法复用已完成成员的工作")
	}
	if err := copyTree(ctx, source.Path, candidate.Path); err != nil {
		return err
	}
	after, err := m.digestTree(source.Path)
	if err != nil || after != before {
		return errors.New("原候选执行目录在复制期间变化")
	}
	copied, err := m.digestTree(candidate.Path)
	if err != nil || copied != before {
		return errors.New("复制的候选执行目录与已核实版本不一致")
	}
	candidate.Kind, candidate.BaseRevision, candidate.TreeDigest = "file_copy", source.BaseRevision, copied
	candidate.Observation = "copied verified candidate from execution:" + source.ID
	candidate.UpdatedAt = m.now().UTC().Format(time.RFC3339Nano)
	return m.store.Write(ctx, func(tx *store.Tx) error { return tx.SaveExecutionEpoch(*candidate, candidate.LeaseEpoch) })
}

func (m *Manager) materialize(ctx context.Context, root string, candidate *w.WorkspaceExecution) error {
	if _, err := os.Stat(candidate.Path); !os.IsNotExist(err) {
		return errors.New("candidate path already exists")
	}
	isGit := false
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		isGit = true
	}
	if isGit {
		candidate.Kind = "git_clone"
		cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always", "clone", "--no-local", "--no-hardlinks", "--no-tags", "--quiet", "--", root, candidate.Path)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + m.base, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("isolated git clone failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		cmd = exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "rev-parse", "HEAD")
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
		base, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("read base commit: %w", err)
		}
		candidate.BaseRevision = strings.TrimSpace(string(base))
	} else {
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

// Finish records before/after candidate tree evidence and releases the root
// lease. It does not integrate changes or infer remote effect outcomes.
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
	return m.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveExecutionEpoch(candidate, candidate.LeaseEpoch); err != nil {
			return err
		}
		effect := w.EffectRecord{ID: w.NewID(), TaskID: candidate.TaskID, IdempotencyKey: "execution:" + candidate.ID,
			RequestDigest: digest(before), Status: "applied", BeforeDigest: before, AfterDigest: after,
			Observation: "candidate tree reconciled; integration and external side effects are not observed", CreatedAt: candidate.CreatedAt, UpdatedAt: candidate.UpdatedAt}
		if previous, err := tx.EffectByKey(candidate.TaskID, effect.IdempotencyKey); err == nil {
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

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
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
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0777)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := errors.Join(input.Close(), output.Close())
		return errors.Join(copyErr, closeErr)
	})
}

// SnapshotDigest provides a stable canonical digest for execution metadata tests.
func SnapshotDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return digest(string(encoded))
}
