package httpapi

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func validateResponse(t *testing.T, path, method string, status int, body []byte) {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	operation := doc["paths"].(map[string]any)[path].(map[string]any)[method].(map[string]any)
	responses := operation["responses"].(map[string]any)
	response, ok := responses[strconv.Itoa(status)].(map[string]any)
	if !ok {
		response = responses["default"].(map[string]any)
	}
	schemaValue := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://cocogem.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://cocogem.test/openapi.json" + schemaValue["$ref"].(string))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("%s %s status %d violates contract: %v", method, path, status, err)
	}
}

func TestOpenAPIDescribesMutationAndStreamingRequirements(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	paths := doc["paths"].(map[string]any)
	submit := paths["/api/v1/threads/{id}/runs"].(map[string]any)["post"].(map[string]any)
	if submit["responses"].(map[string]any)["202"] == nil {
		t.Fatal("missing accepted status")
	}
	for path, method := range map[string]string{"/auth/status": "get", "/auth/setup": "post", "/auth/login": "post"} {
		operation := paths[path].(map[string]any)[method].(map[string]any)
		if len(operation["security"].([]any)) != 0 {
			t.Fatalf("%s requires an existing session", path)
		}
	}
	headers := map[string]bool{}
	for _, param := range submit["parameters"].([]any) {
		p := param.(map[string]any)
		headers[p["name"].(string)] = true
	}
	if !headers["Idempotency-Key"] || !headers["X-CSRF-Token"] {
		t.Fatal("missing mutation headers")
	}
}
