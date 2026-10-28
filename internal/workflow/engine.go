package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Engine owns one durable FIFO worker and each run's cancellation context.
type Engine struct {
	Store         *store.Store
	Model         model.Model
	Runners       map[string]agent.Runner
	Fallback      agent.Runner
	Adapters      agent.AdapterChecker
	Web           tools.WebClient
	NativeProxy   string
	AllowedRoots  []string
	AllowedModels []string
	Limits        Limits
	InstanceID    string
	Owner         proc.Identity
	Errors        chan error

	mu     sync.Mutex
	active map[string]context.CancelFunc
	done   chan struct{}
}

// Start recovers work owned by a dead server before accepting new work.
func (e *Engine) Start(ctx context.Context) error {
	e.active = map[string]context.CancelFunc{}
	e.done = make(chan struct{})
	e.Errors = make(chan error, 1)
	if e.InstanceID == "" {
		e.InstanceID = w.NewID()
	}
	if e.Owner.PID == 0 {
		owner, err := proc.Self()
		if err != nil {
			return err
		}
		e.Owner = owner
	}
	if err := e.recoverActive(ctx); err != nil {
		return err
	}
	go e.worker(ctx)
	return nil
}

// Wait joins the scheduler and releases only this instance's dispatch leases.
func (e *Engine) Wait() {
	if e.done != nil {
		<-e.done
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.Store.Write(ctx, func(tx *store.Tx) error { return tx.ReleaseClaims(e.InstanceID) })
}

// Cancel persists intent before signaling the exact live execution.
func (e *Engine) Cancel(ctx context.Context, id string) (w.Run, error) {
	var result w.Run
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		run, err := tx.Run(id)
		if err != nil {
			return err
		}
		result = run
		if w.Terminal(run.Status) {
			return nil
		}
		if run.Status == "queued" {
			run.Status = "canceled"
			run.FinishedAt = w.Now()
			if err := settlePending(tx, run.ID, "canceled before dispatch"); err != nil {
				return err
			}
		} else {
			run.Status = "canceling"
		}
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		kind := "run_canceling"
		if run.Status == "canceled" {
			kind = "run_finished"
		}
		if err := tx.AppendEvent(id, "", "", kind, run); err != nil {
			return err
		}
		result = run
		return nil
	})
	if err != nil {
		return result, err
	}
	e.mu.Lock()
	cancel := e.active[id]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return result, nil
}

// CancelMember stops live runs that are executing a step for a member whose
// grant was just revoked. Revocation is persisted before this call, so a
// queued step fails validation at dispatch; a running native process must be
// stopped now, not after its next MCP call.
func (e *Engine) CancelMember(ctx context.Context, workspaceID, agentID string) error {
	// The permission write is already committed; stopping the native process
	// must not depend on the browser connection that delivered the revocation.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var ids []string
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		runs, err := tx.Active()
		if err != nil {
			return err
		}
		for _, run := range runs {
			if workspaceID != "" && run.WorkspaceID != workspaceID {
				continue
			}
			steps, err := tx.Steps(run.ID)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(steps, func(step w.Step) bool { return step.AgentID == agentID && step.Status == "running" }) {
				ids = append(ids, run.ID)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var cancelErr error
	for _, id := range ids {
		if _, err := e.Cancel(ctx, id); err != nil {
			cancelErr = errors.Join(cancelErr, err)
		}
	}
	return cancelErr
}

func (e *Engine) worker(ctx context.Context) {
	defer close(e.done)
	deferred := map[string]time.Time{}
	for {
		wake := e.Store.Changed()
		if ctx.Err() != nil {
			return
		}
		var next *w.Run
		err := e.Store.Read(ctx, func(tx *store.Tx) error {
			runs, err := tx.Active()
			if err != nil {
				return err
			}
			for _, r := range runs {
				if until, ok := deferred[r.ID]; ok && time.Now().Before(until) {
					continue
				}
				if r.Status == "queued" {
					copy := r
					next = &copy
					break
				}
			}
			return nil
		})
		if err != nil {
			if ctx.Err() == nil {
				e.report(err)
			}
			return
		}
		if next == nil {
			var retry <-chan time.Time
			if len(deferred) > 0 {
				retry = time.After(250 * time.Millisecond)
			}
			select {
			case <-ctx.Done():
				return
			case <-wake:
				continue
			case <-retry:
				for id, until := range deferred {
					if time.Now().After(until) {
						delete(deferred, id)
					}
				}
				continue
			}
		}
		claimed, err := e.claim(ctx, next.ID)
		if err != nil {
			if ctx.Err() == nil {
				e.report(err)
			}
			return
		}
		if !claimed {
			// Another live server owns this run; try a later queued run first.
			deferred[next.ID] = time.Now().Add(2 * time.Second)
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		delete(deferred, next.ID)
		e.executeRun(ctx, *next)
		e.release(context.WithoutCancel(ctx), next.ID)
	}
}

func (e *Engine) executeRun(parent context.Context, run w.Run) {
	// Queue time is excluded: the policy and absolute clock start only when a
	// worker actually dispatches this run.
	if err := e.Store.Read(parent, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
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
		history, err = tx.MessagePreviews(run.ThreadID, 6000)
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
			groupHistory, readErr = tx.MessagePreviews(run.ThreadID, 6000)
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

func (e *Engine) finish(parent context.Context, run w.Run, status string, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if w.Terminal(current.Status) {
			return nil
		}
		if current.Status == "canceling" {
			// User cancellation is committed before any later failure can settle.
			status, cause = "canceled", nil
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		hasFailed := false
		for _, step := range steps {
			if step.Status == "failed" {
				hasFailed = true
			}
		}
		if status == "interrupted" && hasFailed {
			status = "failed"
			cause = nil
		}
		if status == "succeeded" {
			cause = nil
		}
		if cause == nil && (status == "failed" || status == "interrupted") {
			_, aggregated, _ := aggregateSteps(steps, false)
			cause = errorFrom(aggregated)
		}
		if status != "interrupted" {
			if err := settlePending(tx, run.ID, "run ended before step started"); err != nil {
				return err
			}
		}
		for _, step := range steps {
			if step.Status != "running" {
				continue
			}
			switch status {
			case "interrupted":
				step.Status = "interrupted"
			case "canceled":
				step.Status = "canceled"
			default:
				step.Status = "failed"
			}
			step.Error = "step ended without a committed completion"
			if status == "succeeded" {
				step.Status = "failed"
				step.Error = "step ended without a committed completion"
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, step.AgentID, "step_finished", step); err != nil {
				return err
			}
		}
		attempts, err := tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attempt.Status != "running" {
				continue
			}
			switch status {
			case "interrupted":
				attempt.Status = "interrupted"
			case "canceled":
				attempt.Status = "canceled"
			default:
				attempt.Status = "failed"
			}
			attempt.Error = "attempt ended without a committed completion"
			attempt.FinishedAt = w.Now()
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, attempt.StepID, "", "attempt_finished", attempt); err != nil {
				return err
			}
		}
		from := map[string]bool{"processing": true}
		if status == "interrupted" {
			from["canceled"] = true
		}
		if err := transitionMessages(tx, run.ID, run.ThreadID, from, status, "message_finished"); err != nil {
			return err
		}
		kind := current.FailureKind
		if run.FailureKind != "" {
			kind = run.FailureKind
		}
		switch status {
		case "canceled", "succeeded":
			kind = ""
		case "failed":
			if cause != nil && strings.HasPrefix(cause.Error(), failureRunTimeout+":") {
				kind = failureRunTimeout
			} else if cause != nil && strings.HasPrefix(cause.Error(), failureStepTimeout+":") {
				kind = failureStepTimeout
			}
			if kind == "" || kind == failureCheckpointInterrupt {
				_, _, aggregatedKind := aggregateSteps(steps, false)
				kind = aggregatedKind
			}
			if kind == "" {
				kind = failureUnknown
			}
		case "interrupted":
			if kind == "" {
				_, _, aggregatedKind := aggregateSteps(steps, false)
				kind = aggregatedKind
			}
		}
		current.Status = status
		current.FailureKind = kind
		current.FinishedAt = w.Now()
		if cause != nil && (status == "failed" || status == "interrupted") {
			current.Error = cause.Error()
		} else {
			current.Error = ""
		}
		if status == "succeeded" {
			// Delivery and the terminal run state commit together; a run can
			// never be observed succeeded without its authoritative references.
			// Incomplete final results fail the run in this same transaction
			// instead of returning an error that leaves it running forever.
			delivery, deliveryErr := buildDelivery(tx, current)
			if deliveryErr != nil {
				status = "failed"
				current.Status = "failed"
				current.FailureKind = failureArtifactContract
				current.Error = "final delivery is incomplete: " + deliveryErr.Error()
				if current.Delivery == nil {
					current.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
				}
			} else {
				current.Delivery = delivery
				current.Method = settledMethod(current, steps)
			}
		}
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		if status == "succeeded" && current.SelectedWorkflow == nil && (current.Input.Workflow == "" || current.Input.Workflow == "free") {
			if err := tx.AppendEvent(run.ID, "", "", "method_recorded", map[string]any{"method": current.Method}); err != nil {
				return err
			}
		}
		return tx.AppendEvent(run.ID, "", "", "run_finished", current)
	})
	if err != nil {
		e.report(err)
	}
	if err == nil {
		slog.Info("run settled", "run_id", run.ID, "status", status)
	}
}

// transitionMessages changes only lifecycle fields, so a failure cleanup can
// never replace a chunked preview with an empty full-body save.
func transitionMessages(tx *store.Tx, runID, threadID string, from map[string]bool, to, kind string) error {
	messages, err := tx.MessagesMetadata(threadID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.RunID != runID || !from[message.Status] {
			continue
		}
		if err := tx.SetMessageStatus(message.ID, to); err != nil {
			return err
		}
		preview, err := tx.MessagePreview(message.ID, 6000)
		if err != nil {
			return err
		}
		if err := tx.AppendEvent(runID, message.StepID, message.AgentID, kind, preview); err != nil {
			return err
		}
	}
	return nil
}

// buildDelivery reconstructs the terminal delivery from the persisted plan and
// message lifecycle, never from timestamps or model self-descriptions. It is
// called only inside the transaction that commits a successful run. Every
// designated final target must be complete and contribute at least one actual
// committed reference; an incomplete delivery is an error, never a nil success.
func buildDelivery(tx *store.Tx, run w.Run) (*w.Delivery, error) {
	steps, err := tx.Steps(run.ID)
	if err != nil {
		return nil, err
	}
	finals := finalSteps(effectiveInput(run), steps)
	if len(finals) == 0 {
		return nil, errors.New("no final delivery target")
	}
	messages, err := tx.MessagesMetadata(run.ThreadID)
	if err != nil {
		return nil, err
	}
	artifacts, err := tx.ArtifactsMetadata(run.ThreadID)
	if err != nil {
		return nil, err
	}
	delivery := &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
	for _, step := range finals {
		if step.Status != "succeeded" {
			return nil, fmt.Errorf("final target %s (%s) is %s", step.AgentName, step.Role, step.Status)
		}
		committed := 0
		for _, message := range messages {
			// A retry leaves earlier failed attempts in history; only a completed
			// message from the designated final step is a delivery reference.
			if message.RunID != run.ID || message.StepID != step.ID || message.Status != "completed" {
				continue
			}
			visible, err := messageHasVisibleText(tx, message)
			if err != nil {
				return nil, err
			}
			// A runner that finishes without emitting text leaves an empty
			// completed message; it is not a final result. A final artifact can
			// still carry the delivery on its own.
			if !visible {
				continue
			}
			delivery.MessageIDs = append(delivery.MessageIDs, message.ID)
			committed++
		}
		for _, artifact := range artifacts {
			if artifact.RunID == run.ID && artifact.StepID == step.ID {
				delivery.ArtifactRefs = append(delivery.ArtifactRefs, w.ArtifactRef{ID: artifact.ID, Version: artifact.Version})
				committed++
			}
		}
		if committed == 0 {
			return nil, fmt.Errorf("final target %s (%s) has no committed message or artifact", step.AgentName, step.Role)
		}
	}
	return delivery, nil
}

// messageHasVisibleText reports whether a completed message carries at least
// one non-whitespace byte. The metadata projection never includes the body, so
// a bounded preview decides the common case and paging handles a body whose
// visible text starts after the preview.
func messageHasVisibleText(tx *store.Tx, message w.Message) (bool, error) {
	preview, err := tx.MessagePreview(message.ID, 6000)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(preview.Content) != "" {
		return true, nil
	}
	if preview.ContentBytes <= len(preview.Content) {
		return false, nil
	}
	// shortcut: only the first 2 MiB of a body is inspected, so an all-blank
	// prefix longer than that is treated as empty; raise the budget if model
	// output of that shape becomes real.
	const scanBudget = 2 << 20
	scanned, cursor := len(preview.Content), ""
	for scanned < scanBudget {
		page, err := tx.MessageContent(message.ID, cursor, store.MaxContentChunks)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(page.Content) != "" {
			return true, nil
		}
		if !page.HasMore || page.Content == "" {
			return false, nil
		}
		scanned += len(page.Content)
		cursor = page.NextCursor
	}
	return false, nil
}

// mayPublish reports whether this step may submit a formal artifact. Built-ins
// reserve publication for the designated final role. A free run allows its roots
// and the final continuation of each root chain; delegated tasks and earlier
// continuations are rejected before a draft can be created. A root's artifact
// is kept as an attempt draft if the root later delegates.
func (e *Engine) mayPublish(ctx context.Context, run w.Run, step w.Step) (bool, error) {
	current := run
	if run.SelectedWorkflow == nil && step.Kind == "" && (run.Input.Workflow == "" || run.Input.Workflow == "free") {
		// The planning root may select a method during this same invocation, so
		// its publication authority follows the persisted run, not the stale
		// dispatch snapshot.
		if err := e.Store.Read(ctx, func(tx *store.Tx) error {
			var err error
			current, err = tx.Run(run.ID)
			return err
		}); err != nil {
			return false, err
		}
	}
	in := effectiveInput(current)
	if RequiredArtifact(step, in) != "" {
		return true, nil
	}
	if in.Workflow != "" && in.Workflow != "free" {
		return false, nil
	}
	if step.Kind == "" {
		return true, nil
	}
	if step.Kind != "continuation" {
		return false, nil
	}
	var steps []w.Step
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(run.ID)
		return err
	}); err != nil {
		return false, err
	}
	// Only the root chain's final continuation may publish; an inner
	// continuation belongs to a delegated task and stays a draft.
	return slices.ContainsFunc(finalSteps(in, steps), func(final w.Step) bool {
		return final.ID == step.ID
	}), nil
}

// suspend persists a recoverable checkpoint without pretending the run ended.
func (e *Engine) suspend(parent context.Context, run w.Run) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	var canceled bool
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if w.Terminal(current.Status) {
			return nil
		}
		if current.Status == "canceling" {
			canceled = true
			return nil
		}
		if current.Status == "queued" {
			return nil
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.Status != "running" && step.Status != "canceled" {
				continue
			}
			step.Status = "interrupted"
			step.Error = "service suspended before step completion; will resume"
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, step.AgentID, "step_finished", step); err != nil {
				return err
			}
		}
		attempts, err := tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attempt.Status != "running" && attempt.Status != "canceled" {
				continue
			}
			attempt.Status = "interrupted"
			attempt.Error = "service suspended before the attempt settled"
			attempt.Recovery = "new_attempt_from_context"
			attempt.FinishedAt = w.Now()
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, attempt.StepID, "", "attempt_finished", attempt); err != nil {
				return err
			}
		}
		from := map[string]bool{"processing": true, "canceled": true}
		if err := transitionMessages(tx, run.ID, run.ThreadID, from, "interrupted", "message_finished"); err != nil {
			return err
		}
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		return tx.AppendEvent(run.ID, "", "", "run_suspended", map[string]any{"reason": "service_stop"})
	})
	if canceled {
		e.finish(context.Background(), run, "canceled", nil)
		return
	}
	if err != nil {
		e.report(err)
	}
}

func (e *Engine) saveStep(ctx context.Context, s w.Step, kind string) error {
	return e.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveStep(s); err != nil {
			return err
		}
		return tx.AppendEvent(s.RunID, s.ID, s.AgentID, kind, s)
	})
}

func (e *Engine) executeStep(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source) w.Step {
	return e.executeStepWith(ctx, run, step, history, all, sourceSnapshot, nil)
}

func (e *Engine) executeStepWith(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source, checkpoint *checkpointState) w.Step {
	memberIndex := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == step.AgentID })
	if memberIndex < 0 {
		step.Status, step.Error = "failed", "member is outside this run roster"
		step.FailureKind = failureUnknown
		if err := e.saveStep(ctx, step, "step_finished"); err != nil {
			e.report(err)
		}
		return step
	}
	a := run.Agents[memberIndex]
	var adapter agent.AdapterInfo
	var connection agent.ConnectionInfo
	connectionKnown := false
	connectionBlocked := false
	if a.Executor != "" && e.Adapters != nil {
		var checkErr error
		adapter, checkErr = e.Adapters.Check(ctx, a.Executor)
		if checkErr != nil && !adapter.Installed {
			adapter.Reason = checkErr.Error()
		}
		if adapter.Supported {
			var connectionErr error
			connection, connectionKnown, connectionErr = e.connectionInfo(ctx, a.Executor, a.Model)
			if connectionErr != nil {
				adapter.Supported = false
				adapter.Reason = "连接配置不可用: " + connectionErr.Error()
				connectionBlocked = true
			}
		}
	}
	allowed := e.allowedTools(run, a)
	required := RequiredArtifact(step, effectiveInput(run))
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: step.ID, AgentID: a.ID, AgentName: a.DisplayName, Role: "assistant", Status: "processing", ContentFormat: w.ContentFormatChunksV1, CreatedAt: w.Now()}
	step.Status = "running"
	var handoff Handoff
	var attempt w.Attempt
	var scope *readScope
	var stepDeadline time.Time
	var previousDraft *w.Artifact
	var retiredSession string
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return errors.New("run stopped before dispatch")
		}
		if err := e.validateMember(tx, run, step.AgentID); err != nil {
			return err
		}
		existing, err := tx.StepAttempts(step.ID)
		if err != nil {
			return err
		}
		number := len(existing) + 1
		reason := "initial"
		recovery := ""
		recoveredFrom := ""
		if len(existing) > 0 {
			latest := existing[len(existing)-1]
			reason = "retry_after_interruption"
			recovery = "new_attempt_from_context"
			recoveredFrom = latest.ID
			if latest.Status == "failed" && latest.FailureKind == string(nativecli.FailureContextOverflow) {
				reason = "context_fresh"
				recovery = "context_fresh"
				retiredSession = latest.SessionID
			}
			previousDraft = latest.DraftArtifact
		}
		connectionChange := ""
		for i := len(existing) - 1; i >= 0; i-- {
			if existing[i].Status == "blocked" {
				continue
			}
			connectionChange = connectionChanged(existing[i], connection, connectionKnown)
			break
		}
		policy := w.ExecutionPolicy{}
		if run.ExecutionPolicy != nil {
			policy = *run.ExecutionPolicy
		}
		dispatchNow := time.Now()
		deadline, alreadyExpired := applyStepDeadline(&step, existing, policy, dispatchNow)
		attempt = w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: step.ID, Number: number, Status: "running", Reason: reason, Recovery: recovery, RecoveredFrom: recoveredFrom, SourceIDs: sourceIDs(sourceSnapshot), StartedAt: dispatchNow.UTC().Format(time.RFC3339Nano), LastProgress: dispatchNow.UTC().Format(time.RFC3339Nano), Executor: a.Executor, Model: a.Model, ResolvedModel: connection.ResolvedModel, ConnectionID: connection.Identity, Adapter: adapter.Adapter, CLIVersion: adapter.Version, CLIProgram: adapter.Program, CLIIdentity: adapter.Identity, SessionMode: "fresh"}
		// The message belongs to this exact attempt, so a retry cannot make the
		// UI attribute it to a newer attempt's model.
		message.AttemptID, message.Model, message.ResolvedModel = attempt.ID, attempt.Model, attempt.ResolvedModel
		if a.Executor != "" && e.Adapters != nil && (!adapter.Supported || connectionChange != "") {
			reason := adapter.Reason
			if reason == "" {
				reason = "adapter compatibility could not be confirmed"
			}
			attempt.Status = "blocked"
			switch {
			case connectionChange != "":
				attempt.Error = connectionChange
			case connectionBlocked:
				attempt.Error = connectionBlockedPrefix + " " + a.Executor + ": " + reason
			default:
				attempt.Error = capabilityBlockedPrefix + " " + a.Executor + ": " + reason
			}
			step.Status = "interrupted"
			if ctx.Err() != nil {
				attempt.Status = "canceled"
				attempt.Error = ctx.Err().Error()
				step.Status = "canceled"
			}
			attempt.FailureKind = "capability_blocked"
			if strings.HasPrefix(attempt.Error, connectionBlockedPrefix) || strings.HasPrefix(attempt.Error, connectionChangedPrefix) {
				attempt.FailureKind = "connection_blocked"
			}
			attempt.FinishedAt = w.Now()
			step.Error = attempt.Error
			step.FailureKind = attempt.FailureKind
			step.AttemptCount = number
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", attempt)
		}
		// A blocked preflight never reaches this point, so this is the first
		// actual dispatch of the logical step and its clock starts here.
		if step.StartedAt == "" {
			startStepClock(&step, policy, dispatchNow)
			deadline = parseMoment(step.DeadlineAt)
			alreadyExpired = expired(deadline, dispatchNow)
		}
		stepDeadline = deadline
		if len(step.DependsOn) > 0 || len(step.ContextRefs) > 0 {
			artifacts, err := tx.ArtifactsMetadata(run.ThreadID)
			if err != nil {
				return err
			}
			// The planning root is a scheduling dependency only. Its message and
			// artifacts are withheld from the handoff so a planner that answered
			// the task anyway cannot seed the independent views.
			planningRoot := planningRootStep(run)
			dependentArtifacts := []w.Artifact{}
			for _, artifact := range artifacts {
				if artifact.RunID != run.ID || artifact.StepID == planningRoot || !slices.Contains(step.DependsOn, artifact.StepID) {
					continue
				}
				preview, err := tx.ArtifactPreview(artifact.ID, artifact.Version, 6000)
				if err != nil {
					return err
				}
				dependentArtifacts = append(dependentArtifacts, preview)
			}
			messages := all
			if planningRoot != "" {
				messages = slices.DeleteFunc(slices.Clone(all), func(m w.Message) bool { return m.StepID == planningRoot })
			}
			handoff = prepareHandoff(step, run.Input.Message, messages, dependentArtifacts, sourceSnapshot)
			if err := addReferencedInputs(tx, step, run.ThreadID, &handoff); err != nil {
				return err
			}
			if err := sealHandoff(tx, &handoff); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "handoff_prepared", handoff); err != nil {
				return err
			}
		}
		selected := recentHistory(history, run.ID, 12, 16000)
		historyIDs := make([]string, 0, len(selected))
		for _, message := range selected {
			historyIDs = append(historyIDs, message.ID)
		}
		scope, err = buildReadScope(tx, run, step, historyIDs, sourceSnapshot)
		if err != nil {
			return err
		}
		attempt.InputPlanDigest, err = inputPlanDigest(tx, step, run, a.Model, connection, adapter, scope, allowed, sourceSnapshot)
		if err != nil {
			return err
		}
		attempt.CheckpointStart, err = checkpointDigest(tx, run.ID, run.ThreadID, step.ID, attempt.SourceIDs, previousDraft)
		if err != nil {
			return err
		}
		if alreadyExpired {
			attempt.Status = "failed"
			attempt.FailureKind = failureStepTimeout
			attempt.Error = "step_timeout: the frozen step deadline elapsed before dispatch"
			attempt.FinishedAt = w.Now()
			step.Status = "failed"
			step.FailureKind = failureStepTimeout
			step.Error = attempt.Error
			step.AttemptCount = number
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", attempt); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "step_finished", step)
		}
		step.AttemptCount = number
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "step_started", step); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
			return err
		}
		if recovery == "context_fresh" && retiredSession != "" {
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "session_retired", map[string]any{"attempt_id": recoveredFrom, "session_id": retiredSession}); err != nil {
				return err
			}
		}
		return tx.AppendEvent(run.ID, step.ID, a.ID, "message_started", message)
	})
	if err != nil {
		step.Status = "failed"
		step.FailureKind = failureUnknown
		step.Error = err.Error()
		if saveErr := e.saveStep(context.WithoutCancel(ctx), step, "step_finished"); saveErr != nil {
			e.report(saveErr)
		}
		return step
	}
	if attempt.Status == "blocked" {
		return step
	}
	if step.Status == "failed" {
		// A frozen step deadline already elapsed; no CLI was launched.
		return step
	}
	var stepCtx context.Context
	var stopStep context.CancelFunc
	if !stepDeadline.IsZero() {
		stepCtx, stopStep = context.WithDeadline(ctx, stepDeadline)
	} else {
		stepCtx, stopStep = context.WithCancel(ctx)
	}
	defer stopStep()
	var internalFailure atomic.Bool
	writer := newMessageWriter(e.Store, stepCtx, 100*time.Millisecond, run.ID, step.ID, a.ID, attempt.ID, message, func(err error) {
		internalFailure.Store(true)
		e.report(err)
		stopStep()
	})
	var stateMu sync.Mutex
	var execWG sync.WaitGroup
	execClosed := false
	sources := slices.Clone(sourceSnapshot)
	nativeCaptured := map[string]bool{}
	// A previous attempt's draft is recovery evidence for the prompt and the
	// checkpoint digest only. It is never published on behalf of the resumed
	// step: the model must re-read, revalidate and submit it again.
	var published *w.Artifact
	publishAttempts := 0
	delegated := false
	capture := func(source w.Source) (w.Source, error) {
		source.ID = w.NewID()
		source.WorkspaceID = run.WorkspaceID
		source.ThreadID = run.ThreadID
		source.RunID = run.ID
		source.CaptureStepID = step.ID
		source.ContentDigest = contentDigest(source.Content)
		metadata := source
		metadata.Content = ""
		err := e.Store.Write(ctx, func(tx *store.Tx) error {
			if err := tx.SaveSource(source); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "source_captured", metadata)
		})
		if err == nil {
			stateMu.Lock()
			// Only bounded metadata is retained in memory; exact bodies are
			// read by ID from the store when a tool explicitly requests them.
			sources = append(sources, metadata)
			stateMu.Unlock()
		}
		return source, err
	}
	execute := func(callCtx context.Context, name string, input json.RawMessage) (string, error) {
		stateMu.Lock()
		if execClosed {
			stateMu.Unlock()
			return "", errors.New("step is no longer accepting tool calls")
		}
		execWG.Add(1)
		stateMu.Unlock()
		defer execWG.Done()
		if permissionErr := e.Store.Read(callCtx, func(tx *store.Tx) error { return e.validateMember(tx, run, step.AgentID) }); permissionErr != nil {
			return "", permissionErr
		}
		switch name {
		case "read_skill":
			return e.readSkill(callCtx, run, step, input)
		case "read_context":
			return e.readContext(callCtx, run, step, scope, input)
		case "handoff_task":
			result, handoffErr := e.submitHandoff(callCtx, run, step, input)
			if handoffErr == nil {
				stateMu.Lock()
				delegated = true
				stateMu.Unlock()
			}
			return result, handoffErr
		case "get_task":
			return e.getTask(callCtx, run, input)
		case "choose_workflow":
			return e.chooseWorkflow(callCtx, run, step, input)
		case "read_source":
			stateMu.Lock()
			snapshot := slices.Clone(sources)
			stateMu.Unlock()
			return e.readSource(callCtx, run, snapshot, input)
		case "list_sources":
			stateMu.Lock()
			snapshot := slices.Clone(sources)
			stateMu.Unlock()
			return e.listSources(callCtx, run, snapshot, input)
		}
		if name == "web_search" && run.Input.ResearchScope != "web_discovery" && run.Input.ResearchScope != "auto" {
			return "", errors.New("web discovery is outside this run scope")
		}
		if name == "fetch_url" && run.Input.ResearchScope != "web_discovery" && run.Input.ResearchScope != "auto" {
			var in struct {
				URL string `json:"url"`
			}
			if err := tools.Decode(input, &in); err != nil {
				return "", err
			}
			stateMu.Lock()
			snapshot := slices.Clone(sources)
			stateMu.Unlock()
			if !slices.Contains(run.Input.SourceURLs, in.URL) && !slices.ContainsFunc(snapshot, func(s w.Source) bool { return s.URL == in.URL }) {
				return "", errors.New("URL is outside the selected given-source research scope")
			}
		}
		if name != "publish_artifact" {
			return tools.Execute(callCtx, name, input, tools.Scope{Root: run.Workspace.Root, Web: e.Web, Capture: capture})
		}
		allowedPublisher, permissionErr := e.mayPublish(callCtx, run, step)
		if permissionErr != nil {
			return "", permissionErr
		}
		if !allowedPublisher {
			return "", errors.New("intermediate steps cannot publish a formal artifact; finish this step and let the final delivery step publish")
		}
		stateMu.Lock()
		if publishAttempts >= 4 {
			stateMu.Unlock()
			return "", errors.New("publication retry budget exceeded; finish this step with the accepted or previous valid content")
		}
		publishAttempts++
		stateMu.Unlock()
		var payload struct {
			Type        string      `json:"type"`
			Title       string      `json:"title"`
			Body        string      `json:"body"`
			Findings    []w.Finding `json:"findings"`
			SourceIDs   []string    `json:"source_ids"`
			Limitations []string    `json:"limitations"`
		}
		if err := tools.Decode(input, &payload); err != nil {
			return "", err
		}
		artifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: step.ID, Type: payload.Type, Title: payload.Title, Body: payload.Body, Findings: payload.Findings, SourceIDs: payload.SourceIDs, Limitations: payload.Limitations, Feedback: "unreviewed", CreatedAt: w.Now()}
		stateMu.Lock()
		knownSources := slices.Clone(sources)
		stateMu.Unlock()
		ensureNativeObservationDisclosure(&artifact, knownSources)
		if err := ValidateArtifact(artifact, required, knownSources); err != nil {
			return "", err
		}
		// Persist the unpublished payload on the attempt immediately; a process
		// interruption must not turn a submitted draft back into guessable text.
		draft := artifact
		draftCtx, draftCancel := context.WithTimeout(context.WithoutCancel(callCtx), 5*time.Second)
		draftErr := e.Store.Write(draftCtx, func(tx *store.Tx) error {
			current, err := tx.Attempt(attempt.ID)
			if err != nil {
				return err
			}
			current.DraftArtifact = &draft
			return tx.SaveAttempt(current)
		})
		draftCancel()
		if draftErr != nil {
			return "", draftErr
		}
		stateMu.Lock()
		published = &artifact
		attempt.DraftArtifact = &draft
		stateMu.Unlock()
		return `{"accepted":true,"publication":"after_step_success"}`, nil
	}
	observe := func(kind string, data map[string]any) error {
		if stringValue(data["origin"]) == "native" && stepCtx.Err() != nil {
			// Revocation, user cancel or a frozen deadline already stopped
			// this step; native output emitted afterwards is not evidence.
			return nil
		}
		if kind == "tool_started" || kind == "tool_finished" {
			slog.Info(kind, "run_id", run.ID, "step_id", step.ID, "agent_id", a.ID, "tool_call_id", data["tool_call_id"], "tool", data["name"], "status", data["status"])
		}
		if kind == "tool_finished" && stringValue(data["origin"]) == "native" {
			callID := stringValue(data["tool_call_id"])
			stateMu.Lock()
			duplicate := callID != "" && nativeCaptured[callID]
			if callID != "" {
				nativeCaptured[callID] = true
			}
			stateMu.Unlock()
			if !duplicate {
				if source, ok := nativeSource(run, data); ok {
					saved, err := capture(source)
					if err != nil {
						return err
					}
					data["source_id"] = saved.ID
				}
			}
			data = nativeEventPayload(data)
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		if execClosed {
			return nil
		}
		if writer.SnapshotError() != nil {
			return writer.SnapshotError()
		}
		switch kind {
		case "text":
			text := stringValue(data["text"])
			if text == "" {
				return nil
			}
			// The adapter decides whether a frame is real activity; a missing
			// flag (test runner, non-native loop) falls back to non-blank text.
			activity, explicit := data["activity"].(bool)
			if !explicit {
				activity = strings.TrimSpace(text) != ""
			}
			if activity {
				return writer.Append(text)
			}
			// Non-progress text is still persisted exactly, just not credited
			// as liveness.
			return writer.AppendDuplicate(text)
		case "reasoning":
			// Reasoning content never enters storage; only its support flag.
			supported, _ := data["supported"].(bool)
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				return tx.AppendEvent(run.ID, step.ID, a.ID, "reasoning", map[string]any{"supported": supported})
			})
		case "native_activity":
			activityKind := stringValue(data["kind"])
			if activityKind != "reasoning" && activityKind != "text_delta" {
				return errors.New("native_activity kind must be reasoning or text_delta")
			}
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				if err := touchAttempt(tx, attempt.ID, w.Now()); err != nil {
					return err
				}
				return tx.AppendEvent(run.ID, step.ID, a.ID, "native_activity", map[string]any{"kind": activityKind})
			})
		case "liveness_warning":
			warning := &w.LivenessWarning{
				State:          stringValue(data["state"]),
				SilenceSeconds: max(0, intValue(data["silence_seconds"])),
				LastEventType:  stringValue(data["last_event_type"]),
				ProcessAlive:   boolValue(data["process_alive"]),
				ToolActive:     boolValue(data["tool_active"]),
				ObservedAt:     w.Now(),
			}
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				current, err := tx.Attempt(attempt.ID)
				if err != nil {
					return err
				}
				current.LivenessWarning = warning
				if err := tx.SaveAttempt(current); err != nil {
					return err
				}
				attempt = current
				return tx.AppendEvent(run.ID, step.ID, a.ID, "liveness_warning", warning)
			})
		case "usage":
			known, _ := data["known"].(bool)
			turn := stringValue(data["semantics"]) == "turn"
			input, output := int64(intValue(data["input_tokens"])), int64(intValue(data["output_tokens"]))
			// Native usage is emitted after cancellation/stall cleanup, so the
			// write cannot depend on the stopped step context.
			usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer usageCancel()
			return e.Store.Write(usageCtx, func(tx *store.Tx) error {
				current, err := tx.Attempt(attempt.ID)
				if err != nil {
					return err
				}
				if turn {
					if known {
						current.InputTokens += input
						current.OutputTokens += output
						if current.UsageCompleteness == "" || current.UsageCompleteness == "known" {
							current.UsageCompleteness = "known"
						} else {
							current.UsageCompleteness = "partial"
						}
					} else if current.UsageCompleteness == "known" || current.InputTokens > 0 || current.OutputTokens > 0 {
						current.UsageCompleteness = "partial"
					} else {
						current.UsageCompleteness = "unknown"
					}
				} else if known {
					current.InputTokens, current.OutputTokens = input, output
					current.UsageCompleteness = "known"
				} else if input > 0 || output > 0 {
					// The native CLI can report one side of a cumulative pair.
					if input > 0 {
						current.InputTokens = input
					}
					if output > 0 {
						current.OutputTokens = output
					}
					current.UsageCompleteness = "partial"
				} else if current.UsageCompleteness == "" {
					current.UsageCompleteness = "unknown"
				}
				current.UsageKnown = current.UsageCompleteness == "known"
				if err := tx.SaveAttempt(current); err != nil {
					return err
				}
				attempt = current
				return tx.AppendEvent(run.ID, step.ID, a.ID, "usage_updated", data)
			})
		case "native_adapter", "native_session", "native_process", "tool_started", "tool_finished":
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				current, err := tx.Attempt(attempt.ID)
				if err != nil {
					return err
				}
				changed := false
				if identity, _ := data["identity"].(string); identity != "" && current.CLIIdentity != "" && current.CLIIdentity != identity {
					changed = true
				}
				switch kind {
				case "native_adapter":
					current.CLIVersion, _ = data["version"].(string)
					current.CLIProgram, _ = data["program"].(string)
					current.CLIIdentity, _ = data["identity"].(string)
					current.Adapter, _ = data["adapter"].(string)
				case "native_session":
					session := stringValue(data["session_id"])
					if attempt.Recovery == "context_fresh" && retiredSession != "" && session == retiredSession {
						return &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: "context_fresh_reused_session: the native CLI reused the retired session; fresh recovery was not confirmed"}
					}
					current.SessionID = session
					current.SessionMode, _ = data["mode"].(string)
				case "native_process":
					current.Process = &w.ProcessRef{PID: intValue(data["pid"]), PGID: intValue(data["pgid"]), Start: stringValue(data["start"]), Executable: stringValue(data["executable"])}
				}
				if kind == "tool_started" || kind == "tool_finished" {
					current.LastProgress = w.Now()
					current.LivenessWarning = nil
				}
				if err := tx.SaveAttempt(current); err != nil {
					return err
				}
				attempt = current
				if changed {
					if err := tx.AppendEvent(run.ID, step.ID, a.ID, "cli_changed", map[string]any{"executor": current.Executor, "previous_identity": attempt.CLIIdentity, "current_identity": current.CLIIdentity, "version": current.CLIVersion}); err != nil {
						return err
					}
				}
				return tx.AppendEvent(run.ID, step.ID, a.ID, kind, data)
			})
		default:
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				return tx.AppendEvent(run.ID, step.ID, a.ID, kind, data)
			})
		}
	}
	request, requestErr := e.buildRequest(stepCtx, run, step, a, allowed, required, connection, adapter, message, handoff, scope, history, all, sourceSnapshot, previousDraft, attempt)
	if requestErr != nil {
		err = requestErr
	} else if err = stepCtx.Err(); err != nil {
		// A checkpoint, user cancel or frozen deadline arrived before launch.
	} else if a.Executor != "" {
		runner := e.Runners[a.Executor]
		if runner == nil {
			runner = e.Fallback
		}
		if runner == nil {
			err = errors.New(capabilityBlockedPrefix + " " + a.Executor + ": selected CLI executor is unavailable")
		} else {
			err = runner.Run(stepCtx, request, execute, observe)
		}
	} else {
		err = (agent.Loop{Model: e.Model}).Run(stepCtx, request, execute, observe)
	}
	stateMu.Lock()
	execClosed = true
	stateMu.Unlock()
	execWG.Wait()
	if closeErr := writer.Close(); closeErr != nil && err == nil {
		err = closeErr
	} else if closeErr != nil {
		e.report(closeErr)
	}
	message = writer.Snapshot()
	stateMu.Lock()
	publishedCopy := published
	delegatedCopy := delegated
	knownSources := slices.Clone(sources)
	stateMu.Unlock()
	if err == nil && required != "" && publishedCopy == nil && !delegatedCopy {
		err = &nativecli.Failure{Kind: nativecli.FailureKind(failureArtifactContract), Reason: "required artifact was not submitted"}
	}
	runExpired := ctx.Err() == context.DeadlineExceeded
	stepDeadlineExpired := !stepDeadline.IsZero() && !time.Now().Before(stepDeadline) && errors.Is(stepCtx.Err(), context.DeadlineExceeded)
	if err == nil && stepCtx.Err() != nil {
		err = stepCtx.Err()
	}
	outcome := stepOutcome{step: "succeeded", attempt: "succeeded"}
	if err != nil {
		checkpointed := checkpoint != nil && checkpoint.by(step.ID)
		outcome = classifyStepError(err, stepCtx.Err(), checkpointed, internalFailure.Load(), runExpired, stepDeadlineExpired)
	}
	step.Status = outcome.step
	step.Error = outcome.reason
	step.FailureKind = outcome.kind
	if step.Status == "succeeded" {
		step.Error = ""
		step.FailureKind = ""
	}
	attempt.Status = outcome.attempt
	attempt.Error = outcome.reason
	attempt.FailureKind = outcome.kind
	if outcome.recovery != "" {
		attempt.Recovery = outcome.recovery
	}
	attempt.FinishedAt = w.Now()
	if step.Status != "succeeded" && publishedCopy != nil {
		draft := *publishedCopy
		attempt.DraftArtifact = &draft
	} else if step.Status == "succeeded" {
		attempt.DraftArtifact = nil
	}
	if outcome.kind == failureCheckpointInterrupt && checkpoint != nil {
		if description := checkpoint.describe(); description != "" {
			step.Error = "checkpoint_interrupted: " + description
			attempt.Error = step.Error
		}
	}
	if step.Status == "interrupted" && checkpoint != nil {
		checkpoint.request(step.ID, step.Error)
	}
	switch step.Status {
	case "succeeded":
		message.Status = "completed"
	case "interrupted":
		message.Status = "interrupted"
	case "canceled":
		message.Status = "canceled"
	default:
		message.Status = "failed"
	}
	if step.Status == "succeeded" && publishedCopy != nil {
		if validationErr := ValidateArtifact(*publishedCopy, required, knownSources); validationErr != nil {
			step.Status, message.Status, attempt.Status = "failed", "failed", "failed"
			step.Error, attempt.Error = validationErr.Error(), validationErr.Error()
			step.FailureKind, attempt.FailureKind = "artifact_contract", "artifact_contract"
			attempt.DraftArtifact = publishedCopy
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	saveErr := e.Store.Write(cleanup, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		// Re-read the run: this step may itself have selected a workflow.
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		formal := map[string]bool{}
		for _, final := range finalSteps(effectiveInput(current), steps) {
			formal[final.ID] = true
		}
		if publishedCopy != nil && step.Status == "succeeded" && formal[step.ID] {
			if len(current.Input.ArtifactRefs) > 0 {
				ref := current.Input.ArtifactRefs[0]
				previous, err := tx.Artifact(ref.ID, ref.Version)
				if err != nil {
					return err
				}
				if previous.Type == publishedCopy.Type {
					versions, err := tx.Artifacts(run.ThreadID)
					if err != nil {
						return err
					}
					publishedCopy.ID = previous.ID
					for _, v := range versions {
						if v.ID == previous.ID {
							publishedCopy.Version = max(publishedCopy.Version, v.Version+1)
						}
					}
				}
			}
			if err := tx.SaveArtifact(*publishedCopy); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "artifact_published", publishedCopy); err != nil {
				return err
			}
		}
		if publishedCopy != nil && step.Status == "succeeded" && !formal[step.ID] {
			// A root that delegated, or a superseded continuation, keeps its
			// submission as attempt evidence; it must never become formal.
			draft := *publishedCopy
			attempt.DraftArtifact = &draft
		}
		if stored, readErr := tx.Attempt(attempt.ID); readErr == nil {
			stored.Status = attempt.Status
			stored.Error = attempt.Error
			stored.FailureKind = attempt.FailureKind
			stored.Recovery = attempt.Recovery
			stored.RecoveredFrom = attempt.RecoveredFrom
			stored.SessionID = attempt.SessionID
			stored.SessionMode = attempt.SessionMode
			stored.DraftArtifact = attempt.DraftArtifact
			stored.UsageKnown = attempt.UsageKnown
			stored.UsageCompleteness = attempt.UsageCompleteness
			stored.InputTokens = attempt.InputTokens
			stored.OutputTokens = attempt.OutputTokens
			stored.FinishedAt = attempt.FinishedAt
			attempt = stored
		}
		if digest, err := checkpointDigest(tx, run.ID, run.ThreadID, step.ID, attempt.SourceIDs, attempt.DraftArtifact); err == nil {
			attempt.CheckpointDigest = digest
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "message_finished", message); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", attempt); err != nil {
			return err
		}
		return tx.AppendEvent(run.ID, step.ID, a.ID, "step_finished", step)
	})
	if saveErr != nil {
		step.Status = "failed"
		step.FailureKind = failureUnknown
		step.Error = saveErr.Error()
		if updateErr := e.Store.Write(cleanup, func(tx *store.Tx) error {
			stored, readErr := tx.Attempt(attempt.ID)
			if readErr != nil {
				return readErr
			}
			stored.Status = "failed"
			stored.Error = saveErr.Error()
			stored.FailureKind = failureUnknown
			stored.FinishedAt = w.Now()
			if err := tx.SaveAttempt(stored); err != nil {
				return err
			}
			return tx.SaveStep(step)
		}); updateErr != nil {
			e.report(updateErr)
		}
		e.report(saveErr)
	}
	return step
}

func shortTaskResult(value string) string {
	if len(value) > 6000 {
		end := 6000
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		return value[:end] + "\n[truncated; reference IDs and exact sources remain available]"
	}
	return value
}

func shortPointer(value string) string {
	r := []rune(value)
	if len(r) > 180 {
		return string(r[:180]) + "…"
	}
	return value
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// sourceIDs snapshots the exact source authority an attempt was dispatched
// with; later sibling captures cannot enter its plan or checkpoint.
func sourceIDs(sources []w.Source) []string {
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	return ids
}

const (
	capabilityBlockedPrefix = "capability_blocked:"
	connectionBlockedPrefix = "connection_blocked:"
	connectionChangedPrefix = "connection_changed:"
)

func isBlockedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), capabilityBlockedPrefix) || strings.Contains(err.Error(), connectionBlockedPrefix) || strings.Contains(err.Error(), connectionChangedPrefix) || strings.Contains(err.Error(), "required_tools_unavailable:")
}

func isCapabilityBlocked(step w.Step) bool {
	if step.Status != "interrupted" {
		return false
	}
	return strings.HasPrefix(step.Error, capabilityBlockedPrefix) || strings.HasPrefix(step.Error, connectionBlockedPrefix) || strings.HasPrefix(step.Error, connectionChangedPrefix) || strings.Contains(step.Error, "required_tools_unavailable:")
}

func (e *Engine) report(err error) {
	select {
	case e.Errors <- err:
	default:
	}
}
func settlePending(tx *store.Tx, runID, reason string) error {
	steps, err := tx.Steps(runID)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.Status == "pending" {
			step.Status = "skipped"
			step.Error = reason
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(runID, step.ID, step.AgentID, "step_finished", step); err != nil {
				return err
			}
		}
	}
	return nil
}
