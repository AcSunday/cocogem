package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func addTransferRecipient(t *testing.T, db *store.Store, workspaceID string) w.Agent {
	t.Helper()
	member := w.Agent{ID: "new-owner", WorkspaceID: workspaceID, DisplayName: "New owner", Handle: "new-owner", Revision: 1, Tools: []string{"read"}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveAgent(member) }); err != nil {
		t.Fatal(err)
	}
	return member
}

func TestTaskTransferFreezesAdmissionThenCASOwner(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	recipient := addTransferRecipient(t, db, th.WorkspaceID)
	frozen, err := service.BeginTaskTransfer(t.Context(), created.Task.ID, recipient.ID, "transfer-1", 1)
	if err != nil || frozen.OwnerMemberID != created.Task.OwnerMemberID || frozen.PendingOwnerID != recipient.ID || frozen.RootControlEpoch != created.Task.RootControlEpoch+1 {
		t.Fatalf("transfer freeze = %+v, %v", frozen, err)
	}
	candidate := w.Run{TaskID: created.Task.ID, TaskRevision: 1}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return service.EnsureRunAuthorization(tx, &candidate, "must-not-admit")
	}); !IsCode(err, "authority_revoked") {
		t.Fatalf("frozen owner admitted new work: %v", err)
	}
	transferred, err := service.CompleteTaskTransfer(t.Context(), created.Task.ID, "transfer-1", false)
	if err != nil || transferred.OwnerMemberID != recipient.ID || transferred.OwnerRevision != 2 || transferred.TransferRequestID != "" || transferred.PendingOwnerID != "" {
		t.Fatalf("transfer CAS = %+v, %v", transferred, err)
	}
	replay, err := service.BeginTaskTransfer(t.Context(), created.Task.ID, recipient.ID, "transfer-1", 1)
	if err != nil || replay.OwnerRevision != transferred.OwnerRevision || replay.RootControlEpoch != transferred.RootControlEpoch {
		t.Fatalf("completed transfer replay = %+v, %v", replay, err)
	}
	if _, err := service.BeginTaskTransfer(t.Context(), created.Task.ID, recipient.ID, "transfer-1", 2); !IsCode(err, "conflict") {
		t.Fatalf("transfer request collision = %v", err)
	}
}

func TestTaskTransferUncertaintyRetainsOldOwnerAndCanRetry(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	recipient := addTransferRecipient(t, db, th.WorkspaceID)
	if _, err := service.BeginTaskTransfer(t.Context(), created.Task.ID, recipient.ID, "transfer-uncertain", 1); err != nil {
		t.Fatal(err)
	}
	pending, err := service.CompleteTaskTransfer(t.Context(), created.Task.ID, "transfer-uncertain", true)
	if !IsCode(err, "revocation_uncertain") || pending.OwnerMemberID != created.Task.OwnerMemberID || pending.PendingOwnerID != recipient.ID || pending.TransferRequestID == "" {
		t.Fatalf("uncertain transfer = %+v, %v", pending, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		receipt, err := tx.TaskAction(created.Task.ID, "transfer-uncertain")
		if err != nil || receipt.Action != "transfer_uncertain" {
			t.Fatalf("uncertain receipt = %+v, %v", receipt, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	settled, err := service.CompleteTaskTransfer(t.Context(), created.Task.ID, "transfer-uncertain", false)
	if err != nil || settled.OwnerMemberID != recipient.ID || settled.OwnerRevision != 2 {
		t.Fatalf("verified retry = %+v, %v", settled, err)
	}
}
