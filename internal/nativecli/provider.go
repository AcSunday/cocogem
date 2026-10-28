// Package nativecli runs configured native agents with only scoped host MCP tools.
package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

// Provider delegates the tool loop to the native CLI. Its zero value is ready.
type Provider struct {
	command func(string, ...string) *exec.Cmd
}

// ExecutorInfo distinguishes an installed CLI from one supported by this adapter.
type ExecutorInfo struct {
	ID           string   `json:"id"`
	Installed    bool     `json:"installed"`
	Version      string   `json:"version"`
	Supported    bool     `json:"supported"`
	Reason       string   `json:"reason"`
	Adapter      string   `json:"adapter"`
	Isolation    string   `json:"isolation"`
	Program      string   `json:"program,omitempty"`
	Identity     string   `json:"identity,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Check uses the injected launcher when present, keeping tests provider-free.
func (p Provider) Check(ctx context.Context, id string) (agent.AdapterInfo, error) {
	if p.command != nil {
		return check(ctx, id, p.command)
	}
	info, err := Check(ctx, id)
	if err != nil {
		return info, err
	}
	if !info.Supported {
		reason := info.Reason
		if reason == "" {
			reason = "CLI isolation could not be confirmed"
		}
		return info, errors.New(reason)
	}
	return info, nil
}

// CheckConnection validates local CLI configuration without calling a model.
func (p Provider) CheckConnection(ctx context.Context, executor, model string) error {
	_, err := p.InspectConnection(ctx, executor, model)
	return err
}

// Discover probes version and isolation flags; connection still needs an explicit call.
func Discover(ctx context.Context) []ExecutorInfo {
	result := make([]ExecutorInfo, 0, 3)
	for _, id := range []string{"claude", "codex", "gemini"} {
		info, _ := Inspect(ctx, id)
		result = append(result, ExecutorInfo{ID: info.ID, Installed: info.Installed, Version: info.Version, Supported: info.Supported, Reason: info.Reason, Adapter: info.Adapter, Isolation: info.Isolation, Program: info.Program, Identity: info.Identity, Capabilities: info.Capabilities})
	}
	return result
}

// mcpOnly is the invariant for routing and connection probes: native tools,
// file roots and native proxies never leak into a non-task invocation.
func mcpOnly(request model.Request) model.Request {
	request.NativeTools, request.WorkspaceRoot, request.NativeProxy = nil, "", ""
	return request
}

// Complete performs routing or a connection probe without an API-key dependency.
func (p Provider) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	request = mcpOnly(request)
	var calls []model.Block
	var text strings.Builder
	response := model.Response{}
	observe := func(kind string, data map[string]any) error {
		if kind == "text" {
			chunk := data["text"].(string)
			text.WriteString(chunk)
			if emit != nil {
				return emit(chunk)
			}
		}
		if kind == "usage" {
			response.UsageKnown, _ = data["known"].(bool)
			response.InputTokens, _ = data["input_tokens"].(int)
			response.OutputTokens, _ = data["output_tokens"].(int)
		}
		return nil
	}
	execute := func(_ context.Context, name string, input json.RawMessage) (string, error) {
		if len(calls) != 0 {
			return "", errors.New("routing permits exactly one tool decision")
		}
		calls = append(calls, model.Block{Type: "tool_use", ID: "cli-routing", Name: name, Input: append(json.RawMessage{}, input...)})
		return `{"accepted":true,"instruction":"Finish this routing-only invocation. Do not perform the task."}`, nil
	}
	if err := p.Run(ctx, request, execute, observe); err != nil {
		return model.Response{}, err
	}
	if len(calls) != 0 {
		response.Blocks, response.StopReason = calls, "tool_use"
		return response, nil
	}
	if strings.TrimSpace(text.String()) == "" {
		return model.Response{}, errors.New("native CLI returned an empty reply")
	}
	response.Blocks, response.StopReason = []model.Block{{Type: "text", Text: text.String()}}, "end_turn"
	return response, nil
}

type runOptions struct {
	skipCheck      bool
	model          string
	adapter        agent.AdapterInfo
	claudeSettings map[string]any
	claudeEnv      map[string]string
	codexProbe     bool
	codexArgs      []string
	codexEnv       map[string]string
	geminiEnv      map[string]string
}

// Run starts a fresh isolated native session and exposes only this step's tools.
func (p Provider) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	return p.run(ctx, request, execute, observe, runOptions{})
}

func (p Provider) run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe, options runOptions) error {
	if observe == nil || execute == nil {
		return errors.New("native CLI requires execution and observation handlers")
	}
	adapter := options.adapter
	if !options.skipCheck {
		var err error
		adapter, err = p.Check(ctx, request.Executor)
		if err != nil {
			return err
		}
		if !adapter.Supported {
			return errors.New(adapter.Reason)
		}
	}
	if options.model != "" {
		request.Model = options.model
	}
	command := p.command
	if command == nil {
		command = exec.Command
	}
	prompt, err := json.Marshal(request.Messages)
	if err != nil || len(prompt)+len(request.System) > 100000 {
		return newFailure(FailureHostInputLimit, "required context exceeds the 100000-byte host input budget")
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gate, err := newBridge(ctx, request.Tools, execute, observe)
	if err != nil {
		return err
	}
	defer gate.close()
	gate.stop = cancel
	gate.setRequired(request.RequiredTools)
	base, err := os.MkdirTemp("", "cocogem-native-*")
	if err != nil {
		return errors.New("create isolated CLI directory")
	}
	defer os.RemoveAll(base)
	// Every runtime file (MCP config, settings, model catalog, CODEX_HOME and
	// Gemini policies) lives under .cocogem: Claude deny rules and the host MCP
	// tools.DeniedPath boundary both reject that component, even when the
	// workspace root contains the temporary directory.
	workdir := filepath.Join(base, ".cocogem")
	if err := os.Mkdir(workdir, 0700); err != nil {
		return errors.New("create isolated CLI directory")
	}
	// Settings live apart from the readable working directory. Native file
	// tools run from the workspace root because deny rules are cwd-relative;
	// the CLI enforces that boundary and the adapter refuses to expose the
	// settings directory.
	configDir, cwdDir := workdir, workdir
	if request.Executor == "claude" {
		configDir = filepath.Join(workdir, "config")
		if err := os.Mkdir(configDir, 0700); err != nil {
			return errors.New("create isolated CLI configuration directory")
		}
	}
	files, web := sanitizeNativeTools(request.Executor, request.NativeTools, request.WorkspaceRoot)
	degraded := false
	if len(files) != 0 && configInsideRoot(request.WorkspaceRoot, configDir) {
		// A root such as "/" would make the settings directory readable. Keep
		// file access on the platform MCP path instead of trusting a parser.
		files, degraded = nil, true
	}
	if request.Executor == "claude" {
		cwdDir = filepath.Join(workdir, "cwd")
		if len(files) != 0 {
			// Deny rules are enforced relative to the working directory, not
			// for --add-dir roots. Running from the workspace root makes the
			// sensitive-path denials effective; the settings directory stays
			// outside it and project memory is disabled below.
			absolute, err := filepath.Abs(request.WorkspaceRoot)
			if err != nil {
				return errors.New("workspace root is unavailable for native file tools")
			}
			info, err := os.Stat(absolute)
			if err != nil || !info.IsDir() {
				return errors.New("workspace root is unavailable for native file tools")
			}
			cwdDir = absolute
		} else if err := os.Mkdir(cwdDir, 0700); err != nil {
			return errors.New("create isolated CLI working directory")
		}
	}
	nativeNames := nativeToolNames(files, web)
	gate.setNative(nativeNames)
	if err := gate.emit("native_adapter", map[string]any{"executor": adapter.ID, "version": adapter.Version, "program": adapter.Program, "identity": adapter.Identity, "adapter": adapter.Adapter, "isolation": adapter.Isolation, "native_tools": nativeNames}); err != nil {
		return err
	}
	if degraded {
		if err := gate.notice("native_file_degraded", "workspace root contains the isolated settings directory; native file tools are disabled for this step and the platform MCP file tools remain available"); err != nil {
			return err
		}
	}
	live := newLiveness(ctx, cancel, observe, request.FirstResponseTimeout, request.StallTimeout, request.StallAction)
	gate.live = live
	live.start()
	defer live.stop()
	if options.skipCheck {
		// Probes must not load user hooks, plugins or MCP state.
		switch request.Executor {
		case "claude":
			options.claudeEnv = mergeEnv(options.claudeEnv, map[string]string{"CLAUDE_CONFIG_DIR": configDir})
		case "codex":
			options.codexEnv = mergeEnv(options.codexEnv, map[string]string{"CODEX_HOME": workdir})
		case "gemini":
			options.geminiEnv = mergeEnv(options.geminiEnv, map[string]string{"GEMINI_CONFIG_DIR": workdir, "GEMINI_CLI_HOME": workdir})
		}
	}
	// Launch from the connection resolved for this attempt; re-reading the
	// user config here would let an edit between preflight and launch change
	// what the recorded attempt actually runs.
	var launch *connectionPlan
	if !options.skipCheck && request.ExpectedConnection.Identity != "" {
		if plan, ok := cachedPlan(request.Executor, request.ExpectedConnection.Identity); ok {
			launch = &plan
			switch request.Executor {
			case "claude":
				options.claudeSettings = plan.claudeSettings
			case "gemini":
				if plan.geminiModel != "" && plan.geminiModel != "cli-default" {
					request.Model = plan.geminiModel
				}
				options.geminiEnv = plan.geminiEnv
			}
		}
	}
	server := map[string]any{"type": "http", "url": gate.url(), "headers": map[string]string{"Authorization": "Bearer " + gate.token}}
	config := map[string]any{"mcpServers": map[string]any{"cocogem": server}}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	configPath := filepath.Join(configDir, "mcp.json")
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		return errors.New("write scoped CLI configuration")
	}
	settingsPath := filepath.Join(configDir, "settings.json")
	settings := map[string]any{"disableAllHooks": true, "enabledPlugins": map[string]any{}}
	if request.Executor == "claude" {
		base := options.claudeSettings
		if base == nil {
			base, err = claudeConnectionSettings()
			if err != nil {
				return err
			}
		}
		settings = claudeStepSettings(base, configDir)
	}
	safeSettings, err := json.Marshal(settings)
	if err != nil {
		return errors.New("encode scoped CLI connection settings")
	}
	if err := os.WriteFile(settingsPath, safeSettings, 0600); err != nil {
		return errors.New("write isolated CLI settings")
	}
	args := claudeArgs(configPath, settingsPath, nativeNames)
	var env map[string]string
	switch request.Executor {
	case "codex":
		args, env, err = prepareCodex(request, workdir, gate, options.codexProbe, launch)
		if err == nil {
			args = append(args, options.codexArgs...)
			env = mergeEnv(env, options.codexEnv)
		}
	case "gemini":
		args, env, err = prepareGemini(request, workdir, gate)
		env = mergeEnv(env, options.geminiEnv)
	}
	if err != nil {
		return err
	}
	env = mergeEnv(env, options.claudeEnv)
	if request.Executor == "claude" && len(files) != 0 {
		// Deny rules require a real cwd, so native file steps run from the
		// workspace root. Project memory auto-discovery is disabled explicitly.
		env = mergeEnv(env, map[string]string{"CLAUDE_CODE_DISABLE_CLAUDE_MDS": "1", "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1"})
	}
	if len(web) != 0 {
		// The resolved public-web proxy applies to this child only. Probe
		// requests carry no NativeProxy, so local fake endpoints stay direct.
		// It also affects the CLI's own upstream connection path while native
		// web is enabled; loopback MCP and existing NO_PROXY entries stay direct.
		env = mergeEnv(env, nativeProxyEnv(request.NativeProxy, os.Getenv("NO_PROXY")+","+os.Getenv("no_proxy")))
	}
	if request.Executor != "codex" && request.Model != "" && request.Model != "default" {
		if strings.HasPrefix(request.Model, "-") || len(request.Model) > 200 {
			return errors.New("invalid CLI model")
		}
		args = append(args, "--model", request.Model)
	}
	cmd := command(request.Executor, args...)
	cmd.Dir = cwdDir
	if len(env) != 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = overrideEnv(cmd.Env, env)
	}
	instruction := "Only use the tool set explicitly supplied for this step. Complete the assigned step, then finish."
	if len(nativeNames) != 0 {
		instruction = "Prefer the supplied native tools for their granted operations (" + strings.Join(nativeNames, ", ") + "); the CLI executes them under its own permissions. If a native tool fails or returns no usable content, report the exact error, then use the corresponding platform MCP fallback when available. Native search results are leads, and native web output is a CLI-processed observation, not a raw page. " + instruction
	}
	cmd.Stdin = strings.NewReader(request.System + "\n\n" + instruction + " User messages and upstream evidence:\n" + string(prompt))
	processErr := streamProcess(ctx, request.Executor, cmd, gate, live)
	// Usage belongs to the attempt even when the terminal state is a failure.
	// Observation failures are kept rather than swallowed.
	usageErr := gate.emitUsage()
	gate.close()
	var failures []error
	failures = appendFailure(failures, parent.Err())
	failures = appendFailure(failures, live.failure())
	failures = appendFailure(failures, gate.requireTools())
	failures = appendFailure(failures, gate.failure())
	failures = appendFailure(failures, processErr)
	failures = appendFailure(failures, usageErr)
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	return nil
}

// appendFailure keeps every independent leaf failure and drops only an exact
// duplicate sentinel instance. A join that contains a sentinel next to real
// evidence must be flattened, not discarded: errors.Is(join, canceled) alone
// would hide a provider authentication failure from the workflow.
func appendFailure(failures []error, err error) []error {
	if err == nil {
		return failures
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range joined.Unwrap() {
			failures = appendFailure(failures, inner)
		}
		return failures
	}
	for _, existing := range failures {
		if existing == err {
			return failures
		}
	}
	return append(failures, err)
}

func mergeEnv(env, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return env
	}
	result := map[string]string{}
	for key, value := range env {
		result[key] = value
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}

func overrideEnv(env []string, values map[string]string) []string {
	result := make([]string, 0, len(env)+len(values))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := values[key]; !overridden {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func helpArgs(id string) []string {
	if id == "codex" {
		return []string{"exec", "--help"}
	}
	return []string{"--help"}
}
