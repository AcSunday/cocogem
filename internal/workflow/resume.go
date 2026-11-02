package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// OwnedProcessAlive refuses deletion while a recorded child may still run.
func (e *Engine) OwnedProcessAlive(ctx context.Context, threadID string) (bool, error) {
	var identities []proc.Identity
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		runs, err := tx.Runs(threadID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			attempts, err := tx.Attempts(run.ID)
			if err != nil {
				return err
			}
			for _, attempt := range attempts {
				if attempt.Process == nil || attempt.Status == "succeeded" || attempt.Status == "blocked" {
					continue
				}
				identities = append(identities, proc.Identity{PID: attempt.Process.PID, PGID: attempt.Process.PGID, Start: attempt.Process.Start})
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	for _, identity := range identities {
		if identity.PGID > 0 && identity.PGID == identity.PID {
			alive, err := proc.GroupAlive(identity.PGID)
			if err != nil {
				return false, err
			}
			if alive {
				return true, nil
			}
			continue
		}
		if identity.Start != "" && identity.Alive() {
			return true, nil
		}
	}
	return false, nil
}

// Resume explicitly reopens an interrupted checkpoint; canceled or failed
// work never revives, and repeated calls are idempotent once queued.
func (e *Engine) Resume(ctx context.Context, id string) (w.Run, error) {
	var run w.Run
	var steps []w.Step
	var attempts []w.Attempt
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		if run, err = tx.Run(id); err != nil {
			return err
		}
		// A soft-deleted conversation is not addressable through any run API
		// or resume path, even if its visible-thread guard raced the delete.
		if _, err = tx.Thread(run.ThreadID); err != nil {
			return err
		}
		if steps, err = tx.Steps(id); err != nil {
			return err
		}
		attempts, err = tx.Attempts(id)
		return err
	}); err != nil {
		return run, err
	}
	if run.Status == "queued" || run.Status == "running" {
		return run, nil
	}
	if run.Status != "interrupted" {
		return run, w.ErrConflict
	}
	if e.Model == nil && len(e.Runners) == 0 && e.Fallback == nil {
		return run, errors.New("model is unavailable; cannot resume")
	}
	if run.ExecutionPolicy == nil {
		// Record the one-time legacy adoption before any expiry decision, so a
		// refused resume still explains which policy it applied and when.
		if err := e.Store.Write(ctx, func(tx *store.Tx) error {
			current, err := tx.Run(id)
			if err != nil {
				return err
			}
			if _, err := tx.Thread(current.ThreadID); err != nil {
				return err
			}
			if current.Status != "interrupted" {
				run = current
				return nil
			}
			storedAttempts, err := tx.Attempts(id)
			if err != nil {
				return err
			}
			storedSteps, err := tx.Steps(id)
			if err != nil {
				return err
			}
			adoptedAt, adopted := adoptPolicy(&current, e.Limits, storedAttempts, time.Now())
			if !adopted {
				run = current
				return nil
			}
			if adoptStepClocks(storedSteps, storedAttempts, *current.ExecutionPolicy, adoptedAt) {
				for _, step := range storedSteps {
					if err := tx.SaveStep(step); err != nil {
						return err
					}
				}
			}
			if err := tx.UpdateRunBody(current); err != nil {
				return err
			}
			if err := tx.AppendEvent(id, "", "", "legacy_policy_adopted", map[string]any{"adopted_at": current.PolicyAdoptedAt, "policy": current.ExecutionPolicy}); err != nil {
				return err
			}
			run = current
			return nil
		}); err != nil {
			return run, err
		}
		if run.Status == "queued" || run.Status == "running" {
			return run, nil
		}
		if run.Status != "interrupted" {
			return run, w.ErrConflict
		}
	}
	if run.SkillSnapshot != "" {
		hash, err := skills.SnapshotHash(run.Workspace.SkillIDs)
		if err != nil || hash != run.SkillSnapshot {
			return run, errors.New("shared skill version changed; start a new run")
		}
	}
	for _, attempt := range attempts {
		uncertain := attempt.Status == "uncertain" || attempt.FailureKind == string(nativecli.FailureProcessUncertain) || attempt.Recovery == recoveryProcessUncertain
		if uncertain && attempt.Process == nil {
			// Without a recorded identity there is no way to prove the owned
			// group exited; never skip it and dispatch beside it.
			return run, ErrRecoveryUncertain
		}
		if attempt.Status == "succeeded" || attempt.Status == "blocked" || attempt.Status == "failed" {
			continue
		}
		if attempt.Process == nil {
			continue
		}
		if attempt.Process.Start == "" || attempt.Process.PGID <= 0 || attempt.Process.PGID != attempt.Process.PID {
			return run, ErrRecoveryUncertain
		}
		if alive, err := proc.GroupAlive(attempt.Process.PGID); err != nil || alive {
			return run, ErrRecoveryUncertain
		}
		if !uncertain {
			continue
		}
		if current, err := proc.Inspect(attempt.Process.PID); err == nil {
			if current.Start != attempt.Process.Start {
				return run, ErrRecoveryUncertain
			}
		} else if !errors.Is(err, proc.ErrNotRunning) {
			return run, ErrRecoveryUncertain
		}
		if alive, err := proc.GroupAlive(attempt.Process.PGID); err != nil || alive {
			return run, ErrRecoveryUncertain
		}
	}
	reason, changes := e.checkAdapters(ctx, run, steps, attempts)
	if reason != "" {
		return run, errors.New(reason)
	}
	type identity struct {
		adapter    agent.AdapterInfo
		connection agent.ConnectionInfo
	}
	identities := map[string]identity{}
	for _, step := range steps {
		if step.Status == "succeeded" || step.Status == "skipped" || step.Status == "failed" {
			continue
		}
		latest := latestAttempt(attempts, step.ID)
		if latest == nil || latest.FailureKind != string(nativecli.FailureContextOverflow) || latest.Recovery != "context_fresh" {
			continue
		}
		index := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == step.AgentID })
		if index < 0 {
			continue
		}
		member := run.Agents[index]
		value := identity{}
		if member.Executor != "" && e.Adapters != nil {
			value.adapter, _ = e.Adapters.Check(ctx, member.Executor)
			value.connection, _, _ = e.connectionInfo(ctx, member.Executor, member.Model, member.ThinkingLevel)
		}
		identities[step.ID] = value
	}
	var expiredResumeErr error
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(id)
		if err != nil {
			return err
		}
		// Re-check visibility inside the final transaction; a competing
		// soft-delete may have committed after the initial read.
		if _, err := tx.Thread(current.ThreadID); err != nil {
			return err
		}
		if current.Status == "queued" {
			run = current
			return nil
		}
		if current.Status != "interrupted" {
			return w.ErrConflict
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		for _, other := range active {
			if other.ThreadID == current.ThreadID && other.ID != current.ID {
				return w.ErrConflict
			}
		}
		workspace, err := tx.Workspace(current.WorkspaceID)
		if err != nil || workspace.Root != current.Workspace.Root {
			return errors.New("source directory authorization changed; start a new run")
		}
		if err := tools.ValidateSourceRoot(current.Workspace.Root, e.AllowedRoots); err != nil {
			return errors.New("source directory authorization changed; start a new run")
		}
		stored, err := tx.Steps(id)
		if err != nil {
			return err
		}
		storedAttempts, err := tx.Attempts(id)
		if err != nil {
			return err
		}
		now := time.Now()
		adoptedAt, adopted := adoptPolicy(&current, e.Limits, storedAttempts, now)
		policy := w.ExecutionPolicy{}
		if current.ExecutionPolicy != nil {
			policy = *current.ExecutionPolicy
		}
		if adoptStepClocks(stored, storedAttempts, policy, adoptedAt) {
			for _, step := range stored {
				if err := tx.SaveStep(step); err != nil {
					return err
				}
			}
		}
		expiryKind, expiryReason := "", ""
		if deadline := parseMoment(current.DeadlineAt); expired(deadline, now) {
			expiryKind, expiryReason = failureRunTimeout, "run_timeout: the frozen run deadline has expired; resume cannot extend it, start a new run"
		}
		if expiryKind == "" {
			for _, step := range stored {
				if step.Status == "succeeded" || step.Status == "skipped" || step.Status == "failed" {
					continue
				}
				if deadline := parseMoment(step.DeadlineAt); expired(deadline, now) {
					expiryKind, expiryReason = failureStepTimeout, "step_timeout: a frozen step deadline has expired; resume cannot extend it, start a new run"
					break
				}
			}
		}
		if expiryKind != "" {
			// An expired checkpoint becomes a durable failed outcome; leaving
			// the run interrupted would let a later resume retry past its budget.
			for _, step := range stored {
				if step.Status == "succeeded" || step.Status == "failed" || step.Status == "skipped" {
					continue
				}
				if expiryKind == failureRunTimeout || expired(parseMoment(step.DeadlineAt), now) {
					step.Status, step.FailureKind, step.Error = "failed", expiryKind, expiryReason
				} else {
					step.Status, step.FailureKind, step.Error = "skipped", "", "run ended because a frozen deadline expired"
				}
				if err := tx.SaveStep(step); err != nil {
					return err
				}
				if err := tx.AppendEvent(id, step.ID, step.AgentID, "step_finished", step); err != nil {
					return err
				}
			}
			if err := transitionMessages(tx, id, current.ThreadID, map[string]bool{"processing": true}, "interrupted", "message_finished"); err != nil {
				return err
			}
			current.Status = "failed"
			current.FailureKind = expiryKind
			current.Error = expiryReason
			current.FinishedAt = w.Now()
			if err := tx.TransitionRun("interrupted", current); err != nil {
				return err
			}
			if err := tx.AppendEvent(id, "", "", "run_finished", current); err != nil {
				return err
			}
			run = current
			expiredResumeErr = errors.New(expiryReason)
			return nil
		}
		for _, step := range stored {
			if step.Status == "failed" {
				return errors.New("a failed step cannot be resumed; start a new run")
			}
		}
		for _, step := range stored {
			if step.Status == "succeeded" || step.Status == "skipped" || step.Status == "failed" {
				continue
			}
			latest := latestAttempt(storedAttempts, step.ID)
			if latest == nil || latest.FailureKind != string(nativecli.FailureContextOverflow) || latest.Recovery != "context_fresh" {
				continue
			}
			if latest.CheckpointStart == "" || latest.CheckpointStart != latest.CheckpointDigest {
				continue
			}
			value := identities[step.ID]
			digest, digestErr := e.planDigestFor(tx, current, step, value.adapter, value.connection, latest.SourceIDs)
			if digestErr != nil {
				return fmt.Errorf("context_overflow_replay_unverified: %w", digestErr)
			}
			if latest.InputPlanDigest != "" && digest == latest.InputPlanDigest {
				return errors.New("context_overflow_replay_blocked: the fresh attempt exhausted the same input plan without new content; change the input plan or start a new run")
			}
		}
		for _, step := range stored {
			if step.Status != "succeeded" && step.Status != "skipped" {
				if err := e.validateMember(tx, current, step.AgentID); err != nil {
					return err
				}
			}
			if step.Status == "running" || step.Status == "interrupted" || step.Status == "canceled" {
				step.Status = "pending"
				step.Error = ""
				step.FailureKind = ""
				if err := tx.SaveStep(step); err != nil {
					return err
				}
			}
		}
		current.Status = "queued"
		current.Error = ""
		current.FailureKind = ""
		current.FinishedAt = ""
		current.RecoveryCount++
		current.RecoveredAt = w.Now()
		current.RecoveryNote = "manual_resume"
		if len(changes) > 0 {
			current.RecoveryNote += ";cli_changed:" + strings.Join(changes, ",")
			if err := tx.AppendEvent(id, "", "", "cli_changed", map[string]any{"executors": changes, "reason": "manual_resume"}); err != nil {
				return err
			}
		}
		if adopted {
			if err := tx.AppendEvent(id, "", "", "legacy_policy_adopted", map[string]any{"adopted_at": current.PolicyAdoptedAt, "policy": current.ExecutionPolicy}); err != nil {
				return err
			}
		}
		if err := tx.ReopenRun(current); err != nil {
			return err
		}
		run = current
		return tx.AppendEvent(id, "", "", "run_resumed", current)
	})
	if err != nil {
		return run, err
	}
	if expiredResumeErr != nil {
		return run, expiredResumeErr
	}
	return run, nil
}

func latestAttempt(attempts []w.Attempt, stepID string) *w.Attempt {
	var latest *w.Attempt
	for index := range attempts {
		if attempts[index].StepID != stepID {
			continue
		}
		if latest == nil || attempts[index].Number > latest.Number {
			candidate := attempts[index]
			latest = &candidate
		}
	}
	return latest
}
