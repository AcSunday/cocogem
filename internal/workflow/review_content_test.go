package workflow

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestReviewCheckpointDraftIdentityDoesNotCreateProgress(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "researcher", Instruction: "review"}})
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draft := w.Artifact{ID: w.NewID(), Version: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, Type: "note", Title: "Evidence", Body: "same content", Findings: []w.Finding{{Title: "Condition", Detail: "not yet verified", Status: "insufficient"}}}
	clone := draft
	clone.ID, clone.Version, clone.Revision, clone.CreatedAt = w.NewID(), 2, 3, w.Now()
	var before, after string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		before, err = checkpointDigest(tx, run.ID, run.ThreadID, steps[0].ID, nil, &draft)
		if err != nil {
			return err
		}
		after, err = checkpointDigest(tx, run.ID, run.ThreadID, steps[0].ID, nil, &clone)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("an identical draft under a new artifact ID/version bypassed the no-content replay check")
	}
}

func TestReviewRetainedDraftIsNotLostFromFreshCheckpoint(t *testing.T) {
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
	draft := w.Artifact{ID: w.NewID(), Version: 1, Type: "note", Title: "Draft", Body: "saved evidence"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: steps[0].ID, Number: 1, Status: "failed", DraftArtifact: &draft})
	}); err != nil {
		t.Fatal(err)
	}
	var start, end string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		start, err = checkpointDigest(tx, run.ID, run.ThreadID, steps[0].ID, nil, &draft)
		if err != nil {
			return err
		}
		end, err = checkpointDigest(tx, run.ID, run.ThreadID, steps[0].ID, nil, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if start != end {
		t.Fatal("omitting an old draft from the new attempt falsely counted as new content")
	}
}
