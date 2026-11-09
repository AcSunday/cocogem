package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// completionFixture seeds one task thread with:
//   - a successful run of the task delivering exactly artifact v2 (v1 stays an
//     undelivered earlier version of the same artifact),
//   - a successful run of the same thread that predates the task (promotion),
//   - a failed run of the task whose artifact was never delivered,
//   - a second task in another thread with its own successful delivery.
func completionFixture(t *testing.T) (*store.Store, *Service, TaskView, TaskView) {
	t.Helper()
	db, service, th := setup(t)
	created := create(t, service, th)
	otherThread := w.Thread{ID: "other-thread", WorkspaceID: th.WorkspaceID, Title: "另一任务", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveThread(otherThread); err != nil {
			return err
		}
		// A chat run in the same thread before the task was promoted.
		if err := tx.InsertRun(w.Run{ID: "pre-task", ThreadID: th.ID, WorkspaceID: th.WorkspaceID, Status: "succeeded", CreatedAt: w.Now()}, "pre-task-key", "pre-task-hash"); err != nil {
			return err
		}
		// The task's successful delivery.
		if err := tx.InsertRun(w.Run{
			ID: "run", ThreadID: th.ID, WorkspaceID: th.WorkspaceID, TaskID: created.Task.ID,
			Status: "succeeded", CreatedAt: w.Now(),
			Delivery: &w.Delivery{MessageIDs: []string{"reply"}, ArtifactRefs: []w.ArtifactRef{{ID: "report", Version: 2}}},
		}, "completion-test-key", "completion-test-hash"); err != nil {
			return err
		}
		// A failed run of the same task produced an artifact that was never delivered.
		if err := tx.InsertRun(w.Run{
			ID: "failed-run", ThreadID: th.ID, WorkspaceID: th.WorkspaceID, TaskID: created.Task.ID,
			Status: "failed", CreatedAt: w.Now(),
		}, "failed-key", "failed-hash"); err != nil {
			return err
		}
		// Another task in another thread with its own delivery.
		if err := tx.InsertRun(w.Run{
			ID: "foreign-run", ThreadID: otherThread.ID, WorkspaceID: th.WorkspaceID, TaskID: "foreign-task",
			Status: "succeeded", CreatedAt: w.Now(),
			Delivery: &w.Delivery{ArtifactRefs: []w.ArtifactRef{{ID: "other", Version: 1}}},
		}, "foreign-test-key", "foreign-test-hash"); err != nil {
			return err
		}
		for _, artifact := range []w.Artifact{
			{ID: "report", Version: 1, Revision: 1, ThreadID: th.ID, RunID: "run", Title: "报告", Body: "旧版"},
			{ID: "report", Version: 2, Revision: 1, ThreadID: th.ID, RunID: "run", Title: "报告", Body: "候选正文"},
			{ID: "stale", Version: 1, Revision: 1, ThreadID: th.ID, RunID: "pre-task", Title: "升级前成果", Body: "升级之前"},
			{ID: "failed", Version: 1, Revision: 1, ThreadID: th.ID, RunID: "failed-run", Title: "失败成果", Body: "失败"},
			{ID: "other", Version: 1, Revision: 1, ThreadID: otherThread.ID, RunID: "foreign-run", Title: "其他任务"},
		} {
			if err := tx.SaveArtifact(artifact); err != nil {
				return err
			}
		}
		if err := tx.SaveSource(w.Source{ID: "source", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: "run", Kind: "web", Title: "证据"}); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: "reply", RunID: "run", ThreadID: th.ID, Role: "assistant", Status: "completed", Content: "最终交付正文"})
	}); err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: otherThread.ID, OwnerMemberID: "owner",
		Goal: "另一任务目标", SourceMessageID: "message-2", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, service, created, other
}

// TestExternalCompletionRequiresTaskRunAndDeliveredVersion proves the completion
// gate is not satisfied by a thread_id match alone: the artifact must come from a
// successful run of this task and be the exact delivered version.
func TestExternalCompletionRequiresTaskRunAndDeliveredVersion(t *testing.T) {
	_, service, created, other := completionFixture(t)
	input := RecordCompletionInput{
		EvaluationGeneration: 1, InputSnapshotDigest: created.Revision.Digest,
		SubjectDigest: "browser-forged-digest", CandidateRefs: []string{"artifact:report@v2"},
		Results:       []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{"artifact:report@v2"}}},
		JudgeMemberID: "user", EvidenceProducer: "human_confirmed",
	}
	record, err := service.RecordCompletion(t.Context(), created.Task.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	delivered := w.Artifact{ID: "report", Version: 2, Revision: 1, ThreadID: created.Task.ThreadID, RunID: "run", Title: "报告", Body: "候选正文"}
	if record.SubjectDigest == input.SubjectDigest || record.SubjectDigest != Digest([]w.Artifact{delivered}) {
		t.Fatalf("server-derived subject digest = %q", record.SubjectDigest)
	}

	for _, test := range []struct {
		name      string
		task      string
		candidate string
		evidence  string
	}{
		{name: "other task artifact", task: created.Task.ID, candidate: "artifact:other@v1", evidence: "artifact:report@v2"},
		{name: "same thread artifact from before the task", task: created.Task.ID, candidate: "artifact:stale@v1", evidence: "artifact:report@v2"},
		{name: "undelivered earlier version", task: created.Task.ID, candidate: "artifact:report@v1", evidence: "artifact:report@v2"},
		{name: "failed run artifact", task: created.Task.ID, candidate: "artifact:failed@v1", evidence: "artifact:report@v2"},
		{name: "missing candidate version", task: created.Task.ID, candidate: "artifact:report@v3", evidence: "artifact:report@v2"},
		{name: "unversioned candidate", task: created.Task.ID, candidate: "artifact:report", evidence: "artifact:report@v2"},
		{name: "foreign evidence", task: created.Task.ID, candidate: "artifact:report@v2", evidence: "artifact:other@v1"},
		{name: "missing evidence", task: created.Task.ID, candidate: "artifact:report@v2", evidence: "artifact:report@v9"},
		{name: "delivery of another task cannot complete this one", task: other.Task.ID, candidate: "artifact:report@v2", evidence: "artifact:report@v2"},
		{name: "captured source is no longer acceptance evidence", task: created.Task.ID, candidate: "artifact:report@v2", evidence: "source:source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := input
			invalid.CandidateRefs = []string{test.candidate}
			invalid.Results = []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{test.evidence}}}
			if _, err := service.RecordCompletion(t.Context(), test.task, invalid); !IsCode(err, "invalid_request") && !IsCode(err, "evidence_stale") {
				t.Fatalf("invalid external evidence error = %v", err)
			}
		})
	}
}

// TestTaskDeliveriesProjectionIsNarrowAndSourceFree pins the TaskDetail contract:
// only this task's runs and exactly the delivered versions appear, and no source
// payload or thread-wide artifact is included.
func TestTaskDeliveriesProjectionIsNarrowAndSourceFree(t *testing.T) {
	_, service, created, _ := completionFixture(t)
	projection, err := service.Deliveries(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.TaskID != created.Task.ID {
		t.Fatalf("task id = %q", projection.TaskID)
	}
	ids := []string{}
	for _, run := range projection.Runs {
		ids = append(ids, run.ID+":"+run.Status)
	}
	if len(projection.Runs) != 2 || ids[0] != "run:succeeded" || ids[1] != "failed-run:failed" {
		t.Fatalf("task runs = %v", ids)
	}
	if len(projection.Artifacts) != 1 || projection.Artifacts[0].ID != "report" || projection.Artifacts[0].Version != 2 {
		t.Fatalf("artifacts = %+v", projection.Artifacts)
	}
	if len(projection.Messages) != 1 || projection.Messages[0].ID != "reply" || projection.Messages[0].Content != "最终交付正文" {
		t.Fatalf("messages = %+v", projection.Messages)
	}
}

// TestHistoricalSourceEvidenceStaysReadable proves rejecting a new source-bearing
// acceptance does not rewrite history: an old completion record keeps its exact
// evidence references and stays visible on the task view.
func TestHistoricalSourceEvidenceStaysReadable(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	historical := w.CompletionRecord{
		ID: "historical", TaskID: created.Task.ID, TaskRevision: 1, EvaluationGeneration: 1,
		InputSnapshotDigest: created.Revision.Digest,
		CandidateRefs:       []string{"artifact:old@v1"},
		Results:             []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{"source:old-capture"}}},
		JudgeMemberID:       "user", EvidenceProducer: "human_confirmed", Validity: "current", CreatedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.InsertCompletion(historical) }); err != nil {
		t.Fatal(err)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range view.Completions {
		if record.ID != historical.ID {
			continue
		}
		found = true
		if len(record.Results) != 1 || len(record.Results[0].Evidence) != 1 || record.Results[0].Evidence[0] != "source:old-capture" {
			t.Fatalf("historical evidence was rewritten: %+v", record.Results)
		}
		if len(record.CandidateRefs) != 1 || record.CandidateRefs[0] != "artifact:old@v1" {
			t.Fatalf("historical candidates were rewritten: %+v", record.CandidateRefs)
		}
	}
	if !found {
		t.Fatal("historical completion record is not readable")
	}
}
