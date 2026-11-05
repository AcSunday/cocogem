package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestProjectMigrationRebuildsLegacyRunChecks(t *testing.T) {
	for _, test := range []struct {
		name      string
		withTask  bool
		wrongTask bool
	}{
		{name: "v4 without task key"},
		{name: "v5 with task key", withTask: true, wrongTask: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			taskColumn := ""
			if test.withTask {
				taskColumn = "task_id TEXT NOT NULL DEFAULT '',"
			}
			if test.wrongTask {
				legacyTasks := `CREATE TABLE tasks(
					id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, parent_id TEXT,
					thread_id TEXT NOT NULL, current_revision INTEGER NOT NULL,
					root_task_id TEXT NOT NULL, budget_account_id TEXT NOT NULL,
					status TEXT NOT NULL, updated_at TEXT NOT NULL, body TEXT NOT NULL)`
				if _, err := db.Exec(legacyTasks); err != nil {
					t.Fatal(err)
				}
			}
			schema := `CREATE TABLE runs(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,thread_id TEXT NOT NULL,` + taskColumn + `status TEXT NOT NULL CHECK(status IN ('queued','running','canceling','succeeded','failed','canceled','interrupted')),idempotency_key TEXT NOT NULL,request_hash TEXT NOT NULL,created_at TEXT NOT NULL,body TEXT NOT NULL,UNIQUE(thread_id,idempotency_key))`
			if _, err := db.Exec(schema); err != nil {
				t.Fatal(err)
			}
			if test.wrongTask {
				if _, err := db.Exec("CREATE UNIQUE INDEX one_active_run_per_task ON tasks(id)"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec("INSERT INTO runs(id,workspace_id,thread_id,status,idempotency_key,request_hash,created_at,body) VALUES('old','ws','thread','succeeded','old-key','hash','2026-01-01T00:00:00Z','{\"id\":\"old\",\"workspace_id\":\"ws\",\"thread_id\":\"thread\",\"status\":\"succeeded\",\"created_at\":\"2026-01-01T00:00:00Z\"}')"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var definition string
			if err := store.db.QueryRow("SELECT sql FROM sqlite_master WHERE name='runs'").Scan(&definition); err != nil {
				t.Fatal(err)
			}
			if !containsYielded(definition) {
				t.Fatalf("legacy CHECK was not rebuilt: %s", definition)
			}
			err = store.Read(t.Context(), func(tx *Tx) error {
				run, err := tx.Run("old")
				if err != nil || run.Status != "succeeded" {
					t.Fatalf("old run = %+v, %v", run, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Write(t.Context(), func(tx *Tx) error {
				if err := tx.SaveWorkspace(w.Workspace{ID: "ws", Revision: 1}); err != nil {
					return err
				}
				return tx.SaveThread(w.Thread{ID: "thread", WorkspaceID: "ws", Revision: 1, UpdatedAt: w.Now()})
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.Write(t.Context(), func(tx *Tx) error {
				return tx.InsertRun(w.Run{ID: "new", WorkspaceID: "ws", ThreadID: "thread", Status: "yielded", CreatedAt: w.Now()}, "new-key", "new-hash")
			}); err != nil {
				t.Fatalf("migrated database rejected yielded: %v", err)
			}
		})
	}
}

func containsYielded(definition string) bool {
	return strings.Contains(definition, "'yielded'")
}
