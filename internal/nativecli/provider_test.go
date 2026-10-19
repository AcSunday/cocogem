package nativecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

const fakeSession = "12345678-1234-4234-8234-123456789abc"

func fakeProvider(mode string) Provider {
	return Provider{command: func(id string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestNativeCLIHelperProcess", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "COCOGEM_NATIVE_HELPER=1", "COCOGEM_NATIVE_MODE="+mode, "COCOGEM_NATIVE_EXECUTOR="+id, "GORACE=atexit_sleep_ms=0")
		return cmd
	}}
}

func testRequest() model.Request {
	return model.Request{Executor: "claude", Model: "default", System: "A scoped public test.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "read the fixture"}}}}, Tools: []model.Tool{{Name: "read_file", Description: "Read fixture", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}, "additionalProperties": false}}}}
}

func TestNativeCLIHelperProcess(t *testing.T) {
	if os.Getenv("COCOGEM_NATIVE_HELPER") != "1" {
		return
	}
	mode := os.Getenv("COCOGEM_NATIVE_MODE")
	executor := os.Getenv("COCOGEM_NATIVE_EXECUTOR")
	if os.Args[len(os.Args)-1] == "--version" {
		switch executor {
		case "codex":
			fmt.Println("codex-cli 0.162.1")
		case "gemini":
			fmt.Println("0.64.0")
		default:
			fmt.Println("2.1.301 (Claude Code)")
		}
		os.Exit(0)
	}
	if os.Args[len(os.Args)-1] == "--help" {
		fmt.Println("--restricted --tools --strict-mcp-config --setting-sources --permission-prompts --no-session-persistence --settings --disable-slash-commands --extensions --allowed-mcp-server-names --approval-mode --output-format --policy --admin-policy --skip-trust --ignore-user-config --ignore-rules --ephemeral --sandbox --json")
		os.Exit(0)
	}
	arg := func(name string) string {
		for i, value := range os.Args {
			if value == name && i+1 < len(os.Args) {
				return os.Args[i+1]
			}
		}
		return "missing"
	}
	if executor != "claude" {
		runOtherNativeHelper(executor, mode, arg)
		os.Exit(0)
	}
	configPath := arg("--mcp-config")
	for _, flag := range []string{"--restricted", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence"} {
		if !strings.Contains(strings.Join(os.Args, " "), flag) {
			os.Exit(21)
		}
	}
	if arg("--tools") != "" || arg("--permission-prompts") != "none" || arg("--setting-sources") != "" || arg("--allowedTools") != "mcp__cocogem__*" {
		os.Exit(22)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		os.Exit(23)
	}
	var config struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(data, &config) != nil || len(config.Servers) != 1 {
		os.Exit(24)
	}
	cwd, _ := os.Getwd()
	configDir, _ := filepath.EvalSymlinks(filepath.Dir(configPath))
	info, _ := os.Stat(configPath)
	if cwd != configDir || info == nil || info.Mode().Perm() != 0600 {
		os.Exit(25)
	}
	prompt, _ := io.ReadAll(os.Stdin)
	if !strings.Contains(string(prompt), "A scoped public test.") {
		os.Exit(26)
	}
	server := config.Servers["cocogem"]
	rpc := func(method string, params any) map[string]any {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(raw))
		req.Header.Set("Authorization", server.Headers["Authorization"])
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			os.Exit(27)
		}
		defer resp.Body.Close()
		var value map[string]any
		if json.NewDecoder(resp.Body).Decode(&value) != nil || resp.StatusCode != http.StatusOK {
			os.Exit(28)
		}
		return value
	}
	rpc("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "1"}})
	listed := rpc("tools/list", map[string]any{})
	var toolNames []string
	for _, value := range listed["result"].(map[string]any)["tools"].([]any) {
		toolNames = append(toolNames, "mcp__cocogem__"+value.(map[string]any)["name"].(string))
	}
	if mode == "unapproved" {
		toolNames = append(toolNames, "Bash")
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "system", "subtype": "init", "session_id": fakeSession, "tools": toolNames})
	if mode == "block" {
		cmd := exec.Command("sh", "-c", "sleep 60 & wait")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
		os.Exit(0)
	}
	if mode == "truncate" {
		os.Exit(0)
	}
	if mode == "session" {
		fmt.Fprintln(os.Stdout, `{"type":"assistant","session_id":"changed","message":{"content":[]}}`)
		os.Exit(0)
	}
	if mode == "badjson" {
		fmt.Fprintln(os.Stdout, "not JSON")
		os.Exit(0)
	}
	if mode == "call" || mode == "routing" {
		name, args := "read_file", map[string]any{"path": "README.md"}
		if mode == "routing" {
			name, args = "routing_decision", map[string]any{"path": "routing"}
		}
		value := rpc("tools/call", map[string]any{"name": name, "arguments": args})
		if failed, _ := value["result"].(map[string]any)["isError"].(bool); failed {
			os.Exit(29)
		}
	}
	fmt.Fprintln(os.Stdout, `{"type":"assistant","message":{"content":[{"type":"text","text":"public fixture answer"}]}}`)
	if mode == "exit" {
		os.Exit(8)
	}
	if mode == "usage" {
		fmt.Fprintln(os.Stdout, `{"type":"result","subtype":"success","is_error":false,"usage":{"input_tokens":7,"output_tokens":11}}`)
	} else if mode == "failed" {
		fmt.Fprintln(os.Stdout, `{"type":"result","subtype":"error_during_execution","is_error":true}`)
	} else {
		fmt.Fprintln(os.Stdout, `{"type":"result","subtype":"success","is_error":false}`)
	}
	if mode == "after" {
		fmt.Fprintln(os.Stdout, `{"type":"assistant","message":{"content":[]}}`)
	}
	os.Exit(0)
}

func TestRunNativeToolLoop(t *testing.T) {
	t.Parallel()
	var names, events []string
	err := fakeProvider("call").Run(context.Background(), testRequest(), func(_ context.Context, name string, input json.RawMessage) (string, error) {
		names = append(names, name)
		if string(input) != `{"path":"README.md"}` {
			t.Errorf("input = %s", input)
		}
		return `{"source":"fixture"}`, nil
	}, func(kind string, _ map[string]any) error { events = append(events, kind); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "read_file" || len(events) != 5 || !strings.Contains(strings.Join(events, ","), "tool_started,tool_finished") {
		t.Fatalf("tools=%v events=%v", names, events)
	}
}

func TestNativeProtocolFailureAndCancellation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"truncate", "session", "badjson", "unapproved", "after", "failed", "exit", "block"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			started := time.Now()
			err := fakeProvider(mode).Run(ctx, testRequest(), func(context.Context, string, json.RawMessage) (string, error) { return "ok", nil }, func(kind string, _ map[string]any) error {
				if mode == "block" && kind == "native_session" {
					time.AfterFunc(30*time.Millisecond, cancel)
				}
				return nil
			})
			if err == nil {
				t.Fatal("unconfirmed execution succeeded")
			}
			if mode == "block" && (!errors.Is(err, context.Canceled) || time.Since(started) > 2*time.Second) {
				t.Fatalf("cancel failed to reclaim process tree: %v elapsed=%s", err, time.Since(started))
			}
		})
	}
}

func TestNativeObserveFailureStopsExecution(t *testing.T) {
	t.Parallel()
	want := errors.New("database unavailable")
	err := fakeProvider("block").Run(context.Background(), testRequest(), func(context.Context, string, json.RawMessage) (string, error) { return "ok", nil }, func(string, map[string]any) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("error=%v want persisted error", err)
	}
}

func TestCompleteRecordsRoutingAndUnknownUsage(t *testing.T) {
	t.Parallel()
	request := testRequest()
	request.Tools[0].Name = "routing_decision"
	var text strings.Builder
	response, err := fakeProvider("routing").Complete(context.Background(), request, func(chunk string) error { text.WriteString(chunk); return nil })
	if err != nil || response.StopReason != "tool_use" || response.UsageKnown || len(response.Blocks) != 1 || response.Blocks[0].Name != "routing_decision" {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	if !strings.Contains(text.String(), "public fixture answer") {
		t.Fatal("visible reply was not emitted")
	}
	request.Tools = nil
	response, err = fakeProvider("ok").Complete(context.Background(), request, nil)
	if err != nil || response.StopReason != "end_turn" || response.UsageKnown {
		t.Fatalf("verification response=%+v error=%v", response, err)
	}
}

func TestNativeFailureDiagnosticDoesNotExposeInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ detail, category string }{{"Failed to authenticate: OAuth session expired credential-secret", "authentication"}, {"Model quota exceeded account-secret", "quota"}, {"Permission denied for path-private", "permissions"}, {"Unknown tool private-tool", "tool_unavailable"}} {
		err := nativeFailure("success", test.detail)
		if !strings.Contains(err.Error(), test.category) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe or unhelpful diagnostic: %v", err)
		}
	}
}

func TestCompletePreservesNativeUsage(t *testing.T) {
	t.Parallel()
	request := testRequest()
	request.Tools = nil
	response, err := fakeProvider("usage").Complete(context.Background(), request, nil)
	if err != nil || !response.UsageKnown || response.InputTokens != 7 || response.OutputTokens != 11 {
		t.Fatalf("response=%+v error=%v", response, err)
	}
}

func TestCapabilitiesFailClosed(t *testing.T) {
	t.Parallel()
	if capabilities("claude", "2.1.301", "--tools") || capabilities("gemini", "0.64.0", "--tools") || capabilities("codex", "1", "--tools") {
		t.Fatal("uninspected CLI capabilities were accepted")
	}
}

func TestUnsupportedExecutorsFailBeforeSpawn(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"unknown"} {
		request := testRequest()
		request.Executor = id
		p := Provider{command: func(string, ...string) *exec.Cmd { t.Fatal("unsupported executor spawned"); return nil }}
		if p.Run(context.Background(), request, nil, nil) == nil {
			t.Errorf("%s unsupported executor succeeded", id)
		}
	}
}

func TestBridgeSecurityAndSerialization(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	inFlight, maximum, calls := 0, 0, 0
	b, err := newBridge(context.Background(), testRequest().Tools, func(context.Context, string, json.RawMessage) (string, error) {
		mu.Lock()
		inFlight++
		maximum = max(maximum, inFlight)
		calls++
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return "ok", nil
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	invoke := func(host, origin, auth, name, arguments string) (int, bool) {
		raw := `{"jsonrpc":"2.0","id":1,"method":"tools/call","_meta":{"progressToken":"fixture"},"params":{"name":"` + name + `","arguments":` + arguments + `}}`
		req, _ := http.NewRequest(http.MethodPost, b.url(), strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		req.Header.Set("Origin", origin)
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("HTTP: %v", err)
			return 0, true
		}
		defer resp.Body.Close()
		var body struct {
			Result struct{ IsError bool } `json:"result"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body.Result.IsError
	}
	auth := "Bearer " + b.token
	for _, fields := range [][3]string{{"evil.test", "", auth}, {"", "https://evil.test", auth}, {"", "", "Bearer wrong"}} {
		if code, _ := invoke(fields[0], fields[1], fields[2], "read_file", `{"path":"README.md"}`); code != http.StatusForbidden {
			t.Errorf("untrusted request status=%d", code)
		}
	}
	for _, input := range [][2]string{{"Bash", `{}`}, {"read_file", `{"path":42}`}, {"read_file", `{"path":"a","extra":true}`}} {
		if _, failed := invoke("", "", auth, input[0], input[1]); !failed {
			t.Fatal("invalid tool call accepted")
		}
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, failed := invoke("", "", auth, "read_file", `{"path":"README.md"}`); failed {
				t.Error("valid tool call failed")
			}
		})
	}
	wg.Wait()
	if maximum != 1 || calls != 5 {
		t.Fatalf("maximum=%d calls=%d", maximum, calls)
	}
}

func TestBridgeLimitsAndPersistFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"budget", "output", "persist"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			b, err := newBridge(context.Background(), testRequest().Tools, func(context.Context, string, json.RawMessage) (string, error) {
				if mode == "output" {
					return strings.Repeat("x", 65537), nil
				}
				return "ok", nil
			}, func(string, map[string]any) error {
				if mode == "persist" {
					return errors.New("persist failure")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			count := 1
			if mode == "budget" {
				count = 31
			}
			for range count {
				b.call(json.RawMessage(`{"name":"read_file","arguments":{"path":"README.md"}}`))
			}
			if b.failure() == nil {
				t.Fatal("limit or persistence failure was not retained")
			}
		})
	}
}
