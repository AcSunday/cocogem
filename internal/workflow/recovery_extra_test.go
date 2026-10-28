package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

func TestStepAndRunDeadlinesPersistAcrossServiceRestart(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "long"}})
	started := make(chan struct{})
	runner := &testRunner{run: func(ctx context.Context, _ model.Request, call int, _ agent.Execute, observe agent.Observe) error {
		if call == 1 {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		return observe("text", map[string]any{"text": "completed after restart"})
	}}
	first := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: Limits{RunTimeout: 5 * time.Second, StepTimeout: 3 * time.Second}}
	cancel, wait := startEngine(t, &first)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		cancel()
		wait()
		t.Fatal("first attempt did not start")
	}
	awaitCondition(t, s, run.ID, func(_ w.Run, _ []w.Step, attempts []w.Attempt) bool { return len(attempts) > 0 })
	cancel()
	wait()
	var before w.Run
	var beforeSteps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		before, readErr = tx.Run(run.ID)
		if readErr != nil {
			return readErr
		}
		beforeSteps, readErr = tx.Steps(run.ID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if before.StartedAt == "" || before.DeadlineAt == "" || before.ExecutionPolicy == nil {
		t.Fatalf("dispatch did not persist the frozen run clock: %+v", before)
	}
	if beforeSteps[0].StartedAt == "" || beforeSteps[0].DeadlineAt == "" {
		t.Fatalf("dispatch did not persist the step clock: %+v", beforeSteps[0])
	}
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: Limits{RunTimeout: time.Millisecond, StepTimeout: time.Millisecond}}
	cancel2, wait2 := startEngine(t, &second)
	defer wait2()
	got := awaitRun(t, s, run.ID)
	_ = cancel2
	if got.Status != "succeeded" || got.RecoveryCount != 1 {
		t.Fatalf("restart did not continue: %+v", got)
	}
	var afterSteps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		afterSteps, readErr = tx.Steps(run.ID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if got.StartedAt != before.StartedAt || got.DeadlineAt != before.DeadlineAt || got.ExecutionPolicy == nil || *got.ExecutionPolicy != *before.ExecutionPolicy {
		t.Fatalf("restart reset the run clock: before=%+v after=%+v", before, got)
	}
	if afterSteps[0].StartedAt != beforeSteps[0].StartedAt || afterSteps[0].DeadlineAt != beforeSteps[0].DeadlineAt {
		t.Fatalf("restart reset the step clock: before=%+v after=%+v", beforeSteps[0], afterSteps[0])
	}
}

func TestOwnInterruptedOutputIsPreviewOnly(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "recover"}})
	partial := "partial-own-output-must-not-be-a-source"
	runner := &testRunner{run: func(ctx context.Context, request model.Request, call int, execute agent.Execute, observe agent.Observe) error {
		if call == 1 {
			if err := observe("text", map[string]any{"text": partial}); err != nil {
				return err
			}
			return &nativecli.Failure{Kind: nativecli.FailureStallTimeout, Reason: "stalled after partial output"}
		}
		var attemptID string
		var messageID string
		if err := s.Read(ctx, func(tx *store.Tx) error {
			attempts, err := tx.Attempts(run.ID)
			if err != nil {
				return err
			}
			attemptID = attempts[0].ID
			messages, err := tx.MessagesMetadata(run.ThreadID)
			if err != nil {
				return err
			}
			for _, message := range messages {
				if message.RunID == run.ID && message.Role == "assistant" {
					messageID = message.ID
				}
			}
			return nil
		}); err != nil {
			return err
		}
		text := request.Messages[0].Content[0].Text
		for _, expected := range []string{partial, "not a committed result", "from_attempt_id=" + attemptID} {
			if !strings.Contains(text, expected) {
				return fmt.Errorf("recovery prompt lost %q", expected)
			}
		}
		if _, err := execute(ctx, "read_context", json.RawMessage(fmt.Sprintf(`{"kind":"message","id":%q}`, messageID))); err == nil {
			return errors.New("own interrupted output was exposed as committed context")
		}
		return observe("text", map[string]any{"text": "completed from a bounded preview"})
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	if got := awaitRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("run=%+v", got)
	}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("recovery failed: %+v", got)
	}
}

func TestSourceCapturedEventCarriesMetadataOnly(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "capture"}})
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("captured body ", 4000)
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	run.Workspace.Root = root
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(run.Workspace); err != nil {
			return err
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, execute agent.Execute, observe agent.Observe) error {
		if _, err := execute(ctx, "read_file", json.RawMessage(`{"path":"evidence.txt"}`)); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "read the capture"})
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	if got := awaitRun(t, s, run.ID); got.Status != "succeeded" {
		t.Fatalf("run=%+v", got)
	}
	_ = cancel
	found := false
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Type != "source_captured" {
				continue
			}
			found = true
			if strings.Contains(string(event.Data), "captured body") {
				return errors.New("source_captured event embedded the full capture")
			}
			var metadata map[string]any
			if err := json.Unmarshal(event.Data, &metadata); err != nil || metadata["id"] == nil {
				return errors.New("source_captured event lost capture metadata")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("capture did not emit a source_captured event")
	}
}

func TestResumeRefusesLiveOwnedProcess(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "owned"}})
	_, identity, done := startRecoveryHelper(t)
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
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: stepID, Number: 1, Status: "interrupted", StartedAt: w.Now(), FinishedAt: w.Now(), Process: &w.ProcessRef{PID: identity.PID, PGID: identity.PGID, Start: identity.Start}})
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}}
	if _, err := e.Resume(t.Context(), run.ID); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("resume started beside a live owned process: %v", err)
	}
	select {
	case <-done:
		t.Fatal("resume killed or waited on the owned process")
	default:
	}
}
