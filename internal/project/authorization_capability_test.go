package project

import (
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestExecutionModesExposeOnlyTheirRealCapabilities(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      string
		effective string
		native    bool
	}{
		{name: "project autonomous fails closed until isolation is verified", mode: w.ExecutionProjectAutonomous, effective: w.ExecutionControlled},
		{name: "full access records native capability and unenforced constraints", mode: w.ExecutionFullAccess, effective: w.ExecutionFullAccess, native: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, service, created, run := authorizationFixture(t)
			if test.native {
				thread := w.Thread{ID: "native-task-thread", WorkspaceID: run.WorkspaceID, AgentIDs: []string{run.Agents[0].ID}, CoordinatorID: run.Agents[0].ID, Internal: true, Revision: 1, UpdatedAt: w.Now()}
				if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(thread) }); err != nil {
					t.Fatal(err)
				}
				run.ThreadID = thread.ID
				var err error
				created, err = service.CreateTask(t.Context(), CreateTaskInput{
					WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, OwnerMemberID: run.Agents[0].ID,
					Goal: "explicitly authorized native development", AllowedActions: []string{"read", "write", "run_test", "collaborate", "native_read", "native_write", "native_execute"},
					SourceMessageID: "native-task-choice", AcceptedBy: "user",
				})
				if err != nil {
					t.Fatal(err)
				}
				run.TaskID = created.Task.ID
			}
			if _, err := service.SetExecutionMode(t.Context(), run.WorkspaceID, test.mode, "mode-choice", 1); err != nil {
				t.Fatal(err)
			}
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				return service.EnsureRunAuthorization(tx, &run, "message-1")
			}); err != nil {
				t.Fatal(err)
			}
			if run.RequestedExecutionMode != test.mode || run.EffectiveExecutionMode != test.effective {
				t.Fatalf("execution mode projection = %+v", run)
			}
			if test.mode != w.ExecutionControlled && len(run.UnenforcedConstraints) != len(w.NativeIsolationUnknowns()) {
				t.Fatalf("unknown isolation constraints = %+v", run.UnenforcedConstraints)
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
