package nativecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestRoutingAndProbeRequestsStayMCPOnly(t *testing.T) {
	request := mcpOnly(model.Request{NativeTools: []string{"Read", "WebFetch"}, WorkspaceRoot: "/workspace", NativeProxy: "http://127.0.0.1:7897"})
	if len(request.NativeTools) != 0 || request.WorkspaceRoot != "" || request.NativeProxy != "" {
		t.Fatalf("native execution fields leaked into a routing/probe request: %+v", request)
	}
}

func TestNativeToolsDeriveOnlyFromExistingGrants(t *testing.T) {
	all := []string{"read_file", "list_files", "search_content", "web_search", "fetch_url", "read_source", "publish_artifact"}
	if got, want := NativeTools("claude", all, "auto"), []string{"Read", "Glob", "Grep", "WebSearch", "WebFetch"}; !slices.Equal(got, want) {
		t.Fatalf("claude auto = %v, want %v", got, want)
	}
	if got, want := NativeTools("claude", all, "web_discovery"), []string{"Read", "Glob", "Grep", "WebSearch", "WebFetch"}; !slices.Equal(got, want) {
		t.Fatalf("claude discovery = %v, want %v", got, want)
	}
	for _, scope := range []string{"given_sources", ""} {
		if got, want := NativeTools("claude", all, scope), []string{"Read", "Glob", "Grep"}; !slices.Equal(got, want) {
			t.Fatalf("claude %q gained native web: %v", scope, got)
		}
	}
	if got := NativeTools("claude", []string{"read_file", "fetch_url"}, "auto"); !slices.Equal(got, []string{"Read", "WebFetch"}) {
		t.Fatalf("unexpected filtered mapping: %v", got)
	}
	if got := NativeTools("claude", []string{"publish_artifact"}, "auto"); len(got) != 0 {
		t.Fatalf("unrelated grants produced native tools: %v", got)
	}
	// Codex has one combined web tool, so fetch alone cannot grant search.
	if got, want := NativeTools("codex", all, "auto"), []string{"web_search"}; !slices.Equal(got, want) {
		t.Fatalf("codex both grants = %v, want %v", got, want)
	}
	if got := NativeTools("codex", []string{"fetch_url"}, "auto"); len(got) != 0 {
		t.Fatalf("fetch alone enabled Codex search: %v", got)
	}
	if got := NativeTools("codex", all, "given_sources"); len(got) != 0 {
		t.Fatalf("given sources enabled Codex web: %v", got)
	}
	if got := NativeTools("gemini", all, "auto"); len(got) != 0 {
		t.Fatalf("Gemini native web has no faithful result path: %v", got)
	}
}

func TestNativeToolsAreSanitizedAgainstTheAdapterWhitelist(t *testing.T) {
	files, web := sanitizeNativeTools("claude", []string{"Read", "Bash", "Task", "Shell", "WebFetch"}, "/workspace")
	if !slices.Equal(files, []string{"Read"}) || !slices.Equal(web, []string{"WebFetch"}) {
		t.Fatalf("files=%v web=%v", files, web)
	}
	files, _ = sanitizeNativeTools("claude", []string{"Read"}, "")
	if len(files) != 0 {
		t.Fatalf("native file tool survived without a root: %v", files)
	}
	if files, web := sanitizeNativeTools("gemini", []string{"web_search", "Read"}, "/workspace"); len(files) != 0 || len(web) != 0 {
		t.Fatalf("Gemini native tools were accepted: %v %v", files, web)
	}
}

func TestConfigInsideRootPreventsCredentialExposure(t *testing.T) {
	root := t.TempDir()
	if !configInsideRoot(root, filepath.Join(root, "config")) {
		t.Fatal("a settings directory under the readable root was considered safe")
	}
	if configInsideRoot(root, filepath.Join(filepath.Dir(root), "config")) {
		t.Fatal("an external settings directory was rejected")
	}
	if !configInsideRoot("/", "/tmp/config") {
		t.Fatal("filesystem root must not expose an isolated settings directory")
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err == nil {
		if !configInsideRoot(link, filepath.Join(root, "config")) {
			t.Fatal("symlinked root bypassed the settings-directory check")
		}
	}
	// A settings directory reached through a different alias of the same
	// physical location must also be treated as inside the readable root.
	area := t.TempDir()
	real := filepath.Join(area, "real")
	if err := os.MkdirAll(filepath.Join(real, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(area, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if !configInsideRoot(real, filepath.Join(alias, "config")) {
		t.Fatal("settings directory reached through another alias was treated as outside")
	}
	if !configInsideRoot(alias, filepath.Join(real, "config")) {
		t.Fatal("aliased root did not cover the physical settings directory")
	}
}

func TestClaudeArgsExposeOnlyTheGrantedNativeSubset(t *testing.T) {
	args := strings.Join(claudeArgs("/tmp/config/mcp.json", "/tmp/config/settings.json", []string{"Read", "WebFetch"}), " ")
	for _, want := range []string{"--restricted", "--tools Read,WebFetch", "--allowedTools Read,WebFetch,mcp__cocogem__*", "--strict-mcp-config", "--setting-sources"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q missing %q", args, want)
		}
	}
	if strings.Contains(args, "--add-dir") {
		t.Fatalf("--add-dir would bypass deny matching: %q", args)
	}
	webOnly := strings.Join(claudeArgs("/tmp/config/mcp.json", "/tmp/config/settings.json", []string{"WebFetch"}), " ")
	if strings.Contains(webOnly, "--add-dir") || !strings.Contains(webOnly, "--tools WebFetch") {
		t.Fatalf("web-only args = %q", webOnly)
	}
	none := strings.Join(claudeArgs("/tmp/config/mcp.json", "/tmp/config/settings.json", nil), " ")
	if !strings.Contains(none, "--tools  --strict") && !strings.Contains(none, "--tools  --strict-mcp-config") {
		t.Fatalf("no native tools must keep an empty --tools: %q", none)
	}
	if !strings.Contains(none, "--allowedTools mcp__cocogem__*") {
		t.Fatalf("MCP tools disappeared without native grants: %q", none)
	}
}

func TestClaudeStepSettingsDenySecretsAndTheConfigDirectory(t *testing.T) {
	settings := claudeStepSettings(map[string]any{"env": map[string]string{"ANTHROPIC_API_KEY": "public-fixture"}}, "/tmp/cocogem-config")
	permissions, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatal("missing permissions")
	}
	rules := permissions["deny"].([]string)
	joined := strings.Join(rules, "\n")
	for _, want := range []string{"Read(**/.env*)", "Grep(**/.git/**)", "Glob(**/*.key)", "Read(**/.cocogem/**)", "Read(/tmp/cocogem-config/**)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("deny rules missing %q: %v", want, rules)
		}
	}
	if settings["autoMemoryEnabled"] != false || settings["disableAllHooks"] != true {
		t.Fatal("isolated Claude settings lost hook or memory restrictions")
	}
	if settings["env"].(map[string]string)["ANTHROPIC_API_KEY"] != "public-fixture" {
		t.Fatal("connection settings were not forwarded")
	}
}

func TestNativeProxyEnvKeepsLoopbackDirectAndMergesExistingNoProxy(t *testing.T) {
	env := nativeProxyEnv("http://127.0.0.1:7897", "internal.example,localhost")
	if env["HTTPS_PROXY"] != "http://127.0.0.1:7897" || env["https_proxy"] != env["HTTPS_PROXY"] {
		t.Fatalf("proxy not forwarded: %v", env)
	}
	noProxy := env["NO_PROXY"]
	for _, want := range []string{"127.0.0.1", "localhost", "::1", "internal.example"} {
		if !strings.Contains(noProxy, want) {
			t.Fatalf("NO_PROXY %q missing %q", noProxy, want)
		}
	}
	if got := nativeProxyEnv("", "x"); got != nil {
		t.Fatalf("empty proxy produced env: %v", got)
	}
}

func TestNativeResultTextAcceptsStringAndTextBlocks(t *testing.T) {
	if text, unsupported := nativeResultText(json.RawMessage(`"plain"`)); text != "plain" || unsupported {
		t.Fatalf("string result = %q unsupported=%v", text, unsupported)
	}
	if text, unsupported := nativeResultText(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)); text != "a\nb" || unsupported {
		t.Fatalf("block result = %q unsupported=%v", text, unsupported)
	}
	if _, unsupported := nativeResultText(json.RawMessage(`[{"type":"image","data":"…"}]`)); !unsupported {
		t.Fatal("non-text result was accepted as evidence")
	}
	if _, unsupported := nativeResultText(json.RawMessage(`not-json`)); !unsupported {
		t.Fatal("malformed result was accepted")
	}
}
