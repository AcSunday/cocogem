package nativecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestLiveTrustedNativeTools(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_NATIVE_EXECUTION") != "1" {
		t.Skip("opt-in real native tool execution")
	}
	for _, cli := range strings.Split(os.Getenv("COCOGEM_NATIVE_EXECUTION_CLIS"), ",") {
		if cli == "" {
			continue
		}
		t.Run(cli, func(t *testing.T) {
			dir := t.TempDir()
			nativeCalls := 0
			request := model.Request{Executor: cli, Model: "default", ExecutionMode: "full_access", TrustedExecution: true, WorkingDirectory: dir, WorkspaceRoot: dir, NativeTools: []string{"Read", "Write", "Edit", "Bash"}, System: "Use your native file-write or shell tool to create proof.txt in the current working directory containing exactly NATIVE_TOOL_EXECUTED. Do not use a Cocogem tool or merely describe the operation. Then finish with a short confirmation.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Create proof.txt now using a native tool."}}}}}
			provider := Provider{}
			connection, err := provider.InspectExecutionConnection(t.Context(), cli, "default", "")
			if err != nil {
				t.Fatal(err)
			}
			request.ExpectedConnection = model.Connection{ResolvedModel: connection.ResolvedModel, Identity: connection.Identity}
			request.System += " The required output file path is " + filepath.Join(dir, "proof.txt") + "; use this absolute path."
			err = provider.Run(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, func(kind string, data map[string]any) error {
				if kind == "native_action" || kind == "tool_finished" && data["origin"] == "native" {
					nativeCalls++
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "proof.txt"))
			if err != nil || strings.TrimSpace(string(data)) != "NATIVE_TOOL_EXECUTED" {
				t.Fatalf("native file proof missing: %v, %q", err, data)
			}
			if nativeCalls == 0 {
				t.Fatal("native effect had no runner observation")
			}
		})
	}
}

func TestTrustedExecutionDoesNotLeakIntoRouting(t *testing.T) {
	request := mcpOnly(model.Request{ExecutionMode: "full_access", TrustedExecution: true, WorkingDirectory: "/task"})
	if request.TrustedExecution || request.WorkingDirectory != "" || request.ExecutionMode != "" {
		t.Fatal("routing inherited task execution")
	}
}

func TestBridgeDeliversImageBlocksWithoutLoggingPixels(t *testing.T) {
	logged := ""
	gate, err := newBridge(t.Context(), []model.Tool{{Name: "view_image", Schema: map[string]any{"type": "object"}}}, func(context.Context, string, json.RawMessage) (string, error) {
		return `{"source_id":"source","image_media_type":"image/png","image_data":"aW1hZ2U=","width":2,"height":3}`, nil
	}, func(kind string, data map[string]any) error {
		if kind == "tool_finished" {
			logged, _ = data["output"].(string)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	result := gate.call(json.RawMessage(`{"name":"view_image","arguments":{}}`)).(map[string]any)
	content := result["content"].([]any)
	if len(content) != 2 || content[1].(map[string]any)["type"] != "image" {
		t.Fatalf("MCP image result=%v", result)
	}
	if strings.Contains(logged, "aW1hZ2U=") || !strings.Contains(logged, "source") {
		t.Fatalf("image event=%q", logged)
	}
}
