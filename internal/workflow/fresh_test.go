package workflow

import (
	"context"
	"encoding/json"
	"errors"
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

func TestContextFreshReplayIsBlockedUntilInputsChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	runner := &testRunner{run: func(_ context.Context, _ model.Request, call int, _ agent.Execute, observe agent.Observe) error {
		if call <= 2 {
			return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "context window exhausted"}
		}
		return observe("text", map[string]any{"text": "recovered after the input plan changed"})
	}}
	first := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: Limits{FirstResponse: time.Minute, Stall: 2 * time.Minute, StallAction: "warn"}}
	cancel, wait := startEngine(t, &first)
	if got := awaitRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("first overflow did not interrupt: %+v", got)
	}
	cancel()
	wait()
	firstAttempts := loadAttempts(t, s, run.ID)
	if len(firstAttempts) != 1 {
		t.Fatalf("attempts=%+v", firstAttempts)
	}
	// A different timeout policy must not masquerade as a changed input plan.
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Limits: Limits{RunTimeout: time.Hour, StepTimeout: 10 * time.Minute, FirstResponse: 0, Stall: 0, StallAction: "stop"}}
	cancel2, wait2 := startEngine(t, &second)
	defer wait2()
	if _, err := second.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" {
		t.Fatalf("second overflow changed the outcome: %+v", got)
	}
	attempts := loadAttempts(t, s, run.ID)
	var fresh w.Attempt
	for _, attempt := range attempts {
		if attempt.Number == 2 {
			fresh = attempt
		}
	}
	if fresh.ID == "" || fresh.Recovery != "context_fresh" || fresh.RecoveredFrom != firstAttempts[0].ID {
		t.Fatalf("fresh attempt identity missing: %+v", fresh)
	}
	if fresh.InputPlanDigest != firstAttempts[0].InputPlanDigest {
		t.Fatalf("timeout policy changed the input plan digest: %q != %q", fresh.InputPlanDigest, firstAttempts[0].InputPlanDigest)
	}
	if fresh.CheckpointStart == "" || fresh.CheckpointStart != fresh.CheckpointDigest {
		t.Fatalf("empty fresh attempt should have no content increment: %+v", fresh)
	}
	before := runner.count("responder")
	if _, err := second.Resume(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), "context_overflow_replay_blocked") {
		t.Fatalf("mechanical replay was not blocked: %v", err)
	}
	if runner.count("responder") != before {
		t.Fatal("blocked resume consumed the checkpoint")
	}
	if got, _ := loadRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("blocked resume changed state: %+v", got)
	}
	steps, _ := loadSteps(t, s, run.ID)
	steps[0].Instruction = "answer with a materially different plan"
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveStep(steps[0]) }); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Resume(t.Context(), run.ID); err != nil {
		t.Fatalf("changed input plan was still blocked: %v", err)
	}
	got = awaitRun(t, s, run.ID)
	_ = cancel2
	if got.Status != "succeeded" {
		t.Fatalf("changed plan did not recover: %+v", got)
	}
	attempts = loadAttempts(t, s, run.ID)
	last := attempts[len(attempts)-1]
	if last.InputPlanDigest == fresh.InputPlanDigest {
		t.Fatal("changed step instruction did not change the input plan digest")
	}
}

func TestFreshAttemptWithNewContentMayResumeAgain(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	runner := &testRunner{run: func(ctx context.Context, _ model.Request, call int, _ agent.Execute, observe agent.Observe) error {
		switch call {
		case 1:
			return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "overflow one"}
		case 2:
			if err := observe("text", map[string]any{"text": "new evidence after the first fresh attempt"}); err != nil {
				return err
			}
			return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "overflow two"}
		default:
			return observe("text", map[string]any{"text": "completed"})
		}
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
	if got := awaitRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("second overflow=%+v", got)
	}
	attempts := loadAttempts(t, s, run.ID)
	var fresh w.Attempt
	for _, attempt := range attempts {
		if attempt.Number == 2 {
			fresh = attempt
		}
	}
	if fresh.CheckpointStart == fresh.CheckpointDigest {
		t.Fatalf("new content did not advance the checkpoint digest: %+v", fresh)
	}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatalf("resume with new content was blocked: %v", err)
	}
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" || runner.count("responder") < 3 {
		t.Fatalf("third attempt did not run: %+v calls=%d", got, runner.count("responder"))
	}
}

func TestFreshSessionReuseIsNotReportedAsSuccess(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	runner := &testRunner{run: func(ctx context.Context, _ model.Request, call int, _ agent.Execute, observe agent.Observe) error {
		if err := observe("native_session", map[string]any{"session_id": "retired-session", "mode": "fresh"}); err != nil {
			return err
		}
		if call == 1 {
			return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "overflow"}
		}
		return observe("text", map[string]any{"text": "reused session should not succeed"})
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	if got := awaitRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("run=%+v", got)
	}
	attempts := loadAttempts(t, s, run.ID)
	if attempts[0].SessionID != "retired-session" {
		t.Fatalf("first session not recorded: %+v", attempts[0])
	}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "failed" || got.FailureKind != string(nativecli.FailureProtocol) {
		t.Fatalf("reused retired session was reported as success: %+v", got)
	}
	attempts = loadAttempts(t, s, run.ID)
	last := attempts[len(attempts)-1]
	if last.Recovery != "context_fresh" || last.RecoveredFrom == "" {
		t.Fatalf("fresh identity was lost: %+v", last)
	}
}

func TestFreshSessionWithNewIdentityCompletes(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	runner := &testRunner{run: func(ctx context.Context, _ model.Request, call int, _ agent.Execute, observe agent.Observe) error {
		session := "session-old"
		if call > 1 {
			session = "session-new"
		}
		if err := observe("native_session", map[string]any{"session_id": session, "mode": "fresh"}); err != nil {
			return err
		}
		if call == 1 {
			return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "overflow"}
		}
		return observe("text", map[string]any{"text": "fresh session result"})
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	awaitRun(t, s, run.ID)
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("fresh session with a new identity did not succeed: %+v", got)
	}
	attempts := loadAttempts(t, s, run.ID)
	last := attempts[len(attempts)-1]
	if last.SessionID != "session-new" || last.Recovery != "context_fresh" {
		t.Fatalf("fresh session identity was not verified: %+v", last)
	}
}

func TestDraftArtifactSurvivesInterruptionAndCommitsOnlyOnSuccess(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "publish"}})
	runner := &testRunner{run: func(ctx context.Context, request model.Request, call int, execute agent.Execute, observe agent.Observe) error {
		if call > 1 {
			text := request.Messages[0].Content[0].Text
			if !strings.Contains(text, "Unpublished draft artifact") || !strings.Contains(text, "field=artifact_json") || strings.Contains(text, "commit it unchanged") {
				return errors.New("recovery prompt did not require re-reading and republishing the draft")
			}
			payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Draft title", "body": "draft body revalidated after interruption", "findings": []any{}, "source_ids": []string{}, "limitations": []string{"draft only"}})
			if _, err := execute(ctx, "publish_artifact", payload); err != nil {
				return err
			}
			return nil
		}
		if err := observe("text", map[string]any{"text": "about to publish"}); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Draft title", "body": "draft body kept across interruption", "findings": []any{}, "source_ids": []string{}, "limitations": []string{"draft only"}})
		if _, err := execute(ctx, "publish_artifact", payload); err != nil {
			return err
		}
		return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "overflow after publish"}
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	if got.Status != "interrupted" {
		t.Fatalf("run=%+v", got)
	}
	var artifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		artifacts, readErr = tx.Artifacts(run.ThreadID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("uncommitted draft polluted the artifact table: %+v", artifacts)
	}
	attempts := loadAttempts(t, s, run.ID)
	if attempts[0].DraftArtifact == nil || attempts[0].DraftArtifact.Body != "draft body kept across interruption" {
		t.Fatalf("draft was not persisted on the attempt: %+v", attempts[0])
	}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got = awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("resume did not commit the draft: %+v", got)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		artifacts, readErr = tx.Artifacts(run.ThreadID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Body != "draft body revalidated after interruption" || artifacts[0].Type != "note" {
		t.Fatalf("resubmitted draft was not committed exactly once: %+v", artifacts)
	}
}
