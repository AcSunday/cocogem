package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

const nativeEvidence = "Public fixture: source custody must survive an agent handoff."
const nativeCapturedEvidence = "1: " + nativeEvidence + "\n"

// nativeHandoffHeader matches the bounded-preview contract in buildRequest.
const nativeHandoffHeader = "Exact upstream handoff (messages, artifact versions, findings and source IDs; bodies are bounded previews, use read_context with the exact id/version for more):\n"

// The PATH shim runs this test binary, never an installed CLI or a model service.
func TestNativeHTTPHelperProcess(t *testing.T) {
	if os.Getenv("COCOGEM_NATIVE_HTTP_HELPER") != "1" {
		return
	}
	if err := runNativeHTTPFixture(); err != nil {
		_ = os.WriteFile(os.Getenv("COCOGEM_NATIVE_HTTP_LEDGER")+".error", []byte(err.Error()), 0600)
		fmt.Fprintln(os.Stderr, "native HTTP fixture:", err)
		os.Exit(31)
	}
	os.Exit(0)
}

func runNativeHTTPFixture() error {
	if os.Args[len(os.Args)-1] == "--version" {
		fmt.Println("2.1.301 (Claude Code)")
		return nil
	}
	if os.Args[len(os.Args)-1] == "--help" {
		fmt.Println("--restricted --tools --strict-mcp-config --setting-sources --permission-prompts --no-session-persistence --settings --disable-slash-commands")
		return nil
	}
	argument := func(flag string) string {
		for i, value := range os.Args {
			if value == flag && i+1 < len(os.Args) {
				return os.Args[i+1]
			}
		}
		return "missing"
	}
	probeInvocation := argument("--model") == "cocogem-probe"
	for _, name := range strings.Split(argument("--tools"), ",") {
		if name == "" {
			continue
		}
		allowed := map[string]bool{"Read": true, "Glob": true, "Grep": true, "WebSearch": true, "WebFetch": true}
		if !probeInvocation {
			allowed["Write"], allowed["Edit"], allowed["Bash"] = true, true, true
		}
		if !allowed[name] {
			return fmt.Errorf("native tool outside the supported subset: %s", name)
		}
	}
	allowedTools := argument("--allowedTools")
	if !strings.HasSuffix(allowedTools, "mcp__cocogem__*") || argument("--setting-sources") != "" || (argument("--permission-mode") != "dontAsk" && argument("--permission-mode") != "manual") || !strings.Contains(strings.Join(os.Args, " "), "--no-session-persistence") {
		return errors.New("native tool or session isolation flags missing")
	}
	configPath := argument("--mcp-config")
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return err
	}
	configDir, err := filepath.EvalSymlinks(filepath.Dir(configPath))
	workdir := filepath.Dir(configDir)
	expectedCwd, _ := filepath.EvalSymlinks(filepath.Join(workdir, "cwd"))
	hasFileTools := false
	for _, name := range strings.Split(argument("--tools"), ",") {
		if name == "Read" || name == "Glob" || name == "Grep" {
			hasFileTools = true
		}
	}
	if err != nil || configDir == cwd || filepath.Base(workdir) != ".cocogem" || !strings.HasPrefix(filepath.Base(filepath.Dir(workdir)), "cocogem-native-") || (cwd != expectedCwd && !hasFileTools && !probeInvocation) {
		return errors.New("native session did not receive an isolated working directory")
	}
	var config struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	encoded, err := os.ReadFile(configPath)
	if err != nil || json.Unmarshal(encoded, &config) != nil || len(config.Servers) != 1 {
		return errors.New("scoped MCP config missing")
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	probe := strings.Contains(string(prompt), "Synthetic isolation probe")
	role := ""
	if !probe {
		if start := strings.Index(string(prompt), "Current role="); start >= 0 {
			rest := string(prompt)[start+len("Current role="):]
			role = strings.TrimSpace(strings.SplitN(rest, " ", 2)[0])
		}
		if role == "" {
			return errors.New("unexpected invocation outside the addressed member step: " + string(prompt[:min(200, len(prompt))]))
		}
		ledger, err := os.OpenFile(os.Getenv("COCOGEM_NATIVE_HTTP_LEDGER"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(ledger, role+" "+workdir)
		if closeErr := ledger.Close(); err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	endpoint := config.Servers["cocogem"]
	client := &http.Client{Timeout: 5 * time.Second}
	rpc := func(method string, params any, target any) error {
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			return err
		}
		req, err := http.NewRequest(http.MethodPost, endpoint.URL, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", endpoint.Headers["Authorization"])
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var reply struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&reply) != nil || len(reply.Error) != 0 {
			return errors.New("MCP request failed")
		}
		return json.Unmarshal(reply.Result, target)
	}
	var initialized map[string]any
	if err := rpc("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "http-integration-fixture", "version": "1"}}, &initialized); err != nil {
		return err
	}
	var listed struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := rpc("tools/list", map[string]any{}, &listed); err != nil {
		return err
	}
	names := []string{}
	for _, tool := range listed.Tools {
		names = append(names, "mcp__cocogem__"+tool.Name)
	}
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": w.NewID(), "tools": names}); err != nil {
		return err
	}
	callTool := func(name string, args any, target any) error {
		var result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		}
		if err := rpc("tools/call", map[string]any{"name": name, "arguments": args}, &result); err != nil {
			return err
		}
		if result.IsError || len(result.Content) != 1 {
			return fmt.Errorf("scoped tool %s failed", name)
		}
		return json.Unmarshal([]byte(result.Content[0].Text), target)
	}
	if probe {
		var result map[string]any
		if err := callTool("probe", map[string]any{}, &result); err != nil {
			return err
		}
		if err := encoder.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "isolation probe complete"}}}}); err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false})
	}
	var source w.Source
	if err := callTool("read_file", map[string]any{"path": "evidence.md"}, &source); err != nil {
		return err
	}
	if source.ID == "" || source.Content != nativeCapturedEvidence {
		return errors.New("captured evidence did not survive the real tool boundary")
	}
	// The addressed member publishes one note that cites the captured source.
	payload := map[string]any{"type": "note", "title": "Native CLI fixture note", "body": "The captured evidence and its limits are preserved.", "findings": []any{}, "source_ids": []string{source.ID}, "limitations": []string{"Process fixture only; no real model quality evidence."}}
	var publication map[string]any
	if err := callTool("publish_artifact", payload, &publication); err != nil {
		return err
	}
	if publication["publication"] != "after_step_success" {
		return errors.New("artifact was not staged for terminal publication")
	}
	if err := encoder.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": role + " inspected the public evidence; uncertainty is preserved."}}}}); err != nil {
		return err
	}
	return encoder.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false})
}

// TestHostToolBoundaryPreservesSourceCustody covers the source-custody contract
// through the real HTTP/MCP host tool boundary. The installed-CLI boundary is
// exercised separately by `make probe-cli`; this test never impersonates a
// provider or a CLI.
func TestHostToolBoundaryPreservesSourceCustody(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.md"), []byte(nativeEvidence), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine := &workflow.Engine{Store: db, Model: &sourceCustodyModel{}, Runners: map[string]agent.Runner{"claude": custodyRunner{}}, AllowedRoots: []string{dir}, AllowedModels: []string{"default"}, Web: tools.NewWeb("")}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	server := httptest.NewUnstartedServer(nil)
	app, err := New(Config{Host: server.Listener.Addr().String(), AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, Roots: []string{dir}, ModelConfigured: true, Model: &sourceCustodyModel{}, Executors: []ExecutorInfo{{ID: "claude", Installed: true, Supported: true}}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = app.Handler()
	server.Start()
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar, client.Timeout = jar, 10*time.Second
	csrf := ""
	call := func(method, path string, input any, headers map[string]string, status int, target any) {
		t.Helper()
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatalf("%s %s: status=%d body=%s err=%v", method, path, resp.StatusCode, raw, err)
		}
		if target != nil && json.Unmarshal(raw, target) != nil {
			t.Fatalf("invalid API response: %s", raw)
		}
	}
	var session SessionReply
	call("POST", "/auth/setup", PasswordInput{Password: "native-test-password"}, nil, 200, &session)
	csrf = session.CSRFToken
	var ws w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "Public research fixture", Root: dir}, nil, 200, &ws)
	var members Page[w.Agent]
	call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	if len(members.Items) != 3 {
		t.Fatalf("members=%+v", members.Items)
	}
	ids := []string{}
	for _, member := range members.Items {
		if member.Executor != "claude" {
			t.Fatalf("wrong native executor: %+v", member)
		}
		ids = append(ids, member.ID)
	}
	var thread w.Thread
	call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "Trace host tool custody", AgentIDs: ids, CoordinatorID: ids[2]}, nil, 200, &thread)
	input := w.RunInput{Message: "Trace custody of the supplied public fixture", ResearchScope: "given_sources"}
	var run w.Run
	call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "native-cli-http-research"}, 202, &run)
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		wake := db.Changed()
		var view RunView
		call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
		if w.Terminal(view.Run.Status) {
			if view.Run.Status != "succeeded" || len(view.Steps) != 1 {
				t.Fatalf("run=%+v steps=%+v", view.Run, view.Steps)
			}
			break
		}
		select {
		case <-wake:
		case <-deadline.C:
			t.Fatal("host tool run did not settle")
		}
	}
	var artifacts Page[w.Artifact]
	call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, &artifacts)
	if len(artifacts.Items) != 1 || artifacts.Items[0].Type != "note" || len(artifacts.Items[0].SourceIDs) != 1 {
		t.Fatalf("published artifacts=%+v", artifacts.Items)
	}
	var captured w.Source
	call("GET", "/api/v1/sources/"+artifacts.Items[0].SourceIDs[0], nil, nil, 200, &captured)
	if captured.Kind != "file" || captured.Content != nativeCapturedEvidence || captured.SHA256 == "" {
		t.Fatalf("captured source=%+v", captured)
	}
}

// sourceCustodyModel reads the bound evidence through the host tool boundary and
// publishes one note that cites the captured source.
type sourceCustodyModel struct {
	step  int
	calls int
}

func (m *sourceCustodyModel) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	tool := func(name string, input any) model.Response {
		raw, _ := json.Marshal(input)
		m.calls++
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: fmt.Sprintf("custody-%s-%d", name, m.calls), Name: name, Input: raw}}}
	}
	sourceID := ""
	for i := len(request.Messages) - 1; i >= 0 && sourceID == ""; i-- {
		for j := len(request.Messages[i].Content) - 1; j >= 0; j-- {
			block := request.Messages[i].Content[j]
			if block.Type != "tool_result" {
				continue
			}
			var payload struct {
				SourceID string `json:"source_id"`
			}
			if json.Unmarshal([]byte(block.Content), &payload) == nil && payload.SourceID != "" {
				sourceID = payload.SourceID
				break
			}
		}
	}
	if m.step == 0 {
		m.step++
		return tool("read_file", map[string]any{"path": "evidence.md"}), nil
	}
	if m.step == 1 {
		m.step++
		payload := map[string]any{"type": "note", "title": "Host custody note", "body": "The captured evidence and its limits are preserved.", "findings": []any{}, "source_ids": []string{sourceID}, "limitations": []string{"Fixture only."}}
		return tool("publish_artifact", payload), nil
	}
	_ = ctx
	_ = emit
	return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: "captured evidence preserved"}}}, nil
}

// custodyRunner runs the scripted custody model through the host tool loop.
type custodyRunner struct{}

func (custodyRunner) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	return (agent.Loop{Model: &sourceCustodyModel{}}).Run(ctx, request, execute, observe)
}
