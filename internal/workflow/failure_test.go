package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestClassifyJoinedCancellationRetainsIndependentFailure(t *testing.T) {
	auth := &nativecli.Failure{Kind: nativecli.FailureAuthentication, Reason: "provider rejected the request"}
	independent := errors.New("independent execution failure")
	for _, tc := range []struct {
		name   string
		err    error
		step   string
		kind   string
		reason string
	}{
		{"typed failure beside cancellation", errors.Join(context.Canceled, auth), "failed", string(nativecli.FailureAuthentication), auth.Error()},
		{"unknown failure beside cancellation", errors.Join(context.Canceled, independent), "failed", failureUnknown, independent.Error()},
		{"only cancellation", errors.Join(context.Canceled, context.DeadlineExceeded), "interrupted", failureCheckpointInterrupt, ""},
		{"wrapped cancellation", fmt.Errorf("runner stopped: %w", context.Canceled), "interrupted", failureCheckpointInterrupt, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStepError(tc.err, context.Canceled, true, false, false, false)
			if got.step != tc.step || got.attempt != tc.step {
				t.Fatalf("wrong outcome: %+v", got)
			}
			if tc.kind != "" && got.kind != tc.kind {
				t.Fatalf("kind=%q want %q", got.kind, tc.kind)
			}
			if tc.reason != "" && got.reason != tc.reason {
				t.Fatalf("reason=%q want %q", got.reason, tc.reason)
			}
		})
	}
}

func TestSiblingFailureAfterCheckpointStillFailsTheRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "A", Instruction: "one"}, {Agent: 1, Role: "B", Instruction: "two"}})
	bStarted := make(chan struct{})
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, _ agent.Execute, _ agent.Observe) error {
		switch {
		case strings.Contains(request.System, "role=A "):
			<-bStarted
			return &nativecli.Failure{Kind: nativecli.FailureStallTimeout, Reason: "checkpoint requested"}
		case strings.Contains(request.System, "role=B "):
			close(bStarted)
			<-ctx.Done()
			return &nativecli.Failure{Kind: nativecli.FailureAuthentication, Reason: "provider rejected the request"}
		default:
			return nil
		}
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "failed" || got.FailureKind != string(nativecli.FailureAuthentication) {
		t.Fatalf("sibling checkpoint hid a real failure: %+v", got)
	}
	steps, _ := loadSteps(t, s, run.ID)
	for _, step := range steps {
		if step.Role == "B" && (step.Status != "failed" || step.FailureKind != string(nativecli.FailureAuthentication) || !strings.Contains(step.Error, "provider rejected the request")) {
			t.Fatalf("independent failure evidence was lost: %+v", step)
		}
	}
}

func TestStepDeadlineAdoptsEarliestDispatchedAttempt(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	blockedAt := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	firstDispatch := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	retry := now.Add(-10 * time.Minute).Format(time.RFC3339Nano)
	policy := w.ExecutionPolicy{StepTimeout: time.Hour}
	step := w.Step{ID: "step"}
	attempts := []w.Attempt{
		{ID: "blocked", Status: "blocked", StartedAt: blockedAt},
		{ID: "retry", Status: "interrupted", StartedAt: retry},
		{ID: "first", Status: "failed", StartedAt: firstDispatch},
	}
	deadline, expiredNow := applyStepDeadline(&step, attempts, policy, now)
	if step.StartedAt != firstDispatch {
		t.Fatalf("legacy step ignored its earliest dispatched attempt: %q", step.StartedAt)
	}
	if want := now.Add(30 * time.Minute); !deadline.Equal(want) || expiredNow {
		t.Fatalf("deadline=%s expired=%v want=%s", deadline, expiredNow, want)
	}
	// A blocked preflight never dispatched: it neither starts the clock nor
	// creates a deadline for a step that has no real attempt.
	pending := w.Step{ID: "pending"}
	onlyBlocked := []w.Attempt{{ID: "blocked", Status: "blocked", StartedAt: blockedAt}}
	deadline, expiredNow = applyStepDeadline(&pending, onlyBlocked, policy, now)
	if pending.StartedAt != "" || pending.DeadlineAt != "" || !deadline.IsZero() || expiredNow {
		t.Fatalf("blocked preflight started the logical step: %+v deadline=%s expired=%v", pending, deadline, expiredNow)
	}
	startStepClock(&pending, policy, now)
	if pending.StartedAt != now.Format(time.RFC3339Nano) || !strings.HasPrefix(pending.DeadlineAt, now.Add(time.Hour).Format(time.RFC3339Nano)) {
		t.Fatalf("first actual dispatch did not start the clock: %+v", pending)
	}
}
