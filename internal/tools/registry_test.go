package tools

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"testing"
)

func TestStrictToolInputAndCapabilitySchemas(t *testing.T) {
	var input struct {
		Path string `json:"path"`
	}
	for _, tc := range []struct {
		name, raw string
		bad       bool
	}{{"valid", `{"path":"a"}`, false}, {"unknown field", `{"path":"a","shell":"rm"}`, true}, {"trailing", `{} {}`, true}, {"null", `null`, true}, {"wrong type", `{"path":42}`, true}} {
		t.Run(tc.name, func(t *testing.T) {
			err := Decode(json.RawMessage(tc.raw), &input)
			if (err != nil) != tc.bad {
				t.Fatal(err)
			}
		})
	}
	defs := Definitions([]string{"read_source", "shell"})
	if len(defs) != 1 || defs[0].Name != "read_source" {
		t.Fatal("unregistered capability exposed", defs)
	}
}

func TestEveryProviderToolSchemaIsValidJSONSchema(t *testing.T) {
	for _, tool := range Definitions(Names) {
		t.Run(tool.Name, func(t *testing.T) {
			compiler := jsonschema.NewCompiler()
			resource := "https://cocogem.test/" + tool.Name + ".json"
			raw, _ := json.Marshal(tool.Schema)
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			if err := compiler.AddResource(resource, value); err != nil {
				t.Fatal(err)
			}
			if _, err := compiler.Compile(resource); err != nil {
				t.Fatal(err)
			}
		})
	}
}
