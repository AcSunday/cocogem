// Package nativecli runs configured native agents with only scoped host MCP tools.
package nativecli

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
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
	result := make([]ExecutorInfo, 0, 5)
	for _, id := range []string{"claude", "codex", "gemini", "pi", "opencode"} {
		info, _ := Inspect(ctx, id)
		result = append(result, ExecutorInfo{ID: info.ID, Installed: info.Installed, Version: info.Version, Supported: info.Supported, Reason: info.Reason, Adapter: info.Adapter, Isolation: info.Isolation, Program: info.Program, Identity: info.Identity, Capabilities: info.Capabilities})
	}
	return result
}

// mcpOnly is the invariant for routing and connection probes: native tools,
// file roots and native proxies never leak into a non-task invocation.
func mcpOnly(request model.Request) model.Request {
	request.TrustedExecution, request.WorkingDirectory = false, ""
	request.NativeTools, request.WorkspaceRoot, request.NativeProxy = nil, "", ""
	request.ExecutionMode, request.SessionMode, request.SessionID = "", "", ""
	request.SessionStorageRoot, request.SessionContextEpoch = "", 0
	request.AuthorizeNative = nil
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
	skipCheck        bool
	model            string
	adapter          agent.AdapterInfo
	claudeSettings   map[string]any
	claudeEnv        map[string]string
	codexProbe       bool
	codexArgs        []string
	codexEnv         map[string]string
	geminiEnv        map[string]string
	geminiUnset      []string
	piProbeURL       string
	openCodeProbeURL string
	// Test-only local egress fixture controls. Production never sets these.
	allowLocalEgress bool
	egressRoots      *x509.CertPool
}

// Run starts a fresh isolated native session and exposes only this step's tools.
func (p Provider) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	return p.run(ctx, request, execute, observe, runOptions{})
}

func (p Provider) run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe, options runOptions) error {
	if observe == nil || execute == nil {
		return errors.New("native CLI requires execution and observation handlers")
	}
	if request.ExecutionMode == "project_autonomous" && request.AuthorizeNative != nil {
		switch request.Executor {
		case "gemini":
			return p.RunGeminiNative(ctx, request, execute, observe)
		case "pi":
			if request.ExpectedConnection.Identity != "" {
				if _, ok := cachedPlan("pi", request.ExpectedConnection.Identity); !ok {
					info, err := p.InspectExecutionConnection(ctx, "pi", request.Model, request.ThinkingLevel)
					if err != nil {
						return err
					}
					if info.Identity != request.ExpectedConnection.Identity {
						return errors.New("Pi connection changed before the gated launch")
					}
				}
			}
			return p.RunPiNative(ctx, request, execute, observe)
		}
	}
	if err := w.ValidateExecutionOptions(request.Executor, request.ContextSize, request.ThinkingLevel); err != nil {
		return err
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
	if err != nil {
		return newFailure(FailureHostInputLimit, "required context could not be encoded for the native CLI")
	}
	if budget := model.InputTokenBudget(request.ContextSize); budget > 0 && model.EstimateTokens(string(prompt))+model.EstimateTokens(request.System) > budget {
		return newFailure(FailureHostInputLimit, "required context exceeds the %d-token host input budget", budget)
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gate, err := newBridge(ctx, request.Tools, execute, observe)
	if err != nil {
		return err
	}
	defer gate.close()
	defer func() {
		if gate.modelGateway != nil {
			gate.modelGateway.close()
		}
	}()
	gate.stop = cancel
	gate.trustedNative = request.TrustedExecution && request.ExecutionMode == "full_access"
	gate.setRequired(request.RequiredTools)
	gate.sessionMode, gate.sessionID = request.SessionMode, request.SessionID
	managed := request.SessionStorageRoot != ""
	if managed && request.Executor != "claude" && request.Executor != "opencode" {
		return errors.New("this CLI does not support a Cocogem-managed resumable session")
	}
	if request.SessionMode == "resume" && (!managed || request.SessionID == "") {
		return errors.New("a managed session resume requires its exact native session identity")
	}
	base := request.SessionStorageRoot
	if managed {
		if err := os.MkdirAll(base, 0700); err != nil {
			return errors.New("create private managed CLI session directory")
		}
		if err := os.Chmod(base, 0700); err != nil {
			return errors.New("secure private managed CLI session directory")
		}
		if info, err := os.Lstat(base); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed CLI session directory is not private")
		}
	} else {
		var err error
		base, err = os.MkdirTemp("", "cocogem-native-*")
		if err != nil {
			return errors.New("create isolated CLI directory")
		}
		defer os.RemoveAll(base)
	}
	// Every runtime file (MCP config, settings, model catalog, CODEX_HOME and
	// Gemini policies) lives under .cocogem. Managed mode preserves only this
	// server-owned directory; controlled mode removes it after the invocation.
	workdir := filepath.Join(base, ".cocogem")
	if err := os.MkdirAll(workdir, 0700); err != nil {
		return errors.New("create isolated CLI directory")
	}
	// Settings live apart from the readable working directory. Native file
	// tools run from the workspace root because deny rules are cwd-relative;
	// the CLI enforces that boundary and the adapter refuses to expose the
	// settings directory.
	configDir, cwdDir := workdir, workdir
	if request.Executor == "claude" {
		configDir = filepath.Join(workdir, "config")
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return errors.New("create isolated CLI configuration directory")
		}
	}
	if request.Executor == "opencode" && gate.approver != nil {
		gate.pluginTokenFile = filepath.Join(workdir, "plugin-token")
		if err := os.WriteFile(gate.pluginTokenFile, []byte(gate.pluginToken), 0600); err != nil {
			return errors.New("write private OpenCode plugin token")
		}
		tokenFile := gate.pluginTokenFile
		time.AfterFunc(30*time.Second, func() { _ = os.Remove(tokenFile) })
	}
	fullAccess := request.ExecutionMode == "full_access"
	interactive := request.Executor == "claude" && (request.ExecutionMode == "project_autonomous" || request.ExecutionMode == "full_access" && request.AuthorizeNative != nil)
	boundary := (request.Executor == "claude" || request.Executor == "opencode") && request.ExecutionMode == "project_autonomous"
	codexAppServer := request.Executor == "codex" && (request.ExecutionMode == "project_autonomous" || request.ExecutionMode == "full_access") && request.AuthorizeNative != nil
	var sandbox *nativeSandbox
	var approver *nativeApprover
	var dangerous []string
	var codexRoots []string
	codexProfile := ""
	codexThreadConfig := map[string]any{}
	if codexAppServer && request.ExecutionMode == "project_autonomous" && !codexNamedProfileSupported(adapter.Version) {
		// The old exec-json baseline proves only read-only execution. Named
		// filesystem profiles and their explicit deny semantics need the newer
		// app-server protocol; an unknown version must not fall back to a broad
		// default sandbox while still exposing native tools.
		codexAppServer = false
		request.NativeTools = withoutNativeFileEffectTools(request.NativeTools)
		if err := gate.notice("native_boundary_unavailable", "the installed Codex version or platform does not prove named permission-profile support; native file, command and image tools are disabled and scoped host tools remain available"); err != nil {
			return err
		}
	}
	if codexAppServer && request.ExecutionMode == "project_autonomous" {
		// view_image reads through the named sandbox profile but produces no
		// per-call host approval, so it cannot satisfy live role/epoch
		// revalidation. Keep the exact host file tools available instead.
		beforeImage := len(request.NativeTools)
		request.NativeTools = withoutNativeImageTool(request.NativeTools)
		if len(request.NativeTools) != beforeImage {
			if err := gate.notice("codex_view_image_unavailable", "native image reads cannot be bound to a per-call host approval; native image is disabled and host file tools remain available"); err != nil {
				return err
			}
		}
		codexProfile = newCodexProfileName()
		if codexProfile == "" {
			codexAppServer = false
			request.NativeTools = withoutNativeFileEffectTools(request.NativeTools)
			if err := gate.notice("native_boundary_unavailable", "a private Codex permission profile could not be created; native file, command and image tools are disabled and scoped host tools remain available"); err != nil {
				return err
			}
		}
	}
	if boundary {
		if request.WorkspaceRoot == "" {
			return errors.New("project-autonomous native execution requires a bound project root")
		}
		created, sandboxErr := newNativeSandbox(base)
		if sandboxErr != nil {
			// No proven OS boundary: keep the native catalog closed and say so.
			// Host MCP tools remain available; this is recorded as a capability
			// gap, never silently reported as project-autonomous native access.
			if request.Executor == "claude" {
				interactive = false
			}
			if err := gate.notice("native_boundary_unavailable", sandboxErr.Error()+"; native file/command tools are disabled for this step"); err != nil {
				return err
			}
		} else {
			sandbox = created
			sandbox.addRoots(request.WorkspaceRoot, request.WorkingDirectory)
			proxy, proxyErr := newEgressProxy(func(host, method string) {
				_ = gate.notice("native_egress_blocked", "sandboxed command attempted "+method+" "+host+"; arbitrary external writes are not authorized")
			})
			if proxyErr != nil {
				if request.Executor == "claude" {
					interactive = false
				}
				if err := gate.notice("native_boundary_unavailable", "egress policy unavailable: "+proxyErr.Error()); err != nil {
					return err
				}
			} else {
				sandbox.proxyURL = proxy.url()
				sandbox.proxyAddr = proxy.addr()
				proxy.allowTest = options.allowLocalEgress
				proxy.upstreamRoots = options.egressRoots
				proxy.approve = func(ctx context.Context, method, target, digest string) (bool, string) {
					if gate.approver == nil {
						return false, "external writes require a host approval callback"
					}
					return gate.approver.authorizeExternal(ctx, method, target, digest)
				}
				if caErr := sandbox.setCA(proxy.caPEM); caErr != nil {
					if request.Executor == "claude" {
						interactive = false
					}
					if err := gate.notice("native_boundary_unavailable", caErr.Error()); err != nil {
						return err
					}
				}
				defer proxy.close()
			}
		}
	}
	if codexAppServer && request.ExecutionMode == "project_autonomous" {
		codexRoots = codexNativeRoots(request.WorkspaceRoot, request.WorkingDirectory)
		if configInsideRoot(request.WorkspaceRoot, workdir) || configInsideRoot(request.WorkingDirectory, workdir) {
			codexAppServer = false
			request.NativeTools = withoutNativeFileEffectTools(request.NativeTools)
			if err := gate.notice("native_boundary_unavailable", "the private Codex configuration would be readable through a native root; native file, command and image tools are disabled and scoped host tools remain available"); err != nil {
				return err
			}
		} else {
			scan := gateHardlinkScan(nil, codexRoots...)
			if scan.unknown {
				codexAppServer = false
				request.NativeTools = withoutNativeFileEffectTools(request.NativeTools)
				if err := gate.notice("native_hardlink_unverified", "the Codex project/task roots could not be fully scanned for external hardlinks; native file, command and image tools are disabled and scoped host tools remain available"); err != nil {
					return err
				}
			} else if len(scan.dangerous) != 0 {
				dangerous = scan.dangerous
				if err := gate.notice("native_hardlink_isolated", fmt.Sprintf("%d file(s) link outside the Codex project/task roots; those exact paths are denied to native commands", len(scan.dangerous))); err != nil {
					return err
				}
			}
		}
	}
	files, web := sanitizeExecutionNativeTools(request.Executor, request.NativeTools, request.WorkspaceRoot, request.ExecutionMode)
	if sandbox != nil {
		hardlinks := scanProjectHardlinks(request.WorkspaceRoot)
		if hardlinks.unknown {
			if err := gate.notice("native_hardlink_unverified", "the project hardlink scan could not prove that no inside file aliases an outside inode; native file and command tools are disabled for this step and host MCP tools remain available"); err != nil {
				return err
			}
			files = nil
		} else if len(hardlinks.dangerous) != 0 {
			sandbox.setHardlinkDenies(hardlinks.dangerous, false)
			dangerous = hardlinks.dangerous
			if err := gate.notice("native_hardlink_isolated", fmt.Sprintf("%d project file(s) link outside the project; those exact paths are denied to native commands", len(hardlinks.dangerous))); err != nil {
				return err
			}
		}
	}
	if !interactive && request.ExecutionMode == "project_autonomous" && request.Executor == "claude" {
		// Keep read-only native tools only when the OS boundary is unavailable.
		files, web = sanitizeExecutionNativeTools(request.Executor, request.NativeTools, request.WorkspaceRoot, "controlled")
	}
	if gate.trustedNative && request.Executor == "claude" {
		files = []string{nativeRead, nativeGlob, nativeGrep, nativeBash, nativeWrite, nativeEdit}
		web = []string{nativeWebFetch, nativeWebSearch}
	}
	degraded := false
	if len(files) != 0 && !fullAccess && configInsideRoot(request.WorkspaceRoot, configDir) {
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
		} else if err := os.MkdirAll(cwdDir, 0700); err != nil {
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
	if interactive {
		approver = newNativeApprover(gate, live, request, adapter.ID, sandbox)
		approver.setDangerousPaths(dangerous)
	}
	if codexAppServer {
		approver = newNativeApprover(gate, live, request, adapter.ID, nil)
		approver.setDangerousPaths(dangerous)
		gate.approver = approver
		gate.nativeBoundary = true
	}
	if request.Executor == "opencode" && (request.ExecutionMode == "project_autonomous" || request.ExecutionMode == "full_access") && request.AuthorizeNative != nil && (sandbox != nil || request.ExecutionMode == "full_access") {
		approver = newNativeApprover(gate, live, request, adapter.ID, sandbox)
		approver.setDangerousPaths(dangerous)
		gate.nativeBoundary = true
		if sandbox != nil {
			gate.sandbox = sandbox
		}
		gate.cwd = cwdDir
		if gate.pluginTokenFile == "" {
			gate.pluginTokenFile = filepath.Join(workdir, "plugin-token")
			if err := os.WriteFile(gate.pluginTokenFile, []byte(gate.pluginToken), 0600); err != nil {
				return errors.New("write private OpenCode plugin token")
			}
			tokenFile := gate.pluginTokenFile
			time.AfterFunc(30*time.Second, func() { _ = os.Remove(tokenFile) })
		}
	}
	if approver != nil {
		gate.approver = approver
	}
	if sandbox != nil {
		gate.sandbox = sandbox
		gate.cwd = cwdDir
	}
	if codexAppServer {
		gate.cwd = request.WorkspaceRoot
		gate.nativeFullAccess = request.ExecutionMode == "full_access"
	}
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
				options.geminiUnset = plan.geminiUnset
			}
		}
	}
	if (request.Executor == "pi" || request.Executor == "opencode") && !options.skipCheck && launch == nil {
		info, err := p.InspectExecutionConnection(ctx, request.Executor, request.Model, request.ThinkingLevel)
		if err != nil {
			return err
		}
		if request.ExpectedConnection.Identity != "" && request.ExpectedConnection.Identity != info.Identity {
			return errors.New("CLI model or upstream changed before launch")
		}
		plan, ok := cachedPlan(request.Executor, info.Identity)
		if !ok {
			return errors.New("CLI connection snapshot is unavailable")
		}
		launch = &plan
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
		if fullAccess {
			settings = map[string]any{}
			for key, value := range base {
				settings[key] = value
			}
			settings["disableAllHooks"] = true
			settings["enabledPlugins"] = map[string]any{}
			settings["autoMemoryEnabled"] = false
		} else {
			settings = claudeStepSettings(base, configDir)
		}
	}
	safeSettings, err := json.Marshal(settings)
	if err != nil {
		return errors.New("encode scoped CLI connection settings")
	}
	if err := os.WriteFile(settingsPath, safeSettings, 0600); err != nil {
		return errors.New("write isolated CLI settings")
	}
	var args []string
	if interactive {
		args = claudeBidirectionalArgs(configPath, settingsPath, nativeNames, request)
	} else {
		args = claudeExecutionArgs(configPath, settingsPath, nativeNames, request)
	}
	if request.Executor == "claude" && managed {
		// Persistent sessions belong to Cocogem, not to the user's interactive
		// terminal. Reusing that config root could resume an unrelated session.
		options.claudeEnv = mergeEnv(options.claudeEnv, map[string]string{"CLAUDE_CONFIG_DIR": configDir})
	}
	if request.Executor == "claude" && request.ThinkingLevel != "" {
		args = append(args, "--effort", request.ThinkingLevel)
	}
	var env map[string]string
	switch request.Executor {
	case "codex":
		if codexAppServer {
			args, env, err = prepareCodexAppServer(request, workdir, gate, launch, codexProfile, codexRoots, dangerous, &codexThreadConfig)
		} else {
			args, env, err = prepareCodex(request, workdir, gate, options.codexProbe, launch)
		}
		if err == nil {
			args = append(args, options.codexArgs...)
			env = mergeEnv(env, options.codexEnv)
		}
	case "gemini":
		args, env, err = prepareGemini(request, workdir, gate, launch)
		env = mergeEnv(env, options.geminiEnv)
	case "pi":
		args, env, err = preparePi(request, workdir, gate, options.piProbeURL, launch)
	case "opencode":
		args, env, err = p.prepareOpenCode(ctx, request, workdir, gate, options.openCodeProbeURL, launch)
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
	if request.Executor == "pi" && request.ThinkingLevel != "" {
		args = append(args, "--thinking", request.ThinkingLevel)
	}
	if request.Executor != "codex" && request.Executor != "pi" && request.Executor != "opencode" && request.Model != "" && request.Model != "default" {
		if strings.HasPrefix(request.Model, "-") || len(request.Model) > 200 {
			return errors.New("invalid CLI model")
		}
		args = append(args, "--model", request.Model)
	}
	cmd := command(request.Executor, args...)
	cmd.Dir = cwdDir
	if codexAppServer {
		cmd.Dir = request.WorkspaceRoot
	}
	if gate.trustedNative && request.WorkingDirectory != "" && request.Executor != "gemini" {
		cmd.Dir = request.WorkingDirectory
	}
	if request.Executor == "pi" && launch != nil {
		cmd.Env = append([]string{}, launch.piEnv...)
	}
	if request.Executor == "opencode" && launch != nil {
		cmd.Env = append([]string{}, launch.openCodeEnv...)
	}
	if len(env) != 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = overrideEnv(cmd.Env, env)
	}
	if len(options.geminiUnset) != 0 {
		// A frozen absence must not be filled in by a variable exported after
		// the connection was recorded.
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = removeEnv(cmd.Env, options.geminiUnset)
	}
	if request.Executor == "gemini" {
		if err := configureGeminiSSE(cmd, workdir); err != nil {
			return err
		}
	}
	instruction := "Only use the tool set explicitly supplied for this step. Complete the assigned step, then finish."
	if gate.trustedNative {
		instruction = "Native file, image and command tools are enabled for this authorized task. Preserve input files and put final outputs in the task working directory. Use the Cocogem tools for member handoff and final file delivery. "
	}
	if gate.trustedNative && request.WorkingDirectory != "" {
		instruction += " Task working directory: " + request.WorkingDirectory + ". The CLI may start from a private runtime directory; native commands must explicitly select the task directory or use absolute output paths. "
	}
	if len(nativeNames) != 0 {
		instruction = "Prefer the supplied native tools for their granted operations (" + strings.Join(nativeNames, ", ") + "); the CLI executes them under its own permissions. If a native tool fails or returns no usable content, report the exact error, then use the corresponding platform MCP fallback when available. Native search results are leads, and native web output is a CLI-processed observation, not a raw page. " + instruction
	}
	inputText := request.System + "\n\n" + instruction + " User messages and upstream evidence:\n" + string(prompt)
	if !interactive && !codexAppServer {
		cmd.Stdin = strings.NewReader(inputText)
	}
	var processErr error
	if request.Executor == "opencode" {
		processErr = p.streamOpenCode(ctx, cmd, gate, live)
	} else if interactive {
		processErr = streamProcessWithClaude(ctx, request.Executor, cmd, gate, live, &claudeStreamInput{
			prompt: inputText, approver: approver, sandbox: sandbox, cwd: cwdDir,
		})
	} else if codexAppServer {
		processErr = p.streamCodexAppServer(ctx, cmd, gate, live, inputText, approver, codexProfile, codexRoots, dangerous, codexThreadConfig)
	} else {
		processErr = streamProcess(ctx, request.Executor, cmd, gate, live)
	}
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

// removeEnv drops the named variables from the inherited environment.
func removeEnv(env []string, keys []string) []string {
	drop := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		drop[key] = struct{}{}
	}
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, removed := drop[key]; !removed {
			result = append(result, entry)
		}
	}
	return result
}

func helpArgs(id string) []string {
	switch id {
	case "codex":
		return []string{"exec", "--help"}
	case "opencode":
		return []string{"run", "--help"}
	default:
		return []string{"--help"}
	}
}
