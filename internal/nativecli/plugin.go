package nativecli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// pluginDecision is the one-time result of an execute.before gate. The private
// OpenCode plugin asks before a tool runs, then calls /plugin/execute only for
// native commands; the host executes that command under a per-call profile.
type pluginDecision struct {
	allow  bool
	read   []string
	write  []string
	cwd    string
	tool   string
	name   string
	input  json.RawMessage
	digest string
}

const pluginOutputLimit = 256 << 10

func (b *bridge) pluginAuthorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return subtleConstantTimeEqual(strings.TrimPrefix(header, prefix), b.pluginToken)
}

func (b *bridge) servePlugin(w http.ResponseWriter, r *http.Request) {
	if !b.pluginAuthorized(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
	if err != nil {
		http.Error(w, "invalid plugin request", http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/plugin/permission":
		b.pluginPermission(w, r.Context(), raw)
	case "/plugin/execute":
		b.pluginExecute(w, r.Context(), raw)
	case "/plugin/file":
		b.pluginFile(w, r.Context(), raw)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (b *bridge) pluginPermission(w http.ResponseWriter, ctx context.Context, raw []byte) {
	var request struct {
		ID    string          `json:"id"`
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &request) != nil || request.ID == "" || request.Tool == "" {
		http.Error(w, "invalid permission request", http.StatusBadRequest)
		return
	}
	if b.approver == nil {
		writePluginJSON(w, map[string]any{"allow": false, "reason": "native permission callback is unavailable"})
		return
	}
	name, input, cwd := openCodePluginCall(request.Tool, request.Input, b.cwd)
	authorization, err := b.approver.authorize(ctx, "cli:"+request.ID, name, input, "")
	if err != nil {
		writePluginJSON(w, map[string]any{"allow": false, "reason": err.Error()})
		return
	}
	if !authorization.Allow {
		reason := authorization.Reason
		if reason == "" {
			reason = "用户拒绝或授权已取消"
		}
		writePluginJSON(w, map[string]any{"allow": false, "reason": reason})
		return
	}
	b.mu.Lock()
	if len(b.pluginDecisions) > 64 {
		b.pluginDecisions = map[string]*pluginDecision{}
	}
	b.pluginDecisions["cli:"+request.ID] = &pluginDecision{
		allow: true, read: authorization.ReadScopes, write: authorization.WriteScopes,
		cwd: cwd, tool: request.Tool, name: name, input: append(json.RawMessage{}, input...), digest: jsonDigest(input),
	}
	b.mu.Unlock()
	writePluginJSON(w, map[string]any{"allow": true})
}

func (b *bridge) pluginExecute(w http.ResponseWriter, ctx context.Context, raw []byte) {
	var request struct {
		ID    string          `json:"id"`
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &request) != nil || request.ID == "" || request.Tool == "" || len(request.Input) == 0 {
		http.Error(w, "invalid execute request", http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	decision := b.pluginDecisions["cli:"+request.ID]
	delete(b.pluginDecisions, "cli:"+request.ID)
	b.mu.Unlock()
	if decision == nil || !decision.allow || request.Tool != decision.tool || jsonDigest(request.Input) != decision.digest {
		writePluginJSON(w, map[string]any{"ok": false, "error": "the command was not authorized for this exact operation"})
		return
	}
	var operation struct {
		Command string `json:"command"`
		Workdir string `json:"workdir"`
		CWD     string `json:"cwd"`
	}
	_ = json.Unmarshal(decision.input, &operation)
	cwd := firstString(operation.Workdir, operation.CWD, decision.cwd, b.cwd)
	command := operation.Command
	if command == "" {
		writePluginJSON(w, map[string]any{"ok": false, "error": "the approved command is empty"})
		return
	}
	if b.sandbox != nil && b.sandbox.canWrap() {
		profile, err := b.sandbox.profile("plugin-"+safeToken(request.ID), decision.read, decision.write)
		if err != nil {
			writePluginJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		command = b.sandbox.wrap(profile, cwd, command)
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/bin/bash", "--noprofile", "--norc", "-c", command)
	cmd.Dir = cwd
	output := &boundedBuffer{limit: pluginOutputLimit, truncate: true}
	cmd.Stdout, cmd.Stderr = output, output
	started := time.Now()
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			exitCode = exit.ExitCode()
		} else {
			writePluginJSON(w, map[string]any{"ok": false, "error": "command could not start"})
			return
		}
	}
	writePluginJSON(w, map[string]any{
		"ok": true, "output": output.String(), "exit_code": exitCode,
		"duration_ms": time.Since(started).Milliseconds(),
	})
}

func openCodePluginCall(tool string, raw json.RawMessage, fallbackCWD string) (name string, input json.RawMessage, cwd string) {
	var values map[string]any
	_ = json.Unmarshal(raw, &values)
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := values[key].(string); ok && value != "" {
				return value
			}
		}
		return ""
	}
	cwd = pick("workdir", "cwd")
	if cwd == "" {
		cwd = fallbackCWD
	}
	switch strings.ToLower(tool) {
	case "bash", "shell", "execute", "run":
		return "Bash", append(json.RawMessage{}, raw...), cwd
	case "read":
		return "Read", append(json.RawMessage{}, raw...), cwd
	case "write", "edit", "patch":
		return "Write", append(json.RawMessage{}, raw...), cwd
	case "webfetch", "fetch", "websearch", "web_search":
		return "WebFetch", append(json.RawMessage{}, raw...), cwd
	default:
		return "Native", append(json.RawMessage{}, raw...), cwd
	}
}

func jsonDigest(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) == nil {
		if encoded, err := json.Marshal(value); err == nil {
			raw = encoded
		}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func pluginJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func writePluginJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func (b *bridge) pluginFile(w http.ResponseWriter, _ context.Context, raw []byte) {
	var request struct {
		ID    string          `json:"id"`
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &request) != nil || request.ID == "" || request.Tool == "" || len(request.Input) == 0 {
		http.Error(w, "invalid file request", http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	decision := b.pluginDecisions["cli:"+request.ID]
	delete(b.pluginDecisions, "cli:"+request.ID)
	b.mu.Unlock()
	if decision == nil || !decision.allow || request.Tool != decision.tool || jsonDigest(request.Input) != decision.digest {
		writePluginJSON(w, map[string]any{"ok": false, "error": "the file operation was not authorized for this exact operation"})
		return
	}
	var operation struct {
		OP        string `json:"op"`
		Path      string `json:"path"`
		FilePath  string `json:"filePath"`
		FileAlias string `json:"file_path"`
		Content   string `json:"content"`
		Old       string `json:"oldString"`
		New       string `json:"newString"`
	}
	_ = json.Unmarshal(decision.input, &operation)
	op := strings.ToLower(request.Tool)
	path := firstString(operation.Path, operation.FilePath, operation.FileAlias)
	if path == "" {
		writePluginJSON(w, map[string]any{"ok": false, "error": "the approved file path is empty"})
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.cwd, path)
	}
	path = resolvePhysicalPath(path)
	if b.approver != nil && b.approver.dangerous[path] {
		writePluginJSON(w, map[string]any{"ok": false, "error": "the path aliases an inode linked outside the project"})
		return
	}
	writeScope := op == "write" || op == "edit"
	if !b.pluginPathAllowed(path, decision, writeScope) {
		writePluginJSON(w, map[string]any{"ok": false, "error": "the path is outside the approved scope"})
		return
	}
	switch op {
	case "read":
		file, err := os.Open(path)
		if err != nil {
			writePluginJSON(w, map[string]any{"ok": false, "error": "the file could not be opened"})
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
		if err != nil || len(data) > 1<<20 {
			writePluginJSON(w, map[string]any{"ok": false, "error": "the file exceeds the bounded read"})
			return
		}
		writePluginJSON(w, map[string]any{"ok": true, "content": string(data)})
	case "write":
		if err := os.WriteFile(path, []byte(operation.Content), 0600); err != nil {
			writePluginJSON(w, map[string]any{"ok": false, "error": "the file could not be written"})
			return
		}
		writePluginJSON(w, map[string]any{"ok": true, "content": "wrote " + path})
	case "edit":
		data, err := os.ReadFile(path)
		if err != nil {
			writePluginJSON(w, map[string]any{"ok": false, "error": "the file could not be read"})
			return
		}
		if operation.Old == "" || !strings.Contains(string(data), operation.Old) {
			writePluginJSON(w, map[string]any{"ok": false, "error": "the edit target was not found"})
			return
		}
		updated := strings.Replace(string(data), operation.Old, operation.New, 1)
		if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
			writePluginJSON(w, map[string]any{"ok": false, "error": "the file could not be written"})
			return
		}
		writePluginJSON(w, map[string]any{"ok": true, "content": "edited " + path})
	default:
		writePluginJSON(w, map[string]any{"ok": false, "error": "unsupported file operation"})
	}
}

func (b *bridge) pluginPathAllowed(path string, decision *pluginDecision, write bool) bool {
	if b.approver != nil && withinAnyRoot(path, b.approver.roots) {
		return true
	}
	scopes := decision.read
	if write {
		scopes = decision.write
	}
	for _, scope := range scopes {
		if scopeCovers(scope, path) {
			return true
		}
	}
	return false
}
