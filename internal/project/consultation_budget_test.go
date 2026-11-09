package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestConsultationBudgetExhaustionPreservesQueuedIntent(t *testing.T) {
	db, service, senderThread := setup(t)
	sender := create(t, service, senderThread)
	targetThread := w.Thread{ID: "consult-budget-target", WorkspaceID: senderThread.WorkspaceID, CoordinatorID: "owner", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveAgent(w.Agent{ID: "owner", WorkspaceID: senderThread.WorkspaceID, DisplayName: "Owner", Handle: "owner", Revision: 1, Tools: []string{"read", "collaborate"}}); err != nil {
			return err
		}
		return tx.SaveThread(targetThread)
	}); err != nil {
		t.Fatal(err)
	}
	target, err := service.CreateTask(t.Context(), CreateTaskInput{WorkspaceID: senderThread.WorkspaceID, ThreadID: targetThread.ID, OwnerMemberID: "owner", Goal: "original work", SourceMessageID: "target", AcceptedBy: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(sender.Budget.ID)
		if err != nil {
			return err
		}
		account.Policy.AutoContinuations = 1
		account.UsedContinuations = 1
		account.Revision++
		return tx.SaveBudgetAccount(account, account.Revision-1)
	}); err != nil {
		t.Fatal(err)
	}
	input := SendTaskMessageInput{SenderTaskID: sender.Task.ID, TargetTaskID: target.Task.ID, Kind: "question", Body: "answer from durable work", RequestID: "consult-budget-request"}
	consultation, message, err := service.Consult(t.Context(), input)
	if !IsCode(err, "budget_exhausted") || consultation.Task.Status != "waiting" || consultation.Task.WaitingReason != "budget" || message.Message.ID == "" {
		t.Fatalf("exhausted consultation = %+v, %+v, %v", consultation, message, err)
	}
	replay, replayMessage, err := service.Consult(t.Context(), input)
	if err != nil || replay.Task.ID != consultation.Task.ID || replayMessage.Message.ID != message.Message.ID {
		t.Fatalf("waiting consultation replay = %+v, %+v, %v", replay, replayMessage, err)
	}
	account, err := service.Task(t.Context(), sender.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy := account.Budget.Policy
	policy.AutoContinuations = 2
	if _, err := service.AdjustBudget(t.Context(), sender.Task.ID, account.Budget.Revision, policy); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		task, err := tx.Task(consultation.Task.ID)
		if err != nil || task.Status != "ready" {
			t.Fatalf("expanded consultation = %+v, %v", task, err)
		}
		runs, err := tx.Runs(task.ThreadID)
		if err != nil || len(runs) != 1 || !runs[0].BudgetReserved || runs[0].Status != "queued" {
			t.Fatalf("durable consultation run = %+v, %v", runs, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
