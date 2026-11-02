package workflow

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// DefaultMaxParallelRuns bounds how many conversations execute at once. The
// database keeps at most one active run per conversation, so this only controls
// independent conversations; steps inside one plan already run in parallel.
const DefaultMaxParallelRuns = 4

// Engine owns a bounded durable dispatcher and each run's cancellation context.
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
	// History bounds committed conversation history inlined per attempt. Zero
	// uses DefaultHistoryLimits; explicit member context_size scales it.
	History HistoryLimits
	// MaxParallelRuns bounds independent conversations executing at once.
	// Zero uses DefaultMaxParallelRuns.
	MaxParallelRuns int
	// RetryBackoff is the base delay for automatic transient retries. Tests
	// may lower it; production uses a bounded exponential backoff.
	RetryBackoff time.Duration
	InstanceID   string
	Owner        proc.Identity
	Errors       chan error

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

func (e *Engine) report(err error) {
	select {
	case e.Errors <- err:
	default:
	}
}
