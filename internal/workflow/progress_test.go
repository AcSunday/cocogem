package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func readAttempt(t *testing.T, ctx context.Context, s *store.Store, runID string) w.Attempt {
	t.Helper()
	var attempts []w.Attempt
	if err := s.Read(ctx, func(tx *store.Tx) error {
		var err error
		attempts, err = tx.Attempts(runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(attempts) == 0 {
		t.Fatal("no attempt recorded")
	}
	return attempts[len(attempts)-1]
}

func TestLivenessWarningSnapshotAndActivityClear(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "liveness"}})
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		if err := observe("liveness_warning", map[string]any{"state": "stalled", "silence_seconds": 42, "last_event_type": "usage", "process_alive": true, "tool_active": false}); err != nil {
			return err
		}
		warning := readAttempt(t, ctx, s, run.ID)
		if warning.LivenessWarning == nil || warning.LivenessWarning.State != "stalled" || warning.LivenessWarning.SilenceSeconds != 42 || warning.LivenessWarning.LastEventType != "usage" || !warning.LivenessWarning.ProcessAlive || warning.LivenessWarning.ObservedAt == "" {
			return errors.New("liveness warning snapshot was not persisted")
		}
		if err := observe("usage", map[string]any{"known": false, "input_tokens": 0, "output_tokens": 0}); err != nil {
			return err
		}
		if got := readAttempt(t, ctx, s, run.ID); got.LivenessWarning == nil {
			return errors.New("generic usage cleared the liveness warning")
		}
		before := readAttempt(t, ctx, s, run.ID).LastProgress
		if err := observe("text", map[string]any{"text": "   "}); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
		afterBlank := readAttempt(t, ctx, s, run.ID)
		if afterBlank.LastProgress != before || afterBlank.LivenessWarning == nil {
			return errors.New("blank text counted as activity or cleared the warning")
		}
		if err := observe("native_activity", map[string]any{"kind": "bogus"}); err == nil {
			return errors.New("an invalid native_activity kind was accepted")
		}
		if err := observe("native_activity", map[string]any{"kind": "reasoning"}); err != nil {
			return err
		}
		cleared := readAttempt(t, ctx, s, run.ID)
		if cleared.LivenessWarning != nil || cleared.LastProgress == before {
			return errors.New("explicit native activity did not clear the warning and refresh progress")
		}
		return observe("text", map[string]any{"text": "liveness check complete"})
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("liveness run failed: %+v", got)
	}
}

func TestUsageTurnAccumulatesAndNativeAssigns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		observe  func(agent.Observe) error
		input    int64
		output   int64
		known    bool
		complete string
	}{
		{
			name: "turn accumulation with a later unknown turn",
			observe: func(observe agent.Observe) error {
				for _, turn := range []map[string]any{
					{"known": true, "input_tokens": 10, "output_tokens": 5, "semantics": "turn"},
					{"known": true, "input_tokens": 3, "output_tokens": 2, "semantics": "turn"},
					{"known": false, "input_tokens": 0, "output_tokens": 0, "semantics": "turn"},
				} {
					if err := observe("usage", turn); err != nil {
						return err
					}
				}
				return nil
			},
			input: 13, output: 7, known: false, complete: "partial",
		},
		{
			name: "native cumulative assignment does not double count",
			observe: func(observe agent.Observe) error {
				for i := 0; i < 2; i++ {
					if err := observe("usage", map[string]any{"known": true, "input_tokens": 10, "output_tokens": 5}); err != nil {
						return err
					}
				}
				return nil
			},
			input: 10, output: 5, known: true, complete: "known",
		},
		{
			name: "native partial pair is retained",
			observe: func(observe agent.Observe) error {
				return observe("usage", map[string]any{"known": false, "input_tokens": 7, "output_tokens": 0})
			},
			input: 7, output: 0, known: false, complete: "partial",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "usage"}})
			runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
				if err := tc.observe(observe); err != nil {
					return err
				}
				return observe("text", map[string]any{"text": "usage recorded"})
			})
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
			cancel, wait := startEngine(t, &e)
			defer wait()
			got := awaitRun(t, s, run.ID)
			_ = cancel
			if got.Status != "succeeded" {
				t.Fatalf("usage run failed: %+v", got)
			}
			attempt := readAttempt(t, t.Context(), s, run.ID)
			if attempt.InputTokens != tc.input || attempt.OutputTokens != tc.output || attempt.UsageKnown != tc.known || attempt.UsageCompleteness != tc.complete {
				t.Fatalf("usage=%+v want input=%d output=%d known=%v completeness=%s", attempt, tc.input, tc.output, tc.known, tc.complete)
			}
		})
	}
}

func TestUsagePersistsThroughCanceledCleanup(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "usage"}})
	started := make(chan struct{})
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		close(started)
		<-ctx.Done()
		if err := observe("usage", map[string]any{"known": true, "input_tokens": 9, "output_tokens": 4}); err != nil {
			return err
		}
		return ctx.Err()
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
	if _, err := e.Cancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "canceled" {
		t.Fatalf("run was not canceled: %+v", got)
	}
	attempt := readAttempt(t, t.Context(), s, run.ID)
	if attempt.InputTokens != 9 || attempt.OutputTokens != 4 || !attempt.UsageKnown || attempt.UsageCompleteness != "known" {
		t.Fatalf("known usage was lost in canceled cleanup: %+v", attempt)
	}
}

func TestWhitespaceTextIsSavedWithoutActivity(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "blank"}})
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		before := readAttempt(t, ctx, s, run.ID).LastProgress
		if err := observe("text", map[string]any{"text": "  \n"}); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
		if got := readAttempt(t, ctx, s, run.ID); got.LastProgress != before {
			return errors.New("whitespace-only text refreshed progress")
		}
		return nil
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	// Whitespace-only output is persisted as-is but is not a visible delivery.
	if got.Status != "failed" || got.FailureKind != failureArtifactContract || !strings.Contains(got.Error, "no committed message or artifact") {
		t.Fatalf("blank text run did not fail the delivery contract: %+v", got)
	}
	previews, err := previewsOf(t, s, run.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range previews {
		if message.Content == "  \n" {
			found = true
		}
	}
	if !found {
		t.Fatal("whitespace-only text was not persisted exactly")
	}
}

func previewsOf(t *testing.T, s *store.Store, threadID string) ([]w.Message, error) {
	t.Helper()
	var previews []w.Message
	err := s.Read(t.Context(), func(tx *store.Tx) error {
		messages, err := tx.MessagesMetadata(threadID)
		if err != nil {
			return err
		}
		for _, message := range messages {
			preview, err := tx.MessagePreview(message.ID, 6000)
			if err != nil {
				return err
			}
			previews = append(previews, preview)
		}
		return nil
	})
	return previews, err
}
