package httpapi

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestSettingsFieldMergeAndPermissionEpoch is the settings API regression: a
// permission-only PATCH preserves the other fields, advances the server-owned
// epoch exactly once, and the retired preset role assignments are gone.
func TestSettingsFieldMergeAndPermissionEpoch(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := newDeliveryAPI(t, dir, db, nil)
	patch := func(in SettingsInput, revision, want int, out any) []byte {
		t.Helper()
		return api.call("PATCH", "/api/v1/settings", in, map[string]string{"If-Match": strconv.Itoa(revision)}, want, out)
	}

	var settings w.Settings
	api.call("GET", "/api/v1/settings", nil, nil, 200, &settings)
	if settings.Revision != 1 || settings.DefaultCoordinatorID != "" || settings.ExecutionMode != w.DefaultExecutionMode || settings.PermissionEpoch != 1 {
		t.Fatalf("unset settings = %+v", settings)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"assignments"`, `"workflow"`} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Fatalf("settings still expose %s: %s", banned, raw)
		}
	}

	days := 14
	budget := w.BudgetPolicy{AutoContinuations: 9, ParallelBranches: 3, WriterBranches: 1, FixRounds: 2, NoProgressFailures: 2}
	var seeded w.Settings
	patch(SettingsInput{RetentionDays: &days, CollaborationBudget: &budget}, 1, 200, &seeded)
	if seeded.RetentionDays != 14 || seeded.CollaborationBudget.AutoContinuations != 9 {
		t.Fatalf("seed = %+v", seeded)
	}
	epoch := seeded.PermissionEpoch
	full := w.ExecutionFullAccess
	var moved w.Settings
	patch(SettingsInput{ExecutionMode: &full}, seeded.Revision, 200, &moved)
	if moved.ExecutionMode != w.ExecutionFullAccess || moved.PermissionEpoch != epoch+1 {
		t.Fatalf("permission move = %+v", moved)
	}
	if moved.RetentionDays != 14 || moved.CollaborationBudget.AutoContinuations != 9 {
		t.Fatalf("permission-only patch cleared unrelated fields: %+v", moved)
	}
	var same w.Settings
	patch(SettingsInput{ExecutionMode: &full}, moved.Revision, 200, &same)
	if same.PermissionEpoch != moved.PermissionEpoch {
		t.Fatalf("unchanged mode advanced the epoch: %+v", same)
	}
	// The epoch is server-owned.
	var rejected struct {
		Error struct{ Code string }
	}
	epochValue := 7
	patch(SettingsInput{PermissionEpoch: &epochValue}, same.Revision, 400, &rejected)
	if rejected.Error.Code != "invalid_request" {
		t.Fatalf("client-owned epoch was accepted: %+v", rejected)
	}
}
