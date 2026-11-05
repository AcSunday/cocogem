// Command contract regenerates the OpenAPI contract from shared Go wire objects.
package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"

	"github.com/AcSunday/cocogem/internal/httpapi"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func schema(t reflect.Type) map[string]any {
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return map[string]any{"anyOf": []any{schema(t.Elem()), map[string]any{"type": "null"}}}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Slice:
		return map[string]any{"type": []string{"array", "null"}, "items": schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": schema(t.Elem())}
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			parts := strings.Split(f.Tag.Get("json"), ",")
			if parts[0] == "" || parts[0] == "-" {
				continue
			}
			properties[parts[0]] = schema(f.Type)
			if len(parts) == 1 {
				required = append(required, parts[0])
			}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	return map[string]any{}
}
func ref(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
func main() {
	schemas := map[string]any{}
	for _, value := range []any{w.Workspace{}, w.Agent{}, w.WorkspaceMember{}, w.Settings{}, w.Thread{}, w.RunInput{}, w.Run{}, w.Delivery{}, w.Step{}, w.Attempt{}, w.ProcessRef{}, w.Message{}, w.Source{}, w.SourceImport{}, w.Artifact{}, w.Event{}, w.Finding{}, w.ArtifactRef{}, skills.Info{}, httpapi.SystemInfo{}, httpapi.ThreadView{}, httpapi.RunView{}, httpapi.RoutingPreview{}, httpapi.WorkspaceInput{}, httpapi.SettingsInput{}, httpapi.ThreadUpdate{}, httpapi.AuthStatus{}, httpapi.PasswordInput{}, httpapi.SessionReply{}, httpapi.FeedbackInput{}, httpapi.VerifyReply{}, httpapi.VerifyInput{}, httpapi.ExecutorInfo{}, httpapi.DeleteReply{}, httpapi.TrashItem{}, httpapi.TrashList{}, httpapi.DirectoryListing{}, workflow.Routing{}, store.MessageContentPage{}, store.EventPage{}, w.BudgetPolicy{}, w.BudgetPolicyOverride{}, w.BudgetSources{}, w.BudgetAccount{}, w.AcceptanceCriterion{}, w.TaskRevision{}, w.Task{}, w.ProjectRecord{}, w.TaskDependency{}, w.CriterionResult{}, w.CompletionRecord{}, w.Grant{}, w.WorkspaceExecution{}, w.ExecutionSession{}, w.EffectRecord{}, w.InboxItem{}, w.Question{}, w.CollaborationMessage{}, w.DeliveryReceipt{}, w.Wakeup{}, w.HandoffSummary{}, w.MemoryRecord{}, w.Claim{}, w.DatasetManifest{}, w.ContextEntry{}, w.EligibleScope{}, w.ContextBudget{}, w.ContextItem{}, w.ContextManifest{}, w.ReadReceipt{}, httpapi.ContextView{}, project.TaskView{}, project.Overview{}, project.TaskBoard{}, project.CreateTaskInput{}, project.ReviseTaskInput{}, project.RecordCompletionInput{}, project.SubmitInboxInput{}, httpapi.TaskList{}, httpapi.InboxList{}, httpapi.RevisionList{}, httpapi.DependencyList{}, httpapi.RecordList{}, httpapi.GrantList{}, httpapi.RevokeGrantInput{}, httpapi.TaskActionInput{}, httpapi.TaskBudgetInput{}, httpapi.ExecutionPolicyInput{}, httpapi.BudgetPolicyInput{}, httpapi.QuestionList{}, httpapi.QuestionAnswerInput{}, httpapi.TaskMessageInput{}, project.MessageView{}, httpapi.CollaborationMessageList{}, httpapi.UserDelegationInput{}, project.DelegationView{}, httpapi.HandoffInput{}, httpapi.WakeupList{}, httpapi.WakeEventInput{}, httpapi.ClaimList{}, httpapi.DatasetList{}, httpapi.MemoryList{}, httpapi.BootstrapInput{}, httpapi.BootstrapReply{}} {
		t := reflect.TypeOf(value)
		schemas[t.Name()] = schema(t)
	}
	schemas["ModelOptions"] = schema(reflect.TypeFor[httpapi.ModelOptions]())
	schemas["ModelOptions"].(map[string]any)["properties"].(map[string]any)["thinking_levels"].(map[string]any)["type"] = "array"
	schemas["PasswordInput"].(map[string]any)["properties"].(map[string]any)["password"] = map[string]any{"type": "string", "minLength": 12, "maxLength": 128, "writeOnly": true}
	schemas["Event"].(map[string]any)["description"] = "Persisted per-run event. seq is strictly increasing; clients deduplicate (run_id,seq)."
	props := func(name string) map[string]any { return schemas[name].(map[string]any)["properties"].(map[string]any) }
	// A content page reuses the Message entity for author/model snapshots;
	// reference it instead of inlining a second copy of the same schema.
	props("MessageContentPage")["message"] = ref("Message")
	for name, description := range map[string]string{
		"id":           "Accepted for compatibility; ignored. The path id is authoritative.",
		"workspace_id": "Accepted for compatibility; ignored. A thread never changes workspace.",
		"revision":     "Accepted for compatibility; ignored. If-Match is authoritative.",
		"updated_at":   "Accepted for compatibility; ignored. The server sets the timestamp.",
	} {
		props("ThreadUpdate")[name].(map[string]any)["description"] = description
	}
	props("DirectoryListing")["items"].(map[string]any)["type"] = "array"
	for _, name := range []string{"models", "roots", "verified", "limits"} {
		p := props("SystemInfo")[name].(map[string]any)
		if name == "models" || name == "roots" {
			p["type"] = "array"
		} else {
			p["type"] = "object"
		}
	}
	for _, name := range []string{"runs", "sources"} {
		props("ThreadView")[name].(map[string]any)["type"] = "array"
	}
	for _, name := range []string{"message_ids", "artifact_refs"} {
		props("Delivery")[name].(map[string]any)["type"] = "array"
	}
	// Run is inlined inside RunView and ThreadView; point every copy at the one
	// named Delivery schema so clients do not get incompatible duplicate types.
	var normalizeDelivery func(any)
	normalizeDelivery = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if _, ok := node["delivery"]; ok {
				node["delivery"] = map[string]any{"anyOf": []any{ref("Delivery"), map[string]any{"type": "null"}}, "description": "Authoritative final message and exact artifact version references, recorded atomically on success. Null on legacy history that never recorded a delivery; never inferred from timestamps."}
			}
			for _, child := range node {
				normalizeDelivery(child)
			}
		case []any:
			for _, child := range node {
				normalizeDelivery(child)
			}
		}
	}
	for _, value := range schemas {
		normalizeDelivery(value)
	}
	props("ThreadView")["messages"].(map[string]any)["properties"].(map[string]any)["items"].(map[string]any)["type"] = "array"
	props("EventPage")["items"].(map[string]any)["type"] = "array"
	schemas["Error"] = map[string]any{"type": "object", "required": []string{"error"}, "properties": map[string]any{"error": map[string]any{"type": "object", "required": []string{"code", "message", "request_id", "details"}, "properties": map[string]any{"code": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}, "request_id": map[string]any{"type": "string"}, "details": map[string]any{"type": "object"}}}}}
	paths := map[string]any{}
	add := func(path, method, name, response string, request string) {
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		op := map[string]any{"operationId": name, "responses": map[string]any{"200": map[string]any{"description": "Success", "content": map[string]any{"application/json": map[string]any{"schema": ref(response)}}}, "default": map[string]any{"description": "Structured error", "content": map[string]any{"application/json": map[string]any{"schema": ref("Error")}}}}}
		params := []any{}
		for _, part := range strings.Split(path, "/") {
			if strings.HasPrefix(part, "{") {
				params = append(params, map[string]any{"in": "path", "name": strings.Trim(part, "{}"), "required": true, "schema": map[string]any{"type": "string"}})
			}
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if request != "" {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": ref(request)}}}
		}
		item[method] = op
	}
	lists := map[string]string{"WorkspaceList": "Workspace", "AgentList": "Agent", "ThreadList": "Thread", "ArtifactList": "Artifact"}
	for name, item := range lists {
		schemas[name] = map[string]any{"type": "object", "required": []string{"items"}, "properties": map[string]any{"items": map[string]any{"type": []string{"array", "null"}, "items": ref(item)}, "next_cursor": map[string]any{"type": "string"}}, "additionalProperties": false}
	}
	schemas["EmptyInput"] = map[string]any{"type": "object", "additionalProperties": false}
	schemas["RunRequest"] = schema(reflect.TypeFor[w.RunInput]())
	schemas["RunRequest"].(map[string]any)["required"] = []string{"message"}
	for _, properties := range []map[string]any{
		props("RunInput"), props("RunRequest"),
		props("Run")["input"].(map[string]any)["properties"].(map[string]any),
	} {
		properties["research_scope"].(map[string]any)["description"] = "auto: research proactively with granted tools as needed, including URLs in the message; given_sources: supplied URLs, bound files and captured sources only; web_discovery: explicitly require online search. Empty retains legacy given-source behavior."
		properties["engineering_action"].(map[string]any)["description"] = "Explicit Composer choice for workflow=engineering: analyze (read-only, default) or develop (may write files and run sandboxed tests only when the member grant and step role allow it). A tool grant alone never changes this value."
	}
	schemas["WorkspaceInput"].(map[string]any)["required"] = []string{"name"}
	schemas["AgentInput"] = schema(reflect.TypeFor[w.Agent]())
	schemas["AgentInput"].(map[string]any)["required"] = []string{"display_name", "handle", "model", "tools"}
	schemas["WorkspaceMemberInput"] = schema(reflect.TypeFor[w.WorkspaceMember]())
	schemas["WorkspaceMemberInput"].(map[string]any)["required"] = []string{"enabled", "tools"}
	schemas["ThreadInput"] = schema(reflect.TypeFor[w.Thread]())
	schemas["ThreadInput"].(map[string]any)["required"] = []string{"title"}
	for _, endpoint := range [][5]string{
		{"/api/v1/system/model-options", "get", "modelOptions", "ModelOptions", ""},
		{"/api/v1/directories", "get", "directories", "DirectoryListing", ""}, {"/api/v1/bootstrap", "post", "bootstrap", "BootstrapReply", "BootstrapInput"},
		{"/auth/status", "get", "authStatus", "AuthStatus", ""}, {"/auth/setup", "post", "setupPassword", "SessionReply", "PasswordInput"}, {"/auth/login", "post", "login", "SessionReply", "PasswordInput"}, {"/api/v1/auth/logout", "post", "logout", "AuthStatus", "EmptyInput"}, {"/api/v1/system", "get", "system", "SystemInfo", ""}, {"/api/v1/system/verify", "post", "verify", "VerifyReply", "VerifyInput"},
		{"/api/v1/workspaces", "get", "workspaces", "WorkspaceList", ""}, {"/api/v1/workspaces", "post", "createWorkspace", "Workspace", "WorkspaceInput"}, {"/api/v1/workspaces/{id}", "patch", "updateWorkspace", "Workspace", "WorkspaceInput"}, {"/api/v1/workspaces/{id}", "delete", "deleteWorkspace", "DeleteReply", ""},
		{"/api/v1/workspaces/{id}/agents", "get", "agents", "AgentList", ""}, {"/api/v1/workspaces/{id}/agents", "post", "createAgent", "Agent", "AgentInput"}, {"/api/v1/agents/{id}", "patch", "updateAgent", "Agent", "AgentInput"},
		{"/api/v1/agents", "get", "globalAgents", "AgentList", ""}, {"/api/v1/agents", "post", "createGlobalAgent", "Agent", "AgentInput"},
		{"/api/v1/settings", "get", "settings", "Settings", ""}, {"/api/v1/settings", "patch", "updateSettings", "Settings", "SettingsInput"},
		{"/api/v1/workspaces/{id}/members/{agentID}", "put", "linkMember", "Agent", "WorkspaceMemberInput"}, {"/api/v1/workspaces/{id}/members/{agentID}", "patch", "updateMember", "Agent", "WorkspaceMemberInput"}, {"/api/v1/workspaces/{id}/members/{agentID}", "delete", "unlinkMember", "DeleteReply", ""},
		{"/api/v1/workspaces/{id}/threads", "get", "threads", "ThreadList", ""}, {"/api/v1/workspaces/{id}/threads", "post", "createThread", "Thread", "ThreadInput"}, {"/api/v1/threads/{id}", "get", "thread", "ThreadView", ""}, {"/api/v1/threads/{id}", "patch", "updateThread", "Thread", "ThreadUpdate"}, {"/api/v1/threads/{id}", "delete", "deleteThread", "DeleteReply", ""},
		{"/api/v1/trash", "get", "trash", "TrashList", ""}, {"/api/v1/threads/{id}/restore", "post", "restoreThread", "Thread", "EmptyInput"}, {"/api/v1/threads/{id}/permanent", "delete", "permanentDeleteThread", "DeleteReply", ""},
		{"/api/v1/threads/{id}/routing-preview", "post", "routingPreview", "RoutingPreview", "RunRequest"}, {"/api/v1/threads/{id}/runs", "post", "submitRun", "Run", "RunRequest"}, {"/api/v1/runs/{id}", "get", "run", "RunView", ""}, {"/api/v1/runs/{id}/context", "get", "runContext", "ContextView", ""}, {"/api/v1/runs/{id}/cancel", "post", "cancelRun", "Run", "EmptyInput"}, {"/api/v1/runs/{id}/resume", "post", "resumeRun", "Run", "EmptyInput"}, {"/api/v1/runs/{id}/retry", "post", "retryRun", "Run", "EmptyInput"},
		{"/api/v1/tasks", "get", "projectTaskBoard", "TaskBoard", ""}, {"/api/v1/workspaces/{id}/overview", "get", "projectOverview", "Overview", ""}, {"/api/v1/workspaces/{id}/tasks", "get", "projectTasks", "TaskList", ""}, {"/api/v1/workspaces/{id}/tasks", "post", "createProjectTask", "TaskView", "CreateTaskInput"}, {"/api/v1/tasks/{id}", "get", "projectTask", "TaskView", ""},
		{"/api/v1/tasks/{id}/revisions", "get", "projectTaskRevisions", "RevisionList", ""}, {"/api/v1/tasks/{id}/revisions", "post", "reviseProjectTask", "TaskView", "ReviseTaskInput"}, {"/api/v1/tasks/{id}/dependencies", "get", "taskDependencies", "DependencyList", ""}, {"/api/v1/tasks/{id}/dependencies", "post", "addTaskDependency", "TaskDependency", "TaskDependency"}, {"/api/v1/tasks/{id}/completions", "post", "completeProjectTask", "CompletionRecord", "RecordCompletionInput"}, {"/api/v1/tasks/{id}/inbox", "get", "taskInbox", "InboxList", ""}, {"/api/v1/tasks/{id}/inbox", "post", "submitTaskInbox", "InboxItem", "SubmitInboxInput"},
		{"/api/v1/tasks/{id}/actions", "post", "projectTaskAction", "Task", "TaskActionInput"},
		{"/api/v1/tasks/{id}/questions", "get", "taskQuestions", "QuestionList", ""}, {"/api/v1/tasks/{id}/questions/{questionID}/answers", "post", "answerTaskQuestion", "Question", "QuestionAnswerInput"},
		{"/api/v1/tasks/{id}/messages", "get", "taskMessages", "CollaborationMessageList", ""}, {"/api/v1/tasks/{id}/messages", "post", "sendTaskMessage", "MessageView", "TaskMessageInput"}, {"/api/v1/collaboration-messages/{id}", "get", "collaborationMessage", "MessageView", ""},
		{"/api/v1/tasks/{id}/delegations", "post", "delegateTask", "DelegationView", "UserDelegationInput"}, {"/api/v1/tasks/{id}/handoffs", "post", "handoffTask", "Task", "HandoffInput"}, {"/api/v1/tasks/{id}/wakeups", "get", "taskWakeups", "WakeupList", ""}, {"/api/v1/wake-events", "post", "observeWakeEvent", "Run", "WakeEventInput"},
		{"/api/v1/tasks/{id}/claims", "get", "taskClaims", "ClaimList", ""}, {"/api/v1/tasks/{id}/claims", "post", "saveTaskClaim", "Claim", "Claim"}, {"/api/v1/tasks/{id}/datasets", "get", "taskDatasets", "DatasetList", ""}, {"/api/v1/tasks/{id}/datasets", "post", "saveTaskDataset", "DatasetManifest", "DatasetManifest"}, {"/api/v1/workspaces/{id}/memories", "get", "projectMemories", "MemoryList", ""}, {"/api/v1/workspaces/{id}/memories", "post", "saveProjectMemory", "MemoryRecord", "MemoryRecord"},
		{"/api/v1/executions/{id}", "get", "execution", "WorkspaceExecution", ""}, {"/api/v1/effects/{id}", "get", "effect", "EffectRecord", ""},
		{"/api/v1/tasks/{id}/budget", "get", "projectTaskBudget", "BudgetAccount", ""}, {"/api/v1/tasks/{id}/budget", "patch", "adjustProjectTaskBudget", "BudgetAccount", "TaskBudgetInput"}, {"/api/v1/workspaces/{id}/records", "get", "projectRecords", "RecordList", ""}, {"/api/v1/workspaces/{id}/records", "post", "saveProjectRecord", "ProjectRecord", "ProjectRecord"}, {"/api/v1/workspaces/{id}/budget-policy", "patch", "setProjectBudgetPolicy", "Workspace", "BudgetPolicyInput"}, {"/api/v1/workspaces/{id}/execution-policy", "patch", "setProjectExecutionPolicy", "Workspace", "ExecutionPolicyInput"}, {"/api/v1/workspaces/{id}/grants", "get", "projectGrants", "GrantList", ""}, {"/api/v1/workspaces/{id}/grants/{memberID}/revoke", "post", "revokeProjectGrant", "Grant", "RevokeGrantInput"},
		{"/api/v1/runs/{id}/events", "get", "runEvents", "Event", ""}, {"/api/v1/runs/{id}/event-history", "get", "runEventHistory", "EventPage", ""}, {"/api/v1/runs/{id}/messages/{messageID}/content", "get", "runMessageContent", "MessageContentPage", ""}, {"/api/v1/sources/{id}", "get", "source", "Source", ""}, {"/api/v1/threads/{id}/artifacts", "get", "artifacts", "ArtifactList", ""}, {"/api/v1/artifacts/{id}/versions/{version}", "get", "artifact", "Artifact", ""}, {"/api/v1/artifacts/{id}/versions/{version}", "patch", "feedback", "Artifact", "FeedbackInput"}, {"/api/v1/artifacts/{id}/versions/{version}/export", "get", "exportArtifact", "Artifact", ""},
	} {
		add(endpoint[0], endpoint[1], endpoint[2], endpoint[3], endpoint[4])
	}
	for path, raw := range paths {
		for method, rawOperation := range raw.(map[string]any) {
			op := rawOperation.(map[string]any)
			params, _ := op["parameters"].([]any)
			if path == "/api/v1/system/model-options" {
				params = append(params, map[string]any{"in": "query", "name": "executor", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"opencode"}}}, map[string]any{"in": "query", "name": "model", "schema": map[string]any{"type": "string", "maxLength": 150}, "description": "OpenCode provider/model, provider/model#variant, or default. Reads the local catalog without an upstream prompt."})
			}
			if path == "/api/v1/directories" {
				params = append(params, map[string]any{"in": "query", "name": "path", "description": "Existing local directory; empty starts at allowed roots or the home directory. Does not bind a workspace.", "schema": map[string]any{"type": "string", "maxLength": 4096}})
			}
			if method != "get" && !strings.HasPrefix(path, "/auth/") {
				params = append(params, map[string]any{"in": "header", "name": "X-CSRF-Token", "required": true, "schema": map[string]any{"type": "string"}})
			}
			if method == "patch" || method == "delete" && (strings.Contains(path, "/members/") || strings.HasSuffix(path, "/permanent")) || path == "/api/v1/threads/{id}/restore" || (strings.HasSuffix(path, "/revisions") || strings.HasSuffix(path, "/answers")) && method == "post" {
				params = append(params, map[string]any{"in": "header", "name": "If-Match", "required": true, "schema": map[string]any{"type": "string"}})
			}
			if path == "/api/v1/bootstrap" {
				params = append(params, map[string]any{"in": "header", "name": "Idempotency-Key", "required": true, "schema": map[string]any{"type": "string", "minLength": 16, "maxLength": 100}})
			}
			if (strings.HasSuffix(path, "/runs") || strings.HasSuffix(path, "/resume") || strings.HasSuffix(path, "/retry")) && method == "post" {
				if strings.HasSuffix(path, "/runs") {
					params = append(params, map[string]any{"in": "header", "name": "Idempotency-Key", "required": true, "schema": map[string]any{"type": "string", "minLength": 16, "maxLength": 100}})
				}
				responses := op["responses"].(map[string]any)
				responses["202"] = responses["200"]
				delete(responses, "200")
			}
			if method == "get" && (strings.HasSuffix(path, "/threads") || strings.HasPrefix(path, "/api/v1/threads/") && !strings.Contains(strings.TrimPrefix(path, "/api/v1/threads/"), "/")) {
				params = append(params, map[string]any{"in": "query", "name": "cursor", "schema": map[string]any{"type": "string"}}, map[string]any{"in": "query", "name": "limit", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}})
			}
			if strings.HasSuffix(path, "/events") {
				params = append(params, map[string]any{"in": "query", "name": "after", "schema": map[string]any{"type": "integer", "minimum": 0}}, map[string]any{"in": "header", "name": "Last-Event-ID", "schema": map[string]any{"type": "string"}})
			}
			if strings.HasSuffix(path, "/event-history") {
				params = append(params, map[string]any{"in": "query", "name": "after", "schema": map[string]any{"type": "integer", "minimum": 0, "default": 0}}, map[string]any{"in": "query", "name": "limit", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 100}})
			}
			if strings.HasSuffix(path, "/content") {
				params = append(params, map[string]any{"in": "query", "name": "cursor", "schema": map[string]any{"type": "string"}}, map[string]any{"in": "query", "name": "limit", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 32, "default": 8}})
			}
			if len(params) > 0 {
				op["parameters"] = params
			}
			if strings.HasPrefix(path, "/auth/") {
				op["security"] = []any{}
			}
			if strings.HasSuffix(path, "/export") {
				op["responses"].(map[string]any)["200"] = map[string]any{"description": "Markdown download", "content": map[string]any{"text/markdown": map[string]any{"schema": map[string]any{"type": "string"}}}}
			}
		}
	}
	stream := paths["/api/v1/runs/{id}/events"].(map[string]any)["get"].(map[string]any)
	stream["responses"].(map[string]any)["200"] = map[string]any{"description": "SSE: each data field is an Event JSON object; id is seq. Heartbeats carry no business sequence.", "content": map[string]any{"text/event-stream": map[string]any{"schema": ref("Event")}}}
	// context_size is a member-facing token budget, not a byte limit. The same
	// property appears in agents, run/thread snapshots and verification inputs.
	var describeContextSize func(any)
	describeContextSize = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if properties, ok := node["properties"].(map[string]any); ok {
				if field, ok := properties["context_size"].(map[string]any); ok {
					field["description"] = "Estimated input token budget. Empty or 0 follows the CLI's configured context window; an explicit value must be at least 1024."
				}
			}
			for _, child := range node {
				describeContextSize(child)
			}
		case []any:
			for _, child := range node {
				describeContextSize(child)
			}
		}
	}
	for _, value := range schemas {
		describeContextSize(value)
	}
	doc := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Cocogem local workspace API", "version": "0.1.0"}, "paths": paths, "components": map[string]any{"schemas": schemas, "securitySchemes": map[string]any{"localSession": map[string]any{"type": "apiKey", "in": "cookie", "name": "cocogem_session"}}}, "security": []any{map[string]any{"localSession": []string{}}}}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll("api", 0755); err != nil {
		panic(err)
	}
	if err := os.WriteFile("api/openapi.yaml", append(b, '\n'), 0644); err != nil {
		panic(err)
	}
}
