package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestRevocationRemovesOnlyManagedNativeSessionStorage(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	attachSessionTask(t, db, &run)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Status, current.FinishedAt = "succeeded", w.Now()
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].Status = "succeeded"
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveSession(w.ExecutionSession{
			ID: "revoked-managed-session", TaskID: run.TaskID, MemberID: run.Agents[0].ID,
			Status: "active", Acknowledgement: "fresh_acknowledged", NativeSessionID: "native-id",
			ExposedRefs: []string{"source_id:private:digest"}, CreatedAt: w.Now(), UpdatedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	managed := filepath.Join(root, "revoked-managed-session", ".cocogem", "config")
	if err := os.MkdirAll(managed, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "session.json"), []byte("managed private cache"), 0600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "terminal-history.json")
	if err := os.WriteFile(external, []byte("not managed by Cocogem"), 0600); err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	if _, err := service.BeginRevocation(t.Context(), run.WorkspaceID, run.Agents[0].ID, "revoke-source"); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: db, NativeSessionRoot: root}
	if err := engine.reconcileRevocations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "revoked-managed-session")); !os.IsNotExist(err) {
		t.Fatalf("managed revoked cache still exists: %v", err)
	}
	if content, err := os.ReadFile(external); err != nil || string(content) != "not managed by Cocogem" {
		t.Fatalf("unmanaged terminal history was modified: %q, %v", content, err)
	}
	grants, err := service.Grants(t.Context(), run.WorkspaceID)
	if err != nil || len(grants) == 0 || grants[0].Status != "revoked" {
		t.Fatalf("settled cache cleanup was not recorded: %+v, %v", grants, err)
	}
}
