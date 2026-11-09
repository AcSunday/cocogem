package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This uses the installed CLI against a local synthetic model, never a real
// provider or the user's credentials.
func TestInstalledGeminiSSEUpstreamError(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_GEMINI") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_GEMINI=1 for the installed Gemini check")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "countTokens") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"totalTokens":1}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": PING\n\ndata: {\"type\":\"error\",\"error\":{\"code\":\"server_error\",\"message\":\"private-fixture\"}}\n\n")
	}))
	defer server.Close()
	provider := Provider{command: func(id string, args ...string) *exec.Cmd {
		cmd := exec.Command(id, args...)
		cmd.Env = overrideEnv(os.Environ(), map[string]string{
			"GEMINI_API_KEY": "fixture-key", "GOOGLE_API_KEY": "fixture-key",
			"GOOGLE_GEMINI_BASE_URL": server.URL, "GEMINI_CLI_AUTH_OVERRIDE": "gemini-api-key",
			"GOOGLE_GENAI_USE_VERTEXAI": "false", "GOOGLE_GENAI_USE_GCA": "false",
		})
		return cmd
	}}
	request := testRequest()
	request.Executor, request.Model = "gemini", "gemini-local-fixture"
	request.Tools = nil
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	err := provider.Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", nil
	}, func(string, map[string]any) error { return nil })
	if FailureKindOf(err) != FailureProviderTransient || strings.Contains(err.Error(), "private-fixture") {
		t.Fatalf("error=%v, want sanitized provider transient failure", err)
	}
}
