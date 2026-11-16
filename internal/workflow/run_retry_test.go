package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func markRunStatus(t *testing.T, s *store.Store, run w.Run, status, message string) {
	t.Helper()
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Status = status
		current.Error = message
		current.FinishedAt = w.Now()
		if status == "succeeded" {
			current.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
		}
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryFailedRunCreatesLinkedQueuedRun(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := seedRun(t, s)
	markRunStatus(t, s, source, "failed", "provider failure")
	var before []w.Message
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		before, err = tx.Messages(source.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s, Model: testutil.Model{}, AllowedModels: []string{"test"}, Limits: DefaultLimits()}
	retried, err := e.Retry(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID == source.ID || retried.Status != "queued" || retried.ThreadID != source.ThreadID {
		t.Fatalf("retry was not a new queued run in the same conversation: %+v", retried)
	}
	if retried.Input.Message != source.Input.Message || !slices.Equal(retried.Input.Targets, source.Input.Targets) || retried.Input.RetryOfRunID != source.ID {
		t.Fatalf("retry did not preserve the accepted input: %+v", retried.Input)
	}
	if retried.Delivery == nil || len(retried.Delivery.MessageIDs) != 0 || len(retried.Delivery.ArtifactRefs) != 0 {
		t.Fatalf("retry did not start with an empty delivery: %+v", retried.Delivery)
	}
	if retried.ExecutionPolicy == nil || retried.ExecutionPolicy.FirstResponse != DefaultLimits().FirstResponse {
		t.Fatalf("retry did not freeze the current execution policy: %+v", retried.ExecutionPolicy)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		messages, err := tx.Messages(source.ThreadID)
		if err != nil {
			return err
		}
		if len(messages) != len(before) {
			t.Fatalf("retry duplicated a user message: %d -> %d", len(before), len(messages))
		}
		steps, err := tx.Steps(retried.ID)
		if err != nil {
			return err
		}
		if len(steps) == 0 {
			return errors.New("retry has no planned steps")
		}
		for _, step := range steps {
			if step.RunID != retried.ID || step.Status != "pending" {
				t.Fatalf("retry step is not a fresh pending step: %+v", step)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	again, err := e.Retry(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != retried.ID {
		t.Fatalf("repeated retry created another run: %s != %s", again.ID, retried.ID)
	}
	e.Web = testutil.Web{}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	finished := awaitRun(t, s, retried.ID)
	cancel()
	e.Wait()
	if finished.Status != "succeeded" {
		t.Fatalf("retry did not execute: %s: %s", finished.Status, finished.Error)
	}
	if finished.Delivery == nil || len(finished.Delivery.MessageIDs) == 0 {
		t.Fatalf("retry succeeded without a delivery: %+v", finished.Delivery)
	}
}

func TestRetryRejectsSettledSuccessAndActiveConversation(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	succeeded := seedRun(t, s)
	markRunStatus(t, s, succeeded, "succeeded", "")
	e := Engine{Store: s, Model: testutil.Model{}, AllowedModels: []string{"test"}}
	if _, err := e.Retry(t.Context(), succeeded.ID); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("successful run retry error = %v, want conflict", err)
	}

	source := seedRun(t, s)
	markRunStatus(t, s, source, "failed", "protocol failure")
	active := source
	active.ID = w.NewID()
	active.Input.Message = "a different active submission"
	active.Status = "queued"
	active.CreatedAt = w.Now()
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.InsertRun(active, w.NewID(), "active") }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Retry(t.Context(), source.ID); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("active conversation retry error = %v, want conflict", err)
	}
}

// TestRetryFreezesTheAcceptedRecipients proves a retry reuses the run's accepted
// recipient set instead of re-resolving routing.
func TestRetryFreezesTheAcceptedRecipients(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := seedRun(t, s)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(source.ID)
		if err != nil {
			return err
		}
		current.Input.Targets = []string{current.Agents[0].ID}
		current.Status, current.FinishedAt = "failed", w.Now()
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s, Model: testutil.Model{}, AllowedModels: []string{"test"}}
	retried, err := e.Retry(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(retried.Input.Targets, []string{source.Agents[0].ID}) {
		t.Fatalf("retry lost the accepted recipients: %+v", retried.Input.Targets)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(retried.ID)
		if err != nil {
			return err
		}
		if len(steps) != 1 || steps[0].AgentID != source.Agents[0].ID || steps[0].Role != "responder" {
			t.Fatalf("retry did not materialize one responder step: %+v", steps)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
