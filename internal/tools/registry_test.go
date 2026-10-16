package tools

import (
	"encoding/json"
	"testing"
)

func TestStrictToolInputAndCapabilitySchemas(t *testing.T) {
	var input struct {
		Path string `json:"path"`
	}
	for _, tc := range []struct {
		name, raw string
		bad       bool
	}{{"valid", `{"path":"a"}`, false}, {"unknown field", `{"path":"a","shell":"rm"}`, true}, {"trailing", `{} {}`, true}, {"wrong type", `{"path":42}`, true}} {
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
