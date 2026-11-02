package nativecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/cli"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/proc"
)

// The installed binary talks only to a loopback synthetic model and MCP bridge.
func TestInstalledOpenCodeIsolation(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_OPENCODE") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_OPENCODE=1 for a provider-free OpenCode protocol check")
	}

	info, err := check(t.Context(), "opencode", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := runIsolationProbe(t.Context(), info)
	if !result.passed {
		t.Fatal(result.reason)
	}
}

func TestInstalledOpenCodeStreamsBeforeProviderFinishes(t *testing.T) {
	first := make(chan string, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		installedPiChunk(w, map[string]any{"role": "assistant", "content": "你"}, nil)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		installedPiChunk(w, map[string]any{"content": "好！"}, nil)
		installedPiChunk(w, map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	installedOpenCodeFixture(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var output strings.Builder
		_, err := (Provider{}).Complete(ctx, model.Request{Executor: "opencode", Model: "default", System: "Public synthetic prompt. No tools.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Public streaming fixture."}}}}}, func(text string) error {
			output.WriteString(text)
			select {
			case first <- text:
			default:
			}
			return nil
		})
		if err == nil && output.String() != "你好！" {
			err = errors.New("streamed reply duplicated or lost content")
		}
		done <- err
	}()
	select {
	case text := <-first:
		if text != "你" {
			t.Errorf("first delta=%q", text)
		}
	case err := <-done:
		t.Fatalf("turn ended before its first delta: %v", err)
	case <-ctx.Done():
		t.Fatal("provider was blocked but no live delta reached the observer")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestInstalledOpenCodeCancellationConvergesProcessGroup(t *testing.T) {
	called := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(called) })
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	installedOpenCodeFixture(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request := testRequest()
	request.Executor, request.Model, request.Tools = "opencode", "default", nil
	var pgid int
	done := make(chan error, 1)
	go func() {
		done <- (Provider{}).Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(kind string, data map[string]any) error {
			if kind == "native_process" {
				pgid, _ = data["pgid"].(int)
			}
			return nil
		})
	}()
	select {
	case <-called:
		cancel()
	case <-ctx.Done():
		t.Fatal("OpenCode did not reach the synthetic model")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was lost: %v", err)
	}
	if alive, err := proc.GroupAlive(pgid); pgid <= 0 || err != nil || alive {
		t.Fatalf("private server/client process group survived: pgid=%d alive=%v err=%v", pgid, alive, err)
	}
}

func installedOpenCodeFixture(t *testing.T, endpoint string) string {
	t.Helper()
	if os.Getenv("COCOGEM_CHECK_INSTALLED_OPENCODE") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_OPENCODE=1 for provider-free OpenCode checks")
	}
	dir := t.TempDir()
	t.Setenv("OPENCODE_CONFIG_DIR", dir)
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "{}")
	t.Setenv("OPENCODE_DB", filepath.Join(dir, "user.db"))
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "1")
	config := openCodeProbeConfig(endpoint)
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInstalledOpenCodeConnectionSnapshot(t *testing.T) {
	dir := installedOpenCodeFixture(t, "https://first.invalid")
	provider := Provider{}
	first, err := provider.InspectConnection(t.Context(), "opencode", "default")
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := provider.InspectConnection(t.Context(), "opencode", "cocogem-probe/cocogem-probe")
	if err != nil || explicit != first {
		t.Fatalf("explicit model differs from native default: %v %v %v", first, explicit, err)
	}
	if _, err := provider.InspectConnection(t.Context(), "opencode", "missing/model"); err == nil {
		t.Fatal("unknown explicit model accepted")
	}
	same, err := provider.InspectConnection(t.Context(), "opencode", "default")
	if err != nil || same != first || first.ResolvedModel != "cocogem-probe/cocogem-probe" {
		t.Fatalf("connection is unstable: %v %v %v", first, same, err)
	}
	config := openCodeProbeConfig("https://second.invalid")
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	next, err := provider.InspectConnection(t.Context(), "opencode", "default")
	if err != nil || next.Identity == first.Identity {
		t.Fatalf("endpoint change missed: %v", err)
	}
	plan, ok := cachedPlan("opencode", first.Identity)
	if !ok {
		t.Fatal("launch snapshot missing")
	}
	frozen, _ := json.Marshal(plan.openCodeConfig)
	if !strings.Contains(string(frozen), "first.invalid") || strings.Contains(string(frozen), "second.invalid") {
		t.Fatal("connection snapshot mutated")
	}
}

func TestInstalledOpenCodeCredentialForwarding(t *testing.T) {
	dir := installedOpenCodeFixture(t, "https://example.invalid")
	db, err := openOpenCodeFixtureDB(filepath.Join(dir, "user.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO credential (id,integration_id,label,value,active,time_created,time_updated) VALUES ('public-fixture','cocogem-probe','synthetic','{"type":"key","key":"synthetic-key-only"}',1,1,1)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Provider{}).openCodePlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.openCodeCredentials) != 1 {
		t.Fatal("active credential was not copied")
	}
	scoped := t.TempDir()
	if err := (Provider{}).writeOpenCodeConfig(t.Context(), scoped, plan, plan.openCodeConfig); err != nil {
		t.Fatal(err)
	}
	raw, err := (Provider{}).openCodeAPI(t.Context(), scoped, plan.openCodeEnv, "credential.list")
	if err != nil || !strings.Contains(string(raw), "synthetic-key-only") {
		t.Fatalf("native CLI could not read its copied credential: %v", err)
	}
}

func TestInstalledOpenCodeTerminalResumeAndFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		stop   string
		kind   FailureKind
	}{
		{"success", 200, "stop", ""},
		{"authentication", 401, "", FailureAuthentication},
		{"truncation", 200, "length", FailureProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.status != 200 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					fmt.Fprint(w, `{"error":{"message":"synthetic invalid api key"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				installedPiChunk(w, map[string]any{"role": "assistant", "content": "public synthetic answer"}, nil)
				installedPiChunk(w, map[string]any{}, test.stop)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			installedOpenCodeFixture(t, server.URL)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			request := testRequest()
			request.Executor, request.Model, request.Tools = "opencode", "default", nil
			response, err := (Provider{}).Complete(ctx, request, nil)
			if FailureKindOf(err) != test.kind || test.kind == "" && err != nil {
				t.Fatalf("response=%v err=%v want=%v", response, err, test.kind)
			}
			t.Chdir(t.TempDir())
			var output bytes.Buffer
			result, err := cli.Invoke("opencode", cli.Request{Prompt: "Public synthetic fixture prompt"}, &output)
			if (err != nil) != (test.kind != "") {
				t.Fatalf("terminal err=%v", err)
			}
			if test.kind != "" {
				return
			}
			if output.String() != "public synthetic answer\n" {
				t.Fatalf("terminal output=%q", output.String())
			}
			resumed, err := cli.Invoke("opencode", cli.Request{Prompt: "Public continuation", SessionID: result.SessionID}, &output)
			if err != nil || resumed.SessionID != result.SessionID {
				t.Fatalf("resume failed: %v", err)
			}
			_, err = cli.Invoke("opencode", cli.Request{Prompt: "Public continuation", SessionID: "ses_missingabcdefghijklmnopqrstuvwxyz"}, &output)
			if err == nil || !strings.Contains(err.Error(), "session is missing or unavailable") {
				t.Fatalf("missing native session was silently recreated: %v", err)
			}
		})
	}
}

func TestInstalledOpenCodeThinkingVariants(t *testing.T) {
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid provider request")
		}
		requested = payload.ReasoningEffort
		w.Header().Set("Content-Type", "text/event-stream")
		installedPiChunk(w, map[string]any{"role": "assistant", "content": "public answer"}, nil)
		installedPiChunk(w, map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	dir := installedOpenCodeFixture(t, server.URL)
	config := openCodeProbeConfig(server.URL)
	provider := config["providers"].(map[string]any)["cocogem-probe"].(map[string]any)
	configured := provider["models"].(map[string]any)["cocogem-probe"].(map[string]any)
	configured["variants"] = []any{map[string]any{"id": "high", "body": map[string]any{"reasoning_effort": "high"}}, map[string]any{"id": "custom-effort", "body": map[string]any{"reasoning_effort": "custom"}}}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	p := Provider{}
	options, err := p.ModelOptions(t.Context(), "opencode", "default")
	if err != nil || !slices.Contains(options.ThinkingLevels, "high") || !slices.Contains(options.ThinkingLevels, "custom-effort") {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	base, err := p.InspectConnection(t.Context(), "opencode", "default")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := p.InspectExecutionConnection(t.Context(), "opencode", "default", "high")
	if err != nil || selected.ResolvedModel != "cocogem-probe/cocogem-probe#high" || selected.Identity == base.Identity {
		t.Fatalf("thinking not frozen: %+v err=%v", selected, err)
	}
	embedded, err := p.InspectConnection(t.Context(), "opencode", "cocogem-probe/cocogem-probe#high")
	if err != nil || embedded != selected {
		t.Fatalf("embedded and separate variants differ: %+v %+v %v", embedded, selected, err)
	}
	if _, err := p.InspectConnection(t.Context(), "opencode", "cocogem-probe/cocogem-probe#unsupported"); err == nil {
		t.Fatal("unsupported embedded variant accepted")
	}
	if _, err := p.InspectExecutionConnection(t.Context(), "opencode", "default", "unsupported"); err == nil {
		t.Fatal("unsupported variant accepted")
	}
	response, err := p.Complete(t.Context(), model.Request{Executor: "opencode", Model: "default", ThinkingLevel: "high", ExpectedConnection: model.Connection{Identity: selected.Identity, ResolvedModel: selected.ResolvedModel}, System: "Public synthetic prompt.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Public variant test."}}}}}, nil)
	if err != nil || len(response.Blocks) != 1 || requested != "high" {
		t.Fatalf("thinking not forwarded to provider: effort=%q err=%v", requested, err)
	}
}
