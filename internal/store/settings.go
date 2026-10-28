package store

import (
	"errors"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Settings reads the device-wide defaults; an unset store starts at revision 1.
func (t *Tx) Settings() (w.Settings, error) {
	value, err := get[w.Settings](t, "SELECT body FROM settings WHERE id='global'")
	if errors.Is(err, ErrNotFound) {
		return w.Settings{Revision: 1, Assignments: map[string]map[string]string{}}, nil
	}
	if err != nil {
		return w.Settings{}, err
	}
	if value.Assignments == nil {
		value.Assignments = map[string]map[string]string{}
	}
	return value, nil
}

// SaveSettings replaces the single global defaults record.
func (t *Tx) SaveSettings(value w.Settings) error {
	return t.exec("INSERT INTO settings(id,revision,body) VALUES('global',?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body", value.Revision, body(value))
}
