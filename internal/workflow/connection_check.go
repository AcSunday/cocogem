package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/proc"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

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

func (e *Engine) connectionInfo(ctx context.Context, executor, model, thinking string) (agent.ConnectionInfo, bool, error) {
	if e.Adapters == nil {
		return agent.ConnectionInfo{}, false, nil
	}
	if inspector, ok := e.Adapters.(agent.ExecutionConnectionInspector); ok {
		info, err := inspector.InspectExecutionConnection(ctx, executor, model, thinking)
		return info, true, err
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
			connection, known, err := e.connectionInfo(ctx, member.Executor, member.Model, member.ThinkingLevel)
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
