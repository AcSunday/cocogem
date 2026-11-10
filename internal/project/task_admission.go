package project

import (
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
	"slices"
)

// engineeringWrites reports whether this engineering request may use write and
// test tools. Engineering always offers the member's granted tools; only the
// retired code_analysis workflow is read-only.
func engineeringWrites(in w.RunInput) bool {
	return in.Workflow == "engineering"
}

// TaskActions derives the objective ceiling from the global execution mode and
// the accepted action. A model-created task never invents capabilities.
func TaskActions(mode string, in w.RunInput) []string {
	// Publishing/deploying outside the project always needs a decision; the
	// capability is recorded so the request can be made, not auto-run.
	actions := []string{"read", "publish_artifact", "collaborate", "network", "external"}
	switch mode {
	case w.ExecutionProjectAutonomous, w.ExecutionFullAccess:
		// Project-internal read/write/test and project-scoped commands are
		// autonomous; the project boundary is enforced at execution time, not by
		// withholding the capability from the task.
		actions = append(actions, "write", "run_test", "execute", "native_read", "native_write", "native_execute")
	}
	if engineeringWrites(in) {
		actions = append(actions, "write", "run_test")
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
	if engineeringWrites(in) && !slices.Contains(view.Revision.AllowedActions, "write") {
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
