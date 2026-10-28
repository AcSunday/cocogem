package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestAcceptedPublishIsPersistedBeforeStepCompletion(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "publish"}})
	accepted := make(chan struct{})
	release := make(chan struct{})
	var acceptedErr error
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, execute agent.Execute, _ agent.Observe) error {
		payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Immediate", "body": "accepted body", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
		if _, err := execute(ctx, "publish_artifact", payload); err != nil {
			acceptedErr = err
			close(accepted)
			return err
		}
		close(accepted)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("publish_artifact was never accepted")
	}
	if acceptedErr != nil {
		t.Fatal(acceptedErr)
	}
	attempt := readAttempt(t, t.Context(), s, run.ID)
	if attempt.DraftArtifact == nil || attempt.DraftArtifact.Title != "Immediate" || attempt.DraftArtifact.Body != "accepted body" {
		t.Fatalf("accepted payload was not persisted immediately: %+v", attempt.DraftArtifact)
	}
	var artifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		artifacts, err = tx.Artifacts(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("an accepted draft became a committed artifact early: %+v", artifacts)
	}
	close(release)
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("run did not commit after release: %+v", got)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		artifacts, err = tx.Artifacts(run.ThreadID)
		return err
	}); err != nil || len(artifacts) != 1 || artifacts[0].Body != "accepted body" {
		t.Fatalf("draft was not committed exactly once: %+v %v", artifacts, err)
	}
}

func TestResumePlanDigestFailureIsFailClosed(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := w.Now()
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "interrupted"
		run.FinishedAt = now
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps[0].Status = "interrupted"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: steps[0].ID, Number: 1, Status: "failed", FailureKind: string(nativecli.FailureContextOverflow), Recovery: "context_fresh", CheckpointStart: "same", CheckpointDigest: "same", InputPlanDigest: "plan", SourceIDs: []string{"missing-source"}, StartedAt: now, FinishedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}}
	if _, err := e.Resume(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), "context_overflow_replay_unverified") {
		t.Fatalf("a digest re-derivation failure was not fail-closed: %v", err)
	}
	got, err := loadRun(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "interrupted" {
		t.Fatalf("failed digest check changed the durable checkpoint: %+v", got)
	}
}

func TestConcurrentResumeCreatesOneDispatch(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		run.Status = "interrupted"
		run.FinishedAt = w.Now()
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps[0].Status = "interrupted"
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, _ agent.Observe) error {
		return nil
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Resume(t.Context(), run.ID)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent resume: %v", err)
		}
	}
	got, err := loadRun(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "queued" || got.RecoveryCount != 1 {
		t.Fatalf("concurrent resumes were not idempotent: %+v", got)
	}
	cancel, wait := startEngine(t, &e)
	defer wait()
	final := awaitRun(t, s, run.ID)
	_ = cancel
	if final.Status != "succeeded" {
		t.Fatalf("resumed run failed: %+v", final)
	}
	attempts := loadAttempts(t, s, run.ID)
	if len(attempts) != 1 {
		t.Fatalf("concurrent resume created %d attempts", len(attempts))
	}
}
