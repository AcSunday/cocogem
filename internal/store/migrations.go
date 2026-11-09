package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// executionPolicyMigration marks the one-time move from per-workspace execution
// modes to the single global permission policy. The marker is written in the
// same transaction as its effect, so a later restart never resets a user's
// choice back to the migration default.
const executionPolicyMigration = "execution-policy-v1"

// migrateExecutionPolicy makes the global policy effective exactly once.
func migrateExecutionPolicy(db *sql.DB) (err error) {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	var marker string
	err = tx.QueryRow("SELECT value FROM schema_meta WHERE key=?", executionPolicyMigration).Scan(&marker)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var encoded string
	err = tx.QueryRow("SELECT body FROM settings WHERE id='global'").Scan(&encoded)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A brand-new database starts at the same default that an upgraded one
		// receives; the marker keeps both paths idempotent.
		settings := w.NormalizeExecutionMode(w.Settings{Revision: 1, Assignments: map[string]map[string]string{}, RetentionDays: w.DefaultRetentionDays, CollaborationBudget: w.DefaultBudgetPolicy()})
		if _, err := tx.Exec("INSERT INTO settings(id,revision,body) VALUES('global',?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body", settings.Revision, mustBody(settings)); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		var settings w.Settings
		if err := json.Unmarshal([]byte(encoded), &settings); err != nil {
			return fmt.Errorf("decode legacy settings: %w", err)
		}
		// Legacy records carry no global mode. The confirmed migration default
		// replaces whatever per-workspace modes existed; workspace bodies keep
		// their old value as inert audit data.
		settings.ExecutionMode = w.DefaultExecutionMode
		if settings.PermissionEpoch < 1 {
			settings.PermissionEpoch = 1
		}
		settings.Revision++
		if _, err := tx.Exec("INSERT INTO settings(id,revision,body) VALUES('global',?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body", settings.Revision, mustBody(settings)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT INTO schema_meta(key,value) VALUES(?,?)", executionPolicyMigration, w.DefaultExecutionMode); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func mustBody(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// Tasks can be saved before their first execution conversation. Rebuild only
// the indexed projection; immutable JSON bodies and their foreign keys survive.
func migrateTaskThreads(db *sql.DB) (err error) {
	var ddl string
	if err = db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'").Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, "thread_id TEXT NOT NULL") {
		return nil
	}
	if _, err = db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() { _, restore := db.Exec("PRAGMA foreign_keys=ON"); err = errors.Join(err, restore) }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'").Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(ddl, "thread_id TEXT NOT NULL") {
		var create string
		for _, line := range strings.Split(schema, "\n") {
			if strings.HasPrefix(line, "CREATE TABLE IF NOT EXISTS tasks(") {
				create = strings.Replace(line, "IF NOT EXISTS tasks(", "tasks_nullable(", 1)
			}
		}
		for _, statement := range []string{create, "INSERT INTO tasks_nullable SELECT * FROM tasks", "DROP TABLE tasks", "ALTER TABLE tasks_nullable RENAME TO tasks", "CREATE INDEX task_workspace ON tasks(workspace_id,updated_at DESC,id)", "CREATE INDEX task_children ON tasks(parent_id,status)"} {
			if _, err = tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	rows, err := tx.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	broken := rows.Next()
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if broken {
		return errors.New("task migration would break a foreign key")
	}
	return tx.Commit()
}
