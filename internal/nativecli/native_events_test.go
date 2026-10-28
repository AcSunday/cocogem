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
