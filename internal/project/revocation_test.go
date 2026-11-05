package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestRevocationClosesAdmissionBeforeStopSettlement(t *testing.T) {
	db, service, _, run := authorizationFixture(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &run, "message-1"); err != nil {
			return err
		}
		return tx.SaveSession(w.ExecutionSession{
			ID: "managed-session", TaskID: run.TaskID, MemberID: "owner", Role: "developer",
			Fingerprint: "fingerprint", VisibilityEpoch: 1, ExposedRefs: []string{"source:1"},
			ExposureDigest: "exposure", Acknowledgement: "resume_unsupported", Status: "fresh_only",
			CreatedAt: w.Now(), UpdatedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	grant, err := service.BeginRevocation(t.Context(), run.WorkspaceID, "owner", "message-revoke")
	if err != nil || grant.Status != "revoking" || grant.Epoch != 2 {
		t.Fatalf("begin revocation = %+v, %v", grant, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		session, err := tx.Session("managed-session")
		if err != nil {
			return err
		}
		if session.Status != "revoked" {
			t.Fatalf("revocation did not retire exposed session: %+v", session)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorizedTools(db, service, run, []string{"read_file"}); !IsCode(err, "authority_revoked") {
		t.Fatalf("revoking grant still admits a host call: %v", err)
	}
	grant, err = service.FinishRevocation(t.Context(), run.WorkspaceID, "owner", true)
	if err != nil || grant.Status != "revocation_uncertain" || grant.RevokedAt != "" {
		t.Fatalf("uncertain stop was hidden: %+v, %v", grant, err)
	}
	grant, err = service.FinishRevocation(t.Context(), run.WorkspaceID, "owner", false)
	if err != nil || grant.Status != "revoked" || grant.RevokedAt == "" {
		t.Fatalf("settled revocation = %+v, %v", grant, err)
	}
	again, err := service.BeginRevocation(t.Context(), run.WorkspaceID, "owner", "message-revoke")
	if err != nil || again.Epoch != grant.Epoch || again.Status != "revoked" {
		t.Fatalf("idempotent revoked projection = %+v, %v", again, err)
	}
}
