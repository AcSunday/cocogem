package nativecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runOtherNativeHelper(executor, mode string, arg func(string) string) {
	var url, token, serverName string
	if executor == "gemini" {
		cwd, _ := os.Getwd()
		raw, err := os.ReadFile(filepath.Join(cwd, ".gemini", "settings.json"))
		if err != nil {
			os.Exit(40)
		}
		var settings map[string]any
		if json.Unmarshal(raw, &settings) != nil {
			os.Exit(41)
		}
		servers := settings["mcpServers"].(map[string]any)
		if len(servers) != 1 || len(settings["tools"].(map[string]any)["core"].([]any)) != 0 || settings["hooksConfig"].(map[string]any)["enabled"] != false || arg("--extensions") != "none" || arg("--policy") != arg("--admin-policy") {
			os.Exit(42)
		}
		for name, value := range servers {
			serverName = name
			config := value.(map[string]any)
			url = config["url"].(string)
			token = strings.TrimPrefix(config["headers"].(map[string]any)["Authorization"].(string), "Bearer ")
		}
		if os.Getenv("GEMINI_CLI_TRUST_WORKSPACE") != "true" || arg("--allowed-mcp-server-names") != serverName {
			os.Exit(43)
		}
		policy, err := os.ReadFile(filepath.Join(arg("--admin-policy"), "cocogem.toml"))
		if err != nil || !strings.Contains(string(policy), `decision = "deny"`) || !strings.Contains(string(policy), serverName) {
			os.Exit(44)
		}
	} else {
		values := map[string]string{}
		for i, value := range os.Args {
			if value == "-c" && i+1 < len(os.Args) {
				key, v, _ := strings.Cut(os.Args[i+1], "=")
				values[key] = v
			}
		}
		for _, key := range []string{"features.shell_tool", "features.view_image", "features.hooks", "features.plugins", "features.apps", "features.multi_agent"} {
			if values[key] != "false" {
				os.Exit(45)
			}
		}
		var path string
		_ = json.Unmarshal([]byte(values["model_catalog_json"]), &path)
		raw, err := os.ReadFile(path)
		if err != nil {
			os.Exit(46)
		}
		var catalog struct {
			Models []map[string]any `json:"models"`
		}
		if json.Unmarshal(raw, &catalog) != nil || len(catalog.Models) != 1 {
			os.Exit(47)
		}
		entry := catalog.Models[0]
		if arg("--model") != entry["slug"] || entry["shell_type"] != "disabled" || entry["apply_patch_tool_type"] != nil || len(entry["experimental_supported_tools"].([]any)) != 0 || values["mcp_servers.cocogem.tools.read_file.approval_mode"] != `"approve"` {
			os.Exit(48)
		}
		_ = json.Unmarshal([]byte(values["mcp_servers.cocogem.url"]), &url)
		token = os.Getenv("COCOGEM_MCP_TOKEN")
		serverName = "cocogem"
		if token == "" || strings.Contains(strings.Join(os.Args, " "), token) {
			os.Exit(49)
		}
	}
	prompt, _ := io.ReadAll(os.Stdin)
	if !strings.Contains(string(prompt), "A scoped public test.") {
		os.Exit(50)
	}
	rpc := func(method string, params any) map[string]any {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			os.Exit(51)
		}
		defer resp.Body.Close()
		var result map[string]any
		if json.NewDecoder(resp.Body).Decode(&result) != nil || resp.StatusCode != 200 {
			os.Exit(52)
		}
		return result
	}
	rpc("initialize", map[string]any{"protocolVersion": "2025-03-26"})
	rpc("tools/list", map[string]any{})
	if executor == "gemini" {
		fmt.Printf("{\"type\":\"init\",\"session_id\":%q}\n", fakeSession)
	} else {
		fmt.Printf("{\"type\":\"thread.started\",\"thread_id\":%q}\n{\"type\":\"turn.started\"}\n", fakeSession)
	}
	if mode == "call" {
		if executor == "gemini" {
			fmt.Printf("{\"type\":\"tool_use\",\"tool_name\":%q}\n", "mcp_"+serverName+"_read_file")
		} else {
			fmt.Fprintln(os.Stdout, `{"type":"item.started","item":{"id":"item_tool","type":"mcp_tool_call","server":"cocogem","tool":"read_file"}}`)
		}
		result := rpc("tools/call", map[string]any{"name": "read_file", "arguments": map[string]any{"path": "README.md"}})
		if result["result"].(map[string]any)["isError"] == true {
			os.Exit(53)
		}
	}
	if mode == "session" {
		if executor == "gemini" {
			fmt.Println(`{"type":"message","session_id":"99999999-9999-4999-8999-999999999999","role":"assistant","content":"changed"}`)
		} else {
			fmt.Println(`{"type":"item.completed","thread_id":"99999999-9999-4999-8999-999999999999","item":{"id":"item_changed","type":"agent_message","text":"changed"}}`)
		}
		return
	}
	if mode == "notice" {
		if executor == "gemini" {
			fmt.Fprintln(os.Stdout, `{"type":"thought","content":"non-critical"}`)
		} else {
			fmt.Fprintln(os.Stdout, `{"type":"thread.updated","status":"non-critical"}`)
		}
	}
	if mode == "truncate" {
		return
	}
	if mode == "unapproved" {
		if executor == "gemini" {
			fmt.Fprintln(os.Stdout, `{"type":"tool_use","tool_name":"run_shell_command"}`)
		} else {
			fmt.Fprintln(os.Stdout, `{"type":"item.started","item":{"id":"item_shell","type":"command_execution"}}`)
		}
		return
	}
	if executor == "gemini" {
		fmt.Fprintln(os.Stdout, `{"type":"message","role":"assistant","content":"public fixture answer","delta":true}`)
		fmt.Fprintln(os.Stdout, `{"type":"result","status":"success"}`)
	} else {
		fmt.Fprintln(os.Stdout, `{"type":"item.completed","item":{"id":"item_message","type":"agent_message","text":"public fixture answer"}}`)
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":4}}`)
	}
}

func TestOtherNativeToolLoops(t *testing.T) {
	for _, id := range []string{"codex", "gemini"} {
		t.Run(id, func(t *testing.T) {
			for _, mode := range []string{"call", "notice", "truncate", "unapproved", "session"} {
				t.Run(mode, func(t *testing.T) {
					request := testRequest()
					request.Executor = id
					request.Model = "fixture-model"
					calls := 0
					err := fakeProvider(mode).Run(context.Background(), request, func(context.Context, string, json.RawMessage) (string, error) { calls++; return "ok", nil }, func(string, map[string]any) error { return nil })
					if mode == "call" && (err != nil || calls != 1) {
						t.Fatalf("mode=%s calls=%d err=%v", mode, calls, err)
					}
					if mode == "notice" && (err != nil || calls != 0) {
						t.Fatalf("mode=%s calls=%d err=%v", mode, calls, err)
					}
					if (mode == "truncate" || mode == "unapproved" || mode == "session") && err == nil {
						t.Fatal("unconfirmed or unsafe native execution succeeded")
					}
				})
			}
		})
	}
}

func TestCodexDefaultRequiresAConfiguredModel(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	request := testRequest()
	request.Executor = "codex"
	err := fakeProvider("call").Run(context.Background(), request, func(context.Context, string, json.RawMessage) (string, error) {
		t.Fatal("unexpected execute")
		return "", nil
	}, func(string, map[string]any) error { return nil })
	if err == nil {
		t.Fatal("default without CLI model configuration was accepted")
	}
}
