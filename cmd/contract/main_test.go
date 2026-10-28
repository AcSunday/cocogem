package main

import (
	"encoding/json"
	"github.com/AcSunday/cocogem/internal/httpapi"
	w "github.com/AcSunday/cocogem/internal/workspace"
	"os"
	"reflect"
	"testing"
)

func TestWireSchemaMatchesRunFields(t *testing.T) {
	s := schema(reflect.TypeFor[w.Run]())
	props := s["properties"].(map[string]any)
	for _, name := range []string{"id", "workspace_id", "thread_id", "input", "status"} {
		if _, ok := props[name]; !ok {
			t.Fatal("missing field", name)
		}
	}
	b, err := json.Marshal(s)
	if err != nil || !json.Valid(b) {
		t.Fatal("invalid schema", err)
	}
	if s["additionalProperties"] != false {
		t.Fatal("wire schema permits unknown fields")
	}
}

func TestSchemasExcludeRemovedMode(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[w.Run](), reflect.TypeFor[w.Artifact](), reflect.TypeFor[httpapi.SystemInfo]()} {
		if _, exists := schema(typ)["properties"].(map[string]any)["demo"]; exists {
			t.Fatalf("%s still exposes the removed mode", typ.Name())
		}
	}
}

func TestWorkspaceContractExcludesConfirmation(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[w.Workspace](), reflect.TypeFor[httpapi.WorkspaceInput]()} {
		if _, exists := schema(typ)["properties"].(map[string]any)["consent_version"]; exists {
			t.Fatalf("%s still requires retired confirmation", typ.Name())
		}
	}
}

func TestOpenAPIDeclaresBoundedHistoryReads(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	operation := func(path string) map[string]any {
		t.Helper()
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing path %s", path)
		}
		get, ok := item["get"].(map[string]any)
		if !ok {
			t.Fatalf("missing GET %s", path)
		}
		return get
	}
	queries := func(get map[string]any) map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		params, _ := get["parameters"].([]any)
		for _, raw := range params {
			param := raw.(map[string]any)
			if param["in"] == "query" {
				out[param["name"].(string)] = param["schema"].(map[string]any)
			}
		}
		return out
	}
	content := operation("/api/v1/runs/{id}/messages/{messageID}/content")
	contentQuery := queries(content)
	if contentQuery["cursor"] == nil || contentQuery["limit"]["maximum"] != float64(32) || contentQuery["limit"]["default"] != float64(8) {
		t.Fatalf("content query bounds = %+v", contentQuery)
	}
	history := operation("/api/v1/runs/{id}/event-history")
	historyQuery := queries(history)
	if historyQuery["after"]["minimum"] != float64(0) || historyQuery["limit"]["maximum"] != float64(500) || historyQuery["limit"]["default"] != float64(100) {
		t.Fatalf("event history query bounds = %+v", historyQuery)
	}
	for name, get := range map[string]map[string]any{"content": content, "history": history} {
		responses := get["responses"].(map[string]any)
		if responses["default"].(map[string]any)["content"].(map[string]any)["application/json"] == nil {
			t.Fatalf("%s errors are not JSON", name)
		}
	}
	run := schema(reflect.TypeFor[w.Run]())["properties"].(map[string]any)
	for _, name := range []string{"execution_policy", "started_at", "deadline_at", "policy_adopted_at"} {
		if _, ok := run[name]; !ok {
			t.Fatalf("Run schema is missing %s", name)
		}
	}
	attempt := schema(reflect.TypeFor[w.Attempt]())["properties"].(map[string]any)
	for _, name := range []string{"failure_kind", "usage_known", "input_tokens", "output_tokens"} {
		if _, ok := attempt[name]; !ok {
			t.Fatalf("Attempt schema is missing %s", name)
		}
	}
	message := schema(reflect.TypeFor[w.Message]())["properties"].(map[string]any)
	for _, name := range []string{"content_format", "content_bytes", "content_truncated"} {
		if _, ok := message[name]; !ok {
			t.Fatalf("Message schema is missing %s", name)
		}
	}
}
