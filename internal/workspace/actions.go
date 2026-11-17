package workspace

// ActionForTool maps host tools to capability categories. Tool availability is
// still intersected with role, task and conversation-grant checks by the caller.
func ActionForTool(name string) string {
	switch name {
	case "read_file", "view_image", "list_files", "search_content", "git_inspect", "preview_page", "read_source", "list_sources", "read_context", "get_task", "read_skill", "read_task":
		return "read"
	case "write_file", "patch_file":
		return "write"
	case "run_test":
		return "run_test"
	case "run_command":
		return "execute"
	case "web_search", "fetch_url":
		return "network"
	case "publish_artifact", "publish_file", "save_claim", "save_memory", "save_dataset", "analyze_csv":
		return "publish_artifact"
	case "external_publish":
		// Publish/deploy/release/install to an external destination is a distinct
		// class from an ordinary public network read.
		return "external"
	case "handoff_task", "promote_to_task", "submit_review_verdict", "send_task_message", "delegate_task", "yield_run", "ask_question":
		return "collaborate"
	default:
		return ""
	}
}

// ModeActions is the upper catalog for one global two-tier mode. It is never a
// member grant by itself: the task/role constraints and the conversation grants
// still apply, and a missing capability becomes a permission request.
//
//	workspace_autonomous  read, write, run_test, publish, collaborate, network,
//	                      the constrained native catalog and project commands.
//	                      Anything outside the workspace, /tmp, the toolchain
//	                      boundary, loopback or the public read proxy is requested.
//	full_access           the autonomous set plus unrestricted external effects.
func ModeActions(mode string) []string {
	actions := []string{"read", "write", "run_test", "publish_artifact", "collaborate", "network"}
	switch mode {
	case ExecutionWorkspaceAutonomous:
		actions = append(actions, "execute", "native_read", "native_write", "native_execute")
	case ExecutionFullAccess:
		actions = append(actions, "execute", "native_read", "native_write", "native_execute", "external")
	}
	return actions
}

// RequestableActions are the capability categories a member may explicitly ask
// for when the current mode does not admit the concrete operation. The concrete
// normalized scope always travels with the request, so a request never widens a
// class beyond the exact target the user approves.
//
// Every class a mode can deny is requestable: a project-external read or write is
// just as much an outside effect as a command, and refusing to offer a request
// would strand the member with an unexplained failure.
func RequestableActions(mode string) []string {
	switch mode {
	case ExecutionWorkspaceAutonomous:
		// Every class the autonomous boundary can deny is requestable under an
		// exact scope: an outside read is as much an outside effect as a command.
		return []string{"read", "write", "run_test", "execute", "network", "publish_artifact", "external"}
	}
	return nil
}

// NativeIsolationUnknowns is the fail-closed capability projection until an
// adapter and OS combination has real behavioral evidence for each boundary.
func NativeIsolationUnknowns() []string {
	return []string{
		"secret_environment_isolation=unknown",
		"descendant_lifecycle=unknown",
		"detached_descendant_lifecycle=unknown",
		"background_job_budget_accounting=unknown",
		"native_effect_audit=partial",
		"git_metadata_isolation=unknown",
		"session_resume_ack=unknown",
	}
}
