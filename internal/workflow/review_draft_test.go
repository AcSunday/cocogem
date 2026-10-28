package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
)

func TestReviewResumeDoesNotPublishPreviousDraftWithoutResubmission(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "coordinator", Instruction: "publish reviewed result"}})
	runner := &testRunner{run: func(ctx context.Context, request model.Request, call int, execute agent.Execute, observe agent.Observe) error {
		if call == 1 {
			_, err := execute(ctx, "publish_artifact", json.RawMessage(`{"type":"note","title":"Draft","body":"unverified previous draft","findings":[],"source_ids":[],"limitations":[]}`))
			if err != nil {
				return err
			}
			return &nativecli.Failure{Kind: nativecli.FailureStallTimeout, Reason: "interrupted before publication"}
		}
		prompt := request.Messages[0].Content[0].Text
		if !strings.Contains(prompt, "unavailable through read_context") || !strings.Contains(prompt, "will not be published automatically") {
			t.Error("draft recovery prompt promised unavailable or automatic draft retrieval/publication")
		}
		return observe("text", map[string]any{"text": "The previous draft still needs verification."})
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	if got := awaitRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("first attempt was not interrupted: %+v", got)
	}
	if _, err := e.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	if got.Status != "failed" || got.FailureKind != failureArtifactContract {
		t.Fatalf("an old draft was treated as a newly submitted deliverable: %+v", got)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		artifacts, err := tx.Artifacts(run.ThreadID)
		if err == nil && len(artifacts) != 0 {
			t.Fatalf("old draft was silently published: %+v", artifacts)
		}
		attempts, err := tx.Attempts(run.ID)
		if err == nil && (len(attempts) != 2 || attempts[0].DraftArtifact == nil) {
			t.Fatalf("original draft was not retained on its original attempt: %+v", attempts)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
