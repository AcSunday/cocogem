package project

import (
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestExecutionModesExposeOnlyTheirRealCapabilities pins the honest capability
// projection: project_autonomous is no longer silently downgraded to controlled,
// its remaining unknowns are recorded, and neither mode rewrites the budget.
func TestExecutionModesExposeOnlyTheirRealCapabilities(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         string
		effective    string
		native       bool
		hasUnknowns  bool
		enforcedList []string
	}{
		{name: "project autonomous keeps its mode and records unknowns", mode: w.ExecutionProjectAutonomous, effective: w.ExecutionProjectAutonomous, native: true, hasUnknowns: true, enforcedList: []string{"host_grant_intersection", "project_root_normalized", "native_allowlist"}},
		{name: "full access records native capability and unenforced constraints", mode: w.ExecutionFullAccess, effective: w.ExecutionFullAccess, native: true, hasUnknowns: true, enforcedList: []string{"process_group_observation"}},
		{name: "controlled keeps commands closed", mode: w.ExecutionControlled, effective: w.ExecutionControlled, native: false, enforcedList: []string{"host_grant_intersection"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, service, created, run := authorizationFixture(t)
			if test.mode != w.ExecutionControlled {
				thread := w.Thread{ID: "native-task-thread", WorkspaceID: run.WorkspaceID, AgentIDs: []string{run.Agents[0].ID}, CoordinatorID: run.Agents[0].ID, Internal: true, Revision: 1, UpdatedAt: w.Now()}
				if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(thread) }); err != nil {
					t.Fatal(err)
				}
				run.ThreadID = thread.ID
				actions := TaskActions(test.mode, w.RunInput{Workflow: "engineering"})
				var err error
				created, err = service.CreateTask(t.Context(), CreateTaskInput{
					WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, OwnerMemberID: run.Agents[0].ID,
					Goal: "explicitly authorized native development", AllowedActions: actions,
					SourceMessageID: "native-task-choice", AcceptedBy: "user",
				})
				if err != nil {
					t.Fatal(err)
				}
				run.TaskID = created.Task.ID
			}
			if _, err := setGlobalMode(t, service, test.mode); err != nil {
				t.Fatal(err)
			}
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				return service.EnsureRunAuthorization(tx, &run, "message-1")
			}); err != nil {
				t.Fatal(err)
			}
			if run.RequestedExecutionMode != test.mode || run.EffectiveExecutionMode != test.effective {
				t.Fatalf("execution mode projection = %s/%s, want %s/%s", run.RequestedExecutionMode, run.EffectiveExecutionMode, test.mode, test.effective)
			}
			if run.PermissionEpoch < 1 {
				t.Fatalf("run did not freeze a permission epoch: %+v", run)
			}
			for _, capability := range test.enforcedList {
				if !slices.Contains(run.EnforcedCapabilities, capability) {
					t.Fatalf("enforced capability %q missing from %v", capability, run.EnforcedCapabilities)
				}
			}
			if test.hasUnknowns != (len(run.UnenforcedConstraints) > 0) {
				t.Fatalf("unenforced constraints = %+v", run.UnenforcedConstraints)
			}
			for _, action := range []string{"native_read", "native_write", "native_execute"} {
				if slices.Contains(run.GrantActions, action) != test.native {
					t.Fatalf("native action %s projection = %+v", action, run.GrantActions)
				}
			}
			view, err := service.Task(t.Context(), created.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if view.Budget.Revision != created.Budget.Revision || view.Budget.UsedContinuations != created.Budget.UsedContinuations {
				t.Fatalf("mode change reset the budget: before=%+v after=%+v", created.Budget, view.Budget)
			}
		})
	}
}

// TestExecutionModeRejectsUnknownValue keeps the three stable wire values.
func TestExecutionModeRejectsUnknownValue(t *testing.T) {
	_, service, _ := setup(t)
	bad := "unsafe"
	if _, err := service.UpdateSettings(t.Context(), 1, SettingsPatch{ExecutionMode: &bad}); !IsCode(err, "invalid_request") {
		t.Fatalf("unknown mode error = %v", err)
	}
}
