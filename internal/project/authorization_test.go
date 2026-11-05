package project

import (
	"context"
	"slices"
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
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveAgent(member) }); err != nil {
		t.Fatal(err)
	}
	created := create(t, service, th)
	run := w.Run{ID: "authorized-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: 1, Agents: []w.Agent{member}, Status: "queued", CreatedAt: w.Now()}
	return db, service, created, run
}

func TestRunAuthorizationFreezesModeAndTaskActions(t *testing.T) {
	db, service, created, run := authorizationFixture(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return service.EnsureRunAuthorization(tx, &run, "message-1")
	}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"read", "write", "run_test"} {
		if !slices.Contains(run.GrantActions, action) {
			t.Fatalf("controlled develop lost %q: %+v", action, run.GrantActions)
		}
	}
	for _, action := range w.ModeActions(w.ExecutionControlled) {
		if slices.Contains([]string{"native_read", "native_write", "native_execute"}, action) {
			t.Fatalf("controlled mode exposed native action %q", action)
		}
	}
	frozen := slices.Clone(run.GrantActions)
	if _, err := service.SetExecutionMode(t.Context(), created.Task.WorkspaceID, w.ExecutionFullAccess, "message-mode", 1); err != nil {
		t.Fatal(err)
	}
	allowed, err := readAuthorizedTools(db, service, run, []string{"read_file", "write_file", "run_test"})
	if err != nil || len(allowed) != 3 {
		t.Fatalf("current controlled develop tools = %+v, %v", allowed, err)
	}
	if !slices.Equal(frozen, run.GrantActions) {
		t.Fatalf("mode switch rewrote old Run actions: before=%v after=%v", frozen, run.GrantActions)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil || view.Budget.Revision != created.Budget.Revision {
		t.Fatalf("mode switch reset budget: %+v, %v", view.Budget, err)
	}
}

func TestCurrentGrantRevocationAndExpiryBlockOldRun(t *testing.T) {
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
		grant.Status = "revoked"
		grant.Epoch++
		grant.Revision++
		grant.RevokedAt = w.Now()
		return tx.SaveGrant(grant)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorizedTools(db, service, run, []string{"read_file"}); !IsCode(err, "authority_revoked") {
		t.Fatalf("revoked old Run tool error = %v", err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return service.EnsureRunAuthorization(tx, &run, "message-1")
	}); !IsCode(err, "authority_revoked") {
		t.Fatalf("new Run revived revoked grant: %v", err)
	}
	if _, err := service.SetExecutionMode(t.Context(), run.WorkspaceID, w.ExecutionProjectAutonomous, "message-mode", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		grant, err := tx.Grant(grantID(run.WorkspaceID, "owner"))
		if err != nil {
			return err
		}
		if grant.Status != "revoked" {
			t.Fatalf("mode switch revived grant: %+v", grant)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		grant, err := tx.Grant(grantID(run.WorkspaceID, "owner"))
		if err != nil {
			return err
		}
		grant.Status = "active"
		grant.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		return tx.SaveGrant(grant)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthorizedTools(db, service, run, []string{"read_file"}); !IsCode(err, "authority_revoked") {
		t.Fatalf("expired old Run tool error = %v", err)
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
