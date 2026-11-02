package workflow

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type dispatchedRun struct {
	id       string
	threadID string
}

func (e *Engine) maxParallelRuns() int {
	if e.MaxParallelRuns > 0 {
		return e.MaxParallelRuns
	}
	return DefaultMaxParallelRuns
}

// worker dispatches queued runs from independent conversations concurrently.
// A reservation map prevents two local workers from renewing the same run
// claim, and running thread ids keep a conversation serial even if legacy data
// contains more than one active run.
func (e *Engine) worker(ctx context.Context) {
	defer close(e.done)
	limit := e.maxParallelRuns()
	deferred := map[string]time.Time{}
	reserved := map[string]bool{}
	running := map[string]string{}
	finished := make(chan dispatchedRun, limit)
	var executing sync.WaitGroup
	drain := func() {
		for {
			select {
			case done := <-finished:
				delete(reserved, done.id)
				delete(running, done.threadID)
			default:
				return
			}
		}
	}
	for {
		drain()
		now := time.Now()
		for id, until := range deferred {
			if !now.Before(until) {
				delete(deferred, id)
			}
		}
		wake := e.Store.Changed()
		if ctx.Err() != nil {
			break
		}
		var next *w.Run
		err := e.Store.Read(ctx, func(tx *store.Tx) error {
			runs, err := tx.Active()
			if err != nil {
				return err
			}
			now := time.Now()
			for _, r := range runs {
				if r.Status != "queued" || reserved[r.ID] {
					continue
				}
				if _, busy := running[r.ThreadID]; busy {
					continue
				}
				if until, ok := deferred[r.ID]; ok && now.Before(until) {
					continue
				}
				copy := r
				next = &copy
				break
			}
			return nil
		})
		if err != nil {
			if ctx.Err() == nil {
				e.report(err)
			}
			break
		}
		if next == nil || len(running) >= limit {
			var retry <-chan time.Time
			if len(deferred) > 0 {
				retry = time.After(250 * time.Millisecond)
			}
			select {
			case <-ctx.Done():
			case <-wake:
			case done := <-finished:
				delete(reserved, done.id)
				delete(running, done.threadID)
			case <-retry:
				for id, until := range deferred {
					if time.Now().After(until) {
						delete(deferred, id)
					}
				}
			}
			continue
		}
		// Reserve locally before the durable claim: claim treats a repeated
		// owner as a renewable lease, so two workers must never race one run.
		reserved[next.ID] = true
		claimed, err := e.claim(ctx, next.ID)
		if err != nil {
			delete(reserved, next.ID)
			if ctx.Err() == nil {
				e.report(err)
			}
			break
		}
		if !claimed {
			// Another live server owns this run; try a later queued run first.
			delete(reserved, next.ID)
			deferred[next.ID] = time.Now().Add(2 * time.Second)
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		delete(deferred, next.ID)
		running[next.ThreadID] = next.ID
		executing.Add(1)
		go func(run w.Run) {
			defer executing.Done()
			e.executeRun(ctx, run)
			e.release(context.WithoutCancel(ctx), run.ID)
			finished <- dispatchedRun{id: run.ID, threadID: run.ThreadID}
		}(*next)
	}
	executing.Wait()
}

func (e *Engine) executeRun(parent context.Context, run w.Run) {
	// Queue time is excluded: the policy and absolute clock start only when a
	// worker actually dispatches this run.
	if err := e.Store.Read(parent, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		// A worker that wakes for a deleted conversation must not dispatch it.
		if _, err := tx.Thread(current.ThreadID); err != nil {
			return err
		}
		run = current
		return nil
	}); err != nil {
		if parent.Err() == nil {
			e.finish(parent, run, "failed", err)
		}
		return
	}
	if run.Status == "canceling" {
		e.finish(parent, run, "canceled", nil)
		return
	}
	if run.Status != "queued" {
		return
	}
	var history []w.Message
	var steps []w.Step
	var adopted bool
	// The policy, start and absolute deadline are adopted and persisted in the
	// same preflight transaction, before any deadline-bound context exists.
	err := e.Store.Write(parent, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Thread(current.ThreadID); err != nil {
			return err
		}
		if current.Status != "queued" {
			return w.ErrConflict
		}
		currentAttempts, err := tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		initialSteps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		now := time.Now()
		adoptedAt, adoptedNow := adoptPolicy(&current, e.Limits, currentAttempts, now)
		adopted = adoptedNow
		if adoptStepClocks(initialSteps, currentAttempts, *current.ExecutionPolicy, adoptedAt) {
			for _, initial := range initialSteps {
				if err := tx.SaveStep(initial); err != nil {
					return err
				}
			}
		}
		run = current
		if run.Delivery == nil {
			// A queued run accepted under the delivery contract starts with an
			// explicit empty delivery. A failure before it succeeds must not
			// look like legacy history that exposes draft artifacts. Legacy
			// runs already succeeded stay nil and are settled by recovery.
			run.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
		}
		if run.SkillSnapshot != "" {
			hash, skillErr := skills.SnapshotHash(run.Workspace.SkillIDs)
			if skillErr != nil || hash != run.SkillSnapshot {
				return errors.New("shared skill version changed before dispatch; start a new run")
			}
		}
		if e.Model == nil && len(e.Runners) == 0 && e.Fallback == nil {
			return errors.New("model is unavailable at dispatch")
		}
		if run.Input.ResearchScope == "web_discovery" && (e.Web == nil || !e.Web.SearchEnabled()) {
			return errors.New("online search is unavailable at dispatch")
		}
		if err := tools.ValidateSourceRoot(run.Workspace.Root, e.AllowedRoots); err != nil {
			return errors.New("source directory authorization was revoked")
		}
		ws, err := tx.Workspace(run.WorkspaceID)
		if err != nil {
			return err
		}
		if ws.Root != run.Workspace.Root {
			return errors.New("source binding changed before dispatch")
		}
		for _, initial := range initialSteps {
			if initial.Status == "pending" {
				if err := e.validateMember(tx, run, initial.AgentID); err != nil {
					return err
				}
			}
		}
		history, err = tx.MessagePreviews(run.ThreadID, e.historyPreviewBytes(run))
		if err != nil {
			return err
		}
		steps, err = tx.Steps(run.ID)
		if err != nil {
			return err
		}
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		kind := "run_started"
		if run.RecoveryCount > 0 {
			kind = "run_resumed"
		}
		if err := tx.AppendEvent(run.ID, "", "", kind, run); err != nil {
			return err
		}
		if adopted {
			return tx.AppendEvent(run.ID, "", "", "legacy_policy_adopted", map[string]any{"adopted_at": run.PolicyAdoptedAt, "policy": run.ExecutionPolicy})
		}
		return nil
	})
	if errors.Is(err, w.ErrConflict) {
		if current, readErr := e.currentRun(parent, run.ID); readErr == nil && current.Status == "canceling" {
			e.finish(parent, run, "canceled", nil)
		}
		return
	}
	if err != nil {
		if parent.Err() != nil {
			e.suspend(parent, run)
		} else {
			e.finish(parent, run, "failed", err)
		}
		return
	}
	if parent.Err() != nil {
		e.suspend(parent, run)
		return
	}
	deadline := runDeadline(run)
	var ctx context.Context
	var cancel context.CancelFunc
	if !deadline.IsZero() {
		ctx, cancel = context.WithDeadline(parent, deadline)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	e.mu.Lock()
	e.active[run.ID] = cancel
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.active, run.ID); e.mu.Unlock() }()
	if expired(deadline, time.Now()) {
		e.finish(parent, run, "failed", errors.New("run_timeout: the frozen run deadline elapsed before dispatch"))
		return
	}
	slog.Info("run started", "workspace_id", run.WorkspaceID, "thread_id", run.ThreadID, "run_id", run.ID)
	waiting := map[string]bool{}
	for {
		var current w.Run
		if err = e.Store.Read(ctx, func(tx *store.Tx) error {
			var readErr error
			if current, readErr = tx.Run(run.ID); readErr != nil {
				return readErr
			}
			steps, readErr = tx.Steps(run.ID)
			return readErr
		}); err != nil {
			break
		}
		// A planning step may have selected a workflow and persisted it while
		// this loop was running; later steps must see the effective plan input.
		run = current
		ready := []int{}
		pending := false
		skipped := false
		waitingNow := []w.Step{}
		for i, step := range steps {
			if step.Status != "pending" {
				continue
			}
			pending = true
			depsOK := true
			failed := false
			for _, id := range step.DependsOn {
				j := slices.IndexFunc(steps, func(s w.Step) bool { return s.ID == id })
				if j < 0 {
					failed, depsOK = true, false
					continue
				}
				dependency := steps[j]
				allowFailure := step.Kind == "continuation" && id != step.FromStepID
				switch dependency.Status {
				case "succeeded":
				case "failed", "skipped":
					if allowFailure {
						continue
					}
					failed, depsOK = true, false
				default:
					// An interrupted or canceled dependency keeps the dependent
					// pending for explicit resume; it is not a true failure.
					depsOK = false
				}
			}
			if failed {
				skipped = true
				steps[i].Status = "skipped"
				steps[i].Error = "upstream step did not succeed"
				if err := e.saveStep(ctx, steps[i], "step_finished"); err != nil {
					cancel()
				}
			} else if depsOK {
				ready = append(ready, i)
			} else {
				waitingNow = append(waitingNow, step)
			}
		}
		for _, step := range waitingNow {
			if waiting[step.ID] {
				continue
			}
			waiting[step.ID] = true
			if waitErr := e.Store.Write(ctx, func(tx *store.Tx) error {
				return tx.AppendEvent(run.ID, step.ID, step.AgentID, "step_waiting", map[string]any{"reason": "dependency", "depends_on": step.DependsOn})
			}); waitErr != nil && ctx.Err() == nil {
				e.report(waitErr)
			}
		}
		if !pending || ctx.Err() != nil {
			break
		}
		if len(ready) == 0 {
			if skipped {
				continue
			}
			break
		}
		// Every group receives the same committed snapshot; siblings cannot read half replies.
		var groupHistory []w.Message
		var groupSources []w.Source
		err = e.Store.Read(ctx, func(tx *store.Tx) error {
			var readErr error
			groupHistory, readErr = tx.MessagePreviews(run.ThreadID, e.historyPreviewBytes(run))
			if readErr != nil {
				return readErr
			}
			groupSources, readErr = tx.SourceMetadata(run.ThreadID)
			return readErr
		})
		if err != nil {
			cancel()
			break
		}
		dispatchCtx, stopDispatch := context.WithCancel(ctx)
		checkpoint := newCheckpointState(stopDispatch)
		var wg sync.WaitGroup
		for _, index := range ready {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				steps[i] = e.executeStepWith(dispatchCtx, run, steps[i], history, groupHistory, groupSources, checkpoint)
			}(index)
		}
		wg.Wait()
		stopDispatch()
		if checkpoint.triggered() {
			break
		}
	}
	if parent.Err() != nil {
		e.suspend(parent, run)
		return
	}
	runTimedOut := ctx.Err() == context.DeadlineExceeded
	for _, step := range steps {
		if step.Status == "failed" && step.FailureKind == failureRunTimeout {
			runTimedOut = true
		}
	}
	status, cause, kind := aggregateSteps(steps, runTimedOut)
	if kind != "" {
		run.FailureKind = kind
	}
	e.finish(parent, run, status, errorFrom(cause))
}

func (e *Engine) currentRun(ctx context.Context, id string) (w.Run, error) {
	var run w.Run
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var readErr error
		run, readErr = tx.Run(id)
		return readErr
	})
	return run, err
}
