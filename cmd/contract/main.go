// Command contract regenerates the OpenAPI contract from shared Go wire objects.
package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"

	"github.com/AcSunday/cocogem/internal/httpapi"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func schema(t reflect.Type) map[string]any {
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}
	}
	switch t.Kind() {
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
	for _, value := range []any{w.Workspace{}, w.Agent{}, w.Thread{}, w.RunInput{}, w.Run{}, w.Step{}, w.Message{}, w.Source{}, w.Artifact{}, w.Event{}, w.Finding{}, w.ArtifactRef{}, httpapi.SystemInfo{}, httpapi.ThreadView{}, httpapi.RunView{}, httpapi.RoutingPreview{}, httpapi.WorkspaceInput{}, httpapi.BootstrapInput{}, httpapi.BootstrapReply{}, httpapi.FeedbackInput{}, httpapi.VerifyReply{}, httpapi.VerifyInput{}, httpapi.ExecutorInfo{}, httpapi.DeleteReply{}, workflow.Routing{}} {
		t := reflect.TypeOf(value)
		schemas[t.Name()] = schema(t)
	}
	schemas["Event"].(map[string]any)["description"] = "Persisted per-run event. seq is strictly increasing; clients deduplicate (run_id,seq)."
	props := func(name string) map[string]any { return schemas[name].(map[string]any)["properties"].(map[string]any) }
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
	props("ThreadView")["messages"].(map[string]any)["properties"].(map[string]any)["items"].(map[string]any)["type"] = "array"
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
	schemas["WorkspaceInput"].(map[string]any)["required"] = []string{"name"}
	schemas["AgentInput"] = schema(reflect.TypeFor[w.Agent]())
	schemas["AgentInput"].(map[string]any)["required"] = []string{"display_name", "handle", "model", "tools"}
	schemas["ThreadInput"] = schema(reflect.TypeFor[w.Thread]())
	schemas["ThreadInput"].(map[string]any)["required"] = []string{"title", "agent_ids", "coordinator_id"}
	for _, endpoint := range [][5]string{
		{"/bootstrap", "post", "bootstrap", "BootstrapReply", "BootstrapInput"}, {"/api/v1/system", "get", "system", "SystemInfo", ""}, {"/api/v1/system/verify", "post", "verify", "VerifyReply", "VerifyInput"},
		{"/api/v1/workspaces", "get", "workspaces", "WorkspaceList", ""}, {"/api/v1/workspaces", "post", "createWorkspace", "Workspace", "WorkspaceInput"}, {"/api/v1/workspaces/{id}", "patch", "updateWorkspace", "Workspace", "WorkspaceInput"}, {"/api/v1/workspaces/{id}", "delete", "deleteWorkspace", "DeleteReply", ""},
		{"/api/v1/workspaces/{id}/agents", "get", "agents", "AgentList", ""}, {"/api/v1/workspaces/{id}/agents", "post", "createAgent", "Agent", "AgentInput"}, {"/api/v1/agents/{id}", "patch", "updateAgent", "Agent", "AgentInput"},
		{"/api/v1/workspaces/{id}/threads", "get", "threads", "ThreadList", ""}, {"/api/v1/workspaces/{id}/threads", "post", "createThread", "Thread", "ThreadInput"}, {"/api/v1/threads/{id}", "get", "thread", "ThreadView", ""}, {"/api/v1/threads/{id}", "patch", "updateThread", "Thread", "ThreadInput"}, {"/api/v1/threads/{id}", "delete", "deleteThread", "DeleteReply", ""},
		{"/api/v1/threads/{id}/routing-preview", "post", "routingPreview", "RoutingPreview", "RunRequest"}, {"/api/v1/threads/{id}/runs", "post", "submitRun", "Run", "RunRequest"}, {"/api/v1/runs/{id}", "get", "run", "RunView", ""}, {"/api/v1/runs/{id}/cancel", "post", "cancelRun", "Run", "EmptyInput"},
		{"/api/v1/runs/{id}/events", "get", "runEvents", "Event", ""}, {"/api/v1/sources/{id}", "get", "source", "Source", ""}, {"/api/v1/threads/{id}/artifacts", "get", "artifacts", "ArtifactList", ""}, {"/api/v1/artifacts/{id}/versions/{version}", "get", "artifact", "Artifact", ""}, {"/api/v1/artifacts/{id}/versions/{version}", "patch", "feedback", "Artifact", "FeedbackInput"}, {"/api/v1/artifacts/{id}/versions/{version}/export", "get", "exportArtifact", "Artifact", ""},
	} {
		add(endpoint[0], endpoint[1], endpoint[2], endpoint[3], endpoint[4])
	}
	for path, raw := range paths {
		for method, rawOperation := range raw.(map[string]any) {
			op := rawOperation.(map[string]any)
			params, _ := op["parameters"].([]any)
			if method != "get" && path != "/bootstrap" {
				params = append(params, map[string]any{"in": "header", "name": "X-CSRF-Token", "required": true, "schema": map[string]any{"type": "string"}})
			}
			if method == "patch" {
				params = append(params, map[string]any{"in": "header", "name": "If-Match", "required": true, "schema": map[string]any{"type": "string"}})
			}
			if strings.HasSuffix(path, "/runs") && method == "post" {
				params = append(params, map[string]any{"in": "header", "name": "Idempotency-Key", "required": true, "schema": map[string]any{"type": "string", "minLength": 16, "maxLength": 100}})
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
			if len(params) > 0 {
				op["parameters"] = params
			}
			if path == "/bootstrap" {
				op["security"] = []any{}
			}
			if strings.HasSuffix(path, "/export") {
				op["responses"].(map[string]any)["200"] = map[string]any{"description": "Markdown download", "content": map[string]any{"text/markdown": map[string]any{"schema": map[string]any{"type": "string"}}}}
			}
		}
	}
	stream := paths["/api/v1/runs/{id}/events"].(map[string]any)["get"].(map[string]any)
	stream["responses"].(map[string]any)["200"] = map[string]any{"description": "SSE: each data field is an Event JSON object; id is seq. Heartbeats carry no business sequence.", "content": map[string]any{"text/event-stream": map[string]any{"schema": ref("Event")}}}
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
