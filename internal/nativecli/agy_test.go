package nativecli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

func TestAgyCapabilitiesCheckAndRejection(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "agy")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "1.5.18"; exit 0; fi
echo "--input-format --output-format --settings --add-dir --disable-slash-commands --conversation --effort --model"
`
	if err := os.WriteFile(good, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	info, err := check(t.Context(), "agy", func(_ string, args ...string) *exec.Cmd {
		return exec.Command(good, args...)
	})
	if err != nil {
		t.Fatalf("check(agy) error = %v (%+v)", err, info)
	}
	if !info.Supported || info.Adapter != "agy-stream-json" {
		t.Fatalf("check(agy) = %+v", info)
	}

	missingFlag := filepath.Join(dir, "agy-bad")
	if err := os.WriteFile(missingFlag, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 1.5.18; else echo --settings; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if badInfo, err := check(t.Context(), "agy", func(_ string, args ...string) *exec.Cmd {
		return exec.Command(missingFlag, args...)
	}); err == nil || badInfo.Supported {
		t.Fatalf("missing flag should fail capability check: %+v %v", badInfo, err)
	}
}

func TestAgyStepSettingsAndPrepareAgy(t *testing.T) {
	settings := agyStepSettings(map[string]any{
		"agentMode":             "sandbox",
		"enableTerminalSandbox": true,
		"toolPermission":        "ask",
	}, "http://127.0.0.1:9999")
	if settings["agentMode"] != "" {
		t.Fatalf("agentMode not cleared: %v", settings["agentMode"])
	}
	if settings["enableTerminalSandbox"] != false {
		t.Fatalf("enableTerminalSandbox not disabled: %v", settings["enableTerminalSandbox"])
	}
	if settings["toolPermission"] != "always-proceed" {
		t.Fatalf("toolPermission = %v", settings["toolPermission"])
	}
	if settings["modelProvider"] != "gemini" || settings["model"] != "cocogem-probe" {
		t.Fatalf("probe settings invalid: %+v", settings)
	}

	gate, err := newBridge(t.Context(), nil, func(context.Context, string, json.RawMessage) (string, error) {
		return "{}", nil
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	broker, err := newGateBroker(nil, nil, t.TempDir(), t.TempDir(), "agy")
	if err != nil {
		t.Fatal(err)
	}
	defer broker.close()

	configDir := t.TempDir()
	req := model.Request{
		SessionMode:   "resume",
		SessionID:     "11111111-1111-4111-8111-111111111111",
		ThinkingLevel: "xhigh",
	}
	args, err := prepareAgy(req, configDir, gate, broker, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--input-format stream-json", "--output-format stream-json", "--disable-slash-commands", "--add-dir " + configDir, "--conversation 11111111-1111-4111-8111-111111111111", "--effort xhigh"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
	mcpData, err := os.ReadFile(filepath.Join(configDir, ".agents", "mcp_config.json"))
	if err != nil || !strings.Contains(string(mcpData), `"serverUrl"`) || !strings.Contains(string(mcpData), gate.url()) {
		t.Fatalf("mcp_config.json invalid: err=%v body=%s", err, mcpData)
	}
	hooksData, err := os.ReadFile(filepath.Join(configDir, ".agents", "hooks.json"))
	if err != nil || !strings.Contains(string(hooksData), "PreToolUse") || !strings.Contains(string(hooksData), "PostToolUse") {
		t.Fatalf("hooks.json invalid: err=%v body=%s", err, hooksData)
	}
}

func TestParseAgyStreamAndStartupFailure(t *testing.T) {
	var events []string
	var texts strings.Builder
	var usageSeen map[string]any
	gate, err := newBridge(t.Context(), nil, func(context.Context, string, json.RawMessage) (string, error) {
		return "{}", nil
	}, func(kind string, data map[string]any) error {
		events = append(events, kind)
		switch kind {
		case "text":
			texts.WriteString(data["text"].(string))
		case "usage":
			usageSeen = data
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	live := newLiveness(t.Context(), func() {}, func(string, map[string]any) error { return nil }, 0, 0, "")
	stream := strings.Join([]string{
		`{"event":"init","conversation_id":"11111111-1111-4111-8111-111111111111","init":{}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"cocogem","ToolName":"probe","Arguments":{}}}}}`,
		`{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"README.md"}}}}`,
		`{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"read_file","tool_info":{"name":"read_file","parameters":{"Path":"README.md"},"output":"file body"}}}`,
		`{"event":"step_update","step_update":{"step_index":3,"state":"ACTIVE","step_type":"agent_response","text_delta":"hello "}}`,
		`{"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"agent_response","text_delta":"world"}}`,
		`{"event":"result","result":{"conversation_id":"11111111-1111-4111-8111-111111111111","status":"SUCCESS","response":"hello world","usage":{"input_tokens":10,"output_tokens":5,"cache_read_tokens":3,"total_tokens":18}}}`,
	}, "\n") + "\n"
	if err := parseAgy(strings.NewReader(stream), gate, live); err != nil {
		t.Fatalf("parseAgy error: %v", err)
	}
	if err := gate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	if texts.String() != "hello world" {
		t.Fatalf("streamed text = %q want %q", texts.String(), "hello world")
	}
	if usageSeen["input_tokens"] != 10 || usageSeen["output_tokens"] != 5 || usageSeen["cache_read_tokens"] != 3 {
		t.Fatalf("usage mismatch: %+v", usageSeen)
	}
	// call_mcp_tool must NOT emit duplicate native tool_started/tool_finished; read_file emits once.
	startedCount := 0
	for _, ev := range events {
		if ev == "tool_started" {
			startedCount++
		}
	}
	if startedCount != 1 {
		t.Fatalf("tool_started count = %d want 1 (%v)", startedCount, events)
	}

	protoErr := newFailure(FailureProtocol, "native CLI ended without a confirmed terminal result")
	rateLimitErr := agyStartupFailure(protoErr, `AGY_ERROR: {"status":"RESOURCE_EXHAUSTED","error_code":429,"message":"quota reached"}`, 0)
	if f, ok := rateLimitErr.(*Failure); !ok || f.Kind != FailureQuota {
		t.Fatalf("429 AGY_ERROR classified as %v", rateLimitErr)
	}
	authErr := agyStartupFailure(protoErr, `AGY_ERROR: {"status":"UNAUTHENTICATED","error_code":401,"message":"bad key"}`, 0)
	if f, ok := authErr.(*Failure); !ok || f.Kind != FailureAuthentication {
		t.Fatalf("401 AGY_ERROR classified as %v", authErr)
	}
}

func TestParseAgyThinkingDoesNotLeakIntoVisibleText(t *testing.T) {
	var thinking, texts strings.Builder
	gate, err := newBridge(t.Context(), nil, func(context.Context, string, json.RawMessage) (string, error) {
		return "{}", nil
	}, func(kind string, data map[string]any) error {
		switch kind {
		case "thinking":
			thinking.WriteString(data["text"].(string))
		case "text":
			texts.WriteString(data["text"].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	live := newLiveness(t.Context(), func() {}, func(string, map[string]any) error { return nil }, 0, 0, "")
	stream := strings.Join([]string{
		`{"event":"init","conversation_id":"11111111-1111-4111-8111-111111111111","init":{}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"thinking","text_delta":"check files first"}}`,
		`{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"agent_response","thinking_delta":" and verify","text_delta":"Final answer."}}`,
		`{"event":"result","result":{"conversation_id":"11111111-1111-4111-8111-111111111111","status":"SUCCESS","response":"Final answer."}}`,
	}, "\n") + "\n"
	if err := parseAgy(strings.NewReader(stream), gate, live); err != nil {
		t.Fatalf("parseAgy error: %v", err)
	}
	if thinking.String() != "check files first and verify" {
		t.Fatalf("thinking = %q, want %q", thinking.String(), "check files first and verify")
	}
	if texts.String() != "Final answer." {
		t.Fatalf("visible text = %q, want %q", texts.String(), "Final answer.")
	}
}

func TestAgyGatePolicyEnforcesMCPAndNativeBoundaries(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	allowedFile := filepath.Join(root, "note.txt")
	outsideFile := filepath.Join(outside, "secret.txt")
	schemaDir := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "mcp", "cocogem")
	if err := os.MkdirAll(schemaDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowedFile, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}

	broker, err := newGateBroker(nil, nil, t.TempDir(), root, "agy")
	if err != nil {
		t.Fatal(err)
	}
	defer broker.close()
	broker.agyPolicy = newAgyGatePolicy([]string{nativeRead}, []string{nativeWebSearch}, []string{root}, schemaDir, true, false)

	callAuthorize := func(name string, args any) map[string]any {
		rawArgs, _ := json.Marshal(args)
		payload, _ := json.Marshal(map[string]any{
			"hookEventName":  "PreToolUse",
			"conversationId": "11111111-1111-4111-8111-111111111111",
			"cwd":            root,
			"stepIdx":        1,
			"toolCall": map[string]any{
				"id":   "call-1",
				"name": name,
				"args": json.RawMessage(rawArgs),
			},
		})
		req := httptest.NewRequest(http.MethodPost, broker.url()+"/gate/agy/authorize", bytes.NewReader(payload))
		req.Host = broker.listener.Addr().String()
		req.Header.Set("Authorization", "Bearer "+broker.token)
		rw := httptest.NewRecorder()
		broker.serveAgyAuthorize(rw, req)
		var out map[string]any
		if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if res := callAuthorize("call_mcp_tool", map[string]any{"ServerName": "cocogem", "ToolName": "read_source"}); res["decision"] != "allow" {
		t.Fatalf("cocogem MCP denied: %+v", res)
	}
	if res := callAuthorize("call_mcp_tool", map[string]any{"ServerName": "rogue", "ToolName": "exec"}); res["decision"] != "deny" {
		t.Fatalf("external MCP allowed: %+v", res)
	}
	if res := callAuthorize("run_command", map[string]any{"CommandLine": "ls"}); res["decision"] != "deny" {
		t.Fatalf("ungranted run_command allowed: %+v", res)
	}
	if res := callAuthorize("read_file", map[string]any{"Path": filepath.Join(schemaDir, "probe.json")}); res["decision"] != "allow" {
		t.Fatalf("cocogem MCP schema doc read denied: %+v", res)
	}
	if res := callAuthorize("read_file", map[string]any{"Path": allowedFile}); res["decision"] != "allow" {
		t.Fatalf("workspace read denied: %+v", res)
	}
	if res := callAuthorize("read_file", map[string]any{"Path": outsideFile}); res["decision"] != "deny" {
		t.Fatalf("outside read allowed: %+v", res)
	}
	if res := callAuthorize("search_web", map[string]any{"query": "go proxy"}); res["decision"] != "allow" {
		t.Fatalf("granted web search denied: %+v", res)
	}
}

func TestAgyFullAccessAndRunFixture(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agy")
	body := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "1.5.18"; exit 0; fi
if [ "$1" = "--help" ]; then echo "--input-format --output-format --settings --add-dir --disable-slash-commands --conversation --effort --model"; exit 0; fi
cat > /dev/null
printf '%s\n' '{"event":"init","conversation_id":"22222222-2222-4222-8222-222222222222","init":{}}'
printf '%s\n' '{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"agy fixture ok"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"22222222-2222-4222-8222-222222222222","status":"SUCCESS","response":"agy fixture ok","usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}}'
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	var gotText strings.Builder
	p := Provider{command: func(_ string, args ...string) *exec.Cmd { return exec.Command(script, args...) }}
	req := model.Request{
		Executor: "agy",
		Model:    "default",
		System:   "fixture prompt",
		Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "hello"}}}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := p.run(ctx, req, func(context.Context, string, json.RawMessage) (string, error) {
		return "{}", nil
	}, func(kind string, data map[string]any) error {
		if kind == "text" {
			gotText.WriteString(data["text"].(string))
		}
		return nil
	}, runOptions{
		skipCheck: true,
		adapter:   agent.AdapterInfo{ID: "agy", Version: "1.5.18", Supported: true, Adapter: "agy-stream-json"},
	}); err != nil {
		t.Fatalf("p.run(agy) error: %v", err)
	}
	if gotText.String() != "agy fixture ok" {
		t.Fatalf("text = %q want agy fixture ok", gotText.String())
	}
}
