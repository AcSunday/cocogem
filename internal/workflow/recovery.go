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
		stored, err := tx.Attempts(id)
		if err != nil {
			return err
		}
		for _, attempt := range stored {
			verdict, ok := verdicts[attempt.ID]
			if !ok || attempt.Status != "running" {
				continue
			}
			if current.Status == "canceling" {
				verdict.status = "canceled"
			}
			attempt.Status = verdict.status
			attempt.Error = verdict.reason
			attempt.Recovery = verdict.recovery
			attempt.FinishedAt = w.Now()
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(id, attempt.StepID, "", "attempt_finished", attempt); err != nil {
				return err
			}
		}
		finished, err := tx.Steps(id)
		if err != nil {
			return err
		}
		if current.Status == "canceling" {
			current.Status = "canceled"
			current.FinishedAt = w.Now()
			current.Error = ""
			if err := settlePending(tx, id, "canceled before restart recovery"); err != nil {
				return err
			}
			finished, err = tx.Steps(id)
			if err != nil {
				return err
			}
			for _, step := range finished {
				if step.Status == "running" || step.Status == "interrupted" {
					step.Status = "canceled"
					step.Error = "canceled by the user before restart recovery"
					if err := tx.SaveStep(step); err != nil {
						return err
					}
					if err := tx.AppendEvent(id, step.ID, step.AgentID, "step_finished", step); err != nil {
						return err
					}
				}
			}
			for _, message := range messages {
				if message.RunID == id && message.Status == "processing" {
					if err := tx.SetMessageStatus(message.ID, "canceled"); err != nil {
						return err
					}
				}
			}
			if err := tx.SaveRun(current); err != nil {
				return err
			}
			return tx.AppendEvent(id, "", "", "run_finished", current)
		}
		if blockedReason == "" {
			workspace, err := tx.Workspace(current.WorkspaceID)
			if err != nil || workspace.Root != current.Workspace.Root || tools.ValidateSourceRoot(current.Workspace.Root, e.AllowedRoots) != nil {
				blockedReason = "checkpoint_blocked: source directory authorization changed before recovery"
			}
		}
		if blockedReason == "" {
			for _, step := range finished {
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
			for _, step := range finished {
				if step.Status == "running" || step.Status == "canceled" {
					step.Status = "interrupted"
					if step.Error == "" {
						step.Error = "service restarted; checkpoint preserved"
					}
					if err := tx.SaveStep(step); err != nil {
						return err
					}
					if err := tx.AppendEvent(id, step.ID, step.AgentID, "step_finished", step); err != nil {
						return err
					}
				}
			}
			for _, message := range messages {
				if message.RunID == id && message.Status == "processing" {
					if err := tx.SetMessageStatus(message.ID, "interrupted"); err != nil {
						return err
					}
				}
			}
			current.Status = "interrupted"
			current.Error = blockedReason
			current.FinishedAt = w.Now()
			if err := tx.SaveRun(current); err != nil {
				return err
			}
			return tx.AppendEvent(id, "", "", "run_finished", current)
		}
		failed := ""
		canceled := ""
		unfinished := false
		for _, step := range finished {
			switch step.Status {
			case "failed":
				if failed == "" {
					failed = step.Error
				}
			case "canceled":
				canceled = step.Error
			case "pending", "running", "interrupted":
				unfinished = true
			case "skipped":
				if failed == "" {
					failed = step.Error
				}
			}
		}
		if !unfinished && (failed != "" || canceled != "") {
			status, cause := "failed", failed
			if failed == "" {
				status, cause = "canceled", canceled
			}
			if err := settlePending(tx, id, "run ended during restart recovery"); err != nil {
				return err
			}
			finished, err = tx.Steps(id)
			if err != nil {
				return err
			}
			for _, step := range finished {
				if step.Status != "running" && step.Status != "interrupted" {
					continue
				}
				step.Status = "interrupted"
				step.Error = "run ended during restart recovery"
				if status == "canceled" {
					step.Status = "canceled"
				}
				if err := tx.SaveStep(step); err != nil {
					return err
				}
				if err := tx.AppendEvent(id, step.ID, step.AgentID, "step_finished", step); err != nil {
					return err
				}
			}
			for _, message := range messages {
				if message.RunID == id && message.Status == "processing" {
					target := "interrupted"
					if status == "canceled" {
						target = "canceled"
					}
					if err := tx.SetMessageStatus(message.ID, target); err != nil {
						return err
					}
				}
			}
			current.Status = status
			current.Error = cause
			current.FinishedAt = w.Now()
			if err := tx.SaveRun(current); err != nil {
				return err
			}
			return tx.AppendEvent(id, "", "", "run_finished", current)
		}
		if !unfinished {
			current.Status = "succeeded"
			current.Error = ""
			current.FinishedAt = w.Now()
			if err := tx.SaveRun(current); err != nil {
				return err
			}
			return tx.AppendEvent(id, "", "", "run_finished", current)
		}
		for i := range finished {
			if finished[i].Status == "running" || finished[i].Status == "interrupted" || finished[i].Status == "canceled" {
				finished[i].Status = "pending"
				finished[i].Error = ""
				if err := tx.SaveStep(finished[i]); err != nil {
					return err
				}
			}
		}
		for _, message := range messages {
			if message.RunID == id && message.Status == "processing" {
				if err := tx.SetMessageStatus(message.ID, "interrupted"); err != nil {
					return err
				}
				preview, err := tx.MessagePreview(message.ID, 6000)
				if err != nil {
					return err
				}
				if err := tx.AppendEvent(id, message.StepID, message.AgentID, "message_finished", preview); err != nil {
					return err
				}
			}
		}
		current.Status = "queued"
		current.Error = ""
		current.FinishedAt = ""
		current.RecoveryCount++
		current.RecoveredAt = w.Now()
		current.RecoveryNote = "service_restart"
		if len(changes) > 0 {
			current.RecoveryNote = "service_restart;cli_changed:" + strings.Join(changes, ",")
		}
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		return tx.AppendEvent(id, "", "", "run_recovered", map[string]any{"reason": "service_restart", "changed_clis": changes, "attempts": len(verdicts)})
	})
	if err != nil {
		e.release(ctx, id)
		return err
	}
	e.release(ctx, id)
	return nil
}

// reconcileAttempt proves process ownership before any resume decision.
func (e *Engine) reconcileAttempt(attempt w.Attempt) attemptVerdict {
	if attempt.Process == nil || attempt.Process.PID <= 0 || attempt.Process.Start == "" {
		return attemptVerdict{status: "uncertain", reason: "service restarted without recorded process ownership; no process was signaled and manual confirmation is required", recovery: "manual", uncertain: true}
	}
	if attempt.Process.PGID <= 0 || attempt.Process.PGID != attempt.Process.PID {
		return attemptVerdict{status: "uncertain", reason: "owned process group cannot be verified; no process was signaled", recovery: "manual", uncertain: true}
	}
	if current, err := proc.Inspect(attempt.Process.PID); err == nil {
		if current.Start != attempt.Process.Start {
			return attemptVerdict{status: "uncertain", reason: "owned process identity no longer matches; no process was signaled", recovery: "manual", uncertain: true}
		}
	} else if !errors.Is(err, proc.ErrNotRunning) {
		return attemptVerdict{status: "uncertain", reason: "process state could not be verified: " + err.Error() + "; no process was signaled", recovery: "manual", uncertain: true}
	}
	members, err := proc.GroupMembers(attempt.Process.PGID)
	if err != nil {
		return attemptVerdict{status: "uncertain", reason: "owned process group state could not be verified: " + err.Error() + "; no process was signaled", recovery: "manual", uncertain: true}
	}
	if len(members) == 0 {
		return attemptVerdict{status: "interrupted", reason: "service restarted after the owned process group exited without a committed result; continuing from saved context", recovery: "new_attempt_from_context"}
	}
	owned := proc.Identity{PID: attempt.Process.PID, PGID: attempt.Process.PGID, Start: attempt.Process.Start}
	if err := proc.Terminate(owned, 3*time.Second); err != nil {
		return attemptVerdict{status: "uncertain", reason: "owned process group could not be stopped: " + err.Error(), recovery: "manual", uncertain: true}
	}
	if alive, err := proc.GroupAlive(attempt.Process.PGID); err != nil || alive {
		return attemptVerdict{status: "uncertain", reason: "owned process group cleanup could not be verified; no new attempt was started", recovery: "manual", uncertain: true}
	}
	return attemptVerdict{status: "interrupted", reason: "service restarted; the owned process group was stopped and its result is unknown; continuing from saved context", recovery: "owned_process_reclaimed"}
}

func (e *Engine) connectionInfo(ctx context.Context, executor, model string) (agent.ConnectionInfo, bool, error) {
	if e.Adapters == nil {
		return agent.ConnectionInfo{}, false, nil
	}
	if inspector, ok := e.Adapters.(agent.ConnectionInspector); ok {
		info, err := inspector.InspectConnection(ctx, executor, model)
		return info, true, err
	}
	if checker, ok := e.Adapters.(agent.ConnectionChecker); ok {
		return agent.ConnectionInfo{}, true, checker.CheckConnection(ctx, executor, model)
	}
	return agent.ConnectionInfo{}, false, nil
}

func connectionChanged(previous w.Attempt, current agent.ConnectionInfo, known bool) string {
	if !known || previous.Status == "blocked" {
		return ""
	}
	if previous.ConnectionID == "" {
		return connectionChangedPrefix + " previous attempt did not record the resolved model/upstream connection; start a new run or explicitly confirm the connection"
	}
	if current.Identity == "" {
		return connectionChangedPrefix + " current connection identity could not be verified; start a new run"
	}
	if previous.ConnectionID != current.Identity {
		was := previous.ResolvedModel
		if was == "" {
			was = previous.Model
		}
		return connectionChangedPrefix + " resolved model/upstream changed from " + was + " to " + current.ResolvedModel + "; start a new run or explicitly confirm the new connection"
	}
	return ""
}

// checkAdapters rechecks every executor used by unfinished work.
func (e *Engine) checkAdapters(ctx context.Context, run w.Run, steps []w.Step, attempts []w.Attempt) (string, []string) {
	if e.Adapters == nil {
		return "", nil
	}
	latest := map[string]w.Attempt{}
	for _, attempt := range attempts {
		if attempt.Status == "blocked" {
			continue
		}
		if old, ok := latest[attempt.StepID]; !ok || attempt.Number > old.Number {
			latest[attempt.StepID] = attempt
		}
	}
	changes := []string{}
	checkedAdapter := map[string]agent.AdapterInfo{}
	checkedConnection := map[string]bool{}
	connections := map[string]agent.ConnectionInfo{}
	connectionKnown := map[string]bool{}
	for _, step := range steps {
		if step.Status == "succeeded" || step.Status == "failed" || step.Status == "skipped" {
			continue
		}
		i := -1
		for index, member := range run.Agents {
			if member.ID == step.AgentID {
				i = index
				break
			}
		}
		if i < 0 || run.Agents[i].Executor == "" {
			continue
		}
		member := run.Agents[i]
		info, checked := checkedAdapter[member.Executor]
		if !checked {
			var err error
			info, err = e.Adapters.Check(ctx, member.Executor)
			if err != nil {
				reason := info.Reason
				if reason == "" {
					reason = err.Error()
				}
				return capabilityBlockedPrefix + " " + member.Executor + ": " + reason, changes
			}
			checkedAdapter[member.Executor] = info
		}
		key := member.Executor + "\x00" + member.Model
		if !checkedConnection[key] {
			connection, known, err := e.connectionInfo(ctx, member.Executor, member.Model)
			if err != nil {
				return connectionBlockedPrefix + " " + member.Executor + ": 连接配置不可用: " + err.Error(), changes
			}
			connections[key], connectionKnown[key] = connection, known
			checkedConnection[key] = true
		}
		if previous, ok := latest[step.ID]; ok {
			if reason := connectionChanged(previous, connections[key], connectionKnown[key]); reason != "" {
				return reason, changes
			}
		}
		if previous, ok := latest[step.ID]; ok && previous.CLIIdentity != "" && info.Identity != "" && previous.CLIIdentity != info.Identity {
			changes = append(changes, member.Executor)
		}
	}
	return "", changes
}

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
			value.connection, _, _ = e.connectionInfo(ctx, member.Executor, member.Model)
		}
		identities[step.ID] = value
	}
	var expiredResumeErr error
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(id)
		if err != nil {
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
