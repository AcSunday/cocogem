package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

var rolePattern = regexp.MustCompile(`Current role=(\w+)`)

type testRunner struct {
	mu    sync.Mutex
	calls map[string]int
	run   func(context.Context, model.Request, int, agent.Execute, agent.Observe) error
}

func (r *testRunner) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	key := "unknown"
	if match := rolePattern.FindStringSubmatch(request.System); match != nil {
		key = match[1]
	}
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[key]++
	call := r.calls[key]
	r.mu.Unlock()
	return r.run(ctx, request, call, execute, observe)
}

func (r *testRunner) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}

func defaultRunner() *testRunner {
	return &testRunner{run: func(ctx context.Context, request model.Request, _ int, execute agent.Execute, observe agent.Observe) error {
		if err := observe("text", map[string]any{"text": "result for role " + request.System}); err != nil {
			return err
		}
		if strings.Contains(request.System, "role=coordinator") {
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Recovered report", "body": "Recovered body with limitations.", "findings": []any{}, "source_ids": []string{}, "limitations": []string{"synthetic test"}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		return nil
	}}
}

func newUUID(t *testing.T) string {
	t.Helper()
	return w.NewID()
}

func enableExecutor(t *testing.T, s *store.Store, run *w.Run, executor string) {
	t.Helper()
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		for i := range run.Agents {
			run.Agents[i].Executor = executor
			run.Agents[i].Model = "default"
			if err := tx.SaveAgent(run.Agents[i]); err != nil {
				return err
			}
		}
		return tx.SaveRun(*run)
	}); err != nil {
		t.Fatal(err)
	}
}

func awaitCondition(t *testing.T, s *store.Store, id string, check func(w.Run, []w.Step, []w.Attempt) bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var run w.Run
		var steps []w.Step
		var attempts []w.Attempt
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
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
			t.Fatal(err)
		}
		if check(run, steps, attempts) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not settle")
}

func TestDefaultLimitsLeaveWallClockOpen(t *testing.T) {
	limits := DefaultLimits()
	if limits.RunTimeout != 0 || limits.StepTimeout != 0 || limits.FirstResponse != 10*time.Minute || limits.Stall != 15*time.Minute || limits.StallAction != "warn" {
		t.Fatalf("unexpected defaults: %+v", limits)
	}
	passed := Limits{RunTimeout: 0, StepTimeout: 0, FirstResponse: 0, Stall: 0, StallAction: "stop"}
	if got := passed.WithDefaults(); got != passed {
		t.Fatalf("parsed zero was overwritten: %+v", got)
	}
}

func TestRunTimeoutHasItsOwnReason(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	runner := &testRunner{run: func(ctx context.Context, _ model.Request, _ int, _ agent.Execute, _ agent.Observe) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: Limits{RunTimeout: 500 * time.Millisecond, StepTimeout: 5 * time.Second}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "failed" || !strings.Contains(got.Error, "run_timeout:") {
		t.Fatalf("run timeout was not reported: %+v", got)
	}
	var attempts []w.Attempt
	s.Read(t.Context(), func(tx *store.Tx) error { attempts, err = tx.Attempts(run.ID); return err })
	if len(attempts) == 0 || !strings.Contains(attempts[0].Error, "run_timeout:") {
		t.Fatalf("attempt lost timeout reason: %+v", attempts)
	}
}

func TestStepTimeoutHasItsOwnReason(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	runner := &testRunner{run: func(ctx context.Context, _ model.Request, _ int, _ agent.Execute, _ agent.Observe) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: Limits{RunTimeout: 5 * time.Second, StepTimeout: 300 * time.Millisecond}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "failed" || !strings.Contains(got.Error, "step_timeout:") {
		t.Fatalf("step timeout was not reported: %+v", got)
	}
}

func TestGracefulStopSuspendsThenRestartContinues(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	started := make(chan struct{})
	runner := &testRunner{run: func(ctx context.Context, request model.Request, call int, execute agent.Execute, observe agent.Observe) error {
		if strings.Contains(request.System, "role=researcher") && call == 1 {
			if err := observe("text", map[string]any{"text": "partial research result"}); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		if err := observe("text", map[string]any{"text": "completed " + request.System}); err != nil {
			return err
		}
		if strings.Contains(request.System, "role=coordinator") {
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Recovered report", "body": "Recovered body.", "findings": []any{}, "source_ids": []string{}, "limitations": []string{"synthetic test"}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		return nil
	}}
	first := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := first.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		cancel()
		first.Wait()
		t.Fatal("first attempt did not start")
	}
	cancel()
	first.Wait()
	var suspended w.Run
	var steps []w.Step
	var attempts []w.Attempt
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		if suspended, err = tx.Run(run.ID); err != nil {
			return err
		}
		if steps, err = tx.Steps(run.ID); err != nil {
			return err
		}
		attempts, err = tx.Attempts(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if suspended.Status != "running" || suspended.RecoveryCount != 0 {
		t.Fatalf("graceful stop settled a recoverable run: %+v", suspended)
	}
	if steps[0].Status != "interrupted" || attempts[0].Status != "interrupted" {
		t.Fatalf("checkpoint was not persisted: step=%+v attempt=%+v", steps[0], attempts[0])
	}
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel = context.WithCancel(t.Context())
	if err := second.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	second.Wait()
	if got.Status != "succeeded" || got.RecoveryCount != 1 {
		t.Fatalf("restart did not continue: %+v", got)
	}
	var finalAttempts []w.Attempt
	var messages []w.Message
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		if finalAttempts, err = tx.Attempts(run.ID); err != nil {
			return err
		}
		messages, err = tx.Messages(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	seen := []w.Attempt{}
	for _, attempt := range finalAttempts {
		if attempt.StepID == steps[0].ID {
			seen = append(seen, attempt)
		}
	}
	if len(seen) != 2 || seen[0].Status != "interrupted" || seen[1].Status != "succeeded" {
		t.Fatalf("attempts not preserved: %+v", seen)
	}
	preserved := false
	for _, message := range messages {
		if message.Content == "partial research result" && message.Status == "interrupted" {
			preserved = true
		}
	}
	if !preserved {
		t.Fatalf("partial attempt output was overwritten: %+v", messages)
	}
	byAttempt := map[string]w.Attempt{}
	for _, attempt := range finalAttempts {
		byAttempt[attempt.ID] = attempt
	}
	stepMessages := []w.Message{}
	for _, message := range messages {
		if message.StepID == steps[0].ID && message.Role == "assistant" {
			stepMessages = append(stepMessages, message)
		}
	}
	if len(stepMessages) != 2 || stepMessages[0].AttemptID == stepMessages[1].AttemptID {
		t.Fatalf("retried step messages do not keep their own attempts: %+v", stepMessages)
	}
	for _, message := range stepMessages {
		attempt, ok := byAttempt[message.AttemptID]
		if !ok || attempt.StepID != message.StepID || attempt.Model != message.Model {
			t.Fatalf("message %s was relabelled by another attempt: %+v", message.Content, message)
		}
	}
}

func TestCanceledRunNeverRevives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	calls := atomic.Int32{}
	runner := &testRunner{run: func(ctx context.Context, _ model.Request, _ int, _ agent.Execute, _ agent.Observe) error {
		calls.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	awaitCondition(t, s, run.ID, func(_ w.Run, _ []w.Step, attempts []w.Attempt) bool { return len(attempts) > 0 })
	if _, err := e.Cancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "canceled" {
		t.Fatalf("cancel did not settle: %+v", got)
	}
	before := calls.Load()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	ctx, cancel = context.WithCancel(t.Context())
	if err := second.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	second.Wait()
	if calls.Load() != before {
		t.Fatalf("canceled run revived: before=%d after=%d", before, calls.Load())
	}
	if got, _ := loadRun(t, s, run.ID); got.Status != "canceled" {
		t.Fatalf("canceled status changed: %+v", got)
	}
}

func TestDuplicateRecoveryDispatchesEachStepOnce(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	runner := slowRunner(30 * time.Millisecond)
	first := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := first.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	first.Wait()
	second.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("run=%+v", got)
	}
	if runner.count("researcher") != 2 || runner.count("critic") != 2 || runner.count("coordinator") != 1 {
		t.Fatalf("duplicate dispatch occurred: %+v", runner.calls)
	}
}

func TestCrashWindowDoesNotRepeatCompletedSteps(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; steps, err = tx.Steps(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	completed := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, AgentID: steps[0].AgentID, Status: "completed", Content: "research already committed"}
	processing := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[1].ID, AgentID: steps[1].AgentID, Status: "processing", Content: "partial critique"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps[0].Status = "succeeded"
		steps[1].Status = "running"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		if err := tx.SaveStep(steps[1]); err != nil {
			return err
		}
		if err := tx.SaveMessage(completed); err != nil {
			return err
		}
		if err := tx.SaveMessage(processing); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: steps[1].ID, Number: 1, Status: "running", StartedAt: w.Now(), Process: &w.ProcessRef{PID: 999999, PGID: 999999, Start: "Mon Jan  1 00:00:00 2001"}})
	}); err != nil {
		t.Fatal(err)
	}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" || runner.count("researcher") != 1 || runner.count("critic") != 2 || runner.count("coordinator") != 1 {
		t.Fatalf("completed work repeated: run=%+v calls=%+v", got, runner.calls)
	}
	var messages []w.Message
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; messages, err = tx.Messages(run.ThreadID); return err }); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, message := range messages {
		if message.ID == completed.ID && message.Content == completed.Content && message.Status == "completed" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("completed history was overwritten: %+v", messages)
	}
}

func TestCrashBeforeRunFinishDoesNotRepublish(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; steps, err = tx.Steps(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	artifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[2].ID, Type: "research_report", Title: "Committed report", Body: "Body", Feedback: "unreviewed", CreatedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		for i := range steps {
			steps[i].Status = "succeeded"
			if err := tx.SaveStep(steps[i]); err != nil {
				return err
			}
		}
		return tx.SaveArtifact(artifact)
	}); err != nil {
		t.Fatal(err)
	}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("run=%+v", got)
	}
	if runner.count("researcher")+runner.count("critic")+runner.count("coordinator") != 0 {
		t.Fatalf("settled run re-executed: %+v", runner.calls)
	}
	var artifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; artifacts, err = tx.Artifacts(run.ThreadID); return err }); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].ID != artifact.ID {
		t.Fatalf("artifact duplicated: %+v", artifacts)
	}
}

func TestParallelPartialCompletionRestartOnlyRetriesIncompleteStep(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedParallelRun(t, s)
	started := make(chan struct{})
	var aCalls atomic.Int32
	runner := &testRunner{run: func(ctx context.Context, request model.Request, _ int, _ agent.Execute, observe agent.Observe) error {
		if strings.Contains(request.System, "(@cat-a)") && aCalls.Add(1) == 1 {
			if err := observe("text", map[string]any{"text": "partial A"}); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		return observe("text", map[string]any{"text": "completed " + request.System})
	}}
	first := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := first.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		cancel()
		first.Wait()
		t.Fatal("parallel step did not start")
	}
	awaitCondition(t, s, run.ID, func(_ w.Run, steps []w.Step, _ []w.Attempt) bool {
		for _, step := range steps {
			if step.AgentName == "B" && step.Status == "succeeded" {
				return true
			}
		}
		return false
	})
	cancel()
	first.Wait()
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	ctx, cancel = context.WithCancel(t.Context())
	if err := second.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	second.Wait()
	if got.Status != "succeeded" || runner.count("responder") != 3 {
		// cat-a (2 attempts) + cat-b (1 attempt)
		t.Fatalf("run=%+v calls=%+v", got, runner.calls)
	}
	var messages []w.Message
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; messages, err = tx.Messages(run.ThreadID); return err }); err != nil {
		t.Fatal(err)
	}
	completedB := 0
	for _, message := range messages {
		if message.AgentName == "B" && message.Status == "completed" {
			completedB++
		}
	}
	if completedB != 1 {
		t.Fatalf("completed parallel result duplicated: %+v", messages)
	}
}

func TestAttemptPersistsNativeSessionAndProcess(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	self, err := proc.Inspect(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	runner := &testRunner{run: func(ctx context.Context, request model.Request, _ int, execute agent.Execute, observe agent.Observe) error {
		session := newUUID(t)
		if err := observe("native_session", map[string]any{"executor": "claude", "session_id": session, "mode": "fresh", "cwd": "isolated"}); err != nil {
			return err
		}
		if err := observe("native_process", map[string]any{"pid": self.PID, "pgid": self.PGID, "start": self.Start, "executable": "fixture"}); err != nil {
			return err
		}
		if err := observe("text", map[string]any{"text": "done"}); err != nil {
			return err
		}
		if strings.Contains(request.System, "role=coordinator") {
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Session report", "body": "Body", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		return nil
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	var attempts []w.Attempt
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; attempts, err = tx.Attempts(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, attempt := range attempts {
		if attempt.SessionID != "" && attempt.Process != nil && attempt.Process.PID == self.PID && attempt.Process.Start == self.Start {
			found = true
		}
	}
	if !found {
		t.Fatalf("native ownership evidence missing: %+v", attempts)
	}
}

func slowRunner(delay time.Duration) *testRunner {
	return &testRunner{run: func(ctx context.Context, request model.Request, _ int, execute agent.Execute, observe agent.Observe) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if err := observe("text", map[string]any{"text": "result"}); err != nil {
			return err
		}
		if strings.Contains(request.System, "role=coordinator") {
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Report", "body": "Body", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		return nil
	}}
}

func loadRun(t *testing.T, s *store.Store, id string) (w.Run, error) {
	t.Helper()
	var run w.Run
	err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; run, err = tx.Run(id); return err })
	return run, err
}

func seedParallelRun(t *testing.T, s *store.Store) w.Run {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "parallel", Revision: 1}
	agents := []w.Agent{
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "A", Handle: "cat-a", Model: "default", Executor: "claude", Tools: tools.Names, Revision: 1},
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "B", Handle: "cat-b", Model: "default", Executor: "claude", Tools: tools.Names, Revision: 1},
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "queued", CreatedAt: w.Now(), Agents: agents, Input: w.RunInput{Message: "parallel task", Workflow: "free", Targets: []string{agents[0].ID, agents[1].ID}, ResearchScope: "given_sources"}}
	steps, err := Plan(run.ID, run.Input, agents)
	if err != nil {
		t.Fatal(err)
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
		if err := tx.InsertRun(run, w.NewID(), "parallel"); err != nil {
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

// TestRecoveryHelperProcess is a real child process used for ownership tests.
func TestRecoveryHelperProcess(t *testing.T) {
	if os.Getenv("COCOGEM_RECOVERY_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

func startRecoveryHelper(t *testing.T) (*exec.Cmd, proc.Identity, chan error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestRecoveryHelperProcess")
	cmd.Env = append(os.Environ(), "COCOGEM_RECOVERY_HELPER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	identity, err := proc.Inspect(cmd.Process.Pid)
	if err != nil {
		_ = proc.Terminate(proc.Identity{PID: cmd.Process.Pid, PGID: cmd.Process.Pid}, time.Second)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = proc.Terminate(identity, time.Second)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	return cmd, identity, done
}

func TestResidualOwnedProcessIsReclaimedBeforeResume(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; steps, err = tx.Steps(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	_, identity, done := startRecoveryHelper(t)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps[0].Status = "running"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: steps[0].ID, Number: 1, Status: "running", StartedAt: w.Now(), Process: &w.ProcessRef{PID: identity.PID, PGID: identity.PGID, Start: identity.Start}})
	}); err != nil {
		t.Fatal(err)
	}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" || got.RecoveryCount != 1 {
		t.Fatalf("run=%+v", got)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("owned residual process was not stopped")
	}
	var attempts []w.Attempt
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; attempts, err = tx.Attempts(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	first := attempts[0]
	if first.Status != "interrupted" || first.Recovery != "owned_process_reclaimed" {
		t.Fatalf("attempt ownership was not recorded: %+v", first)
	}
}

func TestProcessIdentityMismatchBlocksWithoutKilling(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; steps, err = tx.Steps(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	_, identity, done := startRecoveryHelper(t)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps[0].Status = "running"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: steps[0].ID, Number: 1, Status: "running", StartedAt: w.Now(), Process: &w.ProcessRef{PID: identity.PID, PGID: identity.PGID, Start: "Mon Jan  1 00:00:00 2000"}})
	}); err != nil {
		t.Fatal(err)
	}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "interrupted" || !strings.Contains(got.Error, "recovery_uncertain_process") {
		t.Fatalf("uncertain process was not kept explicit: %+v", got)
	}
	if got.RecoveryCount != 0 || len(runner.calls) != 0 {
		t.Fatalf("uncertain run was resumed: recovery=%d calls=%+v", got.RecoveryCount, runner.calls)
	}
	select {
	case <-done:
		t.Fatal("identity-mismatched process was signaled")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := e.Resume(t.Context(), run.ID); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("resume accepted an unverified process: %v", err)
	}
	_ = proc.Terminate(identity, time.Second)
	<-done
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got = awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("manual resume after verification failed: %+v", got)
	}
}

type staticChecker struct {
	mu   sync.Mutex
	info agent.AdapterInfo
	err  error
}

func (c *staticChecker) Check(context.Context, string) (agent.AdapterInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info, c.err
}

func (c *staticChecker) set(info agent.AdapterInfo, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info, c.err = info, err
}

func TestCapabilityBlockedKeepsCheckpointAndResume(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	checker := &staticChecker{}
	checker.set(agent.AdapterInfo{ID: "claude", Installed: true, Supported: false, Reason: "missing --restricted"}, errors.New("missing --restricted"))
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checker, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" || !strings.Contains(got.Error, "capability_blocked:") {
		t.Fatalf("capability checkpoint missing: %+v", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("blocked CLI was invoked: %+v", runner.calls)
	}
	var steps []w.Step
	var attempts []w.Attempt
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		if steps, err = tx.Steps(run.ID); err != nil {
			return err
		}
		attempts, err = tx.Attempts(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if steps[0].Status != "interrupted" || len(attempts) != 1 || attempts[0].Status != "blocked" {
		t.Fatalf("checkpoint was not preserved: steps=%+v attempts=%+v", steps, attempts)
	}
	if _, err := e.Resume(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), "capability_blocked:") {
		t.Fatalf("resume ignored the blocked capability: %v", err)
	}
	checker.set(agent.AdapterInfo{ID: "claude", Installed: true, Supported: true, Version: "2.1.310", Identity: "v2", Adapter: "claude-stream-json", Isolation: "enforced"}, nil)
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got = awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" || got.RecoveryCount != 1 {
		t.Fatalf("resume did not continue: %+v", got)
	}
}

func TestCLIUpgradeIsRedetectedForRetry(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	started := make(chan struct{})
	runner := &testRunner{run: func(ctx context.Context, request model.Request, call int, execute agent.Execute, observe agent.Observe) error {
		if strings.Contains(request.System, "role=researcher") && call == 1 {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		if err := observe("text", map[string]any{"text": "done"}); err != nil {
			return err
		}
		if strings.Contains(request.System, "role=coordinator") {
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Upgrade report", "body": "Body", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		return nil
	}}
	checkerOne := &staticChecker{}
	checkerOne.set(agent.AdapterInfo{ID: "claude", Installed: true, Supported: true, Version: "2.0.1", Identity: "program-a", Adapter: "claude-stream-json", Isolation: "enforced"}, nil)
	first := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checkerOne, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := first.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		cancel()
		first.Wait()
		t.Fatal("first attempt did not start")
	}
	cancel()
	first.Wait()
	checkerTwo := &staticChecker{}
	checkerTwo.set(agent.AdapterInfo{ID: "claude", Installed: true, Supported: true, Version: "2.1.310", Identity: "program-b", Adapter: "claude-stream-json", Isolation: "enforced"}, nil)
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checkerTwo, Web: testutil.Web{}}
	ctx, cancel = context.WithCancel(t.Context())
	if err := second.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	second.Wait()
	if got.Status != "succeeded" || !strings.Contains(got.RecoveryNote, "cli_changed") {
		t.Fatalf("CLI change was not recorded: %+v", got)
	}
	var attempts []w.Attempt
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; attempts, err = tx.Attempts(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	versions := map[string]bool{}
	for _, attempt := range attempts {
		if attempt.StepID == attempts[0].StepID {
			versions[attempt.CLIVersion] = true
		}
	}
	if len(versions) < 2 {
		t.Fatalf("CLI version was reused without rechecking: %+v", attempts)
	}
}

func TestWaitingDependencyDoesNotFailPendingStep(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	runner := &testRunner{run: func(ctx context.Context, request model.Request, _ int, execute agent.Execute, observe agent.Observe) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
		if err := observe("text", map[string]any{"text": "done"}); err != nil {
			return err
		}
		if strings.Contains(request.System, "role=coordinator") {
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Report", "body": "Body", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		return nil
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: Limits{RunTimeout: 5 * time.Second, StepTimeout: 250 * time.Millisecond}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("dependency wait consumed a step deadline: %+v", got)
	}
	var events []w.Event
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; events, err = tx.Events(run.ID, 0); return err }); err != nil {
		t.Fatal(err)
	}
	waiting := false
	for _, event := range events {
		if event.Type == "step_waiting" {
			waiting = true
		}
	}
	if !waiting {
		t.Fatal("dependency waiting was not visible")
	}
}

func TestAttemptStatusConflictIsRejected(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
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
	attempt := w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: stepID, Number: 1, Status: "succeeded"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveAttempt(attempt) }); err != nil {
		t.Fatal(err)
	}
	attempt.Status = "failed"
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveAttempt(attempt) }); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("terminal attempt was rewritten: %v", err)
	}
}

func TestRecoveryClaimTakesOverOnlyDeadOwner(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	self, err := proc.Inspect(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: s, InstanceID: "new-instance", Owner: self}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertRunClaim(w.RunClaim{RunID: run.ID, OwnerID: "old-instance", OwnerPID: self.PID, OwnerStart: self.Start, ClaimedAt: w.Now()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := engine.claim(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("live owner's claim was stolen")
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		return tx.ReleaseRunClaim(run.ID, "old-instance")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertRunClaim(w.RunClaim{RunID: run.ID, OwnerID: "dead-instance", OwnerPID: 999999, OwnerStart: "Mon Jan  1 00:00:00 2001", ClaimedAt: w.Now()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err = engine.claim(t.Context(), run.ID)
	if err != nil || !claimed {
		t.Fatalf("dead owner claim not recovered: claimed=%v err=%v", claimed, err)
	}
}

func TestResumeRejectsFailedRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "failed"
		run.Error = "permanent"
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s, AllowedModels: []string{"test"}}
	if _, err := e.Resume(t.Context(), run.ID); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("failed run was resumed: %v", err)
	}
}

func TestResumeRejectsThreadWithActiveRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		old := run
		old.Status = "interrupted"
		old.FinishedAt = w.Now()
		if err := tx.SaveRun(old); err != nil {
			return err
		}
		next := run
		next.ID = w.NewID()
		next.Status = "queued"
		next.CreatedAt = w.Now()
		return tx.InsertRun(next, w.NewID(), "next")
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s, Model: testutil.Model{}, AllowedModels: []string{"test"}}
	if _, err := e.Resume(t.Context(), run.ID); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("active thread was resumed concurrently: %v", err)
	}
}

func TestResumeRevalidatesSourceBinding(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	run := seedRun(t, s)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "interrupted"
		run.FinishedAt = w.Now()
		run.Workspace.Root = dir
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
	e := Engine{Store: s, Model: testutil.Model{}, AllowedModels: []string{"test"}}
	if _, err := e.Resume(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), "source directory") {
		t.Fatalf("changed binding was not checked: %v", err)
	}
}

func TestRetriedSenderCannotCreateDuplicateHandoffs(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedFreeRun(t, s)
	steps := collaborationSteps(t, s, run.ID)
	sender := steps[0]
	oldAttempt := w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: sender.ID, Number: 1, Status: "interrupted", StartedAt: w.Now(), FinishedAt: w.Now()}
	newAttempt := w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: sender.ID, Number: 2, Status: "running", StartedAt: w.Now()}
	child := w.Step{ID: w.NewID(), RunID: run.ID, AgentID: run.Agents[1].ID, AgentName: run.Agents[1].DisplayName, Role: "member", Instruction: "original delegated task", DependsOn: []string{sender.ID}, Status: "pending", Kind: "handoff", FromStepID: sender.ID, FromAgentID: sender.AgentID, FromAttemptID: oldAttempt.ID, RequestID: "original"}
	collaborationWrite(t, s, func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		sender.Status = "running"
		if err := tx.SaveStep(sender); err != nil {
			return err
		}
		if err := tx.SaveAttempt(oldAttempt); err != nil {
			return err
		}
		if err := tx.SaveAttempt(newAttempt); err != nil {
			return err
		}
		return tx.SaveStep(child)
	})
	e := Engine{Store: s, AllowedModels: []string{"test"}}
	raw, _ := json.Marshal(handoffInput{RequestID: "duplicate", TargetAgentID: run.Agents[1].ID, Task: "new delegated task", Role: "member"})
	if _, err := e.submitHandoff(t.Context(), run, sender, raw); err == nil || !strings.Contains(err.Error(), "previous attempt") {
		t.Fatalf("duplicate handoff accepted: %v", err)
	}
	same, _ := json.Marshal(handoffInput{RequestID: "original", TargetAgentID: run.Agents[1].ID, Task: "original delegated task", Role: "member"})
	if _, err := e.submitHandoff(t.Context(), run, sender, same); err != nil {
		t.Fatalf("idempotent replay rejected: %v", err)
	}
	count := 0
	for _, step := range collaborationSteps(t, s, run.ID) {
		if step.Kind == "handoff" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate handoff was persisted: %d", count)
	}
}

func TestRequiredToolFailureKeepsCheckpoint(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	runner := &testRunner{run: func(context.Context, model.Request, int, agent.Execute, agent.Observe) error {
		return errors.New("required_tools_unavailable: native CLI did not expose read_file")
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "interrupted" || !strings.Contains(got.Error, "required_tools_unavailable") {
		t.Fatalf("required tool failure was not a checkpoint: %+v", got)
	}
	var steps []w.Step
	var attempts []w.Attempt
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		if steps, err = tx.Steps(run.ID); err != nil {
			return err
		}
		attempts, err = tx.Attempts(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if attempts[0].Status != "blocked" || steps[1].Status != "pending" {
		t.Fatalf("checkpoint lost pending work: steps=%+v attempts=%+v", steps, attempts)
	}
}

func TestMissingProcessOwnershipRequiresManualResume(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; steps, err = tx.Steps(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps[0].Status = "running"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: steps[0].ID, Number: 1, Status: "running", StartedAt: w.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" || !strings.Contains(got.Error, "recovery_uncertain_process") {
		t.Fatalf("missing ownership was resumed automatically: %+v", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner was invoked without ownership confirmation: %+v", runner.calls)
	}
	if _, err := e.Resume(t.Context(), run.ID); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("resume bypassed missing process ownership: %v", err)
	}
	got, err = loadRun(t, s, run.ID)
	cancel()
	e.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "interrupted" || len(runner.calls) != 0 {
		t.Fatalf("missing ownership was not kept blocked: %+v calls=%+v", got, runner.calls)
	}
}

func TestForeignLiveClaimDoesNotStarveLaterQueuedRuns(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := seedRun(t, s)
	second := seedRun(t, s)
	enableExecutor(t, s, &first, "claude")
	enableExecutor(t, s, &second, "claude")
	self, err := proc.Inspect(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertRunClaim(w.RunClaim{RunID: first.ID, OwnerID: "other-live-server", OwnerPID: self.PID, OwnerStart: self.Start, ClaimedAt: w.Now()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, second.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("later run starved: %+v", got)
	}
	if firstRun, _ := loadRun(t, s, first.ID); firstRun.Status != "queued" {
		t.Fatalf("foreign-owned run was taken over: %+v", firstRun)
	}
}

func TestShutdownBeforeQueuedDispatchLeavesRunQueued(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	e := Engine{Store: s, Model: testutil.Model{}, Web: testutil.Web{}, AllowedModels: []string{"test"}, active: map[string]context.CancelFunc{}}
	e.executeRun(ctx, run)
	got, _ := loadRun(t, s, run.ID)
	if got.Status != "queued" {
		t.Fatalf("shutdown race settled a queued run: %+v", got)
	}
}

type connectionChecker struct {
	*staticChecker
	err error
}

func (c *connectionChecker) CheckConnection(context.Context, string, string) error { return c.err }

func TestConnectionConfigBlockKeepsCheckpointAndResume(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	base := &staticChecker{}
	base.set(agent.AdapterInfo{ID: "claude", Installed: true, Supported: true, Version: "2.1.310", Identity: "v1", Adapter: "claude-stream-json", Isolation: "enforced"}, nil)
	checker := &connectionChecker{staticChecker: base, err: errors.New("default model is not configured")}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checker, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" || !strings.Contains(got.Error, "connection_blocked:") {
		t.Fatalf("connection config was not a checkpoint: %+v", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("blocked connection invoked the runner: %+v", runner.calls)
	}
	if _, err := e.Resume(t.Context(), run.ID); err == nil {
		t.Fatal("resume ignored the connection configuration")
	}
	checker.err = nil
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got = awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("resume did not continue: %+v", got)
	}
}

func TestLegacyRunningStepWithoutAttemptRequiresManualResume(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; steps, err = tx.Steps(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps[0].Status = "running"
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" || !strings.Contains(got.Error, "no execution attempt ownership") {
		t.Fatalf("legacy in-flight step was resumed without ownership: %+v", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("legacy in-flight step executed: %+v", runner.calls)
	}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got = awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("manual resume failed: %+v", got)
	}
}

func TestFailedDelegationReturnsToCoordinatorAfterRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "normal"
		if restart {
			name = "restart"
		}
		t.Run(name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedFreeRun(t, s)
			steps := collaborationSteps(t, s, run.ID)
			sender := steps[0]
			sender.Status = "succeeded"
			child := w.Step{ID: w.NewID(), RunID: run.ID, AgentID: run.Agents[1].ID, Role: "member", Status: "failed", Error: "synthetic upstream failure", Kind: "handoff", FromStepID: sender.ID, FromAgentID: sender.AgentID, DependsOn: []string{sender.ID}}
			continuation := w.Step{ID: w.NewID(), RunID: run.ID, AgentID: sender.AgentID, Role: sender.Role, Status: "pending", Kind: "continuation", FromStepID: sender.ID, FromAgentID: sender.AgentID, DependsOn: []string{sender.ID, child.ID}}
			child.ContinuationID = continuation.ID
			collaborationWrite(t, s, func(tx *store.Tx) error {
				if restart {
					run.Status = "running"
				}
				for i := range run.Agents {
					run.Agents[i].Executor = "claude"
					run.Agents[i].Model = "default"
					if err := tx.SaveAgent(run.Agents[i]); err != nil {
						return err
					}
				}
				if err := tx.SaveRun(run); err != nil {
					return err
				}
				for _, step := range []w.Step{sender, child, continuation} {
					if err := tx.SaveStep(step); err != nil {
						return err
					}
				}
				return nil
			})
			runner := &testRunner{run: func(_ context.Context, request model.Request, _ int, _ agent.Execute, observe agent.Observe) error {
				if !strings.Contains(request.Messages[0].Content[0].Text, "synthetic upstream failure") {
					t.Error("failure context missing")
				}
				return observe("text", map[string]any{"text": "Coordinator handled the delegated failure."})
			}}
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
			ctx, cancel := context.WithCancel(t.Context())
			if err := e.Start(ctx); err != nil {
				cancel()
				t.Fatal(err)
			}
			_ = awaitRun(t, s, run.ID)
			cancel()
			e.Wait()
			if calls := runner.count(sender.Role); calls != 1 {
				t.Fatalf("coordinator continuation calls=%d; recovery skipped failure result return", calls)
			}
		})
	}
}

type staticInspector struct {
	*staticChecker
	connection agent.ConnectionInfo
	err        error
}

func (c *staticInspector) InspectConnection(context.Context, string, string) (agent.ConnectionInfo, error) {
	return c.connection, c.err
}

func TestResumeBlocksChangedResolvedModelOrUpstream(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; steps, err = tx.Steps(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "interrupted"
		run.FinishedAt = w.Now()
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps[0].Status = "interrupted"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: steps[0].ID, Number: 1, Status: "interrupted", Executor: "claude", Model: "default", ResolvedModel: "synthetic-a", ConnectionID: "connection-a", StartedAt: w.Now(), FinishedAt: w.Now(), Process: &w.ProcessRef{PID: 999999, PGID: 999999, Start: "Mon Jan  1 00:00:00 2001"}})
	}); err != nil {
		t.Fatal(err)
	}
	base := &staticChecker{}
	base.set(agent.AdapterInfo{ID: "claude", Installed: true, Supported: true, Version: "2.1.310", Identity: "same-cli", Adapter: "claude-stream-json", Isolation: "enforced"}, nil)
	inspector := &staticInspector{staticChecker: base, connection: agent.ConnectionInfo{ResolvedModel: "synthetic-b", Identity: "connection-b"}}
	runner := defaultRunner()
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Adapters: inspector, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	if _, err := e.Resume(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), "connection_changed:") {
		t.Fatalf("changed resolved connection was accepted: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner was invoked after connection change: %+v", runner.calls)
	}
	inspector.connection = agent.ConnectionInfo{ResolvedModel: "synthetic-a", Identity: "connection-a"}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("same connection could not resume: %+v", got)
	}
}

func TestPublishArtifactRetryReplacesStagedContent(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	runner := &testRunner{run: func(ctx context.Context, request model.Request, _ int, execute agent.Execute, observe agent.Observe) error {
		if err := observe("text", map[string]any{"text": "done"}); err != nil {
			return err
		}
		if !strings.Contains(request.System, "role=coordinator") {
			return nil
		}
		publish := func(body string) error {
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Staged report", "body": body, "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		if err := publish("placeholder content"); err != nil {
			return err
		}
		return publish("final corrected content")
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" {
		t.Fatalf("run=%+v", got)
	}
	var artifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; artifacts, err = tx.Artifacts(run.ThreadID); return err }); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Body != "final corrected content" {
		t.Fatalf("staged replacement was not committed exactly once: %+v", artifacts)
	}
}
