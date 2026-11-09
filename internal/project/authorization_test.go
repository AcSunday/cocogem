package project

import (
	"context"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func authorizationFixture(t *testing.T) (*store.Store, *Service, TaskView, w.Run) {
	t.Helper()
	db, service, th := setup(t)
	member := w.Agent{ID: "owner", WorkspaceID: th.WorkspaceID, DisplayName: "Owner", Handle: "owner", Revision: 1,
		Tools: []string{"read_file", "write_file", "patch_file", "run_test", "publish_artifact", "web_search", "handoff_task"}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		return tx.SaveMember(w.WorkspaceMember{WorkspaceID: th.WorkspaceID, AgentID: member.ID, Enabled: true, Tools: member.Tools, Revision: 1})
	}); err != nil {
		t.Fatal(err)
	}
	created := create(t, service, th)
	run := w.Run{ID: "authorized-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: 1, Agents: []w.Agent{member}, Status: "queued", CreatedAt: w.Now()}
	return db, service, created, run
}

// TestLegacyWorkspaceGrantIsInertHistory pins the migration outcome: the legacy
// workspace+member grant (whose UI and routes are removed) must not veto a new
// Run of the global policy, in any of its historical statuses. Live authority is
// the global mode, the task/role ceiling and the conversation grants.
func TestLegacyWorkspaceGrantIsInertHistory(t *testing.T) {
	for _, legacy := range []struct {
		name   string
		status string
		expiry string
	}{
		{name: "active", status: "active"},
		{name: "revoked", status: "revoked"},
		{name: "revoking", status: "revoking"},
		{name: "expired active row", status: "active", expiry: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)},
	} {
		t.Run(legacy.name, func(t *testing.T) {
			db, service, _, run := authorizationFixture(t)
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				return service.EnsureRunAuthorization(tx, &run, "message-1")
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				grant, err := tx.Grant(grantID(run.WorkspaceID, "owner"))
				if err != nil {
					return err
				}
				grant.Status, grant.ExpiresAt, grant.Revision = legacy.status, legacy.expiry, grant.Revision+1
				return tx.SaveGrant(grant)
			}); err != nil {
				t.Fatal(err)
			}
			allowed, err := readAuthorizedTools(db, service, run, []string{"read_file"})
			if err != nil || len(allowed) != 1 {
				t.Fatalf("legacy %s grant vetoed a live step: %v %v", legacy.status, allowed, err)
			}
			// A brand-new Run of the same workspace also starts successfully.
			fresh := run
			fresh.ID = "after-legacy-" + legacy.status
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				return service.EnsureRunAuthorization(tx, &fresh, "message-2")
			}); err != nil {
				t.Fatalf("new Run was blocked by legacy %s grant: %v", legacy.status, err)
			}
		})
	}
}

// TestLiveAdmissionIsGovernedByEnablementAndEpoch proves the checks that still
// carry real authority: a disabled member cannot execute, and a policy change
// closes old-epoch admission.
func TestLiveAdmissionIsGovernedByEnablementAndEpoch(t *testing.T) {
	db, service, _, run := authorizationFixture(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return service.EnsureRunAuthorization(tx, &run, "message-1")
	}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := readAuthorizedTools(db, service, run, []string{"read_file"}); err != nil || len(allowed) != 1 {
		t.Fatalf("baseline step was rejected: %v %v", allowed, err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		member, err := tx.Member(run.WorkspaceID, "owner")
		if err != nil {
			return err
		}
		member.Enabled, member.Revision = false, member.Revision+1
		return tx.SaveMember(member)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorizedTools(db, service, run, []string{"read_file"}); !IsCode(err, "authority_revoked") {
		t.Fatalf("disabled member still admitted: %v", err)
	}
}

// TestRevocationSettlementStillRetiresExposedSessions keeps the durable
// revocation machinery (uncertain-stop honesty + session retirement) intact even
// though the legacy grant no longer gates new execution.
func TestRevocationSettlementStillRetiresExposedSessions(t *testing.T) {
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

func readAuthorizedTools(db *store.Store, service *Service, run w.Run, names []string) ([]string, error) {
	var allowed []string
	err := db.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		allowed, err = service.FilterAuthorizedTools(tx, run, "owner", names)
		return err
	})
	return allowed, err
}

// setGlobalMode changes the single global permission policy through the same
// service entry HTTP uses.
func setGlobalMode(t *testing.T, service *Service, mode string) (w.Settings, error) {
	t.Helper()
	var current w.Settings
	if err := service.store.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		return current, err
	}
	change, err := service.UpdateSettings(t.Context(), current.Revision, SettingsPatch{ExecutionMode: &mode})
	return change.Settings, err
}
