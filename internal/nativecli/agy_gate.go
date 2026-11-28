package nativecli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/AcSunday/cocogem/internal/tools"
)

type agyGatePolicy struct {
	allowedNative map[string]bool
	allowedRoots  []string
	mcpSchemaDir  string
	readOnly      bool
	fullAccess    bool
}

func newAgyGatePolicy(files, web, roots []string, mcpSchemaDir string, readOnly, fullAccess bool) *agyGatePolicy {
	allowed := map[string]bool{}
	for _, logical := range files {
		switch logical {
		case nativeRead:
			allowed["read_file"] = true
			allowed["view_file"] = true
			allowed["view_file_outline"] = true
			allowed["view_code_item"] = true
		case nativeGlob:
			allowed["find_by_name"] = true
			allowed["list_dir"] = true
		case nativeGrep:
			allowed["grep_search"] = true
		case nativeWrite:
			if !readOnly {
				allowed["write_file"] = true
				allowed["write_to_file"] = true
			}
		case nativeEdit:
			if !readOnly {
				allowed["edit_file"] = true
				allowed["replace_file_content"] = true
				allowed["multi_replace_file_content"] = true
				allowed["sed_file"] = true
				allowed["notebook_edit"] = true
			}
		case nativeBash:
			if !readOnly {
				allowed["run_command"] = true
				allowed["notebook_execution"] = true
				allowed["command_status"] = true
				allowed["send_command_input"] = true
				allowed["read_terminal"] = true
			}
		}
	}
	for _, logical := range web {
		switch logical {
		case nativeWebSearch:
			allowed["search_web"] = true
		case nativeWebFetch:
			allowed["read_url_content"] = true
		}
	}
	return &agyGatePolicy{
		allowedNative: allowed,
		allowedRoots:  append([]string{}, roots...),
		mcpSchemaDir:  mcpSchemaDir,
		readOnly:      readOnly,
		fullAccess:    fullAccess,
	}
}

func (p *agyGatePolicy) isMCPSchemaRead(args json.RawMessage, cwd string) bool {
	if p == nil || p.mcpSchemaDir == "" {
		return false
	}
	var values map[string]any
	if json.Unmarshal(args, &values) != nil || values == nil {
		return false
	}
	rawPath := ""
	for _, key := range []string{"Path", "AbsolutePath"} {
		if v, ok := values[key].(string); ok && strings.TrimSpace(v) != "" {
			rawPath = v
			break
		}
	}
	if rawPath == "" {
		return false
	}
	resolved := resolveTarget(rawPath, cwd)
	if resolved == "" || !strings.HasSuffix(strings.ToLower(resolved), ".json") {
		return false
	}
	return scopeCovers(p.mcpSchemaDir, resolved)
}

type agyHookPayload struct {
	HookEventName  string `json:"hookEventName"`
	ConversationID string `json:"conversationId"`
	Cwd            string `json:"cwd"`
	StepIdx        int    `json:"stepIdx"`
	Error          string `json:"error"`
	ToolCall       struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"toolCall"`
}

func (p agyHookPayload) canonicalCallID() string {
	id := strings.TrimSpace(p.ToolCall.ID)
	if id == "" {
		id = fmt.Sprintf("step-%d-%s", p.StepIdx, safeToken(p.ToolCall.Name))
	}
	return nativeToolID("agy", id)
}

func writeAgyHookDeny(w http.ResponseWriter, reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "tool execution is not authorized for this step"
	}
	writeGateJSON(w, map[string]any{"decision": "deny", "reason": reason})
}

func writeAgyHookAllow(w http.ResponseWriter, rewritten json.RawMessage) {
	if len(bytes.TrimSpace(rewritten)) == 0 {
		writeGateJSON(w, map[string]any{"decision": "allow"})
		return
	}
	var overwrite map[string]any
	if json.Unmarshal(rewritten, &overwrite) != nil || overwrite == nil {
		writeAgyHookDeny(w, "rewritten native tool input was invalid")
		return
	}
	writeGateJSON(w, map[string]any{"decision": "allow", "overwrite": overwrite})
}

func (b *gateBroker) serveAgyAuthorize(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var payload agyHookPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil || strings.TrimSpace(payload.ToolCall.Name) == "" {
		writeAgyHookDeny(w, "invalid Antigravity PreToolUse hook payload")
		return
	}
	if b.agyPolicy == nil {
		writeAgyHookDeny(w, "Antigravity tool policy is unavailable")
		return
	}
	toolName := payload.ToolCall.Name
	args := bytes.TrimSpace(payload.ToolCall.Args)
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if toolName == "call_mcp_tool" {
		var mcpArgs struct {
			ServerName string `json:"ServerName"`
			ToolName   string `json:"ToolName"`
		}
		if json.Unmarshal(args, &mcpArgs) != nil || mcpArgs.ServerName != "cocogem" || strings.TrimSpace(mcpArgs.ToolName) == "" {
			writeAgyHookDeny(w, "only the scoped cocogem MCP server is permitted")
			return
		}
		writeAgyHookAllow(w, nil)
		return
	}
	if (toolName == "read_file" || toolName == "view_file") && b.agyPolicy.isMCPSchemaRead(args, b.cwd) {
		writeAgyHookAllow(w, nil)
		return
	}
	if !b.agyPolicy.allowedNative[toolName] && !(b.agyPolicy.fullAccess && !b.agyPolicy.readOnly && b.approver != nil) {
		writeAgyHookDeny(w, fmt.Sprintf("tool %s is outside this step's permissions", safeToken(toolName)))
		return
	}
	call, err := classifyNativeCallForGate("agy", toolName, args, b.cwd)
	if err != nil {
		writeAgyHookDeny(w, err.Error())
		return
	}
	if b.agyPolicy.readOnly && (call.Action == "write" || call.Action == "execute") {
		writeAgyHookDeny(w, "read-only step does not permit writes or commands")
		return
	}
	if b.approver != nil {
		canonicalID := payload.canonicalCallID()
		decision := b.authorize(r.Context(), gateRequest{CallID: canonicalID, Name: toolName, Input: args})
		if !decision.Allow {
			writeAgyHookDeny(w, decision.Reason)
			return
		}
		if len(decision.Input) > 0 && !bytes.Equal(bytes.TrimSpace(decision.Input), args) {
			writeAgyHookAllow(w, decision.Input)
			return
		}
		writeAgyHookAllow(w, nil)
		return
	}
	// Non-autonomous mode: only explicitly granted read/glob/grep/web tools may run,
	// and file targets must stay inside the workspace/task roots without hitting sensitive paths.
	if call.Action == "write" || call.Action == "execute" {
		writeAgyHookDeny(w, "autonomous native writes and commands are disabled for this step")
		return
	}
	if call.Spec.network {
		writeAgyHookAllow(w, nil)
		return
	}
	target := filepath.Clean(call.Target)
	if target == "" || !withinAnyRoot(target, b.agyPolicy.allowedRoots) || tools.DeniedPath(target) {
		writeAgyHookDeny(w, "native file path is outside the allowed step scope")
		return
	}
	writeAgyHookAllow(w, nil)
}

func (b *gateBroker) serveAgyFinalize(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var payload agyHookPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid finalize payload", http.StatusBadRequest)
		return
	}
	_ = b.finalize(payload.canonicalCallID(), strings.TrimSpace(payload.Error) != "")
	writeGateJSON(w, map[string]any{})
}
