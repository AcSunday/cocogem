package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestSettingsFieldMergeKeepsUnrelatedFields is the A03 regression: PATCHing only
// the permission must not clear assignments, retention or budget, and saving
// collaboration must not reset the permission. The epoch advances only for a real
// permission change.
func TestSettingsFieldMergeKeepsUnrelatedFields(t *testing.T) {
	db, service, _ := setup(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveAgent(w.Agent{ID: "owner", DisplayName: "Owner", Handle: "owner", Model: "default", Tools: []string{"publish_artifact"}, Revision: 1})
	}); err != nil {
		t.Fatal(err)
	}
	// Seed collaboration, defaults and retention.
	mode := w.ExecutionControlled
	assignments := map[string]map[string]string{"research": {"coordinator": "owner"}}
	days := 14
	budget := w.BudgetPolicy{AutoContinuations: 9, ParallelBranches: 3, WriterBranches: 1, FixRounds: 2, NoProgressFailures: 2}
	seeded, err := service.UpdateSettings(t.Context(), 1, SettingsPatch{
		DefaultCoordinatorID: ptrString("owner"), Assignments: &assignments,
		RetentionDays: &days, CollaborationBudget: &budget, ExecutionMode: &mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	current := seeded.Settings
	if current.ExecutionMode != w.ExecutionControlled {
		t.Fatalf("seed = %+v", current)
	}
	epoch := current.PermissionEpoch
	// 1. Only the permission changes: unrelated fields survive.
	autonomous := w.ExecutionProjectAutonomous
	onlyPermission, err := service.UpdateSettings(t.Context(), current.Revision, SettingsPatch{ExecutionMode: &autonomous})
	if err != nil {
		t.Fatal(err)
	}
	got := onlyPermission.Settings
	if got.DefaultCoordinatorID != "owner" || got.Assignments["research"]["coordinator"] != "owner" || got.RetentionDays != 14 || got.CollaborationBudget.AutoContinuations != 9 {
		t.Fatalf("permission-only patch cleared unrelated fields: %+v", got)
	}
	if got.PermissionEpoch != epoch+1 {
		t.Fatalf("epoch did not advance exactly once: %+v", got)
	}
	if !onlyPermission.PermissionMoved {
		t.Fatal("permission change was not reported")
	}
	// 2. Only collaboration changes: permission and epoch survive.
	newBudget := w.BudgetPolicy{AutoContinuations: 21, ParallelBranches: 5, WriterBranches: 2, FixRounds: 4, NoProgressFailures: 3}
	onlyBudget, err := service.UpdateSettings(t.Context(), got.Revision, SettingsPatch{CollaborationBudget: &newBudget})
	if err != nil {
		t.Fatal(err)
	}
	if onlyBudget.Settings.ExecutionMode != w.ExecutionProjectAutonomous || onlyBudget.Settings.PermissionEpoch != got.PermissionEpoch {
		t.Fatalf("collaboration save reset the permission: %+v", onlyBudget.Settings)
	}
	if onlyBudget.PermissionMoved {
		t.Fatal("collaboration save reported a permission move")
	}
	// 3. Re-saving the same mode does not advance the epoch and is not a move.
	same, err := service.UpdateSettings(t.Context(), onlyBudget.Settings.Revision, SettingsPatch{ExecutionMode: &autonomous})
	if err != nil {
		t.Fatal(err)
	}
	if same.PermissionMoved || same.Settings.PermissionEpoch != onlyBudget.Settings.PermissionEpoch {
		t.Fatalf("unchanged mode advanced the epoch: %+v", same)
	}
	// 4. A stale revision conflicts.
	if _, err := service.UpdateSettings(t.Context(), 1, SettingsPatch{ExecutionMode: &mode}); !IsCode(err, "revision_conflict") {
		t.Fatalf("stale revision error = %v", err)
	}
	// 5. The client cannot set the server-owned epoch.
	if _, err := service.UpdateSettings(t.Context(), same.Settings.Revision, SettingsPatch{RejectEpoch: true}); !IsCode(err, "invalid_request") {
		t.Fatalf("client epoch error = %v", err)
	}
	// 6. An invalid mode is rejected.
	bad := "unsafe"
	if _, err := service.UpdateSettings(t.Context(), same.Settings.Revision, SettingsPatch{ExecutionMode: &bad}); !IsCode(err, "invalid_request") {
		t.Fatalf("invalid mode error = %v", err)
	}
}

// TestPermissionEpochBumpExpiresPendingRequestsAndGrants proves a real permission
// change invalidates earlier authority in the same transaction.
func TestPermissionEpochBumpExpiresPendingRequestsAndGrants(t *testing.T) {
	db, service, th := setup(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertRun(w.Run{ID: "run", ThreadID: th.ID, WorkspaceID: th.WorkspaceID, Status: "running", CreatedAt: w.Now()}, "key", "hash")
	}); err != nil {
		t.Fatal(err)
	}
	request := w.ToolPermissionRequest{
		ID: "pending", ThreadID: th.ID, WorkspaceID: th.WorkspaceID, RunID: "run",
		StepID: "step", AttemptID: "attempt", MemberID: "owner", ToolCallID: "call",
		Origin: w.OriginHost, Action: "write", ToolName: "write_file",
		NormalizedResourceScope: "/tmp/outside", ArgumentsDigest: "digest",
		PermissionEpoch: 1, Status: w.PermissionPending, Revision: 1, CreatedAt: w.Now(),
	}
	grant := w.ConversationGrant{ID: "grant", ThreadID: th.ID, Action: "write", ToolMatcher: "write_file",
		NormalizedResourceScope: "/tmp/outside", PermissionEpoch: 1, SourceRequestID: "x", Status: "active", CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SavePermissionRequest(request); err != nil {
			return err
		}
		return tx.SaveConversationGrant(grant)
	}); err != nil {
		t.Fatal(err)
	}
	var current w.Settings
	if err := service.store.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mode := w.ExecutionControlled
	change, err := service.UpdateSettings(t.Context(), current.Revision, SettingsPatch{ExecutionMode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	if change.Settings.PermissionEpoch != current.PermissionEpoch+1 {
		t.Fatalf("epoch = %d", change.Settings.PermissionEpoch)
	}
	requests, err := service.ThreadPermissionRequests(t.Context(), th.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Status != w.PermissionExpired {
		t.Fatalf("pending request survived the epoch change: %+v", requests)
	}
	grants, err := service.ThreadConversationGrants(t.Context(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].Status != "revoked" {
		t.Fatalf("conversation grant survived the epoch change: %+v", grants)
	}
}

func ptrString(value string) *string { return &value }
