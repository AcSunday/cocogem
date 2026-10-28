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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
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
	for _, name := range strings.Split(argument("--tools"), ",") {
		if name != "" && !map[string]bool{"Read": true, "Glob": true, "Grep": true, "WebSearch": true, "WebFetch": true}[name] {
			return errors.New("native tool outside the read-only subset")
		}
	}
	allowedTools := argument("--allowedTools")
	if !strings.HasSuffix(allowedTools, "mcp__cocogem__*") || argument("--setting-sources") != "" || argument("--permission-mode") != "dontAsk" || !strings.Contains(strings.Join(os.Args, " "), "--no-session-persistence") {
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
	if err != nil || configDir == cwd || filepath.Base(workdir) != ".cocogem" || !strings.HasPrefix(filepath.Base(filepath.Dir(workdir)), "cocogem-native-") || (cwd != expectedCwd && !hasFileTools) {
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
		for _, candidate := range []string{"researcher", "critic", "coordinator"} {
			if strings.Contains(string(prompt), "Current role="+candidate+" ") {
				role = candidate
			}
		}
		if role == "" {
			return errors.New("unexpected invocation outside the research plan")
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
	if role == "researcher" {
		if err := callTool("read_file", map[string]any{"path": "evidence.md"}, &source); err != nil {
			return err
		}
	} else {
		_, rawMessages, ok := strings.Cut(string(prompt), "User messages and upstream evidence:\n")
		if !ok {
			return errors.New("structured prompt missing")
		}
		var messages []model.Message
		if json.Unmarshal([]byte(rawMessages), &messages) != nil || len(messages) != 1 || len(messages[0].Content) != 1 {
			return errors.New("invalid prompt messages")
		}
		_, rawHandoff, ok := strings.Cut(messages[0].Content[0].Text, nativeHandoffHeader)
		var handoff workflow.Handoff
		if !ok || json.NewDecoder(strings.NewReader(rawHandoff)).Decode(&handoff) != nil || len(handoff.SourceIDs) == 0 {
			return errors.New("exact upstream handoff missing")
		}
		if len(handoff.Artifacts) != 1 || handoff.Artifacts[0].Version != 1 || handoff.Artifacts[0].Body != "artifact-only research conclusion" || len(handoff.Artifacts[0].Findings) != 1 {
			return errors.New("structured upstream finding was not handed off")
		}
		wantMessages := 1
		if role == "coordinator" {
			wantMessages = 2
		}
		if len(handoff.Messages) != wantMessages || handoff.Messages[0].ID == "" {
			return errors.New("upstream committed message identities missing")
		}
		if len(handoff.MessageRefs) != len(handoff.Messages) || len(handoff.ArtifactRefs) != len(handoff.Artifacts) {
			return errors.New("handoff exact references are missing")
		}
		for _, message := range handoff.Messages {
			if len(message.Content) > 7000 || handoff.Digests["message:"+message.ID] == "" {
				return errors.New("handoff message preview or digest is invalid")
			}
		}
		ref := handoff.ArtifactRefs[0]
		if handoff.Digests[fmt.Sprintf("artifact:%s@%d", ref.ID, ref.Version)] == "" || handoff.Digests["source:"+handoff.SourceIDs[0]] == "" {
			return errors.New("handoff artifact or source digest is missing")
		}
		type contextPage struct {
			Kind          string   `json:"kind"`
			ID            string   `json:"id"`
			Version       int      `json:"version"`
			Content       string   `json:"content"`
			TotalBytes    int      `json:"total_bytes"`
			HasMore       bool     `json:"has_more"`
			NextCursor    string   `json:"next_cursor"`
			Committed     bool     `json:"committed"`
			ContentFormat string   `json:"content_format"`
			SourceIDs     []string `json:"source_ids"`
		}
		// Full bodies are only reachable through bounded read_context pages.
		readContext := func(kind, id string, version int) (string, contextPage, error) {
			var last contextPage
			body := strings.Builder{}
			cursor := ""
			for page := 0; page < 64; page++ {
				arguments := map[string]any{"kind": kind, "id": id}
				if version > 0 {
					arguments["version"] = version
				}
				if cursor != "" {
					arguments["cursor"] = cursor
				}
				var result contextPage
				if err := callTool("read_context", arguments, &result); err != nil {
					return "", result, err
				}
				if !result.Committed || result.ID != id || (kind == "artifact" && result.Version != version) || (kind == "message" && result.ContentFormat != "chunks_v1") {
					return "", result, errors.New("read_context returned an uncommitted or mismatched version")
				}
				body.WriteString(result.Content)
				last = result
				if !result.HasMore {
					break
				}
				if result.NextCursor == "" {
					return "", result, errors.New("read_context page has no cursor")
				}
				cursor = result.NextCursor
			}
			if last.HasMore || body.Len() != last.TotalBytes {
				return "", last, errors.New("read_context did not return the complete exact body")
			}
			return body.String(), last, nil
		}
		for _, message := range handoff.Messages {
			content, _, err := readContext("message", message.ID, 0)
			if err != nil || !strings.Contains(content, "inspected the public evidence") {
				return errors.New("committed upstream message was not readable through read_context")
			}
		}
		artifactBody, artifactPage, err := readContext("artifact", ref.ID, ref.Version)
		if err != nil || artifactBody != "artifact-only research conclusion" || !slices.Contains(artifactPage.SourceIDs, handoff.SourceIDs[0]) {
			return errors.New("exact artifact version or its source attribution was not readable through read_context")
		}
		if err := callTool("read_source", map[string]any{"source_id": handoff.SourceIDs[0]}, &source); err != nil {
			return err
		}
		if source.SHA256 == "" || source.Path != "evidence.md" || source.StartLine != 1 {
			return errors.New("captured source metadata was lost")
		}
	}
	if source.ID == "" || source.Content != nativeCapturedEvidence {
		return errors.New("captured evidence did not survive the real tool boundary")
	}
	if role != "critic" {
		kind, body := "note", "artifact-only research conclusion"
		if role == "coordinator" {
			kind, body = "research_report", "# Research report\n\nThe critique preserves the original source and its limits."
		}
		payload := map[string]any{"type": kind, "title": "Native CLI fixture report", "body": body, "findings": []any{map[string]any{"title": "Preserved source custody", "detail": "Public fixture evidence survived independent review.", "status": "supported", "source_ids": []string{source.ID}}}, "source_ids": []string{source.ID}, "limitations": []string{"Process fixture only; no real model quality evidence."}}
		var publication map[string]any
		if err := callTool("publish_artifact", payload, &publication); err != nil {
			return err
		}
		if publication["publication"] != "after_step_success" {
			return errors.New("artifact was not staged for terminal publication")
		}
	}
	if err := encoder.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": role + " inspected the public evidence; uncertainty is preserved."}}}}); err != nil {
		return err
	}
	return encoder.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false})
}

func TestNativeCLIResearchThroughAuthenticatedAPI(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	quotedBinary := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	shim := "#!/bin/sh\nexec " + quotedBinary + " -test.run=^TestNativeHTTPHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, "invocations.txt")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("COCOGEM_NATIVE_HTTP_HELPER", "1")
	t.Setenv("COCOGEM_NATIVE_HTTP_LEDGER", ledger)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	for name, text := range map[string]string{"index.html": "fixture", "evidence.md": nativeEvidence} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := nativecli.Provider{}
	engine := &workflow.Engine{Store: db, Model: provider, Runners: map[string]agent.Runner{"claude": provider}, AllowedRoots: []string{dir}, AllowedModels: []string{"default"}, Web: tools.NewWeb("")}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	server := httptest.NewUnstartedServer(nil)
	app, err := New(Config{Host: server.Listener.Addr().String(), AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, Roots: []string{dir}, ModelConfigured: true, Model: provider, Executors: []ExecutorInfo{{ID: "claude", Installed: true, Supported: true}}}, db, engine)
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
	call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "Trace native collaboration", AgentIDs: ids, CoordinatorID: ids[2], Workflow: "research"}, nil, 200, &thread)
	input := w.RunInput{Message: "Trace custody of the supplied public fixture", Workflow: "research", ResearchScope: "given_sources", Assignments: map[string]string{"researcher": ids[0], "critic": ids[1], "coordinator": ids[2]}}
	var preview RoutingPreview
	call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
	if preview.ProposalID == "" {
		t.Fatalf("routing failed: %+v", preview)
	}
	input.ProposalID = preview.ProposalID
	var run w.Run
	call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "native-cli-http-research"}, 202, &run)
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		wake := db.Changed()
		var view RunView
		call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
		if w.Terminal(view.Run.Status) {
			if view.Run.Status != "succeeded" || len(view.Steps) != 3 {
				diagnostic, _ := os.ReadFile(ledger + ".error")
				t.Fatalf("run=%+v steps=%+v fixture=%s", view.Run, view.Steps, diagnostic)
			}
			break
		}
		select {
		case <-wake:
		case <-deadline.C:
			t.Fatal("native process research did not settle")
		}
	}
	var artifacts Page[w.Artifact]
	call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, &artifacts)
	if len(artifacts.Items) != 2 || artifacts.Items[1].Type != "research_report" || len(artifacts.Items[1].SourceIDs) != 1 || len(artifacts.Items[1].Findings) != 1 {
		t.Fatalf("published artifacts=%+v", artifacts.Items)
	}
	var captured w.Source
	call("GET", "/api/v1/sources/"+artifacts.Items[1].SourceIDs[0], nil, nil, 200, &captured)
	if captured.Kind != "file" || captured.Content != nativeCapturedEvidence || captured.SHA256 == "" {
		t.Fatalf("captured source=%+v", captured)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		sessions := map[string]bool{}
		toolReads, contextReads, handoffs := 0, 0, 0
		published := map[string]int{}
		for _, event := range events {
			switch event.Type {
			case "native_session":
				var data map[string]string
				if err := json.Unmarshal(event.Data, &data); err != nil {
					return err
				}
				if data["mode"] != "fresh" || data["session_id"] == "" || sessions[data["session_id"]] {
					return errors.New("native sessions were reused")
				}
				sessions[data["session_id"]] = true
			case "tool_finished":
				var data struct{ Name, Status string }
				if err := json.Unmarshal(event.Data, &data); err != nil {
					return err
				}
				if (data.Name == "read_file" || data.Name == "read_source") && data.Status == "ok" {
					toolReads++
				}
				if data.Name == "read_context" && data.Status == "ok" {
					contextReads++
				}
			case "handoff_prepared":
				var handoff workflow.Handoff
				if err := json.Unmarshal(event.Data, &handoff); err != nil {
					return err
				}
				if len(handoff.Artifacts) != 1 || len(handoff.SourceIDs) != 1 || handoff.Artifacts[0].Version != 1 {
					return errors.New("persisted handoff lost exact versions or evidence")
				}
				if len(handoff.MessageRefs) == 0 || len(handoff.ArtifactRefs) != 1 || len(handoff.Digests) < len(handoff.MessageRefs)+2 {
					return errors.New("persisted handoff lost exact refs or digests")
				}
				handoffs++
			case "artifact_published":
				published[event.StepID]++
			case "step_finished":
				var step w.Step
				if err := json.Unmarshal(event.Data, &step); err != nil {
					return err
				}
				if step.Status != "succeeded" || (step.Role != "critic" && published[step.ID] != 1) {
					return errors.New("artifact publication was not tied to successful terminal write")
				}
			}
		}
		// researcher reads a file; critic and coordinator page the exact captured
		// source. Each dependent also reads committed messages/artifact versions
		// through read_context, never from the inline preview alone.
		if len(sessions) != 3 || toolReads != 3 || contextReads != 5 || handoffs != 2 {
			return fmt.Errorf("sessions=%d reads=%d context_reads=%d handoffs=%d", len(sessions), toolReads, contextReads, handoffs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ledger)
	if err != nil || len(strings.Split(strings.TrimSpace(string(before)), "\n")) != 3 {
		t.Fatalf("native invocations=%s err=%v", before, err)
	}
	directories := map[string]bool{}
	for _, invocation := range strings.Split(strings.TrimSpace(string(before)), "\n") {
		_, directory, ok := strings.Cut(invocation, " ")
		if !ok || directories[directory] {
			t.Fatalf("native invocation directories were reused: %s", before)
		}
		directories[directory] = true
	}
	for i, executor := range []string{"", "anthropic"} {
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			member := members.Items[0]
			member.Executor, member.Model = executor, "legacy-api-model"
			member.Revision += i + 1
			return tx.SaveAgent(member)
		}); err != nil {
			t.Fatal(err)
		}
		legacy := w.RunInput{Message: "Do not migrate my executor implicitly", Workflow: "free", Targets: []string{ids[0]}}
		call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", legacy, nil, 200, &preview)
		legacy.ProposalID = preview.ProposalID
		var denied struct{ Error struct{ Code string } }
		call("POST", "/api/v1/threads/"+thread.ID+"/runs", legacy, map[string]string{"Idempotency-Key": "legacy-api-rejected-" + strconv.Itoa(i)}, 400, &denied)
		if denied.Error.Code != "executor_required" {
			t.Fatalf("legacy profile silently accepted: %+v", denied)
		}
	}
	after, err := os.ReadFile(ledger)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy profiles spawned a CLI: before=%s after=%s err=%v", before, after, err)
	}
}

// cancelingNativeRunner blocks like a native CLI until the step context is
// canceled, then reports one late observation that must not become evidence.
type cancelingNativeRunner struct {
	started    chan struct{}
	canceled   chan struct{}
	lateResult chan error
	startOnce  sync.Once
	cancelOnce sync.Once
}

func (r *cancelingNativeRunner) Run(ctx context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
	r.startOnce.Do(func() { close(r.started) })
	<-ctx.Done()
	r.cancelOnce.Do(func() { close(r.canceled) })
	r.lateResult <- observe("tool_finished", map[string]any{"tool_call_id": "cli:late", "name": "Read", "input": json.RawMessage(`{"file_path":"late.md"}`), "status": "ok", "output": "1\tLATE_OBSERVATION", "origin": "native"})
	return ctx.Err()
}

func TestRevokingMemberPermissionsStopsRunningNativeStep(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stops  bool
		revoke func(t *testing.T, call func(string, string, any, map[string]string) *httptest.ResponseRecorder, ws w.Workspace, agent w.Agent)
	}{
		{"member tools", true, func(t *testing.T, call func(string, string, any, map[string]string) *httptest.ResponseRecorder, ws w.Workspace, agent w.Agent) {
			rw := call("PATCH", "/api/v1/workspaces/"+ws.ID+"/members/"+agent.ID, w.WorkspaceMember{Enabled: true, Tools: []string{}}, map[string]string{"If-Match": "1"})
			if rw.Code != 200 {
				t.Fatalf("member update failed: %s", rw.Body.String())
			}
		}},
		{"profile disabled", true, func(t *testing.T, call func(string, string, any, map[string]string) *httptest.ResponseRecorder, _ w.Workspace, agent w.Agent) {
			profile := agent
			profile.Disabled, profile.WorkspaceID, profile.Membership = true, "", nil
			rw := call("PATCH", "/api/v1/agents/"+agent.ID, profile, map[string]string{"If-Match": "1"})
			if rw.Code != 200 {
				t.Fatalf("profile update failed: %s", rw.Body.String())
			}
		}},
		{"membership unlinked", true, func(t *testing.T, call func(string, string, any, map[string]string) *httptest.ResponseRecorder, ws w.Workspace, agent w.Agent) {
			rw := call("DELETE", "/api/v1/workspaces/"+ws.ID+"/members/"+agent.ID, nil, map[string]string{"If-Match": "1"})
			if rw.Code != 200 {
				t.Fatalf("member unlink failed: %s", rw.Body.String())
			}
		}},
		{"plain rename", false, func(t *testing.T, call func(string, string, any, map[string]string) *httptest.ResponseRecorder, _ w.Workspace, agent w.Agent) {
			profile := agent
			profile.DisplayName, profile.WorkspaceID, profile.Membership = "改名后", "", nil
			rw := call("PATCH", "/api/v1/agents/"+agent.ID, profile, map[string]string{"If-Match": "1"})
			if rw.Code != 200 {
				t.Fatalf("profile rename failed: %s", rw.Body.String())
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			root, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(dir, "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ws := w.Workspace{ID: w.NewID(), Name: "revocation", Root: root, Revision: 1}
			member := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "撤销测试", Handle: "revoker", Aliases: []string{}, RoleDescription: "读取公开资料。", Model: "default", Executor: "claude", Tools: []string{"read_file"}, Revision: 1}
			thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
			run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{member}, Input: w.RunInput{Message: "Read the public fixture", Workflow: "free", ResearchScope: "given_sources", Targets: []string{member.ID}}}
			steps, err := workflow.Plan(run.ID, run.Input, []w.Agent{member})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				if err := tx.SaveWorkspace(ws); err != nil {
					return err
				}
				if err := tx.SaveAgent(member); err != nil {
					return err
				}
				if err := tx.SaveThread(thread); err != nil {
					return err
				}
				if err := tx.InsertRun(run, w.NewID(), "revocation"); err != nil {
					return err
				}
				for _, step := range steps {
					if err := tx.SaveStep(step); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			runner := &cancelingNativeRunner{started: make(chan struct{}), canceled: make(chan struct{}), lateResult: make(chan error, 1)}
			engine := &workflow.Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, AllowedRoots: []string{root}, AllowedModels: []string{"default"}}
			ctx, cancel := context.WithCancel(t.Context())
			if err := engine.Start(ctx); err != nil {
				cancel()
				t.Fatal(err)
			}
			defer func() { cancel(); engine.Wait() }()
			app, err := New(Config{Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, Roots: []string{root}, ModelConfigured: true, Executors: []ExecutorInfo{{ID: "claude", Installed: true, Supported: true}}}, db, engine)
			if err != nil {
				t.Fatal(err)
			}
			handler := app.Handler()
			var cookie *http.Cookie
			csrf := ""
			call := func(method, path string, value any, headers map[string]string) *httptest.ResponseRecorder {
				t.Helper()
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(encoded))
				req.Header.Set("Content-Type", "application/json")
				if cookie != nil {
					req.AddCookie(cookie)
					req.Header.Set("X-CSRF-Token", csrf)
				}
				for key, val := range headers {
					req.Header.Set(key, val)
				}
				rw := httptest.NewRecorder()
				handler.ServeHTTP(rw, req)
				return rw
			}
			rw := call("POST", "/auth/setup", PasswordInput{Password: "revocation-test-password"}, nil)
			if rw.Code != 200 {
				t.Fatal(rw.Body.String())
			}
			cookie = rw.Result().Cookies()[0]
			var session SessionReply
			if json.Unmarshal(rw.Body.Bytes(), &session) != nil || session.CSRFToken == "" {
				t.Fatal("missing CSRF token")
			}
			csrf = session.CSRFToken

			select {
			case <-runner.started:
			case <-time.After(5 * time.Second):
				t.Fatal("native step never started")
			}
			tc.revoke(t, call, ws, member)
			if !tc.stops {
				select {
				case <-runner.canceled:
					t.Fatal("a plain rename canceled the running native step")
				case <-time.After(300 * time.Millisecond):
				}
				if rw := call("POST", "/api/v1/runs/"+run.ID+"/cancel", nil, nil); rw.Code != 200 {
					t.Fatalf("run cancel failed: %s", rw.Body.String())
				}
			}
			select {
			case <-runner.canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("revocation did not cancel the running native step")
			}
			if err := <-runner.lateResult; err != nil {
				t.Fatalf("late observation returned an error instead of being dropped: %v", err)
			}
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			for {
				wake := db.Changed()
				var current w.Run
				if err := db.Read(t.Context(), func(tx *store.Tx) error {
					var readErr error
					current, readErr = tx.Run(run.ID)
					return readErr
				}); err != nil {
					t.Fatal(err)
				}
				if w.Terminal(current.Status) {
					if current.Status != "canceled" {
						t.Fatalf("revoked run settled as %s: %s", current.Status, current.Error)
					}
					break
				}
				select {
				case <-wake:
				case <-deadline.C:
					t.Fatal("canceled run did not settle")
				}
			}
			if err := db.Read(t.Context(), func(tx *store.Tx) error {
				sources, err := tx.Sources(thread.ID)
				if err != nil {
					return err
				}
				if len(sources) != 0 {
					return fmt.Errorf("late native observation was saved: %+v", sources)
				}
				events, err := tx.Events(run.ID, 0)
				if err != nil {
					return err
				}
				for _, event := range events {
					if strings.Contains(string(event.Data), "LATE_OBSERVATION") {
						return errors.New("late native observation was persisted as an event")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
