package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestYieldedMigrationPreservesExistingStepsAndAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE runs(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,thread_id TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('queued','running','canceling','succeeded','failed','canceled','interrupted')),idempotency_key TEXT NOT NULL,request_hash TEXT NOT NULL,created_at TEXT NOT NULL,body TEXT NOT NULL,UNIQUE(thread_id,idempotency_key))`,
		`CREATE TABLE steps(id TEXT PRIMARY KEY,run_id TEXT NOT NULL REFERENCES runs(id),status TEXT NOT NULL CHECK(status IN ('pending','running','succeeded','failed','canceled','skipped','interrupted')),position INTEGER NOT NULL,body TEXT NOT NULL)`,
		`CREATE TABLE attempts(id TEXT PRIMARY KEY,run_id TEXT NOT NULL REFERENCES runs(id),step_id TEXT NOT NULL REFERENCES steps(id),number INTEGER NOT NULL,status TEXT NOT NULL CHECK(status IN ('running','succeeded','failed','canceled','interrupted','uncertain','blocked')),body TEXT NOT NULL,UNIQUE(step_id,number))`,
		`INSERT INTO runs VALUES('run','ws','thread','succeeded','key','digest','2026-01-01T00:00:00Z','{"id":"run","workspace_id":"ws","thread_id":"thread","status":"succeeded","created_at":"2026-01-01T00:00:00Z"}')`,
		`INSERT INTO steps VALUES('step','run','running',0,'{"id":"step","run_id":"run","status":"running"}')`,
		`INSERT INTO attempts VALUES('attempt','run','step',1,'running','{"id":"attempt","run_id":"run","step_id":"step","number":1,"status":"running"}')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Write(t.Context(), func(tx *Tx) error {
		if err := tx.SaveStep(w.Step{ID: "step", RunID: "run", Status: "yielded"}); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: "attempt", RunID: "run", StepID: "step", Number: 1, Status: "yielded"})
	}); err != nil {
		t.Fatalf("migrated states rejected yielded: %v", err)
	}
	if err := store.Read(t.Context(), func(tx *Tx) error {
		steps, err := tx.Steps("run")
		if err != nil || len(steps) != 1 || steps[0].Status != "yielded" {
			t.Fatalf("preserved steps = %+v, %v", steps, err)
		}
		attempt, err := tx.Attempt("attempt")
		if err != nil || attempt.Status != "yielded" || attempt.Number != 1 {
			t.Fatalf("preserved attempt = %+v, %v", attempt, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
