package store

import (
	"context"
	"database/sql"
	"strings"
)

// schemaBaseline is the single authoritative schema generation. This product
// ships a Fresh Start (Clean Cutover) policy: there are no data migrations, and
// a database written by an earlier baseline is discarded and recreated. The
// marker is the only schema bookkeeping the store keeps.
const schemaBaseline = "unified-collaboration-kernel-v1"

// resetStaleSchema drops every table when the database was written by an earlier
// baseline, then recreates the current schema. A brand-new database simply
// receives the schema and the marker.
func resetStaleSchema(db *sql.DB) error {
	var tables int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		var baseline string
		err := db.QueryRowContext(context.Background(), "SELECT value FROM schema_meta WHERE key='schema_baseline'").Scan(&baseline)
		switch {
		case err == nil && baseline == schemaBaseline:
			// Current baseline is already installed.
		case err == nil && baseline != schemaBaseline,
			err == sql.ErrNoRows,
			err != nil && strings.Contains(err.Error(), "no such table"):
			// Foreign keys are enabled by the connection pragma, so the drop order
			// does not matter for a full wipe; disable them to be explicit.
			if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
				return err
			}
			if err := dropAllTables(db); err != nil {
				return err
			}
			if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
				return err
			}
		default:
			// Never wipe a live database on transient or I/O errors such as SQLITE_BUSY.
			return err
		}
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		return err
	}
	_, err := db.ExecContext(context.Background(), "INSERT INTO schema_meta(key,value) VALUES('schema_baseline',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", schemaBaseline)
	return err
}

// dropAllTables removes every user table and index from the current database.
func dropAllTables(db *sql.DB) error {
	rows, err := db.QueryContext(context.Background(), "SELECT name,type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	names := []string{}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return err
		}
		names = append(names, kind+"|"+name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, entry := range names {
		kind, name, _ := strings.Cut(entry, "|")
		statement := "DROP TABLE IF EXISTS "
		if kind == "view" {
			statement = "DROP VIEW IF EXISTS "
		}
		if _, err := db.ExecContext(context.Background(), statement+quoteIdent(name)); err != nil {
			return err
		}
	}
	return nil
}

// quoteIdent quotes one SQLite identifier.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
