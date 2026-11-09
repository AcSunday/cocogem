package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// ErrRecoveryUncertain blocks resuming while an owned process cannot be verified.
var ErrRecoveryUncertain = errors.New("recovery_uncertain_process: an owned native process could not be verified; confirm it has stopped before resuming")

type attemptVerdict struct {
	status    string
	reason    string
	recovery  string
	uncertain bool
}

// claim takes the durable dispatch lease for exactly one live server.
func (e *Engine) claim(ctx context.Context, runID string) (bool, error) {
	own := w.RunClaim{RunID: runID, OwnerID: e.InstanceID, OwnerPID: e.Owner.PID, OwnerStart: e.Owner.Start, ClaimedAt: w.Now()}
	var existing w.RunClaim
	found := false
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		claim, err := tx.RunClaim(runID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		existing, found = claim, true
		return nil
	})
	if err != nil {
		return false, err
	}
	if !found {
		var claimed bool
		err := e.Store.Write(ctx, func(tx *store.Tx) error {
			var err error
			claimed, err = tx.InsertRunClaim(own)
			return err
		})
		return claimed, err
	}
	if existing.OwnerID == own.OwnerID {
		var claimed bool
		err := e.Store.Write(ctx, func(tx *store.Tx) error {
			var err error
			claimed, err = tx.TakeRunClaim(existing, own)
			return err
		})
		return claimed, err
	}
	current, inspectErr := proc.Inspect(existing.OwnerPID)
	if inspectErr == nil && current.Start == existing.OwnerStart {
		return false, nil
	}
	if inspectErr != nil && !errors.Is(inspectErr, proc.ErrNotRunning) {
		return false, fmt.Errorf("verify run claim owner: %w", inspectErr)
	}
	var claimed bool
	err = e.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		claimed, err = tx.TakeRunClaim(existing, own)
		return err
	})
	return claimed, err
}

func (e *Engine) release(ctx context.Context, runID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = e.Store.Write(ctx, func(tx *store.Tx) error { return tx.ReleaseRunClaim(runID, e.InstanceID) })
}

// recoverActive reconciles every run that a dead server left non-terminal.
func (e *Engine) recoverActive(ctx context.Context) error {
	var runs []w.Run
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		runs, err = tx.Active()
		return err
	}); err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status == "queued" {
			continue
		}
		claimed, err := e.claim(ctx, run.ID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if err := e.recoverRun(ctx, run.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				e.release(ctx, run.ID)
				continue
			}
			return err
		}
	}
	return nil
}

func (e *Engine) recoverRun(ctx context.Context, id string) error {
	var run w.Run
	var attempts []w.Attempt
	var steps []w.Step
	var messages []w.Message
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		if run, err = tx.Run(id); err != nil {
			return err
		}
		if _, err = tx.Thread(run.ThreadID); err != nil {
			return err
		}
		if attempts, err = tx.Attempts(id); err != nil {
			return err
		}
		if steps, err = tx.Steps(id); err != nil {
			return err
		}
		messages, err = tx.MessagesMetadata(run.ThreadID)
		return err
	}); err != nil {
		return err
	}
	if w.Terminal(run.Status) {
		e.release(ctx, id)
		return nil
	}
	verdicts := map[string]attemptVerdict{}
	uncertain := ""
	for _, attempt := range attempts {
		if attempt.Status != "running" {
			continue
		}
		verdict := e.reconcileAttempt(attempt)
		verdicts[attempt.ID] = verdict
		if verdict.uncertain {
			uncertain = verdict.reason
		}
	}
	blockedReason := ""
	changes := []string{}
	if uncertain != "" {
		blockedReason = "recovery_uncertain_process: " + uncertain
	} else if len(attempts) == 0 && slices.ContainsFunc(steps, func(step w.Step) bool { return step.Status == "running" }) {
		blockedReason = "recovery_uncertain_process: no execution attempt ownership was recorded; manual confirmation is required"
	} else if run.Status != "canceling" {
		blockedReason, changes = e.checkAdapters(ctx, run, steps, attempts)
		if blockedReason == "" && run.SkillSnapshot != "" {
			hash, err := skills.SnapshotHash(run.Workspace.SkillIDs)
			if err != nil || hash != run.SkillSnapshot {
				blockedReason = "checkpoint_blocked: shared skill version changed before recovery; start a new run"
			}
		}
	}
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(id)
		if err != nil {
			return err
		}
		if w.Terminal(current.Status) {
			return nil
		}
		r := &recoveryTransaction{
			e: e, tx: tx, id: id, run: &current,
			messages: messages, changes: changes, verdicts: verdicts,
		}
		if err := r.applyVerdicts(); err != nil {
			return err
		}
		if current.Status == "canceling" {
			return r.finishCanceled()
		}
		if blockedReason == "" {
			workspace, err := tx.Workspace(current.WorkspaceID)
			if err != nil || workspace.Root != current.Workspace.Root || tools.ValidateSourceRoot(current.Workspace.Root, e.AllowedRoots) != nil {
				blockedReason = "checkpoint_blocked: source directory authorization changed before recovery"
			}
		}
		if blockedReason == "" {
			for _, step := range r.steps {
				if step.Status == "succeeded" || step.Status == "failed" || step.Status == "skipped" {
					continue
				}
				if err := e.validateMember(tx, current, step.AgentID); err != nil {
					blockedReason = "checkpoint_blocked: " + err.Error()
					break
				}
			}
		}
		if blockedReason != "" {
			return r.block(blockedReason)
		}
		unfinished, settled, err := r.settle()
		if err != nil || settled {
			return err
		}
		if !unfinished {
			return r.succeed()
		}
		return r.requeue()
	})
	if err != nil {
		e.release(ctx, id)
		return err
	}
	e.release(ctx, id)
	return nil
}
