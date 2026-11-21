package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestOpenDiscardsAnEarlierSchemaBaseline is the A01 Fresh Start regression: a
// database written by an earlier baseline is dropped and recreated instead of
// being migrated field by field. No preset workflow, assignments column or
// partially converted state can survive into the new kernel.
func TestOpenDiscardsAnEarlierSchemaBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A minimal pre-cutover database: an old settings body carrying the retired
	// fields, plus a conversation that must not be reused.
	for _, statement := range []string{
		"CREATE TABLE schema_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)",
		"CREATE TABLE settings(id TEXT PRIMARY KEY, revision INTEGER NOT NULL, body TEXT NOT NULL)",
		"CREATE TABLE threads(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, updated_at TEXT NOT NULL, revision INTEGER NOT NULL, body TEXT NOT NULL)",
		`INSERT INTO schema_meta(key,value) VALUES('schema_baseline','legacy-preset-workflows')`,
		`INSERT INTO settings(id,revision,body) VALUES('global',4,'{"revision":4,"assignments":{"research":{"critic":"old"}},"execution_mode":"workspace_autonomous"}')`,
		`INSERT INTO threads(id,workspace_id,updated_at,revision,body) VALUES('old-thread','old-ws','2026-10-01T00:00:00Z',1,'{"id":"old-thread","workspace_id":"old-ws"}')`,
	} {
		if _, err := legacy.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Read(t.Context(), func(tx *Tx) error {
		// The old conversation is gone.
		if _, err := tx.ThreadAny("old-thread"); err == nil {
			t.Fatal("legacy conversation survived the Fresh Start reset")
		}
		// Settings start at the new-kernel defaults with no assignments field.
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		if settings.Revision <= 0 && settings.ExecutionMode == "" {
			t.Fatalf("settings were not reinitialized: %+v", settings)
		}
		if settings.ExecutionMode != w.DefaultExecutionMode {
			t.Fatalf("reset settings mode = %q", settings.ExecutionMode)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reopening the same file keeps the current baseline instead of wiping again.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if err := again.Write(t.Context(), func(tx *Tx) error {
		return tx.SaveWorkspace(w.Workspace{ID: "kept", Name: "保留", Revision: 1})
	}); err != nil {
		t.Fatal(err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if err := third.Read(t.Context(), func(tx *Tx) error {
		if _, err := tx.Workspace("kept"); err != nil {
			t.Fatalf("a current-baseline database was wiped again: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
