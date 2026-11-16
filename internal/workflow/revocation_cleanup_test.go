package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestRevocationSealsSessionsWithoutTouchingNativeDirectories is the A30
// regression: revoking a member retires the Cocogem ledger rows so no --resume is
// attempted, and never deletes a file from the CLI's own home directory.
func TestRevocationSealsSessionsWithoutTouchingNativeDirectories(t *testing.T) {
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
			ID: "revoked-managed-session", ThreadID: run.ThreadID, TaskID: run.TaskID, MemberID: run.Agents[0].ID,
			Status: "active", Acknowledgement: "fresh_acknowledged", NativeSessionID: "native-id",
			ExposedRefs: []string{"source_id:private:digest"}, CreatedAt: w.Now(), UpdatedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	// A user-owned native CLI directory with a session file the host must never
	// remove.
	nativeHome := t.TempDir()
	sessionFile := filepath.Join(nativeHome, "sessions", "native-id.json")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile, []byte("user-owned native session"), 0600); err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	if _, err := service.BeginRevocation(t.Context(), run.WorkspaceID, run.Agents[0].ID, "revoke-source"); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: db}
	if err := engine.reconcileRevocations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		session, err := tx.Session("revoked-managed-session")
		if err != nil {
			return err
		}
		if session.Status != "revoked" || session.NativeSessionID != "" || session.Acknowledgement != "revoked" {
			t.Fatalf("revoked session was not sealed: %+v", session)
		}
		// A sealed session can never be reused by a new attempt.
		if sessionReuseEligible(session, session.Fingerprint, session.VisibilityEpoch) {
			t.Fatal("a revoked session is still resume-eligible")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(sessionFile); err != nil || string(content) != "user-owned native session" {
		t.Fatalf("the host modified the user's native CLI directory: %q %v", content, err)
	}
	grants, err := service.Grants(t.Context(), run.WorkspaceID)
	if err != nil || len(grants) == 0 || grants[0].Status != "revoked" {
		t.Fatalf("revocation was not settled: %+v, %v", grants, err)
	}
}
