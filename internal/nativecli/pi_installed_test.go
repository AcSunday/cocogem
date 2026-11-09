package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

// Real binary protocol checks use only synthetic prompts and loopback servers.
func installedPiRequest(t *testing.T, endpoint string) model.Request {
	t.Helper()
	if os.Getenv("COCOGEM_CHECK_INSTALLED_PI") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_PI=1 for provider-free Pi checks")
	}
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("PI_TELEMETRY", "0")
	models, err := json.Marshal(map[string]any{"providers": map[string]any{"fixture": map[string]any{
		"name": "Fixture", "baseUrl": endpoint + "/v1", "api": "openai-completions", "apiKey": "synthetic-dummy-key",
		"models": []any{map[string]any{"id": "fixture-model", "name": "Fixture", "reasoning": true, "input": []string{"text"}, "contextWindow": 128000, "maxTokens": 8192}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	writePiFixture(t, dir, "models.json", string(models))
	writePiFixture(t, dir, "settings.json", `{"defaultProvider":"fixture","defaultModel":"fixture-model","defaultThinkingLevel":"high","retry":{"enabled":false}}`)
	request := testRequest()
	request.Executor, request.Model, request.Tools = "pi", "default", nil
	return request
}

func installedPiChunk(w http.ResponseWriter, delta map[string]any, reason any) {
	writeSSEJSON(w, map[string]any{"id": "public-fixture", "object": "chat.completion.chunk", "created": 1, "model": "fixture-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}})
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
}

func TestInstalledPiFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		stop   string
		kind   FailureKind
	}{
		{name: "authentication", status: 401, kind: FailureAuthentication},
		{name: "truncation", status: 200, stop: "length", kind: FailureProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.status != 200 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					fmt.Fprint(w, `{"error":{"message":"synthetic invalid api key","type":"invalid_api_key","code":"invalid_api_key"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				installedPiChunk(w, map[string]any{"role": "assistant", "content": "public partial reply"}, nil)
				installedPiChunk(w, map[string]any{}, test.stop)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			request := installedPiRequest(t, server.URL)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			if _, err := (Provider{}).Complete(ctx, request, nil); FailureKindOf(err) != test.kind {
				t.Fatalf("web adapter error=%v, want %s", err, test.kind)
			}
		})
	}
}

func TestInstalledPiThinkingPreventsFalseTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 15 {
			installedPiChunk(w, map[string]any{"role": "assistant", "reasoning_content": "public synthetic reasoning "}, nil)
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		installedPiChunk(w, map[string]any{"content": "public final answer"}, nil)
		installedPiChunk(w, map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	request := installedPiRequest(t, server.URL)
	request.FirstResponseTimeout, request.StallTimeout, request.StallAction = time.Second, time.Second, "stop"
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	response, err := (Provider{}).Complete(ctx, request, nil)
	if err != nil || len(response.Blocks) != 1 || response.Blocks[0].Text != "public final answer" {
		t.Fatalf("continuous thinking should keep the task alive without leaking reasoning: response=%+v err=%v", response, err)
	}
}

func TestInstalledPiConnectionTracksEnvironmentSelection(t *testing.T) {
	_ = installedPiRequest(t, "http://127.0.0.1:1")
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	// No model requests are sent. Remove ambient auth to make Pi's default deterministic.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.Contains(key, "API_KEY") || strings.Contains(key, "AUTH_TOKEN") || strings.Contains(key, "OAUTH_TOKEN") || strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "GOOGLE_") {
			t.Setenv(key, "")
		}
	}
	writePiFixture(t, dir, "models.json", `{"providers":{"openai":{"baseUrl":"http://127.0.0.1:1/v1"},"google":{"baseUrl":"http://127.0.0.1:1/v1"}}}`)
	writePiFixture(t, dir, "settings.json", `{}`)
	t.Setenv("OPENAI_API_KEY", "synthetic-openai-key")
	t.Setenv("GEMINI_API_KEY", "")
	first, err := (Provider{}).InspectConnection(t.Context(), "pi", "default")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "synthetic-google-key")
	second, err := (Provider{}).InspectConnection(t.Context(), "pi", "default")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ResolvedModel, "openai/") || !strings.HasPrefix(second.ResolvedModel, "google/") || first.Identity == second.Identity {
		t.Fatalf("actual provider change must block recovery: %+v %+v", first, second)
	}
}

func TestInstalledPiCredentialRefreshAndFrozenLaunch(t *testing.T) {
	authorizations := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		installedPiChunk(w, map[string]any{"role": "assistant", "content": "public frozen connection reply"}, nil)
		installedPiChunk(w, map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	request := installedPiRequest(t, server.URL)
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	writePiFixture(t, dir, "auth.json", `{"fixture":{"type":"api_key","key":"synthetic-original"}}`)
	first, err := (Provider{}).InspectConnection(t.Context(), "pi", "default")
	if err != nil {
		t.Fatal(err)
	}
	writePiFixture(t, dir, "auth.json", `{"fixture":{"type":"api_key","key":"synthetic-refreshed"}}`)
	second, err := (Provider{}).InspectConnection(t.Context(), "pi", "default")
	if err != nil || first.Identity != second.Identity {
		t.Fatalf("credential refresh changed identity, err=%v", err)
	}
	writePiFixture(t, dir, "auth.json", `{"fixture":{"type":"api_key","key":"synthetic-late-edit"}}`)
	writePiFixture(t, dir, "settings.json", `{"defaultProvider":"missing","defaultModel":"missing"}`)
	writePiFixture(t, dir, "models.json", `{}`)
	request.ExpectedConnection = model.Connection{Identity: second.Identity, ResolvedModel: second.ResolvedModel}
	response, err := (Provider{}).Complete(t.Context(), request, nil)
	var authorization string
	select {
	case authorization = <-authorizations:
	default:
	}
	if err != nil || authorization != "Bearer synthetic-refreshed" || len(response.Blocks) != 1 || response.Blocks[0].Text != "public frozen connection reply" {
		t.Fatalf("late edits changed the frozen launch: response=%+v err=%v", response, err)
	}
}

func TestInstalledPiOAuthRefreshKeepsConnectionIdentity(t *testing.T) {
	_ = installedPiRequest(t, "http://127.0.0.1:1")
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	writePiFixture(t, dir, "auth.json", `{"openai-codex":{"type":"oauth","access":"synthetic-old","refresh":"synthetic-refresh-old","expires":1,"accountId":"fixture-account"}}`)
	first, err := (Provider{}).InspectConnection(t.Context(), "pi", "openai-codex/gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}
	writePiFixture(t, dir, "auth.json", `{"openai-codex":{"type":"oauth","access":"synthetic-new","refresh":"synthetic-refresh-new","expires":2,"accountId":"fixture-account"}}`)
	second, err := (Provider{}).InspectConnection(t.Context(), "pi", "openai-codex/gpt-5.5")
	if err != nil || first.Identity != second.Identity || first.ResolvedModel != second.ResolvedModel {
		t.Fatalf("OAuth token replacement changed the same provider/model connection, err=%v", err)
	}
}
