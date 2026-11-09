package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestLegacyWorkspaceModesMigrateOnceToProjectAutonomous is the A01 regression: an
// old database with three different per-workspace modes upgrades to one global
// project_autonomous policy exactly once, and a later controlled choice survives a
// restart.
func TestLegacyWorkspaceModesMigrateOnceToProjectAutonomous(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	// Build a legacy database: schema without the global policy, settings without
	// execution_mode, three workspaces with different modes.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	legacySettings := map[string]any{"revision": 4, "default_coordinator_id": "", "assignments": map[string]any{}, "retention_days": 7,
		"collaboration_budget": map[string]any{"auto_continuations": 20, "parallel_branches": 4, "writer_branches": 2, "fix_rounds": 3, "no_progress_failures": 2}}
	encoded, _ := json.Marshal(legacySettings)
	if _, err := raw.Exec("INSERT INTO settings(id,revision,body) VALUES('global',4,?)", string(encoded)); err != nil {
		t.Fatal(err)
	}
	for id, mode := range map[string]string{"one": "controlled", "two": "project_autonomous", "three": "full_access"} {
		body, _ := json.Marshal(map[string]any{"id": id, "name": id, "revision": 1, "execution_mode": mode})
		if _, err := raw.Exec("INSERT INTO workspaces(id,revision,body) VALUES(?,1,?)", id, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var migrated w.Settings
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var err error
		migrated, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if migrated.ExecutionMode != w.ExecutionProjectAutonomous || migrated.PermissionEpoch != 1 {
		t.Fatalf("migration result = %+v", migrated)
	}
	// An explicit later choice must survive a restart.
	controlled := w.ExecutionControlled
	if err := s.Write(t.Context(), func(tx *Tx) error {
		current, err := tx.Settings()
		if err != nil {
			return err
		}
		current.ExecutionMode = controlled
		current.PermissionEpoch++
		return tx.SaveSettings(current)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var again w.Settings
		if err := s.Read(t.Context(), func(tx *Tx) error {
			var err error
			again, err = tx.Settings()
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if again.ExecutionMode != w.ExecutionControlled || again.PermissionEpoch != 2 {
			t.Fatalf("reopen %d reset the permission: %+v", reopen, again)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNewDatabaseStartsAtProjectAutonomous proves the new-install default and the
// empty-settings read agree.
func TestNewDatabaseStartsAtProjectAutonomous(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Read(t.Context(), func(tx *Tx) error {
		value, err := tx.Settings()
		if err != nil {
			return err
		}
		if value.ExecutionMode != w.ExecutionProjectAutonomous || value.PermissionEpoch < 1 {
			t.Fatalf("new database default = %+v", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
