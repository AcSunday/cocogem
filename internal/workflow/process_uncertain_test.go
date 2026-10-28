package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
)

func TestProcessUncertainInterruptsAndResumeRequiresOwnership(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "stream"}})
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		calls.Add(1)
		// Native streamProcess filters cancellation-derived stdio/wait noise at
		// its boundary; only typed uncertainty and the cancellation remain.
		return errors.Join(
			&nativecli.Failure{Kind: nativecli.FailureProcessUncertain, Reason: "owned process group cleanup could not be verified"},
			context.Canceled,
		)
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" {
		t.Fatalf("pure process uncertainty did not preserve a checkpoint: %+v", got)
	}
	attempts := loadAttempts(t, s, run.ID)
	if len(attempts) != 1 {
		t.Fatalf("attempts=%+v", attempts)
	}
	attempt := attempts[0]
	if attempt.Status != "uncertain" || attempt.FailureKind != string(nativecli.FailureProcessUncertain) || attempt.Recovery != recoveryProcessUncertain {
		t.Fatalf("process uncertainty was not recorded on the attempt: %+v", attempt)
	}
	if attempt.Process != nil {
		t.Fatalf("fixture unexpectedly recorded process ownership: %+v", attempt.Process)
	}
	if _, err := e.Resume(t.Context(), run.ID); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("resume bypassed missing process ownership: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("an uncertain group was redispatched: %d calls", calls.Load())
	}
	stored, err := loadRun(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "interrupted" {
		t.Fatalf("refused resume changed the checkpoint: %+v", stored)
	}
}

func TestProcessUncertainBesideConfirmedFailureStillFailsRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "auth"}})
	process := &nativecli.Failure{Kind: nativecli.FailureProcessUncertain, Reason: "owned process group cleanup could not be verified"}
	auth := &nativecli.Failure{Kind: nativecli.FailureAuthentication, Reason: "provider rejected the request"}
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		return errors.Join(process, auth, context.Canceled)
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "failed" || got.FailureKind != string(nativecli.FailureAuthentication) {
		t.Fatalf("independent authentication failure was masked: %+v", got)
	}
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].FailureKind != string(nativecli.FailureAuthentication) ||
		!strings.Contains(steps[0].Error, string(nativecli.FailureProcessUncertain)) ||
		!strings.Contains(steps[0].Error, string(nativecli.FailureAuthentication)) {
		t.Fatalf("step lost one of the joined failures: %+v", steps[0])
	}
	attempts := loadAttempts(t, s, run.ID)
	if attempts[0].Recovery != recoveryProcessUncertain || !strings.Contains(attempts[0].Error, string(nativecli.FailureProcessUncertain)) {
		t.Fatalf("process uncertainty was not preserved on the attempt: %+v", attempts[0])
	}
}

func TestProcessUncertainRequestsCheckpointForSibling(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "A", Instruction: "uncertain"}, {Agent: 1, Role: "B", Instruction: "sibling"}})
	var releaseOnce sync.Once
	release := make(chan struct{})
	var calls atomic.Int32
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, _ agent.Execute, _ agent.Observe) error {
		calls.Add(1)
		if strings.Contains(request.System, "role=A ") {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("sibling never started")
			}
			return errors.Join(&nativecli.Failure{Kind: nativecli.FailureProcessUncertain, Reason: "group uncertain"}, context.Canceled)
		}
		releaseOnce.Do(func() { close(release) })
		<-ctx.Done()
		return ctx.Err()
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" || calls.Load() != 2 {
		t.Fatalf("checkpoint did not converge both attempts: %+v calls=%d", got, calls.Load())
	}
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Status != "interrupted" {
			t.Fatalf("sibling was not stopped by the checkpoint: %+v", step)
		}
	}
}
