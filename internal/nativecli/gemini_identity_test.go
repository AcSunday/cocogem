package nativecli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

// scopedMCPGate is a controlled/scoped-MCP bridge: one declared host tool and
// the exact prefix Gemini uses for it. It has no gate broker, which is how the
// controlled path runs.
func scopedMCPGate(t *testing.T, observe func(string, map[string]any) error) *bridge {
	t.Helper()
	gate := newTestGate(t, []model.Tool{{Name: "read_file", Description: "read", Schema: map[string]any{
		"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required": []string{"path"}, "additionalProperties": false,
	}}}, observe)
	gate.nativePrefix = "mcp_cocogem_"
	return gate
}

// The controlled/scoped-MCP stream carries only the CLI's own tool_id, and its
// tool_result frames have no parameters. They must keep working exactly as
// before the gated adapter existed: no native lifecycle, no id requirement.
func TestParseGeminiScopedMCPToolResultNeedsNoNativeIdentity(t *testing.T) {
	var events []capturedEvent
	gate := scopedMCPGate(t, func(kind string, data map[string]any) error {
		events = append(events, capturedEvent{kind: kind, data: data})
		return nil
	})
	prefix := gate.nativePrefix
	stream := strings.Join([]string{
		`{"type":"init","session_id":"` + fakeSession + `"}`,
		`{"type":"tool_use","tool_name":"` + prefix + `read_file","tool_id":"mcp_cocogem_read_file_1","parameters":{"path":"README.md"}}`,
		`{"type":"tool_result","tool_id":"mcp_cocogem_read_file_1","status":"success","output":"ok"}`,
		`{"type":"message","role":"assistant","content":"done"}`,
		`{"type":"result","status":"success","stats":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"
	if err := parseGemini(strings.NewReader(stream), gate, testGeminiLive(t)); err != nil {
		t.Fatalf("a scoped-MCP tool result was rejected: %v", err)
	}
	for _, event := range events {
		if event.kind == "tool_started" || event.kind == "tool_finished" {
			t.Fatalf("a host MCP call produced a native tool lifecycle event: %+v", event)
		}
	}
}

// A scoped-MCP tool that is not in this step's inventory is still refused.
func TestParseGeminiScopedMCPUndeclaredToolIsRefused(t *testing.T) {
	gate := scopedMCPGate(t, discardObserve)
	stream := strings.Join([]string{
		`{"type":"init","session_id":"` + fakeSession + `"}`,
		`{"type":"tool_use","tool_name":"` + gate.nativePrefix + `write_file","tool_id":"x","parameters":{}}`,
	}, "\n") + "\n"
	if err := parseGemini(strings.NewReader(stream), gate, testGeminiLive(t)); FailureKindOf(err) != FailureProtocol {
		t.Fatalf("error=%v, want a protocol failure for an undeclared host tool", err)
	}
}

// A controlled stream may not run a CLI-native tool: the controlled policy only
// allows the scoped MCP server, so this is a protocol violation.
func TestParseGeminiControlledNativeToolIsRefused(t *testing.T) {
	gate := scopedMCPGate(t, discardObserve)
	stream := strings.Join([]string{
		`{"type":"init","session_id":"` + fakeSession + `"}`,
		`{"type":"tool_use","tool_name":"run_shell_command","tool_id":"s1","parameters":{"command":"ls"}}`,
	}, "\n") + "\n"
	if err := parseGemini(strings.NewReader(stream), gate, testGeminiLive(t)); FailureKindOf(err) != FailureProtocol {
		t.Fatalf("error=%v, want the controlled path to refuse a native tool", err)
	}
}

// The gated adapter keeps its lifecycle: a native call emits tool_started and
// tool_finished with one identity, even though tool_result has no parameters.
func TestParseGeminiGatedNativeLifecyclePairsOneIdentity(t *testing.T) {
	var events []capturedEvent
	gate := scopedMCPGate(t, func(kind string, data map[string]any) error {
		events = append(events, capturedEvent{kind: kind, data: data})
		return nil
	})
	// A broker-backed attempt registers an alias map on its bridge.
	attachGateAliases(gate, newGateAliases())
	defer detachGateAliases(gate)
	stream := strings.Join([]string{
		`{"type":"init","session_id":"` + fakeSession + `"}`,
		`{"type":"tool_use","tool_name":"run_shell_command","tool_id":"run_shell_command__1","parameters":{"command":"printf hi"}}`,
		`{"type":"tool_result","tool_id":"run_shell_command__1","status":"success","output":"hi"}`,
		`{"type":"message","role":"assistant","content":"done"}`,
		`{"type":"result","status":"success","stats":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"
	if err := parseGemini(strings.NewReader(stream), gate, testGeminiLive(t)); err != nil {
		t.Fatalf("the gated native lifecycle failed: %v", err)
	}
	var started, finished map[string]any
	for _, event := range events {
		switch event.kind {
		case "tool_started":
			started = event.data
		case "tool_finished":
			finished = event.data
		}
	}
	if started == nil || finished == nil {
		t.Fatalf("gated lifecycle incomplete: %+v", events)
	}
	if started["tool_call_id"] == "" || started["tool_call_id"] != finished["tool_call_id"] {
		t.Fatalf("lifecycle ids started=%v finished=%v, want one identity",
			started["tool_call_id"], finished["tool_call_id"])
	}
	if !strings.HasPrefix(started["tool_call_id"].(string), nativeIDGemini) {
		t.Fatalf("identity %v is not canonical", started["tool_call_id"])
	}
	if finished["status"] != "succeeded" || finished["output"] != "hi" {
		t.Fatalf("terminal status/output lost the CLI's own result: %+v", finished)
	}
}

// A gated native result whose identity was never bound is a protocol failure,
// never a silent success.
func TestParseGeminiGatedUnboundResultIsRefused(t *testing.T) {
	gate := scopedMCPGate(t, discardObserve)
	attachGateAliases(gate, newGateAliases())
	defer detachGateAliases(gate)
	stream := strings.Join([]string{
		`{"type":"init","session_id":"` + fakeSession + `"}`,
		`{"type":"tool_result","tool_id":"never_started","status":"success","output":"x"}`,
	}, "\n") + "\n"
	if err := parseGemini(strings.NewReader(stream), gate, testGeminiLive(t)); FailureKindOf(err) != FailureProtocol {
		t.Fatalf("error=%v, want a protocol failure for an unbound native result", err)
	}
}

// The gate reply and the alias identity must stay separate from the MCP prefix:
// a host tool never gets a "gemini:" id and a native tool never gets the MCP one.
func TestParseGeminiIdSpacesDoNotMix(t *testing.T) {
	var events []capturedEvent
	gate := scopedMCPGate(t, func(kind string, data map[string]any) error {
		events = append(events, capturedEvent{kind: kind, data: data})
		return nil
	})
	attachGateAliases(gate, newGateAliases())
	defer detachGateAliases(gate)
	prefix := gate.nativePrefix
	stream := strings.Join([]string{
		`{"type":"init","session_id":"` + fakeSession + `"}`,
		`{"type":"tool_use","tool_name":"` + prefix + `read_file","tool_id":"m1","parameters":{"path":"a"}}`,
		`{"type":"tool_result","tool_id":"m1","status":"success","output":"a"}`,
		`{"type":"tool_use","tool_name":"run_shell_command","tool_id":"n1","parameters":{"command":"printf hi"}}`,
		`{"type":"tool_result","tool_id":"n1","status":"success","output":"hi"}`,
		`{"type":"result","status":"success","stats":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"
	if err := parseGemini(strings.NewReader(stream), gate, testGeminiLive(t)); err != nil {
		t.Fatalf("mixed host/native stream failed: %v", err)
	}
	ids := []string{}
	for _, event := range events {
		if event.kind == "tool_started" || event.kind == "tool_finished" {
			ids = append(ids, event.data["tool_call_id"].(string))
		}
	}
	if len(ids) != 2 {
		t.Fatalf("lifecycle events=%v, want exactly the native call's pair", ids)
	}
	for _, id := range ids {
		if !strings.HasPrefix(id, nativeIDGemini) || strings.Contains(id, gate.nativePrefix) {
			t.Fatalf("id %q mixed the MCP and native identity spaces", id)
		}
	}
}

var _ = json.Marshal

// testGeminiLive is a permissive liveness monitor for parser tests.
func testGeminiLive(t *testing.T) *liveness {
	t.Helper()
	return newLiveness(t.Context(), func() {}, discardObserve, 0, 0, "warn")
}
