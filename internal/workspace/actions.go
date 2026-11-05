package workspace

// ActionForTool maps host tools to capability categories. Tool availability is
// still intersected with role, task, Run and current Grant checks by the caller.
func ActionForTool(name string) string {
	switch name {
	case "read_file", "list_files", "search_content", "read_source", "list_sources", "read_context", "get_task", "read_skill", "read_task":
		return "read"
	case "write_file", "patch_file":
		return "write"
	case "run_test":
		return "run_test"
	case "web_search", "fetch_url":
		return "network"
	case "publish_artifact", "save_claim", "save_memory", "save_dataset", "analyze_csv":
		return "publish_artifact"
	case "handoff_task", "choose_workflow", "send_task_message", "delegate_task", "yield_run", "ask_question":
		return "collaborate"
	default:
		return ""
	}
}

// ModeActions is an upper catalog, never an actual member grant by itself.
func ModeActions(mode string) []string {
	actions := []string{"read", "write", "run_test", "publish_artifact", "collaborate", "network"}
	if mode == ExecutionProjectAutonomous || mode == ExecutionFullAccess {
		actions = append(actions, "native_read", "native_write", "native_execute")
	}
	return actions
}

// NativeIsolationUnknowns is the fail-closed capability projection until an
// adapter and OS combination has real behavioral evidence for each boundary.
func NativeIsolationUnknowns() []string {
	return []string{
		"file_isolation=unknown", "secret_environment_isolation=unknown",
		"network_isolation=unknown", "descendant_lifecycle=unknown",
		"detached_descendant_lifecycle=unknown", "background_job_budget_accounting=unknown",
		"native_effect_audit=partial", "git_metadata_isolation=unknown", "session_resume_ack=unknown",
	}
}
