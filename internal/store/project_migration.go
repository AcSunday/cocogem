package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// prepareProjectRuns upgrades only the indexed task key before schema.sql creates
// its partial uniqueness index. The JSON body remains the authoritative snapshot.
func prepareProjectRuns(db *sql.DB) error {
	var table string
	err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='runs'").Scan(&table)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := db.Query("PRAGMA table_info(runs)")
	if err != nil {
		return err
	}
	defer rows.Close()
	hasTaskID := false
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		hasTaskID = hasTaskID || name == "task_id"
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Version 5 briefly placed this name on tasks(id), which could not enforce
	// Run exclusion. Rebuild the named index on the actual run task key.
	if _, err := db.Exec("DROP INDEX IF EXISTS one_active_run_per_task"); err != nil {
		return err
	}
	if !hasTaskID {
		if _, err := db.Exec("ALTER TABLE runs ADD COLUMN task_id TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("migrate run task key: %w", err)
		}
		if _, err := db.Exec("UPDATE runs SET task_id=COALESCE(json_extract(body,'$.task_id'),'')"); err != nil {
			return fmt.Errorf("backfill run task key: %w", err)
		}
	}
	if strings.Contains(table, "'yielded'") {
		return nil
	}
	return migrateRunTerminalStates(db)
}

// migrateRunTerminalStates rebuilds the old CHECK instead of merely changing
// schema.sql, so databases created by v4/v5 can persist task execution segments.
func migrateRunTerminalStates(db *sql.DB) (returnErr error) {
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable migration foreign keys: %w", err)
	}
	defer func() {
		if _, err := db.Exec("PRAGMA foreign_keys=ON"); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("restore migration foreign keys: %w", err)
		}
	}()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE runs_project_upgrade(
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), thread_id TEXT NOT NULL REFERENCES threads(id), task_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('queued','running','canceling','succeeded','failed','canceled','interrupted','yielded')),
			idempotency_key TEXT NOT NULL, request_hash TEXT NOT NULL, created_at TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(thread_id,idempotency_key))`,
		`INSERT INTO runs_project_upgrade(id,workspace_id,thread_id,task_id,status,idempotency_key,request_hash,created_at,body)
			SELECT id,workspace_id,thread_id,task_id,status,idempotency_key,request_hash,created_at,body FROM runs`,
		`DROP TABLE runs`,
		`ALTER TABLE runs_project_upgrade RENAME TO runs`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("rebuild run terminal states: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run terminal states: %w", err)
	}
	return nil
}

// migrateYieldedStates upgrades databases that already have steps and attempts.
// Editing only schema.sql would leave their original CHECK constraints in place.
func migrateYieldedStates(db *sql.DB) (returnErr error) {
	definitions := map[string]string{}
	for _, table := range []string{"steps", "attempts"} {
		var definition string
		if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&definition); err != nil {
			return err
		}
		if !strings.Contains(definition, "'yielded'") {
			definitions[table] = definition
		}
	}
	if len(definitions) == 0 {
		return nil
	}
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() {
		if _, err := db.Exec("PRAGMA foreign_keys=ON"); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for table, definition := range definitions {
		upgraded := strings.Replace(definition, "'interrupted'", "'interrupted','yielded'", 1)
		if upgraded == definition || strings.Contains(upgraded, table+"_yielded_upgrade") {
			return fmt.Errorf("unsupported %s state constraint", table)
		}
		create := strings.Replace(upgraded, "CREATE TABLE "+table, "CREATE TABLE "+table+"_yielded_upgrade", 1)
		if create == upgraded {
			create = strings.Replace(upgraded, "CREATE TABLE IF NOT EXISTS "+table, "CREATE TABLE "+table+"_yielded_upgrade", 1)
		}
		if create == upgraded {
			return fmt.Errorf("unsupported %s table definition", table)
		}
		statements := []string{
			create,
			"INSERT INTO " + table + "_yielded_upgrade SELECT * FROM " + table,
			"DROP TABLE " + table,
			"ALTER TABLE " + table + "_yielded_upgrade RENAME TO " + table,
		}
		if table == "attempts" {
			statements = append(statements,
				"CREATE INDEX attempt_order ON attempts(run_id,step_id,number)",
				"CREATE INDEX active_attempts ON attempts(status) WHERE status='running'",
			)
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("rebuild %s yielded state: %w", table, err)
			}
		}
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(6,strftime('%Y-%m-%dT%H:%M:%fZ','now'))"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
