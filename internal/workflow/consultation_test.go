package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestConsultationRunsAndSettlesCorrelatedReceipt(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	originalRun := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	original := attachSessionTask(t, db, &originalRun)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(originalRun.ID)
		if err != nil {
			return err
		}
		current.Status = "canceled"
		current.FinishedAt = w.Now()
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	senderThread := w.Thread{ID: "consult-sender-thread", WorkspaceID: originalRun.WorkspaceID, CoordinatorID: originalRun.Agents[0].ID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(senderThread) }); err != nil {
		t.Fatal(err)
	}
	sender, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: originalRun.WorkspaceID, ThreadID: senderThread.ID, OwnerMemberID: originalRun.Agents[0].ID,
		Goal: "request correlated review", SourceMessageID: "consult-request", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := project.SendTaskMessageInput{
		SenderTaskID: sender.Task.ID, TargetTaskID: original.Task.ID,
		Body: "which exact evidence supports the previous result?", RequestID: "consult-question-1",
	}
	message, err := service.SendTaskMessage(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	consultation, err := service.Task(t.Context(), message.Message.TargetTaskID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.SendTaskMessage(t.Context(), input)
	if err != nil || replay.Message.ID != message.Message.ID || replay.Message.TargetTaskID != consultation.Task.ID {
		t.Fatalf("consultation replay = %+v, %v", replay, err)
	}
	if consultation.Task.Kind != "consultation" || consultation.Task.ParentID != sender.Task.ID || consultation.Budget.ID != sender.Budget.ID {
		t.Fatalf("consultation identity or budget = %+v", consultation)
	}
	var run w.Run
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		runs, err := tx.Runs(consultation.Task.ThreadID)
		if err != nil {
			return err
		}
		if len(runs) != 1 {
			t.Fatalf("consultation runs = %+v", runs)
		}
		run = runs[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if run.CollaborationMessageID != message.Message.ID || !run.AutomaticContinuation {
		t.Fatalf("consultation run is not correlated: %+v", run)
	}
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": defaultRunner()}}
	_, wait := startEngine(t, &engine)
	defer wait()
	if got := awaitRun(t, db, run.ID); got.Status != "succeeded" {
		t.Fatalf("consultation run = %+v", got)
	}
	receipts, err := service.DeliveryReceipts(t.Context(), message.Message.ID)
	if err != nil || len(receipts) != 1 || receipts[0].Status != "handled" || receipts[0].ResponseRef == "" {
		t.Fatalf("consultation receipt = %+v, %v", receipts, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		originalTask, err := tx.Task(original.Task.ID)
		if err != nil {
			return err
		}
		if originalTask.Status != "ready" || originalTask.CurrentRunID != "" {
			t.Fatalf("consultation reopened the original task: %+v", originalTask)
		}
		account, err := tx.BudgetAccount(sender.Budget.ID)
		if err != nil {
			return err
		}
		if account.UsedContinuations != 1 || account.ReservedRuns != 0 {
			t.Fatalf("consultation budget = %+v", account)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seedConsultationRequest(t *testing.T) (*store.Store, w.Run, w.CollaborationMessage) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	originalRun := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	original := attachSessionTask(t, db, &originalRun)
	service := project.New(db)
	senderThread := w.Thread{ID: w.NewID(), WorkspaceID: originalRun.WorkspaceID, CoordinatorID: originalRun.Agents[0].ID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(originalRun.ID)
		if err != nil {
			return err
		}
		current.Status = "canceled"
		current.FinishedAt = w.Now()
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		return tx.SaveThread(senderThread)
	}); err != nil {
		t.Fatal(err)
	}
	sender, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: originalRun.WorkspaceID, ThreadID: senderThread.ID, OwnerMemberID: originalRun.Agents[0].ID,
		Goal: "request review", SourceMessageID: "consult-request", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := service.SendTaskMessage(t.Context(), project.SendTaskMessageInput{
		SenderTaskID: sender.Task.ID, TargetTaskID: original.Task.ID,
		Body: "which evidence supports the previous result?", RequestID: "consult-question",
	})
	if err != nil {
		t.Fatal(err)
	}
	consultation, err := service.Task(t.Context(), message.Message.TargetTaskID)
	if err != nil {
		t.Fatal(err)
	}
	var run w.Run
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		runs, err := tx.Runs(consultation.Task.ThreadID)
		if err != nil {
			return err
		}
		if len(runs) != 1 {
			t.Fatalf("consultation runs = %+v", runs)
		}
		run = runs[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return db, run, message.Message
}

func TestConsultationSettlesArtifactOnlyResponse(t *testing.T) {
	db, run, message := seedConsultationRequest(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		grants, err := tx.Grants(run.WorkspaceID)
		if err != nil {
			return err
		}
		for _, grant := range grants {
			if grant.SubjectMemberID == run.Agents[0].ID {
				grant.Actions = append(grant.Actions, "publish_artifact")
				grant.Revision++
				return tx.SaveGrant(grant)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, execute agent.Execute, _ agent.Observe) error {
		payload, _ := json.Marshal(map[string]any{
			"type": "note", "title": "Correlated answer", "body": "The durable work contains the requested evidence.",
			"findings": []any{}, "source_ids": []string{}, "limitations": []string{"synthetic test"},
		})
		_, err := execute(ctx, "publish_artifact", payload)
		return err
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}}
	_, wait := startEngine(t, &engine)
	defer wait()
	if got := awaitRun(t, db, run.ID); got.Status != "succeeded" {
		t.Fatalf("artifact-only consultation run = %+v", got)
	}
	receipts, err := project.New(db).DeliveryReceipts(t.Context(), message.ID)
	if err != nil || len(receipts) != 1 || receipts[0].Status != "handled" || !strings.HasPrefix(receipts[0].ResponseRef, "artifact:") {
		t.Fatalf("artifact consultation receipt = %+v, %v", receipts, err)
	}
}

func TestConsultationRejectsResponseAfterPrivateRead(t *testing.T) {
	db, run, message := seedConsultationRequest(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveSource(w.Source{
			ID: "consult-private-read", WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID,
			Kind: "file", Content: "private evidence", Visibility: "private",
		})
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, execute agent.Execute, observe agent.Observe) error {
		if _, err := execute(ctx, "read_source", json.RawMessage(`{"source_id":"consult-private-read"}`)); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "answer derived from the private read"})
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}}
	_, wait := startEngine(t, &engine)
	defer wait()
	got := awaitRun(t, db, run.ID)
	if got.Status != "failed" || got.FailureKind != failureArtifactContract || !strings.Contains(got.Error, "private work") {
		t.Fatalf("private consultation run = %+v", got)
	}
	receipts, err := project.New(db).DeliveryReceipts(t.Context(), message.ID)
	if err != nil || len(receipts) != 1 || receipts[0].Status != "rejected" || receipts[0].Detail != "disclosure_denied" {
		t.Fatalf("private consultation receipt = %+v, %v", receipts, err)
	}
}
