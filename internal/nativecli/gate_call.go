package nativecli

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// Installed native tool schemas. Classification must use the exact fields the
// CLI actually sends so a missing field can never default to the project
// directory and auto-allow an outside read.
type gateToolSpec struct {
	// action is the workspace action used in the permission request.
	action string
	// pathKeys are the tool's target fields, in priority order. The first one
	// present identifies the exact resource.
	pathKeys []string
	// commandKey is the field holding a shell command.
	commandKey string
	// network marks a tool whose effect is a public read, not a file target.
	network bool
}

// piNativeToolSpecs covers the built-in Pi tools the gate extension guards.
var piNativeToolSpecs = map[string]gateToolSpec{
	"read":  {action: "read", pathKeys: []string{"path"}},
	"write": {action: "write", pathKeys: []string{"path"}},
	"edit":  {action: "write", pathKeys: []string{"path"}},
	"bash":  {action: "execute", commandKey: "command"},
	"grep":  {action: "read", pathKeys: []string{"path"}},
	"find":  {action: "read", pathKeys: []string{"path"}},
	"ls":    {action: "read", pathKeys: []string{"path"}},
}

// agyNativeToolSpecs covers the built-in Antigravity CLI tools guarded by PreToolUse.
var agyNativeToolSpecs = map[string]gateToolSpec{
	"read_file":                  {action: "read", pathKeys: []string{"Path"}},
	"view_file":                  {action: "read", pathKeys: []string{"AbsolutePath"}},
	"view_file_outline":          {action: "read", pathKeys: []string{"AbsolutePath"}},
	"view_code_item":             {action: "read", pathKeys: []string{"File"}},
	"find_by_name":               {action: "read", pathKeys: []string{"SearchDirectory"}},
	"list_dir":                   {action: "read", pathKeys: []string{"DirectoryPath"}},
	"grep_search":                {action: "read", pathKeys: []string{"SearchPath"}},
	"write_file":                 {action: "write", pathKeys: []string{"Path"}},
	"write_to_file":              {action: "write", pathKeys: []string{"TargetFile"}},
	"edit_file":                  {action: "write", pathKeys: []string{"Path"}},
	"replace_file_content":       {action: "write", pathKeys: []string{"TargetFile"}},
	"multi_replace_file_content": {action: "write", pathKeys: []string{"TargetFile"}},
	"sed_file":                   {action: "write", pathKeys: []string{"TargetFile"}},
	"notebook_edit":              {action: "write", pathKeys: []string{"Path"}},
	"run_command":                {action: "execute", commandKey: "CommandLine", pathKeys: []string{"Cwd"}},
	"notebook_execution":         {action: "execute", commandKey: "CommandLine", pathKeys: []string{"Cwd"}},
	"search_web":                 {action: "network", network: true},
	"read_url_content":           {action: "network", network: true},
	"command_status":             {action: "read"},
	"send_command_input":         {action: "execute"},
	"read_terminal":              {action: "read"},
}

// gateCall is one classified pre-effect call. Input is the CLI's original
// input; Normalized is the copy the shared authorizer sees, with the target
// written into `file_path` so an adapter-specific field can never be missed.
type gateCall struct {
	Name       string
	Spec       gateToolSpec
	Action     string
	Target     string
	PathKey    string
	Input      json.RawMessage
	Normalized json.RawMessage
	HostTool   bool
}

// classifyNativeCallForGate resolves one call against the installed schema of
// its adapter. An unknown non-host tool is an error, never a pass-through.
func classifyNativeCallForGate(adapter, name string, input json.RawMessage, cwd string) (gateCall, error) {
	call := gateCall{Name: name, Input: input, Normalized: input}
	if isHostMCPToolName(name) {
		// Host MCP tools are validated by the bridge's own schema and tool
		// permission path; the native gate must not shadow them.
		call.HostTool, call.Action = true, "host"
		return call, nil
	}
	var specs map[string]gateToolSpec
	switch adapter {
	case "pi":
		specs = piNativeToolSpecs
	case "agy":
		specs = agyNativeToolSpecs
	default:
		return call, errors.New("unsupported native gate adapter")
	}
	spec, ok := specs[name]
	if !ok {
		return call, errors.New("the native CLI requested a tool this step did not declare")
	}
	call.Spec, call.Action = spec, spec.action
	if spec.network {
		return call, nil
	}
	var values map[string]any
	if len(input) != 0 {
		if err := json.Unmarshal(input, &values); err != nil || values == nil {
			return call, errors.New("the native tool input is not an object")
		}
	}
	if spec.commandKey != "" {
		command, _ := values[spec.commandKey].(string)
		if strings.TrimSpace(command) == "" {
			return call, errors.New("the native command is empty")
		}
	}
	// The declared directory argument is a real resource for shell and search
	// tools; ignoring it would hide an outside target.
	for _, key := range spec.pathKeys {
		raw, exists := values[key]
		if !exists {
			continue
		}
		text, ok := raw.(string)
		if !ok {
			return call, errors.New("the native tool path is not a string")
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		call.PathKey = key
		call.Target = resolveTarget(text, cwd)
		break
	}
	if spec.commandKey == "" && call.Target == "" && len(spec.pathKeys) != 0 {
		// A file tool must name its target; defaulting to "." would hide the
		// real resource from the host.
		return call, errors.New("the native file tool did not name a path")
	}
	if call.Target != "" {
		normalized := map[string]any{}
		for key, value := range values {
			normalized[key] = value
		}
		normalized["file_path"] = call.Target
		encoded, err := json.Marshal(normalized)
		if err != nil {
			return call, errors.New("the native tool input could not be normalized")
		}
		call.Normalized = encoded
	}
	return call, nil
}

// isHostMCPToolName reports a host-owned MCP tool. Both CLIs prefix MCP tools,
// so the native gate passes them to the bridge instead of classifying them.
func isHostMCPToolName(name string) bool {
	return strings.HasPrefix(name, "mcp_") || strings.HasPrefix(name, "mcp__")
}

// rewriteNativePath replaces the tool's own path field with the exact path the
// host prepared, leaving every other argument byte-identical.
func rewriteNativePath(input json.RawMessage, key, path string) (json.RawMessage, error) {
	if key == "" {
		return nil, errors.New("the native tool input has no path the host can narrow")
	}
	var values map[string]any
	if err := json.Unmarshal(input, &values); err != nil || values == nil {
		return nil, errors.New("the native tool input is not an object")
	}
	if _, ok := values[key].(string); !ok {
		return nil, errors.New("the native tool path field is missing")
	}
	values[key] = path
	return json.Marshal(values)
}

// gateScopeTarget is the resource a rewrite may use for an approved outside
// path: the exact file for file tools, the approved directory for a command.
func gateScopeTarget(call gateCall) string {
	if call.Target == "" {
		return ""
	}
	return filepath.Clean(call.Target)
}
