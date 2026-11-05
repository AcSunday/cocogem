package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestExternalCompletionResolvesExactThreadEvidence(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	artifact := w.Artifact{ID: "report", Version: 2, Revision: 1, ThreadID: th.ID, RunID: "run", Title: "报告", Body: "候选正文"}
	source := w.Source{ID: "source", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: "run", Kind: "web", Title: "证据"}
	foreign := w.Artifact{ID: "other", Version: 1, Revision: 1, ThreadID: "other-thread", RunID: "foreign-run", Title: "其他任务"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveThread(w.Thread{ID: "other-thread", WorkspaceID: th.WorkspaceID, Revision: 1, UpdatedAt: w.Now()}); err != nil {
			return err
		}
		if err := tx.InsertRun(w.Run{ID: "foreign-run", ThreadID: "other-thread", WorkspaceID: th.WorkspaceID, Status: "succeeded", CreatedAt: w.Now()}, "foreign-test-key", "foreign-test-hash"); err != nil {
			return err
		}
		if err := tx.InsertRun(w.Run{ID: "run", ThreadID: th.ID, WorkspaceID: th.WorkspaceID, Status: "succeeded", CreatedAt: w.Now()}, "completion-test-key", "completion-test-hash"); err != nil {
			return err
		}
		if err := tx.SaveArtifact(w.Artifact{ID: artifact.ID, Version: 1, Revision: 1, ThreadID: th.ID, RunID: "run", Title: artifact.Title, Body: "旧版"}); err != nil {
			return err
		}
		if err := tx.SaveArtifact(artifact); err != nil {
			return err
		}
		if err := tx.SaveArtifact(foreign); err != nil {
			return err
		}
		return tx.SaveSource(source)
	}); err != nil {
		t.Fatal(err)
	}
	input := RecordCompletionInput{
		EvaluationGeneration: 1, InputSnapshotDigest: created.Revision.Digest,
		SubjectDigest: "browser-forged-digest", CandidateRefs: []string{"artifact:report@v2"},
		Results:       []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{"source:source"}}},
		JudgeMemberID: "user", EvidenceProducer: "human_confirmed",
	}
	record, err := service.RecordCompletion(t.Context(), created.Task.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if record.SubjectDigest == input.SubjectDigest || record.SubjectDigest != Digest([]w.Artifact{artifact}) {
		t.Fatalf("server-derived subject digest = %q", record.SubjectDigest)
	}

	for _, test := range []struct {
		name      string
		candidate string
		evidence  string
	}{
		{name: "foreign candidate", candidate: "artifact:other@v1", evidence: "source:source"},
		{name: "missing candidate version", candidate: "artifact:report@v3", evidence: "source:source"},
		{name: "unversioned candidate", candidate: "artifact:report", evidence: "source:source"},
		{name: "foreign evidence", candidate: "artifact:report@v2", evidence: "artifact:other@v1"},
		{name: "missing evidence", candidate: "artifact:report@v2", evidence: "source:missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := input
			invalid.CandidateRefs = []string{test.candidate}
			invalid.Results = []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{test.evidence}}}
			if _, err := service.RecordCompletion(t.Context(), created.Task.ID, invalid); !IsCode(err, "invalid_request") {
				t.Fatalf("invalid external evidence error = %v", err)
			}
		})
	}
}
