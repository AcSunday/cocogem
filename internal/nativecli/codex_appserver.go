package nativecli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

// codexAppServer is the bidirectional Codex transport used for
// project_autonomous. Codex executes its own sandboxed commands and asks the
// host before an effect that needs approval; the adapter answers the original
// JSON-RPC request instead of restarting the turn.
type codexAppServer struct {
	gate     *bridge
	live     *liveness
	approver *nativeApprover
	stdin    io.WriteCloser
	write    sync.Mutex
	nextID   int
	pending  map[int]chan json.RawMessage
	threadID string
	turnID   string
	finished bool
	failure  error

	agentText    strings.Builder
	deltaSeen    map[string]bool
	completed    map[string]bool
	startedItem  map[string]string
	fileChanges  map[string]json.RawMessage
	roots        []string
	denies       []string
	profile      string
	threadConfig map[string]any
}

func prepareCodexAppServer(request model.Request, workdir string, gate *bridge, launch *connectionPlan, profile string, roots, hardlinkDenies []string, threadConfig *map[string]any) ([]string, map[string]string, error) {
	// Reuse the scoped connection/snapshot logic, but launch app-server rather
	// than exec. The app-server owns the thread and approval channel.
	resolved := request.Model
	connectionArgs := []string{}
	env := map[string]string{}
	if launch != nil {
		resolved, connectionArgs, env = launch.codexResolved, launch.codexArgs, launch.codexEnv
	}
	if resolved == "" || resolved == "default" {
		resolved = "cocogem-fixture"
	}
	fullAccess := request.ExecutionMode == "full_access"
	shell := fullAccess || slices.Contains(request.NativeTools, nativeCodexCommand) || slices.Contains(request.NativeTools, nativeBash)
	patch := fullAccess || slices.Contains(request.NativeTools, nativeCodexFile) || slices.Contains(request.NativeTools, nativeWrite)
	image := fullAccess || slices.Contains(request.NativeTools, nativeCodexImage)
	entry := map[string]any{
		"slug": resolved, "display_name": resolved, "description": "Scoped collaboration agent", "supported_reasoning_levels": []any{}, "visibility": "list", "supported_in_api": true, "priority": 0,
		"support_verbosity": false, "truncation_policy": map[string]any{"mode": "bytes", "limit": 10000}, "experimental_supported_tools": []string{}, "include_skills_usage_instructions": false, "include_apps_usage_instructions": false, "include_plugin_usage_instructions": false,
		"tool_mode": nil, "multi_agent_version": nil, "node_repl_disabled": true, "use_responses_lite": false, "input_modalities": []string{"text"}, "model_messages": map[string]any{"instructions_template": "You are a collaboration agent. Use only the tools supplied for this step. Follow the supplied task and finish this turn."},
	}
	if shell {
		entry["shell_type"] = "shell_command"
	}
	if patch {
		entry["apply_patch_tool_type"] = "freeform"
	}
	if image {
		entry["input_modalities"] = []string{"text", "image"}
	}
	encoded, err := json.Marshal(map[string]any{"models": []any{entry}})
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(workdir, "models.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		return nil, nil, errors.New("write scoped Codex model catalog")
	}
	quoted := func(value string) string { raw, _ := json.Marshal(value); return string(raw) }
	if request.ExecutionMode == "project_autonomous" {
		if err := os.WriteFile(filepath.Join(workdir, "config.toml"), []byte(codexPermissionProfile(profile, roots, hardlinkDenies)), 0600); err != nil {
			return nil, nil, errors.New("write scoped Codex permission profile")
		}
	}
	if err := snapshotCodexAuth(workdir); err != nil {
		return nil, nil, err
	}
	args := []string{"app-server", "--stdio", "--strict-config"}
	args = append(args, connectionArgs...)
	if request.ThinkingLevel != "" {
		// The scoped catalog reports no reasoning levels, so this explicit
		// config override is the supported per-invocation effort switch. It
		// replaces any configured default carried in the connection snapshot.
		args = withoutReasoningEffort(args)
		args = append(args, "-c", `model_reasoning_effort="`+request.ThinkingLevel+`"`)
	}
	args = append(args, "-c", "model="+quoted(resolved))
	features := map[string]any{}
	for _, config := range []string{"approval_policy=\"on-request\"", "model_catalog_json=" + quoted(path), "web_search=\"disabled\"", "skills.include_instructions=false", "project_doc_max_bytes=0", "tools.update_plan.enabled=false", "tools.experimental_request_user_input.enabled=false", "mcp_servers.cocogem.url=" + quoted(gate.url()), "mcp_servers.cocogem.bearer_token_env_var=\"COCOGEM_MCP_TOKEN\"", "mcp_servers.cocogem.default_tools_approval_mode=\"auto\"", "shell_environment_policy.exclude=[\"COCOGEM_*\", \"CODEX_HOME\"]"} {
		args = append(args, "-c", config)
	}
	for _, feature := range []string{"shell_tool", "view_image", "multi_agent", "multi_agent_v2", "hooks", "plugins", "apps", "code_mode", "code_mode_host", "code_mode_only", "sleep_tool", "token_budget", "request_permissions_tool", "standalone_web_search", "goals"} {
		enabled := feature == "shell_tool" && shell || feature == "view_image" && image
		features[feature] = enabled
		args = append(args, "-c", fmt.Sprintf("features.%s=%t", feature, enabled))
	}
	features["image_generation"] = false
	if request.ExecutionMode == "project_autonomous" && threadConfig != nil {
		toolConfig := map[string]any{}
		for _, tool := range request.Tools {
			toolConfig[tool.Name] = map[string]any{"approval_mode": "approve"}
		}
		cocogem := map[string]any{"url": gate.url(), "bearer_token_env_var": "COCOGEM_MCP_TOKEN", "default_tools_approval_mode": "auto"}
		if len(toolConfig) != 0 {
			cocogem["tools"] = toolConfig
		}
		thread := map[string]any{
			"model":                    resolved,
			"model_catalog_json":       path,
			"approval_policy":          "on-request",
			"web_search":               "disabled",
			"skills":                   map[string]any{"include_instructions": false},
			"project_doc_max_bytes":    0,
			"features":                 features,
			"mcp_servers":              map[string]any{"cocogem": cocogem},
			"shell_environment_policy": map[string]any{"exclude": []string{"COCOGEM_*", "CODEX_HOME"}, "ignore_default_excludes": false},
			"default_permissions":      profile,
			"permissions": map[string]any{profile: map[string]any{
				"filesystem":      codexFilesystemEntries(roots, hardlinkDenies),
				"workspace_roots": codexWorkspaceRoots(roots),
				"network":         map[string]any{"enabled": false},
			}},
		}
		if request.ThinkingLevel != "" {
			thread["model_reasoning_effort"] = request.ThinkingLevel
		}
		*threadConfig = thread
	}
	for _, tool := range request.Tools {
		args = append(args, "-c", "mcp_servers.cocogem.tools."+tool.Name+".approval_mode=\"approve\"")
	}
	if env == nil {
		env = map[string]string{}
	}
	env["COCOGEM_MCP_TOKEN"] = gate.token
	env["CODEX_HOME"] = workdir
	return args, env, nil
}

func codexFilesystemEntries(roots, hardlinkDenies []string) map[string]any {
	entries := map[string]any{":root": "deny", ":minimal": "read", ":workspace_roots": "write"}
	for _, path := range hardlinkDenies {
		if path != "" {
			entries[resolvePhysicalPath(path)] = "deny"
		}
	}
	return entries
}

func codexWorkspaceRoots(roots []string) map[string]any {
	out := map[string]any{}
	for _, root := range codexNativeRoots(roots...) {
		out[root] = true
	}
	return out
}

// codexPermissionProfile builds the narrow named profile. The explicit
// :root deny is load-bearing: Codex only refuses an implicit unsandboxed retry
// after a sandbox denial when the policy has an explicit denied-read entry.
// Without it, an accepted command can be retried outside the sandbox. Exact
// project/task roots stay writable and exact hardlink aliases are denied.
func codexPermissionProfile(profile string, roots, hardlinkDenies []string) string {
	quoted := func(value string) string { raw, _ := json.Marshal(value); return string(raw) }
	var out strings.Builder
	out.WriteString("default_permissions = " + quoted(profile) + "\n\n[permissions." + quoted(profile) + ".filesystem]\n")
	out.WriteString("\":root\" = \"deny\"\n\":minimal\" = \"read\"\n\":workspace_roots\" = \"write\"\n")
	for _, path := range hardlinkDenies {
		if path == "" {
			continue
		}
		out.WriteString(quoted(resolvePhysicalPath(path)) + " = \"deny\"\n")
	}
	out.WriteString("\n[permissions." + quoted(profile) + ".workspace_roots]\n")
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		absolute = resolvePhysicalPath(filepath.Clean(absolute))
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		out.WriteString(quoted(absolute) + " = true\n")
	}
	out.WriteString("\n[permissions." + quoted(profile) + ".network]\nenabled = false\n")
	return out.String()
}

// snapshotCodexAuth forwards the existing local Codex login into the private
// CODEX_HOME so a default openai provider keeps working without a new login.
// The copy is private 0600 and stays outside the project/task write roots.
func snapshotCodexAuth(workdir string) error {
	home, err := codexHome()
	if err != nil {
		return err
	}
	source := filepath.Join(home, "auth.json")
	info, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return errors.New("native Codex authentication snapshot is unavailable")
	}
	target := filepath.Join(workdir, "auth.json")
	if filepath.Clean(source) == filepath.Clean(target) {
		return nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return errors.New("read native Codex authentication snapshot")
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		return errors.New("write scoped Codex authentication snapshot")
	}
	return nil
}

func newCodexProfileName() string {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ""
	}
	return "cocogem-project-" + hex.EncodeToString(nonce[:])
}

func codexNamedProfileSupported(version string) bool {
	if runtime.GOOS != "darwin" {
		// Named filesystem profiles are only installed-verified on macOS. Other
		// platforms must use the scoped host path until their boundary is tested.
		return false
	}
	parsed, _, ok := parseVersion(version)
	if !ok {
		return false
	}
	return versionAtLeast(parsed, [3]int{0, 166, 0})
}

func codexNativeRoots(roots ...string) []string {
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		absolute = resolvePhysicalPath(filepath.Clean(absolute))
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		out = append(out, absolute)
	}
	return out
}

func withoutNativeImageTool(tools []string) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool == nativeCodexImage {
			continue
		}
		out = append(out, tool)
	}
	return out
}

func withoutNativeFileEffectTools(tools []string) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		switch tool {
		case nativeCodexCommand, nativeCodexFile, nativeCodexImage, nativeBash, nativeWrite:
			continue
		}
		out = append(out, tool)
	}
	return out
}

func (p Provider) streamCodexAppServer(ctx context.Context, cmd *exec.Cmd, gate *bridge, live *liveness, prompt string, approver *nativeApprover, profile string, roots, denies []string, threadConfig map[string]any) error {
	if err := configureProcess(cmd); err != nil {
		return err
	}
	stderr := &boundedBuffer{limit: 8192, truncate: true}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return newFailure(FailureProcess, "open native CLI output")
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return newFailure(FailureProcess, "open native CLI input")
	}
	if err := cmd.Start(); err != nil {
		return newFailure(FailureProcess, "start native CLI: executable unavailable")
	}
	if live != nil {
		live.dispatched()
	}
	server := &codexAppServer{gate: gate, live: live, approver: approver, stdin: stdin, pending: map[int]chan json.RawMessage{}, deltaSeen: map[string]bool{}, completed: map[string]bool{}, startedItem: map[string]string{}, fileChanges: map[string]json.RawMessage{}, roots: roots, denies: denies, profile: profile, threadConfig: threadConfig}
	identity, owned, err := inspectOwnership(cmd.Process.Pid, cmd.Path)
	if err != nil {
		terminateProcess(cmd)
		_ = cmd.Wait()
		return newFailure(FailureProcessUncertain, "native CLI process ownership could not be recorded")
	}
	if owned {
		if err := gate.emit("native_process", map[string]any{"pid": identity.PID, "pgid": identity.PGID, "start": identity.Start, "executable": identity.Executable}); err != nil {
			terminateProcess(cmd)
			_ = cmd.Wait()
			return err
		}
	}
	joinCancel := watchCancel(ctx, func() { terminateProcess(cmd) })
	streamErr := server.serve(ctx, stdout, prompt)
	if streamErr != nil {
		terminateProcess(cmd)
	}
	_ = stdin.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-time.After(2 * time.Second):
		terminateProcess(cmd)
		waitErr = <-waitDone
	}
	joinCancel()
	groupErr := convergeProcessGroup(cmd, identity, owned)
	if debugNative() && stderr.String() != "" {
		fmt.Fprintf(os.Stderr, "native debug: codex stderr=%s\n", stderr.String())
	}
	return streamFailure(ctx.Err(), groupErr, streamErr, waitErr)
}

func (s *codexAppServer) writeMessage(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return newFailure(FailureProtocol, "cannot encode Codex request")
	}
	s.write.Lock()
	defer s.write.Unlock()
	if _, err := s.stdin.Write(append(raw, '\n')); err != nil {
		return newFailure(FailureProcess, "Codex app-server control stream is unavailable")
	}
	return nil
}

func (s *codexAppServer) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.nextID++
	id := s.nextID
	wait := make(chan json.RawMessage, 1)
	s.pending[id] = wait
	if debugNative() {
		raw, _ := json.Marshal(params)
		fmt.Fprintf(os.Stderr, "native debug: codex request %s params=%s\n", method, truncateDebug(raw))
	}
	if err := s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case raw := <-wait:
		if debugNative() {
			fmt.Fprintf(os.Stderr, "native debug: codex %s result=%s\n", method, truncateDebug(raw))
		}
		return raw, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(60 * time.Second):
		return nil, newFailure(FailureProtocol, "Codex app-server did not answer %s", safeToken(method))
	}
}

type codexMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (s *codexAppServer) serve(ctx context.Context, reader io.Reader, prompt string) error {
	done := make(chan error, 1)
	go func() { done <- s.readLoop(ctx, reader) }()
	initialized := make(chan error, 1)
	go func() {
		_, err := s.request(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "cocogem", "version": "0.1.0"}})
		initialized <- err
	}()
	select {
	case err := <-initialized:
		if err != nil {
			return err
		}
	case err := <-done:
		if err == nil {
			err = newFailure(FailureProtocol, "Codex app-server exited before initialization")
		}
		return err
	}
	if err := s.writeMessage(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	// Keep every native command behind a before-effect host callback. The
	// explicit :root deny in the named profile makes sandbox denial
	// non-escalatable, so an accepted command cannot be retried outside it.
	approvalPolicy := "untrusted"
	threadParams := map[string]any{
		"cwd": s.gate.cwd, "ephemeral": true, "approvalPolicy": approvalPolicy,
		"approvalsReviewer": "user",
	}
	if s.gate != nil && s.gate.nativeFullAccess {
		threadParams["sandbox"] = "danger-full-access"
	} else if len(s.threadConfig) != 0 {
		threadParams["config"] = s.threadConfig
	}
	thread, err := s.request(ctx, "thread/start", threadParams)
	if err != nil {
		return err
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(thread, &started) != nil || started.Thread.ID == "" {
		return newFailure(FailureProtocol, "Codex app-server returned an invalid thread identity")
	}
	s.threadID = started.Thread.ID
	if !s.gate.nativeFullAccess {
		if err := s.gate.notice("codex_external_scope_unavailable", "Codex app-server approvals cannot express a one-time outside path or call scope; native outside effects stay denied and host MCP tools remain the scoped fallback"); err != nil {
			return err
		}
	}
	if err := s.gate.emit("native_session", map[string]any{"executor": "codex", "session_id": s.threadID, "mode": "fresh", "cwd": "isolated"}); err != nil {
		return err
	}
	turnParams := map[string]any{
		"threadId": s.threadID, "input": []any{map[string]any{"type": "text", "text": prompt}},
		"approvalPolicy": approvalPolicy,
	}
	if s.gate != nil && s.gate.nativeFullAccess {
		turnParams["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
	}
	if _, err := s.request(ctx, "turn/start", turnParams); err != nil {
		return err
	}
	select {
	case err := <-done:
		if debugNative() {
			fmt.Fprintf(os.Stderr, "native debug: codex serve done err=%v\n", err)
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *codexAppServer) readLoop(ctx context.Context, reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxCLIFrameBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message codexMessage
		if json.Unmarshal(line, &message) != nil || message.Method == "" && len(message.ID) == 0 {
			return newFailure(FailureProtocol, "Codex app-server emitted a malformed frame")
		}
		if message.Method == "" {
			var id int
			if json.Unmarshal(message.ID, &id) == nil {
				if wait, ok := s.pending[id]; ok {
					if debugNative() {
						fmt.Fprintf(os.Stderr, "native debug: codex response raw=%s\n", truncateDebug(line))
					}
					delete(s.pending, id)
					wait <- message.Result
					continue
				}
			}
			continue
		}
		if len(message.ID) != 0 {
			if err := s.handleServerRequest(ctx, message); err != nil {
				return err
			}
			continue
		}
		if err := s.handleNotification(message); err != nil {
			return err
		}
		if s.finished {
			break
		}
	}
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: codex read loop ended finished=%v failure=%v\n", s.finished, s.failure)
	}
	if err := scanner.Err(); err != nil {
		return newFailure(FailureProtocol, "Codex app-server output could not be read")
	}
	if s.failure != nil {
		return s.failure
	}
	if !s.finished {
		return newFailure(FailureProtocol, "Codex app-server ended without a confirmed turn result")
	}
	return s.gate.requireTools()
}

func (s *codexAppServer) handleServerRequest(ctx context.Context, message codexMessage) error {
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: codex server request %s params=%s\n", message.Method, truncateDebug(message.Params))
	}
	var params struct {
		ThreadID    string          `json:"threadId"`
		TurnID      string          `json:"turnId"`
		ItemID      string          `json:"itemId"`
		Command     string          `json:"command"`
		CWD         string          `json:"cwd"`
		Reason      string          `json:"reason"`
		Kind        string          `json:"kind"`
		GrantRoot   string          `json:"grantRoot"`
		FileChanges json.RawMessage `json:"fileChanges"`
	}
	_ = json.Unmarshal(message.Params, &params)
	callID := "cli:" + safeToken(params.ItemID)
	if params.ItemID == "" {
		callID = "cli:codex-approval"
	}
	if message.Method == "item/permissions/requestApproval" {
		// The installed response only offers turn/session scope. Returning the
		// requested profile would turn one host approval into broader authority,
		// so an unrepresentable permission overlay is denied instead of widened.
		if s.gate != nil {
			_ = s.gate.notice("codex_permission_scope_unrepresentable", "Codex requested a permission overlay that can only be granted for the whole turn; it was declined and host MCP tools remain available")
		}
		return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"permissions": map[string]any{}, "scope": "turn"}})
	}
	if params.Kind == "writeStdin" {
		if s.gate != nil {
			_ = s.gate.notice("codex_stdin_scope_unrepresentable", "Codex requested terminal stdin authority without an exact call scope; it was declined")
		}
		return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"decision": "decline"}})
	}
	name, input := "Bash", json.RawMessage(`{}`)
	legacyPatch := message.Method == "applyPatchApproval"
	switch message.Method {
	case "item/fileChange/requestApproval", "applyPatchApproval":
		name = "Edit"
		changes := params.FileChanges
		if len(changes) == 0 {
			changes = s.fileChanges[params.ItemID]
		}
		paths, ok := codexChangePaths(changes)
		if !ok || len(paths) == 0 {
			// Without the exact changed paths the host cannot bind authority to
			// this effect. The approval request carries no patch by itself.
			s.gate.notice("codex_filechange_unrepresentable", "a file-change approval did not include its exact changed paths; the patch was declined")
			return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"decision": "decline"}})
		}
		outside := []string{}
		for _, path := range paths {
			if !withinAnyRoot(path, s.roots) {
				outside = append(outside, path)
			}
		}
		if params.GrantRoot != "" && !withinAnyRoot(params.GrantRoot, s.roots) {
			outside = append(outside, params.GrantRoot)
		}
		if len(outside) != 0 {
			s.gate.notice("codex_filechange_outside", "a file-change approval touched a path outside this invocation's OS boundary; the patch was declined and host MCP tools remain available")
			return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"decision": "decline"}})
		}
		input, _ = json.Marshal(map[string]any{"changes": changes, "paths": paths, "path": paths[0], "cwd": params.CWD, "grant_root": params.GrantRoot})
	}
	if params.Command != "" {
		input, _ = json.Marshal(map[string]string{"command": params.Command, "cwd": params.CWD})
	}
	if !s.gate.nativeFullAccess && params.Command != "" {
		commandCWD := firstString(params.CWD, s.gate.cwd)
		if commandCWD != "" && !withinAnyRoot(commandCWD, s.roots) {
			if s.gate != nil {
				_ = s.gate.notice("codex_external_scope_unavailable", "Codex cannot authorize a native command in an outside working directory for one call; it was declined and host MCP tools remain the scoped fallback")
			}
			return s.declineServerRequest(message, legacyPatch)
		}
		action, _, targets, network := classifyNativeCall(name, input, commandCWD, s.roots)
		if !network && action == "execute" {
			if outside := firstOutside(targets, s.roots); outside != "" {
				if s.gate != nil {
					_ = s.gate.notice("codex_external_scope_unavailable", "Codex cannot authorize an exact outside native command target for one call; it was declined and host MCP tools remain the scoped fallback")
				}
				// Do not consume a host approval for an operation the named
				// profile cannot represent as a one-call outside scope.
				return s.declineServerRequest(message, legacyPatch)
			}
		}
	}
	authorization, err := s.approver.authorize(ctx, callID, name, input, params.Reason)
	if err != nil {
		s.declineServerRequest(message, legacyPatch)
		return err
	}
	if !authorization.Allow {
		return s.declineServerRequest(message, legacyPatch)
	}
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: codex approval response id=%s decision=accept\n", message.ID)
	}
	if legacyPatch {
		return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"decision": "approved"}})
	}
	return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"decision": "accept"}})
}

func (s *codexAppServer) declineServerRequest(message codexMessage, legacyPatch bool) error {
	if legacyPatch {
		return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"decision": map[string]any{"denied": map[string]any{"rejection": "declined by the host boundary"}}}})
	}
	return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"decision": "decline"}})
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *codexAppServer) handleNotification(message codexMessage) error {
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: codex notification %s\n", message.Method)
	}
	switch message.Method {
	case "thread/started", "turn/started", "thread/status/changed", "turn/plan/updated", "turn/diff/updated", "serverRequest/resolved", "thread/tokenUsage/updated", "mcpServer/startupStatus/updated", "account/updated", "account/rateLimits/updated", "warning", "deprecationNotice", "hook/started", "hook/completed", "item/autoApprovalReview/started", "item/autoApprovalReview/completed", "autoApprovalReview/strictReviewRequired":
		if debugNative() {
			fmt.Fprintf(os.Stderr, "native debug: codex %s params=%s\n", message.Method, truncateDebug(message.Params))
		}
		return nil
	case "error":
		var params struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		_ = json.Unmarshal(message.Params, &params)
		if params.WillRetry {
			if s.gate != nil {
				_ = s.gate.notice("codex_retry", "Codex reported a transient error and is retrying")
			}
			return nil
		}
		s.failure = classifiedNativeFailure("", "codex_error", params.Error.Message)
		return nil
	case "item/agentMessage/delta":
		var params struct {
			Delta  string `json:"delta"`
			ItemID string `json:"itemId"`
		}
		if json.Unmarshal(message.Params, &params) != nil {
			return newFailure(FailureProtocol, "Codex emitted an invalid message delta")
		}
		if params.Delta == "" {
			return nil
		}
		s.deltaSeen[params.ItemID] = true
		if s.live != nil {
			s.live.valid("text")
		}
		return s.gate.emit("text", map[string]any{"text": params.Delta, "activity": strings.TrimSpace(params.Delta) != ""})
	case "item/started", "item/completed":
		var params struct {
			Item struct {
				ID      string          `json:"id"`
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Command string          `json:"command"`
				Status  string          `json:"status"`
				Error   json.RawMessage `json:"error"`
				Changes json.RawMessage `json:"changes"`
			} `json:"item"`
		}
		if json.Unmarshal(message.Params, &params) != nil || params.Item.ID == "" || params.Item.Type == "" {
			return newFailure(FailureProtocol, "Codex emitted an invalid thread item")
		}
		switch params.Item.Type {
		case "agentMessage":
			if message.Method == "item/completed" && !s.deltaSeen[params.Item.ID] && params.Item.Text != "" {
				if s.live != nil {
					s.live.valid("text")
				}
				if err := s.gate.emit("text", map[string]any{"text": params.Item.Text, "activity": true}); err != nil {
					return err
				}
			}
		case "commandExecution":
			id := "cli:" + params.Item.ID
			if message.Method == "item/started" {
				s.startedItem[params.Item.ID] = "Bash"
				if s.live != nil {
					s.live.valid("tool_call")
				}
				input, _ := json.Marshal(map[string]string{"command": params.Item.Command})
				return s.gate.emit("tool_started", map[string]any{"tool_call_id": id, "name": "Bash", "input": input, "origin": "native"})
			}
			if s.completed[params.Item.ID] {
				return nil
			}
			s.completed[params.Item.ID] = true
			status := "ok"
			if params.Item.Status != "" && params.Item.Status != "completed" {
				status = "error"
			}
			return s.gate.emit("tool_finished", map[string]any{"tool_call_id": id, "name": "Bash", "status": status, "output": "", "origin": "native"})
		case "fileChange":
			// v2 file-change approvals carry only the item id; bind the actual
			// changed paths from the streamed item before any approval can run.
			if len(params.Item.Changes) != 0 {
				s.fileChanges[params.Item.ID] = append(json.RawMessage{}, params.Item.Changes...)
			}
			id := "cli:" + params.Item.ID
			if message.Method == "item/completed" && !s.completed[params.Item.ID] {
				s.completed[params.Item.ID] = true
				if s.live != nil {
					s.live.valid("tool_result")
				}
				return s.gate.emit("tool_finished", map[string]any{"tool_call_id": id, "name": "Edit", "status": "ok", "origin": "native"})
			}
		case "mcpToolCall", "webSearch", "reasoning":
			if s.live != nil {
				s.live.valid("tool_call")
			}
		default:
			if s.gate != nil {
				_ = s.gate.notice("codex.item."+safeToken(params.Item.Type), "ignored non-terminal thread item")
			}
		}
		return nil
	case "turn/completed":
		var params struct {
			Turn struct {
				Status string `json:"status"`
				Error  struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(message.Params, &params) != nil {
			return newFailure(FailureProtocol, "Codex emitted an invalid turn completion")
		}
		if params.Turn.Status != "completed" {
			s.failure = classifiedNativeFailure("", "turn_"+safeToken(params.Turn.Status), params.Turn.Error.Message)
			return nil
		}
		s.finished = true
		return nil
	default:
		if s.gate != nil {
			_ = s.gate.notice(message.Method, "ignored non-terminal Codex notification")
		}
		return nil
	}
}

// codexChangePaths returns the exact changed paths from either the legacy
// applyPatch map or the v2 fileChange array shape.
func codexChangePaths(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var byPath map[string]json.RawMessage
	if json.Unmarshal(raw, &byPath) == nil {
		paths := make([]string, 0, len(byPath))
		for path := range byPath {
			if path != "" {
				paths = append(paths, path)
			}
		}
		slices.Sort(paths)
		return paths, len(paths) != 0
	}
	var list []struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &list) == nil {
		paths := make([]string, 0, len(list))
		for _, change := range list {
			if change.Path != "" {
				paths = appendUnique(paths, change.Path)
			}
		}
		return paths, len(paths) != 0
	}
	return nil, false
}
