package store

import (
	"database/sql"
	"errors"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Settings reads the device-wide defaults; an unset store starts at the
// confirmed project-autonomous default with permission epoch 1.
func (t *Tx) Settings() (w.Settings, error) {
	value, err := get[w.Settings](t, "SELECT body FROM settings WHERE id='global'")
	if errors.Is(err, ErrNotFound) {
		return w.Settings{Revision: 1, Assignments: map[string]map[string]string{}, RetentionDays: w.DefaultRetentionDays,
			CollaborationBudget: w.DefaultBudgetPolicy(), ExecutionMode: w.DefaultExecutionMode, PermissionEpoch: 1}, nil
	}
	if err != nil {
		return w.Settings{}, err
	}
	if value.Assignments == nil {
		value.Assignments = map[string]map[string]string{}
	}
	value = w.NormalizeSettings(value)
	// Legacy settings have no retention or collaboration fields and use the
	// current defaults without rewriting any existing task's frozen account.
	value.RetentionDays = w.EffectiveRetentionDays(value.RetentionDays)
	if value.CollaborationBudget.AutoContinuations == 0 {
		value.CollaborationBudget = w.DefaultBudgetPolicy()
	}
	// A record written before the global permission policy existed has neither
	// field. Normalizing the read keeps one effective source without rewriting
	// a user's later choice.
	value = w.NormalizeExecutionMode(value)
	return value, nil
}

// SaveSettings replaces the single global defaults record.
func (t *Tx) SaveSettings(value w.Settings) error {
	return t.exec("INSERT INTO settings(id,revision,body) VALUES('global',?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body", value.Revision, body(value))
}

// MigrationApplied reports whether the named one-time migration marker exists.
func (t *Tx) MigrationApplied(key string) (bool, error) {
	var value string
	err := t.tx.QueryRowContext(t.ctx, "SELECT value FROM schema_meta WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkMigration records a one-time migration in the same transaction as its
// effects, so a crash either applies both or neither.
func (t *Tx) MarkMigration(key, value string) error {
	return t.exec("INSERT INTO schema_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=value", key, value)
}
