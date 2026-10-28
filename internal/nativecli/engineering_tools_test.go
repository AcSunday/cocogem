package nativecli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/tools"
)

// The engineering tools are host MCP tools, so the same bridge schemas must be
// usable by every CLI adapter without a new native capability.
func TestEngineeringHostToolsPassScopedBridge(t *testing.T) {
	called := []string{}
	gate, err := newBridge(context.Background(), tools.Definitions(tools.AllNames), func(_ context.Context, name string, _ json.RawMessage) (string, error) {
		called = append(called, name)
		return `{"accepted":true}`, nil
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"write_file", map[string]any{"name": "write_file", "arguments": map[string]any{"path": "calc.go", "content": "package calc\n"}}},
		{"patch_file", map[string]any{"name": "patch_file", "arguments": map[string]any{"path": "calc.go", "expected_sha256": strings.Repeat("a", 64), "old_text": "a - b", "new_text": "a + b"}}},
		{"run_test", map[string]any{"name": "run_test", "arguments": map[string]any{"toolchain": "go", "target": "."}}},
	} {
		raw, err := json.Marshal(tc.params)
		if err != nil {
			t.Fatal(err)
		}
		result, _ := gate.call(raw).(map[string]any)
		if failed, _ := result["isError"].(bool); failed {
			t.Fatalf("%s was rejected by the bridge schema: %+v", tc.name, result)
		}
	}
	if len(called) != 3 || called[0] != "write_file" || called[1] != "patch_file" || called[2] != "run_test" {
		t.Fatalf("bridge dispatch = %v", called)
	}
}
