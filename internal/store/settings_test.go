package store

import (
	"path/filepath"
	"reflect"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestSettingsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Read(t.Context(), func(tx *Tx) error {
		value, err := tx.Settings()
		if err != nil || value.Revision != 1 || value.DefaultCoordinatorID != "" || len(value.Assignments) != 0 || value.RetentionDays != w.DefaultRetentionDays {
			t.Fatalf("unset settings: %+v, %v", value, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := w.Settings{
		Revision:             2,
		DefaultCoordinatorID: "agent-a",
		Assignments:          map[string]map[string]string{"research": {"researcher": "agent-a", "critic": "agent-b", "coordinator": "agent-c"}},
		RetentionDays:        w.DefaultRetentionDays,
		CollaborationBudget:  w.DefaultBudgetPolicy(),
	}
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveSettings(want) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Read(t.Context(), func(tx *Tx) error {
			got, err := tx.Settings()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("reopen %d lost settings: %+v, %v", reopen, got, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
