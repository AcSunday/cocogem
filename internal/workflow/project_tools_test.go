package workflow

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestAnalyzeCSVProjectToolUsesHostBoundAttempt(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "analyze the captured CSV"}})
	created := attachSessionTask(t, db, &run)
	var step w.Step
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil && len(steps) == 1 {
			step = steps[0]
		}
		return err
	}); err != nil || step.ID == "" {
		t.Fatalf("load analysis step: %+v, %v", step, err)
	}
	run.GrantActions = append(slices.Clone(run.GrantActions), "publish_artifact")
	tools := projectTools(run)
	found := false
	for _, tool := range tools {
		found = found || tool.Name == "analyze_csv"
	}
	if !found {
		t.Fatal("publish_artifact run was not offered analyze_csv")
	}

	raw := []byte("name,amount\nalpha,2\nbeta,6\n")
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	source := w.Source{
		ID: "workflow-csv-source", WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID,
		Kind: "csv", OriginalContent: base64.StdEncoding.EncodeToString(raw), OriginalDigest: hash, SHA256: hash,
		ParseStatus: "parsed", ParserVersion: "csv-v1", SourceLocator: "row:N; column:M",
		CaptureStepID: step.ID, Visibility: "project", CapturedAt: w.Now(),
	}
	session := w.ExecutionSession{
		ID: "workflow-csv-session", TaskID: created.Task.ID, MemberID: step.AgentID, Status: "fresh_only",
		ExposedRefs: []string{}, CreatedAt: w.Now(), UpdatedAt: w.Now(),
	}
	emptyExposureDigest := sha256.Sum256([]byte(""))
	session.ExposureDigest = hex.EncodeToString(emptyExposureDigest[:])
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		grants, err := tx.Grants(run.WorkspaceID)
		if err != nil {
			return err
		}
		for _, grant := range grants {
			if grant.SubjectMemberID == step.AgentID && grant.Status == "active" {
				session.VisibilityEpoch = max(session.VisibilityEpoch, grant.Epoch)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		if err := tx.SaveSource(source); err != nil {
			return err
		}
		grant, err := tx.Grant("grant-" + run.WorkspaceID + "-" + step.AgentID)
		if err != nil {
			return err
		}
		grant.Actions = append(slices.Clone(grant.Actions), "publish_artifact")
		if err := tx.SaveGrant(grant); err != nil {
			return err
		}
		return tx.SaveSession(session)
	}); err != nil {
		t.Fatal(err)
	}
	attempt := w.Attempt{
		ID: "workflow-csv-attempt", RunID: run.ID, StepID: step.ID, Number: 1, Status: "running",
		ExecutionSessionRef: session.ID, StartedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveAttempt(attempt) }); err != nil {
		t.Fatal(err)
	}
	runtime := &stepRuntime{engine: &Engine{Store: db, Fallback: nativeRunnerFunc(nil)}, run: run, step: step, attempt: attempt}
	encoded, err := runtime.executeTool(t.Context(), "analyze_csv", json.RawMessage(`{"source_id":"workflow-csv-source","column":"amount","request_id":"workflow-csv-request"}`))
	if err != nil {
		t.Fatalf("analyze_csv tool failed: %v", err)
	}
	if !runtime.mutated.Load() {
		t.Fatal("successful analysis did not mark durable mutation")
	}
	var result project.CSVAnalysisView
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	if result.Execution.TaskID != created.Task.ID || result.Execution.LeaseOwner != attempt.ID || result.Chart.RunID != run.ID || result.Chart.StepID != step.ID {
		t.Fatalf("tool did not bind host execution identity: %+v", result)
	}
}
