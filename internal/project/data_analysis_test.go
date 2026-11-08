package project

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestAnalyzeCSVUsesImmutableCaptureAndPublishesVersionedResults(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	raw := []byte("name,amount\nalpha,2\nbeta,4\nmissing,\ninvalid,nope\n")
	digest := sha256.Sum256(raw)
	originalDigest := hex.EncodeToString(digest[:])
	run := w.Run{
		ID: "csv-analysis-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID,
		TaskID: created.Task.ID, TaskRevision: created.Task.CurrentRevision,
		CoordinatorID: "owner", Status: "running", CreatedAt: w.Now(),
	}
	step := w.Step{ID: "csv-analysis-step", RunID: run.ID, AgentID: "owner", Role: "responder", Status: "running"}
	source := w.Source{
		ID: "csv-analysis-source", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: run.ID,
		Kind: "csv", Path: "/a/path/that/does/not/exist.csv", OriginalContent: base64.StdEncoding.EncodeToString(raw),
		OriginalDigest: originalDigest, SHA256: originalDigest, ParseStatus: "parsed", ParseQuality: "complete",
		ParserVersion: "csv-v1", SourceLocator: "row:N; column:M", CaptureStepID: step.ID,
		Visibility: "project", CapturedAt: w.Now(),
	}
	session := w.ExecutionSession{
		ID: "csv-analysis-session", TaskID: created.Task.ID, MemberID: "owner", VisibilityEpoch: 1, Status: "fresh_only",
		ExposedRefs: []string{}, CreatedAt: w.Now(), UpdatedAt: w.Now(),
	}
	session.ExposureDigest = exposureRefsDigest(session.ExposedRefs)
	attempt := w.Attempt{
		ID: "csv-analysis-attempt", RunID: run.ID, StepID: step.ID, Number: 1,
		Status: "running", ExecutionSessionRef: session.ID, StartedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, "csv-analysis-run-key", "csv-analysis-run-digest"); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.SaveSource(source); err != nil {
			return err
		}
		if err := SaveDatasetForSource(tx, run, source); err != nil {
			return err
		}
		manifest, err := service.PrepareContextInTx(tx, created.Task.ID, created.Task.CurrentRevision, 10000)
		if err != nil {
			return err
		}
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.ContextRef = manifest.ID
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		if err := tx.SaveSession(session); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	input := AnalyzeCSVInput{
		TaskID: created.Task.ID, RunID: run.ID, StepID: step.ID, AttemptID: attempt.ID,
		SourceID: source.ID, Column: "amount", RequestID: "csv-request-1",
	}
	result, err := service.AnalyzeCSV(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	var stats CSVStatistics
	if err := json.Unmarshal([]byte(result.Statistics.Content), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Rows != 4 || stats.Numeric != 2 || stats.Missing != 1 || stats.Invalid != 1 || stats.Minimum != 2 || stats.Maximum != 4 || stats.Sum != 6 || stats.Mean != 3 || stats.ChartedValues != 2 {
		t.Fatalf("unexpected CSV statistics: %+v", stats)
	}
	if result.Chart.Type != "data_chart" || !strings.Contains(result.Chart.Body, "<svg") || !slices.Contains(result.Chart.SourceIDs, source.ID) || !slices.Contains(result.Chart.SourceIDs, result.Statistics.ID) {
		t.Fatalf("unexpected chart artifact: %+v", result.Chart)
	}
	if result.Dataset.Revision != 2 || !slices.Contains(result.Dataset.ProcessingSteps, "csv-stats-v1:"+result.Statistics.ID) {
		t.Fatalf("dataset was not versioned with the analysis step: %+v", result.Dataset)
	}
	if result.Execution.Kind != "isolated_csv_v1" || result.Execution.Status != "released" || !strings.Contains(result.Execution.Observation, "no_original_file_access") {
		t.Fatalf("unexpected execution receipt: %+v", result.Execution)
	}
	replay, err := service.AnalyzeCSV(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Statistics.ID != result.Statistics.ID || replay.Chart.ID != result.Chart.ID || replay.Execution.ID != result.Execution.ID || replay.Dataset.Revision != result.Dataset.Revision {
		t.Fatalf("idempotent replay created different results: first=%+v replay=%+v", result, replay)
	}
	conflicting := input
	conflicting.Column = "name"
	if _, err := service.AnalyzeCSV(t.Context(), conflicting); !IsCode(err, "conflict") {
		t.Fatalf("request ID reuse with different input = %v", err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		after, err := tx.Source(source.ID)
		if err != nil {
			return err
		}
		if after.OriginalContent != source.OriginalContent || after.SHA256 != source.SHA256 || after.OriginalDigest != source.OriginalDigest {
			t.Fatalf("immutable capture changed: before=%+v after=%+v", source, after)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	otherThread := w.Thread{ID: "csv-other-thread", WorkspaceID: th.WorkspaceID, CoordinatorID: "owner", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(otherThread) }); err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: otherThread.ID, OwnerMemberID: "owner",
		Goal: "independent private CSV owner", SourceMessageID: "csv-other-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherRun := w.Run{ID: "csv-other-run", WorkspaceID: th.WorkspaceID, ThreadID: otherThread.ID, TaskID: other.Task.ID, TaskRevision: 1, Status: "succeeded", CreatedAt: w.Now()}
	privateSource := source
	privateSource.ID = "csv-private-other-scope"
	privateSource.RunID = otherRun.ID
	privateSource.ThreadID = otherThread.ID
	privateSource.Visibility = "private"
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(otherRun, "csv-other-run-key", "csv-other-run-digest"); err != nil {
			return err
		}
		return tx.SaveSource(privateSource)
	}); err != nil {
		t.Fatal(err)
	}
	outside := input
	outside.SourceID = privateSource.ID
	outside.RequestID = "csv-request-outside"
	if _, err := service.AnalyzeCSV(t.Context(), outside); !IsCode(err, "disclosure_denied") {
		t.Fatalf("private CSV crossed collaboration scopes: %v", err)
	}

	nonNumeric := source
	nonNumeric.ID = "csv-without-numbers"
	nonNumericRaw := []byte("name,amount\nalpha,nope\nbeta,\n")
	nonNumericDigest := sha256.Sum256(nonNumericRaw)
	nonNumeric.SHA256 = hex.EncodeToString(nonNumericDigest[:])
	nonNumeric.OriginalDigest = nonNumeric.SHA256
	nonNumeric.OriginalContent = base64.StdEncoding.EncodeToString(nonNumericRaw)
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveSource(nonNumeric) }); err != nil {
		t.Fatal(err)
	}
	empty := input
	empty.SourceID = nonNumeric.ID
	empty.RequestID = "csv-request-no-numbers"
	if _, err := service.AnalyzeCSV(t.Context(), empty); !IsCode(err, "insufficient_evidence") {
		t.Fatalf("non-numeric analysis = %v", err)
	}
}
