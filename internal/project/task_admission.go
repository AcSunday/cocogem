package project

import (
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TaskActions derives the objective ceiling from the global execution mode and
// the accepted action. A model-created task never invents capabilities.
func TaskActions(mode string, in w.RunInput) []string {
	// Publishing/deploying outside the project always needs a decision; the
	// capability is recorded so the request can be made, not auto-run.
	actions := []string{"read", "publish_artifact", "collaborate", "network", "external"}
	switch mode {
	case w.ExecutionWorkspaceAutonomous, w.ExecutionFullAccess:
		// Workspace-internal read/write/test and workspace-scoped commands are
		// autonomous; the workspace boundary is enforced at execution time, not
		// by withholding the capability from the task.
		actions = append(actions, "write", "run_test", "execute", "native_read", "native_write", "native_execute")
	}
	return actions
}

// BindRunTask freezes the current objective without treating every follow-up
// question as a replacement goal. Explicit revisions and pending inbox items
// keep their existing versioned application path.
func (s *Service) BindRunTask(tx *store.Tx, th w.Thread, in w.RunInput, messageID, runID string) (TaskView, error) {
	if th.TaskID == "" {
		return s.EnsureThreadTask(tx, th, in, messageID, runID)
	}
	view, err := s.taskInTx(tx, th.TaskID)
	if err != nil {
		return TaskView{}, err
	}
	// A follow-up message on a completed task always reactivates it into a new
	// revision and evaluation generation rather than staying stuck as completed.
	if view.Task.Status == "completed" {
		return s.EnsureThreadTask(tx, th, in, messageID, runID)
	}
	if !slices.Contains(view.Revision.AllowedActions, "write") {
		return s.EnsureThreadTask(tx, th, in, messageID, runID)
	}
	items, err := tx.Inbox(th.TaskID)
	if err != nil {
		return TaskView{}, err
	}
	for _, item := range items {
		if item.Status == "pending" {
			return s.EnsureThreadTask(tx, th, in, messageID, runID)
		}
	}
	return view, nil
}
