package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type customStepSpec struct {
	Agent       int
	Role        string
	Instruction string
	DependsOn   []int
	Kind        string
}

func seedCustomRun(t *testing.T, s *store.Store, specs []customStepSpec) w.Run {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "custom", Revision: 1}
	agents := []w.Agent{}
	for i, name := range []string{"A", "B", "C"} {
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: "custom-" + string(rune('a'+i)), Model: "default", Executor: "claude", Tools: tools.Names, Revision: 1})
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "queued", CreatedAt: w.Now(), Agents: agents, Input: w.RunInput{Message: "custom long task", Workflow: "free", ResearchScope: "given_sources"}}
	steps := make([]w.Step, len(specs))
	for i, spec := range specs {
		steps[i] = w.Step{ID: w.NewID(), RunID: run.ID, AgentID: agents[spec.Agent].ID, AgentName: agents[spec.Agent].DisplayName, Role: spec.Role, Instruction: spec.Instruction, Status: "pending", Kind: spec.Kind}
	}
	for i, spec := range specs {
		for _, dep := range spec.DependsOn {
			steps[i].DependsOn = append(steps[i].DependsOn, steps[dep].ID)
		}
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, a := range agents {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "custom"); err != nil {
			return err
		}
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

func startEngine(t *testing.T, e *Engine) (context.CancelFunc, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	return cancel, func() { cancel(); e.Wait() }
}

func loadAttempts(t *testing.T, s *store.Store, runID string) []w.Attempt {
	t.Helper()
	var attempts []w.Attempt
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		attempts, err = tx.Attempts(runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return attempts
}

func TestFrozenPolicyIsUsedInsteadOfCurrentLimits(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	policy := w.ExecutionPolicy{RunTimeout: 0, StepTimeout: 0, FirstResponse: 2 * time.Second, Stall: 3 * time.Second, StallAction: "stop"}
	run.ExecutionPolicy = &policy
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
		t.Fatal(err)
	}
	var seen atomic.Bool
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, _ agent.Execute, observe agent.Observe) error {
		if request.FirstResponseTimeout != 2*time.Second || request.StallTimeout != 3*time.Second || request.StallAction != "stop" {
			t.Errorf("frozen liveness policy was replaced: %+v", request)
		}
		if deadline, ok := ctx.Deadline(); ok {
			t.Errorf("zero run timeout still had a deadline: %v", deadline)
		}
		seen.Store(true)
		time.Sleep(60 * time.Millisecond)
		return observe("text", map[string]any{"text": "frozen policy answer"})
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: Limits{RunTimeout: 20 * time.Millisecond, FirstResponse: 10 * time.Minute, Stall: 15 * time.Minute, StallAction: "warn"}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	_ = cancel
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" || !seen.Load() {
		t.Fatalf("frozen zero run timeout was not honoured: %+v", got)
	}
	if got.ExecutionPolicy == nil || *got.ExecutionPolicy != policy || got.DeadlineAt != "" || got.StartedAt == "" {
		t.Fatalf("persisted policy changed: %+v", got)
	}
}

func TestLegacyPolicyAdoptedOnceFromEarliestAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	stepID := ""
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil {
			stepID = steps[0].ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Second).Format(time.RFC3339Nano)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].Status = "running"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: stepID, Number: 1, Status: "running", StartedAt: started, Process: &w.ProcessRef{PID: 999999, PGID: 999999, Start: "Mon Jan  1 00:00:00 2001"}})
	}); err != nil {
		t.Fatal(err)
	}
	first := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}, Limits: Limits{RunTimeout: 10 * time.Second, FirstResponse: time.Minute, Stall: 2 * time.Minute, StallAction: "warn"}}
	_, wait := startEngine(t, &first)
	got := awaitRun(t, s, run.ID)
	wait()
	if got.Status != "succeeded" {
		t.Fatalf("run=%+v", got)
	}
	adopted := got.ExecutionPolicy
	if adopted == nil || got.PolicyAdoptedAt == "" || got.DeadlineAt == "" {
		t.Fatalf("legacy policy was not adopted explicitly: %+v", got)
	}
	deadline := got.DeadlineAt
	recoveredAt := got.RecoveredAt
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}, Limits: Limits{RunTimeout: time.Hour}}
	cancel2, wait2 := startEngine(t, &second)
	defer wait2()
	time.Sleep(150 * time.Millisecond)
	cancel2()
	wait2()
	final, err := loadRun(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.DeadlineAt != deadline || final.ExecutionPolicy == nil || *final.ExecutionPolicy != *adopted || final.RecoveredAt != recoveredAt {
		t.Fatalf("restart changed an adopted policy: before=%+v after=%+v", got, final)
	}
}

func TestExpiredDeadlinesRefuseResume(t *testing.T) {
	for _, target := range []string{"run", "step"} {
		t.Run(target, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
			past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
			policy := w.ExecutionPolicy{RunTimeout: 10 * time.Second, StepTimeout: 10 * time.Second}
			if err := s.Write(t.Context(), func(tx *store.Tx) error {
				run.Status = "interrupted"
				run.FinishedAt = w.Now()
				run.ExecutionPolicy = &policy
				run.StartedAt = past
				if target == "run" {
					run.DeadlineAt = past
				} else {
					run.DeadlineAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
				}
				if err := tx.SaveRun(run); err != nil {
					return err
				}
				steps, err := tx.Steps(run.ID)
				if err != nil {
					return err
				}
				steps[0].Status = "interrupted"
				steps[0].StartedAt = past
				steps[0].DeadlineAt = past
				if target == "run" {
					steps[0].DeadlineAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
				}
				return tx.SaveStep(steps[0])
			}); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
				calls.Add(1)
				return nil
			})}}
			if _, err := e.Resume(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), target+"_timeout") {
				t.Fatalf("expired %s deadline was resumed: %v", target, err)
			}
			if calls.Load() != 0 {
				t.Fatal("expired deadline launched a CLI")
			}
			got, _ := loadRun(t, s, run.ID)
			if got.Status != "failed" || got.FailureKind != target+"_timeout" {
				t.Fatalf("expired resume did not persist a failed outcome: %+v", got)
			}
			steps, _ := loadSteps(t, s, run.ID)
			if steps[0].Status != "failed" || steps[0].FailureKind != target+"_timeout" {
				t.Fatalf("expired resume left a resumable step: %+v", steps[0])
			}
		})
	}
}

func TestQueuedDispatchRespectsPersistedDeadline(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	policy := w.ExecutionPolicy{RunTimeout: 10 * time.Second}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.ExecutionPolicy = &policy
		run.StartedAt = past
		run.DeadlineAt = past
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		calls.Add(1)
		return nil
	})}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "failed" || got.FailureKind != failureRunTimeout || calls.Load() != 0 {
		t.Fatalf("expired queued run launched work: %+v calls=%d", got, calls.Load())
	}
	steps, _ := loadSteps(t, s, run.ID)
	for _, step := range steps {
		if step.Status == "pending" || step.Status == "running" {
			t.Fatalf("expired run left unsettled step: %+v", step)
		}
	}
}

func TestExpiredStepDeadlineSkipsModel(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	policy := w.ExecutionPolicy{StepTimeout: 10 * time.Second}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.ExecutionPolicy = &policy
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].StartedAt = past
		steps[0].DeadlineAt = past
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		calls.Add(1)
		return nil
	})}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "failed" || got.FailureKind != failureStepTimeout || calls.Load() != 0 {
		t.Fatalf("expired step launched work: %+v calls=%d", got, calls.Load())
	}
}

func TestZeroLimitsCancelAllParallelSteps(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "A", Instruction: "one"}, {Agent: 1, Role: "B", Instruction: "two"}})
	started := make(chan struct{}, 2)
	deadlines := make(chan bool, 2)
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, _ agent.Observe) error {
		_, has := ctx.Deadline()
		deadlines <- has
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("parallel step did not start")
		}
	}
	if _, err := e.Cancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "canceled" || got.DeadlineAt != "" {
		t.Fatalf("zero-limit run did not settle as canceled: %+v", got)
	}
	close(deadlines)
	for has := range deadlines {
		if has {
			t.Fatal("zero limits created an implicit step deadline")
		}
	}
	steps, _ := loadSteps(t, s, run.ID)
	for _, step := range steps {
		if step.Status != "canceled" {
			t.Fatalf("parallel step not canceled: %+v", step)
		}
	}
}

func TestResumeIsIdempotent(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "interrupted"
		run.FinishedAt = w.Now()
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].Status = "interrupted"
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	for i := 0; i < 2; i++ {
		resumed, err := e.Resume(t.Context(), run.ID)
		if err != nil || resumed.Status != "queued" {
			t.Fatalf("resume %d was not idempotent: %+v %v", i, resumed, err)
		}
	}
	if got, _ := loadRun(t, s, run.ID); got.RecoveryCount != 1 {
		t.Fatalf("idempotent resume double-counted: %+v", got)
	}
	cancel, wait := startEngine(t, &e)
	defer wait()
	_ = cancel
	if got := awaitRun(t, s, run.ID); got.Status != "succeeded" {
		t.Fatalf("resumed run failed: %+v", got)
	}
}

func TestRunTimeoutAndStepTimeoutKinds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limits  Limits
		failure string
		attempt string
	}{{"run", Limits{RunTimeout: 120 * time.Millisecond, StepTimeout: 5 * time.Second}, failureRunTimeout, failureRunTimeout}, {"step", Limits{RunTimeout: 5 * time.Second, StepTimeout: 60 * time.Millisecond}, failureMultipleSteps, failureStepTimeout}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "A", Instruction: "one"}, {Agent: 1, Role: "B", Instruction: "two"}})
			runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, _ agent.Observe) error {
				<-ctx.Done()
				return ctx.Err()
			})
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: tc.limits}
			cancel, wait := startEngine(t, &e)
			defer wait()
			got := awaitRun(t, s, run.ID)
			_ = cancel
			if got.Status != "failed" || got.FailureKind != tc.failure {
				t.Fatalf("wrong run timeout outcome: %+v", got)
			}
			for _, attempt := range loadAttempts(t, s, run.ID) {
				if attempt.Status != "failed" || attempt.FailureKind != tc.attempt {
					t.Fatalf("attempt did not record %s: %+v", tc.attempt, attempt)
				}
			}
		})
	}
}

func TestCheckpointInterruptsSiblingAndKeepsPending(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{
		{Agent: 0, Role: "A", Instruction: "one"},
		{Agent: 1, Role: "B", Instruction: "two"},
		{Agent: 2, Role: "C", Instruction: "three", DependsOn: []int{0}},
	})
	bStarted := make(chan struct{})
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, _ agent.Execute, _ agent.Observe) error {
		switch {
		case strings.Contains(request.System, "role=A "):
			<-bStarted
			return &nativecli.Failure{Kind: nativecli.FailureStallTimeout, Reason: "no validated activity"}
		case strings.Contains(request.System, "role=B "):
			close(bStarted)
			<-ctx.Done()
			return ctx.Err()
		default:
			t.Error("dependent step dispatched after checkpoint")
			return nil
		}
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "interrupted" || got.FailureKind != string(nativecli.FailureStallTimeout) {
		t.Fatalf("checkpoint was not a recoverable interruption: %+v", got)
	}
	steps, _ := loadSteps(t, s, run.ID)
	byRole := map[string]w.Step{}
	for _, step := range steps {
		byRole[step.Role] = step
	}
	if byRole["A"].Status != "interrupted" || byRole["A"].FailureKind != string(nativecli.FailureStallTimeout) {
		t.Fatalf("triggering step lost its kind: %+v", byRole["A"])
	}
	if byRole["B"].Status != "interrupted" || byRole["B"].FailureKind != failureCheckpointInterrupt {
		t.Fatalf("sibling was not canceled as an interruption: %+v", byRole["B"])
	}
	if byRole["C"].Status != "pending" {
		t.Fatalf("dependency on interrupted work was not kept pending: %+v", byRole["C"])
	}
	attempts := loadAttempts(t, s, run.ID)
	if len(attempts) != 2 {
		t.Fatalf("checkpoint launched extra work: %+v", attempts)
	}
	lastAttempt := map[string]int64{}
	var finished int64
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Type == "attempt_finished" && (event.StepID == byRole["A"].ID || event.StepID == byRole["B"].ID) {
				lastAttempt[event.StepID] = event.Seq
			}
			if event.Type == "run_finished" {
				finished = event.Seq
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(lastAttempt) != 2 || finished == 0 {
		t.Fatalf("missing convergence evidence: attempts=%v finished=%d", lastAttempt, finished)
	}
	for stepID, seq := range lastAttempt {
		if seq > finished {
			t.Fatalf("run finished before attempt %s persisted", stepID)
		}
	}
}

func TestTrueFailuresOutrankInterruptionDeterministically(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "A", Instruction: "one"}, {Agent: 1, Role: "B", Instruction: "two"}})
	runner := nativeRunnerFunc(func(_ context.Context, request model.Request, _ agent.Execute, _ agent.Observe) error {
		if strings.Contains(request.System, "role=A ") {
			return errors.New("authentication failed permanently")
		}
		return &nativecli.Failure{Kind: nativecli.FailureQuota, Reason: "quota exhausted"}
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "failed" || got.FailureKind != failureMultipleSteps {
		t.Fatalf("multiple true failures were not aggregated: %+v", got)
	}
	steps, _ := loadSteps(t, s, run.ID)
	first := strings.Index(got.Error, steps[0].ID)
	second := strings.Index(got.Error, steps[1].ID)
	if first < 0 || second < 0 || first > second {
		t.Fatalf("aggregate error was not ordered by plan position: %q", got.Error)
	}
	if !strings.Contains(got.Error, string(nativecli.FailureQuota)) || !strings.Contains(got.Error, failureUnknown) {
		t.Fatalf("failure kinds were lost: %q", got.Error)
	}
}

func TestContextOverflowKeepsStepInterruptedThenFreshResume(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	researcherCalls := 0
	runner := &testRunner{run: func(ctx context.Context, request model.Request, call int, execute agent.Execute, observe agent.Observe) error {
		if strings.Contains(request.System, "role=researcher") {
			researcherCalls = call
			if call == 1 {
				return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "native session context window exhausted"}
			}
		}
		if err := observe("text", map[string]any{"text": "recovered result"}); err != nil {
			return err
		}
		if strings.Contains(request.System, "role=coordinator") {
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Recovered", "body": "Recovered body", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		return nil
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" || got.FailureKind != string(nativecli.FailureContextOverflow) {
		t.Fatalf("overflow wrongly failed the run: %+v", got)
	}
	steps, _ := loadSteps(t, s, run.ID)
	if steps[0].Status != "interrupted" || steps[0].FailureKind != string(nativecli.FailureContextOverflow) {
		t.Fatalf("overflow step was not interrupted: %+v", steps[0])
	}
	attempts := loadAttempts(t, s, run.ID)
	if len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].FailureKind != string(nativecli.FailureContextOverflow) {
		t.Fatalf("failed attempt was not recorded: %+v", attempts)
	}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got = awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" || researcherCalls < 2 {
		t.Fatalf("fresh resume did not continue: %+v calls=%d", got, researcherCalls)
	}
	attempts = loadAttempts(t, s, run.ID)
	var fresh w.Attempt
	for _, attempt := range attempts {
		if attempt.StepID == steps[0].ID && attempt.Number > 1 {
			fresh = attempt
		}
	}
	if fresh.ID == "" || fresh.Recovery != "context_fresh" || fresh.RecoveredFrom == "" || fresh.RecoveredFrom == fresh.ID || fresh.SessionMode != "fresh" {
		t.Fatalf("fresh attempt identity missing: %+v", fresh)
	}
	if fresh.InputPlanDigest == "" || fresh.CheckpointStart == "" || fresh.CheckpointDigest == "" {
		t.Fatalf("digests were not persisted: %+v", fresh)
	}
}

func loadSteps(t *testing.T, s *store.Store, runID string) ([]w.Step, error) {
	t.Helper()
	var steps []w.Step
	err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		steps, readErr = tx.Steps(runID)
		return readErr
	})
	return steps, err
}

func TestManualInterruptedCheckpointDoesNotRestartAfterServiceBoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "interrupted"
		run.FinishedAt = w.Now()
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].Status = "interrupted"
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var calls atomic.Int32
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		calls.Add(1)
		return nil
	})}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	time.Sleep(250 * time.Millisecond)
	_ = cancel
	if calls.Load() != 0 {
		t.Fatal("manual interrupted checkpoint was consumed automatically")
	}
	if got, _ := loadRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("manual checkpoint changed on boot: %+v", got)
	}
}

func TestLongRunningClocksAreNotImplicitlyCapped(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "long"}})
	now := time.Now().UTC()
	startedRun := now.Add(-2 * time.Hour)
	policy := w.ExecutionPolicy{RunTimeout: 6 * time.Hour, StepTimeout: 2 * time.Hour}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.ExecutionPolicy = &policy
		run.StartedAt = startedRun.Format(time.RFC3339Nano)
		run.DeadlineAt = startedRun.Add(6 * time.Hour).Format(time.RFC3339Nano)
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].StartedAt = now.Add(-time.Hour).Format(time.RFC3339Nano)
		steps[0].DeadlineAt = now.Add(time.Hour).Format(time.RFC3339Nano)
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < 50*time.Minute {
			return errors.New("run context lost its long persisted deadline")
		}
		return observe("text", map[string]any{"text": "long task still healthy"})
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" || got.DeadlineAt != startedRun.Add(6*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("long healthy task was stopped: %+v", got)
	}
}

func TestFirstResponseTimeoutInterruptsStep(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "wait"}})
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		return &nativecli.Failure{Kind: nativecli.FailureFirstResponseTimeout, Reason: "no validated response"}
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "interrupted" || got.FailureKind != string(nativecli.FailureFirstResponseTimeout) {
		t.Fatalf("first-response timeout became a true failure: %+v", got)
	}
	steps, _ := loadSteps(t, s, run.ID)
	if steps[0].Status != "interrupted" {
		t.Fatalf("step was not interrupted: %+v", steps[0])
	}
	attempts := loadAttempts(t, s, run.ID)
	if attempts[0].Status != "interrupted" || attempts[0].FailureKind != string(nativecli.FailureFirstResponseTimeout) {
		t.Fatalf("attempt was not an interruption: %+v", attempts[0])
	}
}

// TestLegacyResumeAdoptsPolicyFromAdoptionNotHistoricalAttempt replaces the
// old expectation that adoption back-dates a new timeout to the earliest old
// attempt. The plan says an unknown legacy policy starts applying at adoption;
// otherwise a one-hour-old attempt under a new one-minute limit would be
// declared expired without ever running under that policy.
func TestLegacyResumeAdoptsPolicyFromAdoptionNotHistoricalAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "legacy"}})
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	var stepID string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil {
			stepID = steps[0].ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "interrupted"
		run.FinishedAt = w.Now()
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].Status = "interrupted"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: stepID, Number: 1, Status: "interrupted", StartedAt: old, FinishedAt: old})
	}); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}, Limits: Limits{RunTimeout: time.Minute, StepTimeout: 30 * time.Second}}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatalf("legacy resume was expired by a back-dated deadline: %v", err)
	}
	got, err := loadRun(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	adoptedAt := parseMoment(got.PolicyAdoptedAt)
	if adoptedAt.IsZero() || adoptedAt.Before(before) {
		t.Fatalf("legacy policy was not adopted now: %+v", got)
	}
	if got.Status != "queued" || got.ExecutionPolicy == nil || got.ExecutionPolicy.RunTimeout != time.Minute || got.ExecutionPolicy.StepTimeout != 30*time.Second {
		t.Fatalf("legacy policy adoption is wrong: %+v", got)
	}
	if got.StartedAt != old {
		t.Fatalf("historical run start was rewritten: %q", got.StartedAt)
	}
	if want := adoptedAt.Add(time.Minute).Format(time.RFC3339Nano); got.DeadlineAt != want {
		t.Fatalf("run deadline origin=%q, want adoption origin %q", got.DeadlineAt, want)
	}
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].StartedAt != old {
		t.Fatalf("historical step start was rewritten: %+v", steps[0])
	}
	if want := adoptedAt.Add(30 * time.Second).Format(time.RFC3339Nano); steps[0].DeadlineAt != want {
		t.Fatalf("step deadline=%q, want adoption origin %q", steps[0].DeadlineAt, want)
	}
	attempts := loadAttempts(t, s, run.ID)
	if len(attempts) != 1 || attempts[0].StartedAt != old {
		t.Fatalf("historical attempt was rewritten: %+v", attempts)
	}
	if count := adoptionEvents(t, s, run.ID); count != 1 {
		t.Fatalf("legacy policy adoption was recorded %d times", count)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// A restart with different configuration must not grant a new clock.
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}, Limits: Limits{RunTimeout: time.Hour, StepTimeout: time.Hour}}
	cancel, wait := startEngine(t, &second)
	defer wait()
	final := awaitRun(t, s, run.ID)
	_ = cancel
	if final.Status != "succeeded" {
		t.Fatalf("adopted legacy run did not finish: %+v", final)
	}
	if final.DeadlineAt != got.DeadlineAt || final.PolicyAdoptedAt != got.PolicyAdoptedAt || final.ExecutionPolicy == nil || *final.ExecutionPolicy != *got.ExecutionPolicy {
		t.Fatalf("restart reset the adopted policy: before=%+v after=%+v", got, final)
	}
	afterSteps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSteps[0].StartedAt != steps[0].StartedAt || afterSteps[0].DeadlineAt != steps[0].DeadlineAt {
		t.Fatalf("restart reset the adopted step clock: before=%+v after=%+v", steps[0], afterSteps[0])
	}
	if count := adoptionEvents(t, s, run.ID); count != 1 {
		t.Fatalf("restart recorded another legacy policy adoption (%d)", count)
	}
}

func TestLegacyResumeKeepsKnownDeadlineAndFailsOnce(t *testing.T) {
	for _, target := range []string{"run", "step"} {
		t.Run(target, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "legacy"}})
			old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
			var stepID string
			if err := s.Read(t.Context(), func(tx *store.Tx) error {
				steps, err := tx.Steps(run.ID)
				if err == nil {
					stepID = steps[0].ID
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Write(t.Context(), func(tx *store.Tx) error {
				run.Status = "interrupted"
				run.FinishedAt = w.Now()
				run.StartedAt = old
				if target == "run" {
					run.DeadlineAt = old
				}
				if err := tx.SaveRun(run); err != nil {
					return err
				}
				steps, err := tx.Steps(run.ID)
				if err != nil {
					return err
				}
				steps[0].Status = "interrupted"
				steps[0].StartedAt = old
				if target == "step" {
					steps[0].DeadlineAt = old
				}
				if err := tx.SaveStep(steps[0]); err != nil {
					return err
				}
				return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: stepID, Number: 1, Status: "interrupted", StartedAt: old, FinishedAt: old})
			}); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
				calls.Add(1)
				return nil
			})}, Limits: Limits{RunTimeout: time.Hour, StepTimeout: time.Hour}}
			if _, err := e.Resume(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), target+"_timeout") {
				t.Fatalf("known past %s deadline was resumed: %v", target, err)
			}
			if calls.Load() != 0 {
				t.Fatal("expired legacy deadline launched a CLI")
			}
			got, err := loadRun(t, s, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "failed" || got.FailureKind != target+"_timeout" || got.ExecutionPolicy == nil || got.PolicyAdoptedAt == "" {
				t.Fatalf("known past deadline did not fail with recorded adoption: %+v", got)
			}
			if got.StartedAt != old {
				t.Fatalf("historical run start was rewritten: %+v", got)
			}
			deadline, adoptedAt := got.DeadlineAt, got.PolicyAdoptedAt
			if target == "run" && deadline != old {
				t.Fatalf("known run deadline was replaced: %q", deadline)
			}
			steps, err := loadSteps(t, s, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if target == "step" && steps[0].DeadlineAt != old {
				t.Fatalf("known step deadline was extended: %+v", steps[0])
			}
			if _, err := e.Resume(t.Context(), run.ID); !errors.Is(err, w.ErrConflict) {
				t.Fatalf("a failed legacy deadline was resumable again: %v", err)
			}
			again, err := loadRun(t, s, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if again.Status != "failed" || again.DeadlineAt != deadline || again.PolicyAdoptedAt != adoptedAt {
				t.Fatalf("second resume reset the failed deadline: %+v", again)
			}
		})
	}
}

func TestLegacyPolicyZeroTimeoutStaysUnlimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "legacy"}})
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	var stepID string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil {
			stepID = steps[0].ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "interrupted"
		run.FinishedAt = w.Now()
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].Status = "interrupted"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: stepID, Number: 1, Status: "interrupted", StartedAt: old, FinishedAt: old})
	}); err != nil {
		t.Fatal(err)
	}
	// An explicitly disabled timeout stays disabled after adoption.
	first := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}, Limits: Limits{}}
	if _, err := first.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got, err := loadRun(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionPolicy == nil || got.ExecutionPolicy.RunTimeout != 0 || got.DeadlineAt != "" || got.StartedAt != old {
		t.Fatalf("zero legacy policy gained an implicit deadline: %+v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}, Limits: Limits{RunTimeout: time.Hour, StepTimeout: time.Hour}}
	cancel, wait := startEngine(t, &second)
	defer wait()
	final := awaitRun(t, s, run.ID)
	_ = cancel
	if final.Status != "succeeded" || final.DeadlineAt != "" || final.ExecutionPolicy == nil || final.ExecutionPolicy.RunTimeout != 0 {
		t.Fatalf("restart replaced an adopted zero policy: %+v", final)
	}
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].DeadlineAt != "" {
		t.Fatalf("zero step timeout gained an implicit deadline: %+v", steps[0])
	}
}

func TestLegacyStepClockWaitsForFirstDispatch(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	blockedAt := now.Add(-time.Hour).Format(time.RFC3339Nano)
	policy := w.ExecutionPolicy{StepTimeout: time.Minute}
	steps := []w.Step{{ID: "step", Status: "pending"}}
	attempts := []w.Attempt{{ID: "blocked", StepID: "step", Status: "blocked", StartedAt: blockedAt}}
	if adoptStepClocks(steps, attempts, policy, now) {
		t.Fatalf("blocked preflight started the legacy clock: %+v", steps[0])
	}
	if steps[0].StartedAt != "" || steps[0].DeadlineAt != "" {
		t.Fatalf("blocked preflight created a step clock: %+v", steps[0])
	}
	startStepClock(&steps[0], policy, now)
	if steps[0].StartedAt != now.Format(time.RFC3339Nano) || steps[0].DeadlineAt != now.Add(time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("first dispatch did not start the clock at dispatch: %+v", steps[0])
	}
}

func adoptionEvents(t *testing.T, s *store.Store, runID string) int {
	t.Helper()
	count := 0
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(runID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Type == "legacy_policy_adopted" {
				count++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestTrueFailureAfterCheckpointStillFailsRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "A", Instruction: "one"}, {Agent: 1, Role: "B", Instruction: "two"}})
	aStarted := make(chan struct{})
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, _ agent.Execute, _ agent.Observe) error {
		if strings.Contains(request.System, "role=B ") {
			<-aStarted
			return &nativecli.Failure{Kind: nativecli.FailureStallTimeout, Reason: "stalled"}
		}
		close(aStarted)
		time.Sleep(40 * time.Millisecond)
		return &nativecli.Failure{Kind: nativecli.FailureAuthentication, Reason: "authentication failed permanently"}
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	steps, _ := loadSteps(t, s, run.ID)
	if got.Status != "failed" || got.FailureKind != string(nativecli.FailureAuthentication) {
		t.Fatalf("true failure was hidden behind an interrupted sibling: %+v", got)
	}
	statuses := map[string]string{}
	for _, step := range steps {
		statuses[step.Role] = step.Status
	}
	if statuses["B"] != "interrupted" || statuses["A"] != "failed" {
		t.Fatalf("step-level outcomes were collapsed: %+v", statuses)
	}
}
