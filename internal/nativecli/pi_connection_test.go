package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

func piConnectionProvider() Provider {
	return Provider{command: func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestPiConnectionProcess", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "COCOGEM_PI_CONNECTION_HELPER=1", "GORACE=atexit_sleep_ms=0")
		return cmd
	}}
}

func TestPiConnectionProcess(t *testing.T) {
	if os.Getenv("COCOGEM_PI_CONNECTION_HELPER") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if slices.Contains(args, "--version") {
		fmt.Println("1.4.0")
		os.Exit(0)
	}
	if slices.Contains(args, "--help") {
		fmt.Println(strings.Join(adapters["pi"].flags, " "))
		os.Exit(0)
	}
	var settings struct {
		Provider string `json:"defaultProvider"`
		Model    string `json:"defaultModel"`
		Thinking string `json:"defaultThinkingLevel"`
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "settings.json"))
	if err != nil || json.Unmarshal(raw, &settings) != nil {
		os.Exit(2)
	}
	provider, id := settings.Provider, settings.Model
	if provider == "" {
		switch {
		case os.Getenv("OPENAI_API_KEY") != "":
			provider, id = "openai", "fixture-openai"
		case os.Getenv("GEMINI_API_KEY") != "":
			provider, id = "google", "fixture-google"
		}
	}
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) {
			id = args[i+1]
			if before, after, ok := strings.Cut(id, "/"); ok {
				provider, id = before, after
			}
		}
	}
	if slices.Contains(args, "rpc") {
		for _, flag := range []string{"--no-extensions", "--no-mcp", "--no-tools", "--no-session", "--no-approve", "--no-context-files"} {
			if !slices.Contains(args, flag) {
				os.Exit(3)
			}
		}
		input, err := io.ReadAll(os.Stdin)
		if err != nil || strings.TrimSpace(string(input)) != `{"type":"get_state","id":"cocogem-connection"}` {
			os.Exit(4)
		}
		var selected any
		if id != "" && provider != "" {
			selected = map[string]any{"id": id, "provider": provider, "api": "openai-completions", "baseUrl": os.Getenv("COCOGEM_PI_ENDPOINT")}
		}
		response := map[string]any{"type": "response", "command": "get_state", "id": "cocogem-connection", "success": true, "data": map[string]any{"model": selected, "thinkingLevel": settings.Thinking}}
		if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
			os.Exit(5)
		}
		os.Exit(0)
	}
	if dump := os.Getenv("COCOGEM_PI_DUMP"); dump != "" {
		auth, err := os.ReadFile(filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "auth.json"))
		if err != nil {
			os.Exit(6)
		}
		data, err := json.Marshal(map[string]any{"args": args, "env": os.Environ(), "auth": string(auth), "settings": string(raw)})
		if err != nil || os.WriteFile(dump, data, 0600) != nil {
			os.Exit(7)
		}
	}
	fmt.Printf("{\"type\":\"session\",\"id\":\"%s\"}\n", piSession)
	fmt.Println(`{"type":"message_start","message":{"role":"assistant"}}`)
	fmt.Println(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"fixture answer"}}`)
	fmt.Println(`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`)
	fmt.Println(`{"type":"agent_settled","aborted":false}`)
	os.Exit(0)
}

func writePiFixture(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPiConnectionTracksResolvedSelectionAndDropsUserResources(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	p := piConnectionProvider()
	writePiFixture(t, dir, "settings.json", `{"defaultProvider":"local","defaultModel":"model-a","defaultThinkingLevel":"high","packages":["npm:fixture"]}`)
	first, err := p.piPlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if first.info.ResolvedModel != "local/model-a" || strings.Contains(string(first.piSettings), "packages") {
		t.Fatalf("model=%q resources leaked=%v", first.info.ResolvedModel, strings.Contains(string(first.piSettings), "packages"))
	}
	writePiFixture(t, dir, "settings.json", `{"defaultProvider":"local","defaultModel":"model-b","defaultThinkingLevel":"low"}`)
	second, err := p.piPlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if second.info.ResolvedModel != "local/model-b" || first.info.Identity == second.info.Identity {
		t.Fatalf("default selection change was not detected: %+v %+v", first.info, second.info)
	}
	explicit, err := p.piPlan(t.Context(), "other/model-c")
	if err != nil || explicit.info.ResolvedModel != "other/model-c" {
		t.Fatalf("explicit model = %q, err=%v", explicit.info.ResolvedModel, err)
	}
}

func TestPiConnectionTracksAmbientProviderAndEndpoint(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "synthetic-openai")
	t.Setenv("GEMINI_API_KEY", "")
	p := piConnectionProvider()
	first, err := p.piPlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "synthetic-google")
	second, err := p.piPlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if first.info.ResolvedModel != "openai/fixture-openai" || second.info.ResolvedModel != "google/fixture-google" || first.info.Identity == second.info.Identity {
		t.Fatalf("provider change not detected: %+v %+v", first.info, second.info)
	}
	t.Setenv("COCOGEM_PI_ENDPOINT", "https://changed.invalid/v1")
	third, err := p.piPlan(t.Context(), "default")
	if err != nil || third.info.Identity == second.info.Identity {
		t.Fatalf("endpoint change not detected, err=%v", err)
	}
}

func TestPiCredentialsRotateWithoutChangingConnectionIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	writePiFixture(t, dir, "settings.json", `{"defaultProvider":"local","defaultModel":"fixture","defaultThinkingLevel":"off"}`)
	p := piConnectionProvider()
	for _, test := range []struct{ name, before, after string }{
		{"oauth", `{"local":{"type":"oauth","access":"synthetic-old","refresh":"synthetic-refresh-old","expires":1,"accountId":"fixture-account"}}`, `{"local":{"type":"oauth","access":"synthetic-new","refresh":"synthetic-refresh-new","expires":2,"accountId":"fixture-account"}}`},
		{"api key", `{"local":{"type":"api_key","key":"synthetic-old"}}`, `{"local":{"type":"api_key","key":"synthetic-new"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			writePiFixture(t, dir, "auth.json", test.before)
			first, err := p.piPlan(t.Context(), "default")
			if err != nil {
				t.Fatal(err)
			}
			writePiFixture(t, dir, "auth.json", test.after)
			second, err := p.piPlan(t.Context(), "default")
			if err != nil || first.info.Identity != second.info.Identity || string(second.piAuth) != test.after {
				t.Fatalf("credential refresh changed identity or lost new launch credentials, err=%v", err)
			}
		})
	}
	before := piConfigIdentity([]byte(`{"local":{"type":"oauth","accountId":"first"}}`))
	after := piConfigIdentity([]byte(`{"local":{"type":"oauth","accountId":"second"}}`))
	if before == after {
		t.Fatal("account change must change connection identity")
	}
}

func TestPiLaunchFreezesSelectionFilesAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	dump := filepath.Join(t.TempDir(), "launch.json")
	t.Setenv("COCOGEM_PI_DUMP", dump)
	t.Setenv("OPENAI_API_KEY", "synthetic-original")
	t.Setenv("GEMINI_API_KEY", "")
	writePiFixture(t, dir, "settings.json", `{"defaultProvider":"local","defaultModel":"model-a","defaultThinkingLevel":"high"}`)
	writePiFixture(t, dir, "auth.json", `{"local":{"type":"api_key","key":"synthetic-original"}}`)
	p := piConnectionProvider()
	info, err := p.InspectConnection(t.Context(), "pi", "default")
	if err != nil {
		t.Fatal(err)
	}
	writePiFixture(t, dir, "settings.json", `{"defaultProvider":"other","defaultModel":"model-b"}`)
	writePiFixture(t, dir, "auth.json", `{"local":{"type":"api_key","key":"synthetic-later"}}`)
	t.Setenv("OPENAI_API_KEY", "synthetic-later")
	t.Setenv("GEMINI_API_KEY", "synthetic-later")
	request := testRequest()
	request.Executor, request.Tools = "pi", nil
	request.ThinkingLevel = "low"
	request.ExpectedConnection = model.Connection{Identity: info.Identity, ResolvedModel: info.ResolvedModel}
	if err := p.Run(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) { return "ok", nil }, discardObserve); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	var launch struct {
		Args []string `json:"args"`
		Env  []string `json:"env"`
		Auth string   `json:"auth"`
	}
	if err := json.Unmarshal(raw, &launch); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(launch.Args, "local/model-a") || !strings.Contains(launch.Auth, "synthetic-original") || !slices.Contains(launch.Env, "OPENAI_API_KEY=synthetic-original") || !slices.Contains(launch.Env, "GEMINI_API_KEY=") {
		t.Fatal("late configuration or credential changes leaked into the frozen launch")
	}
	if i := slices.Index(launch.Args, "--thinking"); i < 0 || launch.Args[i+1] != "high" || launch.Args[len(launch.Args)-1] != "low" {
		t.Fatalf("configured thinking default and member override not preserved: %v", launch.Args)
	}
}

func TestPiConnectionRequiresResolvedModel(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	if _, err := piConnectionProvider().piPlan(t.Context(), "default"); err == nil {
		t.Fatal("missing resolved model must block the attempt")
	}
}
