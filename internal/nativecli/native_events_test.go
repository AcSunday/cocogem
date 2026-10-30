package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

const nativeSession = "11111111-1111-4111-8111-111111111111"

type capturedEvent struct {
	kind string
	data map[string]any
}

func parseWithEvents(t *testing.T, native []string, stream string, parser func(io.Reader, *bridge, *liveness) error) ([]capturedEvent, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := []capturedEvent{}
	observe := func(kind string, data map[string]any) error {
		events = append(events, capturedEvent{kind: kind, data: data})
		return nil
	}
	gate, err := newBridge(ctx, nil, func(context.Context, string, json.RawMessage) (string, error) {
		return "", nil
	}, observe)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	gate.setNative(native)
	live := newLiveness(ctx, cancel, observe, time.Minute, time.Minute, "stop")
	live.dispatched()
	err = parser(strings.NewReader(stream), gate, live)
	return events, err
}

func TestClaudeNativeResultsBecomeRealEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read","WebFetch"]}`,
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{"file_path":"/workspace/README.md"}}]}}`,
		`{"type":"user","message":{"id":"msg2","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"1\tPUBLIC_FIXTURE"}]}}`,
		`{"type":"assistant","message":{"id":"msg3","content":[{"type":"tool_use","id":"tool_2","name":"WebFetch","input":{"url":"https://example.org/page"}}]}}`,
		`{"type":"user","message":{"id":"msg4","content":[{"type":"tool_result","tool_use_id":"tool_2","content":"CLI summary text","is_error":true}]}}`,
		`{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}`,
	}, "\n") + "\n"
	events, err := parseWithEvents(t, []string{"Read", "WebFetch"}, stream, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	started, finished := 0, 0
	for _, event := range events {
		if event.data["origin"] != "native" {
			continue
		}
		switch event.kind {
		case "tool_started":
			started++
			if id, _ := event.data["tool_call_id"].(string); !strings.HasPrefix(id, "cli:") {
				t.Fatalf("native call id %q does not use the cli: prefix", id)
			}
		case "tool_finished":
			finished++
		}
	}
	if started != 2 || finished != 2 {
		t.Fatalf("started=%d finished=%d events=%+v", started, finished, events)
	}
	var read, web map[string]any
	for _, event := range events {
		if event.kind != "tool_finished" {
			continue
		}
		if event.data["name"] == "Read" {
			read = event.data
		}
		if event.data["name"] == "WebFetch" {
			web = event.data
		}
	}
	if read == nil || read["status"] != "ok" || read["output"] != "1\tPUBLIC_FIXTURE" {
		t.Fatalf("Read result not preserved: %+v", read)
	}
	if web == nil || web["status"] != "error" || web["output"] != "CLI summary text" {
		t.Fatalf("native error was not surfaced verbatim: %+v", web)
	}
	if input, ok := web["input"].(json.RawMessage); !ok || !strings.Contains(string(input), "example.org") {
		t.Fatalf("actual call input was not paired with the result: %+v", web["input"])
	}
}

func TestClaudeReadKeepsCLIFileRangeMetadata(t *testing.T) {
	head := `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read"]}` + "\n" +
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{"file_path":"/workspace/big.md"}}]}}` + "\n"
	tail := `{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}` + "\n"
	result := `{"type":"user","message":{"id":"msg2","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"1\tfirst"}]},"tool_use_result":{"type":"text","file":{"filePath":"/workspace/big.md","numLines":10,"startLine":1,"totalLines":3000}}}`
	events, err := parseWithEvents(t, []string{"Read"}, head+result+"\n"+tail, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	var finished map[string]any
	for _, event := range events {
		if event.kind == "tool_finished" {
			finished = event.data
		}
	}
	raw, ok := finished["file"].(json.RawMessage)
	if !ok {
		t.Fatalf("CLI file metadata was dropped: %+v", finished)
	}
	var meta struct {
		FilePath   string `json:"filePath"`
		NumLines   int    `json:"numLines"`
		StartLine  int    `json:"startLine"`
		TotalLines int    `json:"totalLines"`
	}
	if json.Unmarshal(raw, &meta) != nil || meta.FilePath != "/workspace/big.md" || meta.NumLines != 10 || meta.StartLine != 1 || meta.TotalLines != 3000 {
		t.Fatalf("CLI file metadata changed: %s", raw)
	}
	// A message with several native results cannot attribute one file range;
	// the host must then report no completeness instead of guessing.
	twoCalls := `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read"]}` + "\n" +
		`{"type":"assistant","message":{"id":"msg2","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{"file_path":"/workspace/a.md"}},{"type":"tool_use","id":"tool_2","name":"Read","input":{"file_path":"/workspace/b.md"}}]}}` + "\n" +
		`{"type":"user","message":{"id":"msg3","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"a"},{"type":"tool_result","tool_use_id":"tool_2","content":"b"}]},"tool_use_result":{"file":{"filePath":"/workspace/a.md","numLines":1,"startLine":1,"totalLines":1}}}` + "\n" + tail
	events, err = parseWithEvents(t, []string{"Read"}, twoCalls, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.kind == "tool_finished" && event.data["file"] != nil {
			t.Fatal("ambiguous multi-result message received file metadata")
		}
	}
}

func TestClaudeNativeResultDedupAndConflicts(t *testing.T) {
	prefix := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read"]}`,
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{"file_path":"/workspace/README.md"}}]}}`,
		`{"type":"user","message":{"id":"msg2","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"same"}]}}`,
	}, "\n") + "\n"
	suffix := `{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}` + "\n"
	if _, err := parseWithEvents(t, []string{"Read"}, prefix+`{"type":"user","message":{"id":"msg3","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"same"}]}}`+"\n"+suffix, parseClaude); err != nil {
		t.Fatalf("exact result replay should be deduplicated: %v", err)
	}
	if _, err := parseWithEvents(t, []string{"Read"}, prefix+`{"type":"user","message":{"id":"msg3","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"changed"}]}}`+"\n"+suffix, parseClaude); err == nil {
		t.Fatal("a changed native result was accepted")
	}
}

func TestClaudeNativeToolBoundariesFailClosed(t *testing.T) {
	init := `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Bash"]}` + "\n"
	if _, err := parseWithEvents(t, []string{"Read"}, init, parseClaude); err == nil {
		t.Fatal("a native tool outside the granted subset was accepted at initialization")
	}
	unapproved := `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":[]}` + "\n" +
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"tool_use","id":"tool_1","name":"Bash","input":{"command":"echo"}}]}}` + "\n"
	if _, err := parseWithEvents(t, []string{"Read"}, unapproved, parseClaude); err == nil {
		t.Fatal("an unapproved native tool call was accepted")
	}
	unresolved := `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read"]}` + "\n" +
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{"file_path":"/workspace/README.md"}}]}}` + "\n" +
		`{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}` + "\n"
	if _, err := parseWithEvents(t, []string{"Read"}, unresolved, parseClaude); err == nil {
		t.Fatal("a native tool call without an actual result was treated as success")
	}
}

func claudeNoSuchTool(name string) string {
	return "<tool_use_error>Error: No such tool available: " + name + "</tool_use_error>"
}

func claudeDisabledTool(name string) string {
	return "<tool_use_error>Error: No such tool available: " + name + ". " + name + " is disabled for this session, in subagents as well as here.</tool_use_error>"
}

func claudeDeniedStream(t *testing.T, name, callID, denial string, tail string) string {
	t.Helper()
	return strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read"]}`,
		`{"type":"assistant","message":{"id":"deny-` + callID + `","content":[{"type":"tool_use","id":"` + callID + `","name":"` + name + `","input":{"command":"touch /tmp/cocogem-denied-marker"}}]}}`,
		`{"type":"user","message":{"id":"denied-result-` + callID + `","content":[{"type":"tool_result","tool_use_id":"` + callID + `","is_error":true,"content":` + mustJSON(t, denial) + `}]}}`,
		tail,
	}, "\n") + "\n"
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestClaudeDeniedToolRecoveryRequiresFixedCLIDenial(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   string
		denial string
	}{
		{"Bash", "Bash", claudeDisabledTool("Bash")},
		{"Task", "Task", claudeDisabledTool("Task")},
		{"unknown MCP", "mcp__cocogem__not_exposed_fixture", claudeNoSuchTool("mcp__cocogem__not_exposed_fixture")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tail := `{"type":"assistant","message":{"id":"recovered","content":[{"type":"text","text":"RECOVERED_WITHOUT_TOOL_EXECUTION"}]}}` + "\n" +
				`{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}`
			events, err := parseWithEvents(t, []string{"Read"}, claudeDeniedStream(t, tc.tool, "denied-1", tc.denial, tail), parseClaude)
			if err != nil {
				t.Fatalf("fixed CLI denial was not recoverable: %v", err)
			}
			var denied map[string]any
			for _, event := range events {
				switch event.kind {
				case "tool_started":
					t.Fatalf("denied tool emitted tool_started: %+v", event.data)
				case "tool_finished":
					if event.data["name"] == safeToken(tc.tool) {
						denied = event.data
					}
				}
			}
			if denied == nil {
				t.Fatalf("denial was not recorded: %+v", events)
			}
			if denied["status"] != "error" || denied["denied"] != true {
				t.Fatalf("denial status = %+v", denied)
			}
			if _, present := denied["input"]; present {
				t.Fatalf("denial event retained raw input: %+v", denied)
			}
			if output, _ := denied["output"].(string); !strings.Contains(output, safeToken(tc.tool)) || len(output) > 200 {
				t.Fatalf("denial output is not bounded and readable: %q", output)
			}
		})
	}
}

func TestClaudeAllowedToolStillWorksAfterDeniedCall(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read"]}`,
		`{"type":"assistant","message":{"id":"deny","content":[{"type":"tool_use","id":"bad","name":"Bash","input":{"command":"echo"}}]}}`,
		`{"type":"user","message":{"id":"deny-result","content":[{"type":"tool_result","tool_use_id":"bad","is_error":true,"content":` + mustJSON(t, claudeDisabledTool("Bash")) + `}]}}`,
		`{"type":"assistant","message":{"id":"read","content":[{"type":"tool_use","id":"good","name":"Read","input":{"file_path":"/workspace/README.md"}}]}}`,
		`{"type":"user","message":{"id":"read-result","content":[{"type":"tool_result","tool_use_id":"good","content":"PUBLIC"}]}}`,
		`{"type":"assistant","message":{"id":"answer","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}`,
	}, "\n") + "\n"
	events, err := parseWithEvents(t, []string{"Read"}, stream, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	var readOK, denied bool
	for _, event := range events {
		if event.kind != "tool_finished" {
			continue
		}
		if event.data["tool_call_id"] == "cli:good" && event.data["status"] == "ok" && event.data["output"] == "PUBLIC" {
			readOK = true
		}
		if event.data["tool_call_id"] == "cli:bad" && event.data["denied"] == true {
			denied = true
		}
	}
	if !readOK || !denied {
		t.Fatalf("allowed native call after denial = %+v", events)
	}
}

func TestClaudeDeniedToolRecoveryFailsClosed(t *testing.T) {
	init := `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read"]}` + "\n"
	assistant := func(messageID, callID, name string) string {
		return `{"type":"assistant","message":{"id":"` + messageID + `","content":[{"type":"tool_use","id":"` + callID + `","name":"` + name + `","input":{"command":"echo"}}]}}` + "\n"
	}
	result := func(messageID, callID string, isError bool, content string) string {
		return `{"type":"user","message":{"id":"` + messageID + `","content":[{"type":"tool_result","tool_use_id":"` + callID + `","is_error":` + fmt.Sprint(isError) + `,"content":` + mustJSON(t, content) + `}]}}` + "\n"
	}
	success := `{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}` + "\n"
	for _, tc := range []struct {
		name   string
		stream string
		want   string
	}{
		{
			name:   "non-fixed denial",
			stream: init + assistant("m1", "c1", "Bash") + result("m2", "c1", true, "Permission denied") + success,
			want:   "unrecognized result",
		},
		{
			name:   "successful denied result",
			stream: init + assistant("m1", "c1", "Bash") + result("m2", "c1", false, claudeDisabledTool("Bash")) + success,
			want:   "reported success",
		},
		{
			name:   "denial names another tool",
			stream: init + assistant("m1", "c1", "Bash") + result("m2", "c1", true, claudeNoSuchTool("Write")) + success,
			want:   "unrecognized result",
		},
		{
			name:   "missing call identity",
			stream: init + `{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"","name":"Bash","input":{}}]}}` + "\n" + success,
			want:   "without a safe identity",
		},
		{
			name:   "duplicate denied identity",
			stream: init + assistant("m1", "c1", "Bash") + assistant("m2", "c1", "Task") + success,
			want:   "reused a denied tool-call identity",
		},
		{
			name:   "allowed tool reuses denied identity",
			stream: init + assistant("m1", "c1", "Bash") + assistant("m2", "c1", "Read") + success,
			want:   "reused a denied tool-call identity",
		},
		{
			name:   "denied call without result",
			stream: init + assistant("m1", "c1", "Bash") + success,
			want:   "without the fixed denial result",
		},
		{
			name: "more than three denials",
			stream: init + assistant("m1", "c1", "Bash") + result("r1", "c1", true, claudeDisabledTool("Bash")) +
				assistant("m2", "c2", "Task") + result("r2", "c2", true, claudeDisabledTool("Task")) +
				assistant("m3", "c3", "Glob") + result("r3", "c3", true, claudeDisabledTool("Glob")) +
				assistant("m4", "c4", "Grep") + result("r4", "c4", true, claudeDisabledTool("Grep")) + success,
			want: "more than 3 denied tools",
		},
		{
			name:   "replayed denial result",
			stream: init + assistant("m1", "c1", "Bash") + result("m2", "c1", true, claudeDisabledTool("Bash")) + result("m3", "c1", true, claudeDisabledTool("Bash")) + success,
			want:   "replayed a denied tool result",
		},
		{
			name:   "unsupported denial content",
			stream: init + assistant("m1", "c1", "Bash") + `{"type":"user","message":{"id":"m2","content":[{"type":"tool_result","tool_use_id":"c1","is_error":true,"content":{"type":"image"}}]}}` + "\n" + success,
			want:   "unrecognized result",
		},
		{
			name:   "unsafe tool name",
			stream: init + assistant("m1", "c1", "Bash/../x") + success,
			want:   "without a safe identity",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseWithEvents(t, []string{"Read"}, tc.stream, parseClaude); err == nil {
				t.Fatalf("unsafe stream was accepted: %s", tc.name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	unknown := `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Bash"]}` + "\n"
	if _, err := parseWithEvents(t, []string{"Read"}, unknown, parseClaude); err == nil || !strings.Contains(err.Error(), "bash") {
		t.Fatalf("unknown init inventory error lost the safe tool name: %v", err)
	}
	// An unresolved denied call must still name the safe tool, not only the
	// protocol call identity, so the user and logs can diagnose the refusal.
	unresolvedDenied := init + assistant("m1", "c1", "Bash") + success
	if _, err := parseWithEvents(t, []string{"Read"}, unresolvedDenied, parseClaude); err == nil || !strings.Contains(err.Error(), "tool bash") {
		t.Fatalf("unresolved denied call lost the safe tool name: %v", err)
	}
}

func TestClaudeNativeObservationIsBounded(t *testing.T) {
	large, _ := json.Marshal(strings.Repeat("x", nativeObservationLimit+10))
	stream := `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":["Read"]}` + "\n" +
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{"file_path":"/workspace/README.md"}}]}}` + "\n" +
		`{"type":"user","message":{"id":"msg2","content":[{"type":"tool_result","tool_use_id":"tool_1","content":` + string(large) + `}]}}` + "\n" +
		`{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}` + "\n"
	events, err := parseWithEvents(t, []string{"Read"}, stream, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	var finished map[string]any
	for _, event := range events {
		if event.kind == "tool_finished" {
			finished = event.data
		}
	}
	output, _ := finished["output"].(string)
	if len(output) > nativeObservationLimit || finished["truncated"] != true {
		t.Fatalf("observation was not bounded: bytes=%d truncated=%v", len(output), finished["truncated"])
	}
}

func TestCodexWebSearchResultsAreCapturedAsActualEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"` + nativeSession + `"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"exec-1","type":"web_search","action":{"type":"other"}}}`,
		`{"type":"item.completed","item":{"id":"exec-1","type":"web_search","query":"DeBank API","action":{"type":"search","query":"DeBank API"},"results":[{"type":"text_result","title":"Docs","url":"https://docs.example/","snippet":"actual fixture"}]}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n") + "\n"
	events, err := parseWithEvents(t, []string{nativeCodexWeb}, stream, parseCodex)
	if err != nil {
		t.Fatal(err)
	}
	var finished map[string]any
	started := false
	for _, event := range events {
		switch event.kind {
		case "tool_started":
			started = true
		case "tool_finished":
			finished = event.data
		}
	}
	if !started || finished == nil {
		t.Fatalf("Codex web events missing: %+v", events)
	}
	if finished["origin"] != "native" || finished["name"] != nativeWebSearch || finished["status"] != "ok" {
		t.Fatalf("unexpected Codex web result: %+v", finished)
	}
	output, _ := finished["output"].(string)
	if !strings.Contains(output, "actual fixture") {
		t.Fatalf("actual Codex web result was not preserved: %q", output)
	}
	if input := string(finished["input"].(json.RawMessage)); !strings.Contains(input, "DeBank API") {
		t.Fatalf("actual Codex web input was not paired: %s", input)
	}
	if _, err := parseWithEvents(t, nil, stream, parseCodex); err == nil {
		t.Fatal("Codex web search was accepted without the web grant")
	}
}

func TestCodexWebSearchCompletionRequiredAndReplayCannotReopen(t *testing.T) {
	started := `{"type":"item.started","item":{"id":"web-1","type":"web_search","action":{"type":"search","query":"fixture"}}}`
	completed := `{"type":"item.completed","item":{"id":"web-1","type":"web_search","action":{"type":"other"}}}`
	prefix := strings.Join([]string{
		`{"type":"thread.started","thread_id":"` + nativeSession + `"}`,
		`{"type":"turn.started"}`,
		started,
	}, "\n") + "\n"
	terminal := `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"
	if _, err := parseWithEvents(t, []string{nativeCodexWeb}, prefix+terminal, parseCodex); err == nil {
		t.Fatal("Codex web call without item.completed was accepted as a successful turn")
	}
	// A completed-only result is valid, and replaying its started frame must
	// not leave the finished call pending again.
	if _, err := parseWithEvents(t, []string{nativeCodexWeb}, prefix+completed+"\n"+started+"\n"+terminal, parseCodex); err != nil {
		t.Fatalf("completed Codex web call was reopened by an exact replay: %v", err)
	}
}

func TestGeminiNativeWebStaysUnavailableWithoutFaithfulResults(t *testing.T) {
	stream := fmt.Sprintf(`{"type":"init","session_id":%q}`+"\n"+`{"type":"tool_use","tool_name":"google_web_search","tool_id":"g1","parameters":{"query":"fixture"}}`+"\n"+`{"type":"tool_result","tool_id":"g1","status":"success","output":"Content processed from prompt."}`+"\n"+`{"type":"result","status":"success"}`+"\n", nativeSession)
	if _, err := parseWithEvents(t, nil, stream, parseGemini); err == nil {
		t.Fatal("Gemini native web was accepted even though its tool result does not expose the actual content")
	}
}

func TestCodexNativeWebLaunchFlags(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gate, err := newBridge(ctx, nil, func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	launch := &connectionPlan{codexResolved: "fixture-model", codexArgs: []string{"-c", `model_providers.cocogem_connection.name="fixture"`}, codexEnv: map[string]string{}}
	request := model.Request{Executor: "codex", Model: "fixture-model", NativeTools: []string{nativeCodexWeb}}
	workdir := t.TempDir()
	args, _, err := prepareCodex(request, workdir, gate, false, launch)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{`web_search="live"`, "model_providers.cocogem_connection.supports_standalone_web_search=true"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Codex native web args missing %q: %s", want, joined)
		}
	}
	catalog, err := os.ReadFile(filepath.Join(workdir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(catalog), "only the scoped cocogem MCP tools") || !strings.Contains(string(catalog), "Use only the tools supplied for this step") {
		t.Fatalf("formal Codex catalog contradicts the supplied native toolset: %s", catalog)
	}
	request.NativeTools = nil
	args, _, err = prepareCodex(request, workdir, gate, false, launch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, "\n"), `web_search="disabled"`) {
		t.Fatal("Codex web search was not disabled without the grant")
	}
}
