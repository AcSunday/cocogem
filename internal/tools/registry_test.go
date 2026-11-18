package tools

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
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

func TestEngineeringToolsAreOptInOnly(t *testing.T) {
	for _, name := range EngineeringNames {
		if slices.Contains(Names, name) {
			t.Fatalf("%s became a default grant", name)
		}
	}
	for _, name := range AllNames {
		if !slices.Contains(Names, name) && !slices.Contains(EngineeringNames, name) && name != "run_command" {
			t.Fatalf("%s is outside the complete vocabulary", name)
		}
	}
}

// TestToolBudgetLeavesSelfBoundedToolsAlone is the CI regression: preview_page
// declares and validates its own 1-120 second timeout, so a generic 20 second
// registry budget truncated a healthy browser probe with a CDP deadline error.
func TestToolBudgetLeavesSelfBoundedToolsAlone(t *testing.T) {
	for _, name := range []string{"run_test", "run_command", "preview_page"} {
		if budget := toolBudget(name); budget != 0 {
			t.Fatalf("%s received a generic %s budget", name, budget)
		}
	}
	for _, name := range []string{"read_file", "list_files", "search_content", "view_image"} {
		if budget := toolBudget(name); budget != 20*time.Second {
			t.Fatalf("%s generic budget = %s", name, budget)
		}
	}
}
