package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func transientFailure() error {
	return &nativecli.Failure{Kind: nativecli.FailureNetwork, Reason: "temporary connection error"}
}

func retryTestRun(t *testing.T) (*store.Store, w.Run, w.Step) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "safe public task"}})
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		var steps []w.Step
		if steps, err = tx.Steps(run.ID); err != nil {
			return err
		}
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}
	var step w.Step
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil {
			step = steps[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return db, run, step
}

func stepAttempts(t *testing.T, db *store.Store, stepID string) []w.Attempt {
	t.Helper()
	var attempts []w.Attempt
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		attempts, err = tx.StepAttempts(stepID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return attempts
}

func retryEvents(t *testing.T, db *store.Store, runID string) []w.Event {
	t.Helper()
	var events []w.Event
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		events, err = tx.Events(runID, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return events
}

func countRetryScheduled(events []w.Event) int {
	count := 0
	for _, event := range events {
		if event.Type == "retry_scheduled" {
			count++
		}
	}
	return count
}

func runStep(t *testing.T, engine *Engine, run w.Run, step w.Step) w.Step {
	t.Helper()
	return engine.executeStep(t.Context(), run, step, nil, nil, nil)
}

func TestRetryBudgetAndJoinedFailures(t *testing.T) {
	cases := []struct {
		name    string
		failure func() error
		calls   int
	}{
		{"initial_plus_three_retries", transientFailure, 4},
		{"uncertain_process_beside_network_error", func() error {
			return errors.Join(transientFailure(), &nativecli.Failure{Kind: nativecli.FailureProcessUncertain, Reason: "owned process may still be running"})
		}, 1},
		{"authentication_beside_network_error", func() error {
			return errors.Join(transientFailure(), &nativecli.Failure{Kind: nativecli.FailureAuthentication, Reason: "invalid configured credential"})
		}, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db, run, step := retryTestRun(t)
			var calls atomic.Int32
			runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
				calls.Add(1)
				return test.failure()
			})
			engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
			result := runStep(t, &engine, run, step)
			if got := int(calls.Load()); got != test.calls {
				t.Fatalf("run status=%s calls=%d, want %d", result.Status, got, test.calls)
			}
			if got := len(stepAttempts(t, db, step.ID)); got != test.calls {
				t.Fatalf("durable attempts=%d, want %d", got, test.calls)
			}
		})
	}
}

func TestAutoRetryRecoversAfterTransientFailures(t *testing.T) {
	db, run, step := retryTestRun(t)
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		if calls.Add(1) < 3 {
			return transientFailure()
		}
		return nil
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
	result := runStep(t, &engine, run, step)
	if result.Status != "succeeded" || calls.Load() != 3 {
		t.Fatalf("recovered run status=%s calls=%d, want succeeded after two retries", result.Status, calls.Load())
	}
	attempts := stepAttempts(t, db, step.ID)
	if len(attempts) != 3 {
		t.Fatalf("durable attempts=%d, want 3", len(attempts))
	}
	if attempts[0].Reason != "initial" || attempts[1].Reason != autoRetryReason || attempts[2].Reason != autoRetryReason {
		t.Fatalf("attempt reasons=%q,%q,%q, want initial then durable retry reasons", attempts[0].Reason, attempts[1].Reason, attempts[2].Reason)
	}
	if attempts[2].Status != "succeeded" {
		t.Fatalf("final attempt status=%q, want succeeded", attempts[2].Status)
	}
	if got := countRetryScheduled(retryEvents(t, db, run.ID)); got != 2 {
		t.Fatalf("retry_scheduled events=%d, want 2", got)
	}
}

func TestTransientRetryReusesOriginalMessage(t *testing.T) {
	db, run, step := retryTestRun(t)
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		if calls.Add(1) == 1 {
			if err := observe("text", map[string]any{"text": "第一次输出"}); err != nil {
				return err
			}
			return transientFailure()
		}
		return observe("text", map[string]any{"text": "第二次输出"})
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
	result := runStep(t, &engine, run, step)
	if result.Status != "succeeded" || calls.Load() != 2 {
		t.Fatalf("run status=%s calls=%d, want succeeded after one retry", result.Status, calls.Load())
	}
	var messages []w.Message
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		messages, err = tx.Messages(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages=%d, want one reused bubble", len(messages))
	}
	if messages[0].Content != "第一次输出第二次输出" {
		t.Fatalf("reused message content=%q", messages[0].Content)
	}
	attempts := stepAttempts(t, db, step.ID)
	if messages[0].AttemptID != attempts[len(attempts)-1].ID {
		t.Fatalf("message attempt=%q, want latest %q", messages[0].AttemptID, attempts[len(attempts)-1].ID)
	}
}

func TestAutoRetryExhaustsPersistedBudget(t *testing.T) {
	db, run, step := retryTestRun(t)
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		calls.Add(1)
		return transientFailure()
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
	result := runStep(t, &engine, run, step)
	if result.Status != "failed" {
		t.Fatalf("exhausted run status=%q, want failed", result.Status)
	}
	if calls.Load() != autoRetryLimit+1 {
		t.Fatalf("calls=%d, want initial plus %d retries", calls.Load(), autoRetryLimit)
	}
	attempts := stepAttempts(t, db, step.ID)
	if len(attempts) != autoRetryLimit+1 {
		t.Fatalf("durable attempts=%d, want %d", len(attempts), autoRetryLimit+1)
	}
	if got := countRetryScheduled(retryEvents(t, db, run.ID)); got != autoRetryLimit {
		t.Fatalf("retry_scheduled events=%d, want %d", got, autoRetryLimit)
	}
}

// Prior attempts may be mislabelled in their reason; only the durable count and
// machine failure kind decide the budget, so a restart cannot reset it.
func TestAutoRetryBudgetSurvivesRestartAndMislabelledReasons(t *testing.T) {
	db, run, step := retryTestRun(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		for number := 1; number <= autoRetryLimit; number++ {
			if err := tx.SaveAttempt(w.Attempt{
				ID: w.NewID(), RunID: run.ID, StepID: step.ID, Number: number,
				Status: "failed", Reason: "initial", FailureKind: string(nativecli.FailureNetwork),
				StartedAt: w.Now(), FinishedAt: w.Now(),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		calls.Add(1)
		return transientFailure()
	})
	// A brand-new engine over the same store represents a restart.
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
	result := runStep(t, &engine, run, step)
	if result.Status != "failed" || calls.Load() != 1 {
		t.Fatalf("restart status=%s calls=%d, want failed after one dispatch (budget already spent)", result.Status, calls.Load())
	}
	if attempts := stepAttempts(t, db, step.ID); len(attempts) != autoRetryLimit+1 {
		t.Fatalf("durable attempts=%d, want %d total", len(attempts), autoRetryLimit+1)
	}
	if got := countRetryScheduled(retryEvents(t, db, run.ID)); got != 0 {
		t.Fatalf("retry_scheduled events=%d after budget exhaustion, want 0", got)
	}
}

func TestAutoRetryStopsForNonRetryableKinds(t *testing.T) {
	for _, kind := range []nativecli.FailureKind{
		nativecli.FailureAuthentication,
		nativecli.FailureProcessUncertain,
		nativecli.FailurePermission,
		nativecli.FailureUnknown,
		nativecli.FailureContextOverflow,
	} {
		t.Run(string(kind), func(t *testing.T) {
			db, run, step := retryTestRun(t)
			var calls atomic.Int32
			runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
				calls.Add(1)
				return &nativecli.Failure{Kind: kind, Reason: "configured failure"}
			})
			engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
			runStep(t, &engine, run, step)
			if calls.Load() != 1 {
				t.Fatalf("%s failure replayed %d times, want exactly one dispatch", kind, calls.Load())
			}
		})
	}
	t.Run("cancellation", func(t *testing.T) {
		db, run, step := retryTestRun(t)
		var calls atomic.Int32
		runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
			calls.Add(1)
			return context.Canceled
		})
		engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
		runStep(t, &engine, run, step)
		if calls.Load() != 1 {
			t.Fatalf("cancellation replayed %d times, want exactly one dispatch", calls.Load())
		}
	})
}

func TestAutoRetryStopsAfterSideEffect(t *testing.T) {
	db, run, step := retryTestRun(t)
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, execute agent.Execute, _ agent.Observe) error {
		calls.Add(1)
		// A rejected handoff still counts as a committed handoff attempt: the
		// provider must not replay a step that already invoked a mutating tool.
		_, _ = execute(ctx, "handoff_task", []byte(`{"agent_id":"missing","instruction":"do work"}`))
		return transientFailure()
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
	runStep(t, &engine, run, step)
	if calls.Load() != 1 {
		t.Fatalf("retry after a mutating handoff call: calls=%d, want 1", calls.Load())
	}
}

func TestTrustedNativeInvocationIsNeverAutomaticallyReplayed(t *testing.T) {
	db, run, step := retryTestRun(t)
	run.EffectiveExecutionMode = w.ExecutionFullAccess
	run.Agents[0].Tools = append(run.Agents[0].Tools, "run_command")
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveAgent(run.Agents[0]); err != nil {
			return err
		}
		member, err := tx.Member(run.WorkspaceID, run.Agents[0].ID)
		if err != nil {
			return err
		}
		member.Tools = run.Agents[0].Tools
		if err := tx.SaveMember(member); err != nil {
			return err
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(_ context.Context, request model.Request, _ agent.Execute, _ agent.Observe) error {
		if !request.TrustedExecution {
			t.Error("expected native full access")
		}
		calls.Add(1)
		// A disconnect may precede every observable tool receipt.
		return transientFailure()
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
	result := runStep(t, &engine, run, step)
	if calls.Load() != 1 {
		t.Fatalf("trusted native invocation replayed %d times; status=%s error=%s", calls.Load(), result.Status, result.Error)
	}
}

func TestAutoRetryStopsOnCancellationDuringBackoff(t *testing.T) {
	db, run, step := retryTestRun(t)
	first := make(chan struct{})
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		if calls.Add(1) == 1 {
			close(first)
		}
		return transientFailure()
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: 2 * time.Second}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan w.Step, 1)
	go func() { done <- engine.executeStep(ctx, run, step, nil, nil, nil) }()
	<-first
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled retry backoff did not stop")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d after cancellation during backoff, want 1", calls.Load())
	}
}

func TestAutoRetryStopsWhenMemberLosesAccessDuringBackoff(t *testing.T) {
	db, run, step := retryTestRun(t)
	first := make(chan struct{})
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		if calls.Add(1) == 1 {
			close(first)
		}
		return transientFailure()
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: 200 * time.Millisecond}
	done := make(chan w.Step, 1)
	go func() { done <- engine.executeStep(t.Context(), run, step, nil, nil, nil) }()
	<-first
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		member, err := tx.Agent(step.AgentID)
		if err != nil {
			return err
		}
		member.Disabled = true
		return tx.SaveAgent(member)
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Status == "succeeded" {
			t.Fatalf("revoked member still succeeded: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry after revocation did not stop")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d after revocation, want 1", calls.Load())
	}
}

func TestAutoRetryConcurrentRunsKeepSeparateBudgets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "one"}})
	second := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "two"}})
	for _, run := range []w.Run{first, second} {
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			run.Status = "running"
			return tx.SaveRun(run)
		}); err != nil {
			t.Fatal(err)
		}
	}
	var calls sync.Map
	runner := nativeRunnerFunc(func(_ context.Context, request model.Request, _ agent.Execute, _ agent.Observe) error {
		value, _ := calls.LoadOrStore(request.Executor, new(atomic.Int32))
		value.(*atomic.Int32).Add(1)
		return transientFailure()
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, RetryBackoff: time.Millisecond}
	var wg sync.WaitGroup
	for _, run := range []w.Run{first, second} {
		var step w.Step
		if err := db.Read(t.Context(), func(tx *store.Tx) error {
			steps, err := tx.Steps(run.ID)
			if err == nil {
				step = steps[0]
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(run w.Run, step w.Step) {
			defer wg.Done()
			engine.executeStep(t.Context(), run, step, nil, nil, nil)
		}(run, step)
	}
	wg.Wait()
	value, _ := calls.Load("claude")
	if got := value.(*atomic.Int32).Load(); got != int32(autoRetryLimit+1)*2 {
		t.Fatalf("total calls=%d, want independent budgets of %d each", got, autoRetryLimit+1)
	}
}
