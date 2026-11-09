package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
)

func TestStaleAnswerPersistsSupersededQuestion(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	question, err := service.AskQuestion(t.Context(), created.Task.ID, "branch", "which accepted revision?", []string{"one", "two"}, "one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ReviseTask(t.Context(), created.Task.ID, 1, ReviseTaskInput{
		Goal: "revised goal", Acceptance: created.Revision.Acceptance, AllowedActions: created.Revision.AllowedActions,
		SourceMessageID: "revision-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	answered, err := service.AnswerQuestion(t.Context(), created.Task.ID, question.ID, "two", "answer-request-1", 1)
	if !IsCode(err, "revision_conflict") || answered.Status != "superseded" {
		t.Fatalf("stale answer = %+v, %v", answered, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		persisted, err := tx.Question(question.ID)
		if err != nil || persisted.Status != "superseded" || persisted.Answer != "" {
			t.Fatalf("superseded question rolled back = %+v, %v", persisted, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
