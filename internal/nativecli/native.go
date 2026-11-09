package nativecli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
)

// nativeObservationLimit bounds one captured native observation. The persisted
// event itself is bounded again by the workflow.
const nativeObservationLimit = 512 << 10

type claudeNativeCall struct {
	name         string
	input        json.RawMessage
	resultSeen   bool
	resultDigest [32]byte
}

// nativeResultText accepts the string and text-block forms used by stream-json.
// Non-text content cannot be stored as source evidence.
func nativeResultText(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false
	}
	switch trimmed[0] {
	case '"':
		var text string
		if json.Unmarshal(trimmed, &text) != nil {
			return "", true
		}
		return text, false
	case '[':
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(trimmed, &blocks) != nil {
			return "", true
		}
		text := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Type != "text" {
				return "", true
			}
			text = append(text, block.Text)
		}
		return strings.Join(text, "\n"), false
	case '{':
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(trimmed, &block) != nil || block.Type != "text" {
			return "", true
		}
		return block.Text, false
	}
	return "", true
}

// trimUTF8 cuts at a rune boundary so a persisted observation stays valid text.
func trimUTF8(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

// Native tool names are derived only from the member's existing effective
// grants. They are not an extra permission switch.
const (
	nativeRead      = "Read"
	nativeGlob      = "Glob"
	nativeGrep      = "Grep"
	nativeWebFetch  = "WebFetch"
	nativeWebSearch = "WebSearch"
	// Codex exposes one combined Responses web tool; it is enabled only when
	// both web grants are present so search cannot be gained from fetch alone.
	nativeCodexWeb     = "web_search"
	nativeCodexCommand = "command_execution"
	nativeCodexFile    = "file_change"
	nativeCodexImage   = "view_image"
	nativeBash         = "Bash"
	nativeWrite        = "Write"
	nativeEdit         = "Edit"
)

var claudeNativeNames = []string{nativeRead, nativeGlob, nativeGrep, nativeWebFetch, nativeWebSearch}
var claudeAutonomousNames = []string{nativeRead, nativeGlob, nativeGrep, nativeWebFetch, nativeWebSearch, nativeWrite, nativeEdit, nativeBash}
var claudeFullAccessNames = []string{nativeRead, nativeGlob, nativeGrep, nativeWebFetch, nativeWebSearch, nativeBash, nativeWrite, nativeEdit}

// onlineScope permits native web only for scopes that already permit
// discovery. Given sources and legacy empty scopes keep host-only fetching.
func onlineScope(scope string) bool {
	return scope == "auto" || scope == "web_discovery"
}

// NativeTools maps effective host grants to CLI-owned tools. Codex and Gemini
// are intentionally narrower: no native file tools, and Gemini native web is
// not enabled because its stream-json tool_result does not expose the actual
// returned content. Pi has no native subset:
// its read/bash/edit/write tools cannot be bounded to the workspace root.
func NativeTools(executor string, allowed []string, scope string) []string {
	out := []string{}
	switch executor {
	case "claude":
		if slices.Contains(allowed, "read_file") {
			out = append(out, nativeRead)
		}
		if slices.Contains(allowed, "list_files") {
			out = append(out, nativeGlob)
		}
		if slices.Contains(allowed, "search_content") {
			out = append(out, nativeGrep)
		}
		if onlineScope(scope) {
			if slices.Contains(allowed, "web_search") {
				out = append(out, nativeWebSearch)
			}
			if slices.Contains(allowed, "fetch_url") {
				out = append(out, nativeWebFetch)
			}
		}
	case "codex":
		if onlineScope(scope) && slices.Contains(allowed, "web_search") && slices.Contains(allowed, "fetch_url") {
			out = append(out, nativeCodexWeb)
		}
	}
	return out
}

// NativeExecutionTools maps the mode onto the CLI-owned tool catalog.
// project_autonomous genuinely opens native read/write/command/image tools
// inside a real OS boundary; full_access keeps the existing broad behavior.
func NativeExecutionTools(executor string, allowed []string, scope, mode string) []string {
	out := NativeTools(executor, allowed, scope)
	if executor == "claude" && (mode == "project_autonomous" || mode == "full_access") {
		if slices.Contains(allowed, "write_file") {
			out = append(out, nativeWrite, nativeEdit)
		}
		if slices.Contains(allowed, "run_test") || slices.Contains(allowed, "run_command") {
			out = append(out, nativeBash)
		}
	}
	if executor == "codex" && (mode == "project_autonomous" || mode == "full_access") {
		if slices.Contains(allowed, "write_file") {
			out = append(out, nativeWrite, nativeCodexFile)
		}
		if slices.Contains(allowed, "run_test") || slices.Contains(allowed, "run_command") {
			out = append(out, nativeBash, nativeCodexCommand)
		}
		if slices.Contains(allowed, "view_image") || slices.Contains(allowed, "read_file") {
			out = append(out, nativeCodexImage)
		}
	}
	return out
}

// sanitizeNativeTools is the adapter's own guard: only names this executor
// implements survive, and file tools never appear without a bounded root.
func sanitizeNativeTools(executor string, requested []string, root string) (files, web []string) {
	return sanitizeExecutionNativeTools(executor, requested, root, "")
}

func sanitizeExecutionNativeTools(executor string, requested []string, root string, mode string) (files, web []string) {
	var known []string
	switch executor {
	case "claude":
		switch mode {
		case "project_autonomous":
			known = claudeAutonomousNames
		case "full_access":
			known = claudeFullAccessNames
		default:
			known = claudeNativeNames
		}
	case "codex":
		known = []string{nativeCodexWeb, nativeCodexCommand, nativeCodexFile, nativeCodexImage, nativeBash, nativeWrite}
	default:
		return nil, nil
	}
	for _, name := range requested {
		if !slices.Contains(known, name) {
			continue
		}
		if slices.Contains([]string{nativeRead, nativeGlob, nativeGrep}, name) {
			if root != "" {
				files = append(files, name)
			}
			continue
		}
		if slices.Contains([]string{nativeBash, nativeWrite, nativeEdit}, name) {
			// Write/command tools require both a real root and an OS boundary.
			// The caller decides whether the platform can enforce it; without a
			// root they stay closed.
			if root != "" {
				files = append(files, name)
			}
			continue
		}
		web = append(web, name)
	}
	return files, web
}

func nativeToolNames(files, web []string) []string {
	return append(append([]string{}, files...), web...)
}

// claudeArgs keeps the native subset and the scoped MCP tools in one
// inventory. --tools is the CLI-enforced boundary; the parser is only a
// protocol guard.
func claudeArgs(configPath, settingsPath string, native []string) []string {
	return claudeExecutionArgs(configPath, settingsPath, native, model.Request{})
}

// claudeBidirectionalArgs launches the stream-json control protocol used for
// project_autonomous. Native tools are not pre-allowed: every call goes through
// the private fail-closed PreToolUse hook, which asks the host before effect.
func claudeBidirectionalArgs(configPath, settingsPath string, native []string, request model.Request) []string {
	builtins := []string{}
	for _, name := range native {
		if slices.Contains(claudeAutonomousNames, name) {
			builtins = append(builtins, name)
		}
	}
	toolSet := strings.Join(builtins, ",")
	allowed := "mcp__cocogem__*"
	if request.ExecutionMode == "full_access" && request.TrustedExecution {
		toolSet = "default"
		allowed = "*"
	}
	args := []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--permission-mode", "manual", "--permission-prompts", "host", "--permission-prompt-tool", "stdio",
		"--tools", toolSet,
		"--strict-mcp-config", "--mcp-config", configPath,
		"--allowedTools", allowed,
		"--disallowedTools", "Agent,Task,TeamCreate,SendMessage",
		"--disable-slash-commands", "--no-chrome",
	}
	if request.SessionStorageRoot == "" {
		args = append(args, "--no-session-persistence")
	} else if request.SessionMode == "resume" {
		args = append(args, "--resume", request.SessionID)
	}
	return append(args, "--setting-sources", "", "--settings", settingsPath)
}

func claudeExecutionArgs(configPath, settingsPath string, native []string, request model.Request) []string {
	known := claudeNativeNames
	fullAccess := request.ExecutionMode == "full_access"
	if fullAccess {
		known = claudeFullAccessNames
	}
	builtins := []string{}
	for _, name := range native {
		if slices.Contains(known, name) {
			builtins = append(builtins, name)
		}
	}
	allowed := append(append([]string{}, builtins...), "mcp__cocogem__*")
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	permissionMode := "dontAsk"
	if fullAccess {
		permissionMode = "bypassPermissions"
	} else {
		args = append(args, "--restricted")
	}
	toolSet := strings.Join(builtins, ",")
	if request.TrustedExecution && fullAccess {
		toolSet = "default"
		allowed = []string{"*"}
	}
	args = append(args, "--tools", toolSet, "--strict-mcp-config", "--mcp-config", configPath, "--permission-mode", permissionMode, "--permission-prompts", "none", "--allowedTools", strings.Join(allowed, ","), "--disable-slash-commands", "--no-chrome")
	if request.TrustedExecution && fullAccess {
		args = append(args, "--disallowedTools", "Agent,Task,TeamCreate,SendMessage")
	}
	if request.SessionStorageRoot == "" {
		args = append(args, "--no-session-persistence")
	} else if request.SessionMode == "resume" {
		args = append(args, "--resume", request.SessionID)
	}
	return append(args, "--setting-sources", "", "--settings", settingsPath)
}

// claudeStepSettings adds path denials aligned with tools.DeniedPath. The
// settings directory itself is denied explicitly even when it is not under
// the readable root.
func claudeStepSettings(base map[string]any, configDir string) map[string]any {
	settings := map[string]any{}
	for key, value := range base {
		settings[key] = value
	}
	settings["disableAllHooks"] = true
	settings["enabledPlugins"] = map[string]any{}
	settings["autoMemoryEnabled"] = false
	settings["permissions"] = map[string]any{"deny": claudeDenyRules(configDir)}
	return settings
}

func claudeDenyRules(configDir string) []string {
	patterns := []string{
		"**/.env", "**/.env.*", "**/.env*", "**/.git", "**/.git/**", "**/.cocogem", "**/.cocogem/**",
		"**/node_modules", "**/node_modules/**", "**/dist", "**/dist/**",
		"**/build", "**/build/**", "**/secrets", "**/secrets/**",
		"**/.ssh", "**/.ssh/**", "**/*.pem", "**/*.key", "**/id_rsa*",
		"**/*.sqlite", "**/.git-credentials",
	}
	if absolute, err := filepath.Abs(configDir); err == nil {
		patterns = append(patterns, absolute, filepath.Join(absolute, "**"))
	}
	rules := []string{}
	for _, tool := range []string{nativeRead, nativeGrep, nativeGlob} {
		for _, pattern := range patterns {
			rules = append(rules, tool+"("+pattern+")")
		}
	}
	return rules
}

func resolvePhysicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(parent, filepath.Base(path))
	}
	return path
}

// configInsideRoot reports whether the isolated settings directory would be
// reachable through a native file tool. The native read boundary is enforced
// by the CLI; this guard keeps credentials out of a readable root.
func configInsideRoot(root, dir string) bool {
	if root == "" || dir == "" {
		return false
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return true
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return true
	}
	// Resolve the macOS /var -> /private/var symlink so a physically nested
	// settings directory cannot be mistaken for an external one. A missing
	// target still resolves through its existing parent.
	root, dir = resolvePhysicalPath(root), resolvePhysicalPath(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return true
	}
	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// nativeProxyEnv forwards the resolved public-web proxy to the native child
// only. Loopback MCP stays direct and existing NO_PROXY entries are preserved.
func nativeProxyEnv(proxy, existingNoProxy string) map[string]string {
	if proxy == "" {
		return nil
	}
	noProxy := []string{"127.0.0.1", "localhost", "::1"}
	for _, value := range strings.Split(existingNoProxy, ",") {
		if value = strings.TrimSpace(value); value != "" && !slices.Contains(noProxy, value) {
			noProxy = append(noProxy, value)
		}
	}
	joined := strings.Join(noProxy, ",")
	env := map[string]string{}
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "ALL_PROXY", "all_proxy"} {
		env[key] = proxy
	}
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		env[key] = joined
	}
	return env
}
