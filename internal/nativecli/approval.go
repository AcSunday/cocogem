package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// nativeApprover sits between a CLI-owned tool call and the host's durable
// permission ledger. Every real project_autonomous effect goes through the host
// callback, including calls that are inside the project: the host, not the
// adapter, revalidates the live permission epoch, member role and task scope and
// decides whether the call is automatic or needs a user decision.
type nativeApprover struct {
	gate     *bridge
	live     *liveness
	callback func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error)
	mode     string
	adapter  string
	roots    []string
	cwd      string
	sandbox  *nativeSandbox

	dangerous   map[string]bool
	externalSeq atomic.Uint64
}

func (a *nativeApprover) setDangerousPaths(paths []string) {
	a.dangerous = map[string]bool{}
	for _, path := range paths {
		a.dangerous[path] = true
		a.dangerous[resolvePhysicalPath(path)] = true
	}
}

func newNativeApprover(gate *bridge, live *liveness, request model.Request, adapter string, sandbox *nativeSandbox) *nativeApprover {
	approver := &nativeApprover{
		gate: gate, live: live, callback: request.AuthorizeNative, mode: request.ExecutionMode,
		adapter: adapter, sandbox: sandbox, cwd: request.WorkspaceRoot,
	}
	for _, root := range []string{request.WorkspaceRoot, request.WorkingDirectory} {
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		approver.roots = appendUnique(approver.roots, absolute)
		if resolved := resolvePhysicalPath(absolute); resolved != absolute {
			approver.roots = appendUnique(approver.roots, resolved)
		}
	}
	return approver
}

// nativeAuthorization is the adapter-facing result. ReadScopes/WriteScopes are
// the only extra resources this single call may reach inside the OS sandbox; a
// read approval never becomes a write grant.
type nativeAuthorization struct {
	Allow       bool
	ReadScopes  []string
	WriteScopes []string
	Reason      string
	Decision    model.NativePermissionDecision
}

// authorize admits one not-yet-executed native call. callID is the stable native
// tool-call identity; the host may reuse its persisted request for the same
// run/step/attempt/call.
func (a *nativeApprover) authorize(ctx context.Context, callID, name string, input json.RawMessage, reason string) (nativeAuthorization, error) {
	if a == nil {
		return nativeAuthorization{Allow: true}, nil
	}
	if ctx.Err() != nil {
		return nativeAuthorization{}, ctx.Err()
	}
	action, primary, targets, network := classifyNativeCall(name, input, a.cwd, a.roots)
	if a.mode != "full_access" {
		for _, target := range targets {
			if a.dangerous[resolvePhysicalPath(target)] {
				if a.gate != nil {
					_ = a.gate.emit("native_hardlink_denied", map[string]any{
						"tool_call_id": callID, "name": safeToken(name), "resource": target,
						"detail": "the path aliases an inode linked outside the project; path-based execution cannot isolate the effect",
					})
				}
				return nativeAuthorization{Reason: "the target hardlinks to an outside inode; this effect is refused to protect the original file"}, nil
			}
		}
	}
	if a.mode == "full_access" {
		if a.callback == nil {
			return nativeAuthorization{Allow: true, Decision: model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce}}, nil
		}
		request := model.NativePermissionRequest{
			CallID: callID, Name: name, Input: append(json.RawMessage{}, input...), Resource: firstRoot(a.roots),
			Reason: reason, Origin: w.OriginNative, Adapter: a.adapter, Action: action,
		}
		_ = a.pendingEvent(request, targets)
		decision, err := a.callback(ctx, request)
		if err != nil {
			_ = a.resolvedEvent(request, false, err.Error())
			return nativeAuthorization{}, err
		}
		if !decision.Authorized() {
			_ = a.resolvedEvent(request, false, decision.Reason)
			return nativeAuthorization{Allow: false, Reason: decision.Reason}, nil
		}
		_ = a.resolvedEvent(request, true, decision.ResourceScope)
		return nativeAuthorization{Allow: true, Decision: decision}, nil
	}
	if a.callback == nil {
		return nativeAuthorization{}, errors.New("native_permission_unavailable: no host approval callback is bound; the project-autonomous adapter refuses to execute native effects without the host permission ledger")
	}
	// Keep a concrete scope for the request. Commands inside the project use
	// their working directory; outside commands use the first resolved target.
	resource := primary
	switch {
	case network:
		resource = "network:public"
	case action == "execute":
		outside := firstOutside(targets, a.roots)
		if outside != "" {
			resource = outside
		} else if len(a.roots) != 0 {
			resource = firstRoot(a.roots)
		}
	case resource == "" && len(a.roots) != 0:
		resource = firstRoot(a.roots)
	}
	request := model.NativePermissionRequest{
		CallID: callID, Name: name, Input: append(json.RawMessage{}, input...), Resource: resource,
		Reason: reason, Origin: w.OriginNative, Adapter: a.adapter, Action: action,
	}
	if err := a.pendingEvent(request, targets); err != nil {
		return nativeAuthorization{}, err
	}
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: authorize call=%q name=%q action=%q resource=%q\n", callID, name, action, resource)
	}
	decision, err := a.wait(ctx, request)
	if err != nil {
		_ = a.resolvedEvent(request, false, err.Error())
		return nativeAuthorization{}, err
	}
	if !decision.Authorized() {
		_ = a.resolvedEvent(request, false, decision.Reason)
		return nativeAuthorization{Allow: false, Reason: decision.Reason}, nil
	}
	// The host's normalized scope is the only widening value. It may be empty
	// for an automatically admitted in-project call; the project roots are
	// already in the sandbox profile.
	scope := decision.ResourceScope
	if scope != "" && !filepath.IsAbs(scope) && scope != "network:public" {
		_ = a.resolvedEvent(request, false, "approved scope is not an absolute resource")
		return nativeAuthorization{Reason: "the host returned a non-absolute resource scope; the operation was not executed"}, nil
	}
	if scope != "" && resource != "network:public" && !scopeCovers(scope, resource) && !scopeCovers(resource, scope) {
		_ = a.resolvedEvent(request, false, "approved scope does not match the requested resource")
		return nativeAuthorization{Reason: "the host approved a different resource; the operation was not executed"}, nil
	}
	if resource != "network:public" && filepath.IsAbs(resource) && !withinAnyRoot(resource, a.roots) && scope == "" {
		_ = a.resolvedEvent(request, false, "outside-scope approval has no resource scope")
		return nativeAuthorization{Reason: "the host did not return the approved resource scope; the operation was not executed"}, nil
	}
	authorization := nativeAuthorization{Allow: true, Decision: decision}
	if filepath.IsAbs(scope) && !withinAnyRoot(scope, a.roots) {
		switch action {
		case "read":
			authorization.ReadScopes = append(authorization.ReadScopes, scope)
		case "write", "execute":
			authorization.ReadScopes = append(authorization.ReadScopes, scope)
			authorization.WriteScopes = append(authorization.WriteScopes, scope)
		}
	}
	_ = a.resolvedEvent(request, true, decision.ResourceScope)
	return authorization, nil
}

func firstOutside(targets, roots []string) string {
	// Use the first extracted outside data target. Shell tokens commonly begin
	// with an absolute executable (/bin/zsh, /usr/bin/curl), which is skipped,
	// followed by the effect target. Choosing the first non-system outside path
	// gives the host the exact resource the user is asked about; a later target
	// still has to pass the per-call sandbox and can request its own approval.
	for _, target := range targets {
		if withinAnyRoot(target, roots) || systemExecutable(target) {
			continue
		}
		return target
	}
	return ""
}

// systemExecutable keeps a shell's absolute interpreter path out of the
// approval scope. The user is not being asked to allow /bin/zsh; the effect is
// the command's data target, which is chosen instead.
func systemExecutable(path string) bool {
	for _, prefix := range []string{"/bin/", "/sbin/", "/usr/bin/", "/usr/sbin/", "/usr/local/bin/", "/opt/homebrew/bin/", "/System/", "/Library/Apple/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// authorizeExternal holds one mutating external request (method, URL, body
// digest) until the host approves that exact effect. Reads and dependency
// downloads never reach this path. The adapter forwards only after an allow.
func (a *nativeApprover) authorizeExternal(ctx context.Context, method, target, digest string) (bool, string) {
	if a == nil {
		return true, ""
	}
	if a.mode == "full_access" && a.callback == nil {
		return true, ""
	}
	if a.callback == nil {
		return false, "external writes require a host approval callback"
	}
	sequence := a.externalSeq.Add(1)
	shortDigest := digest
	if len(shortDigest) > 16 {
		shortDigest = shortDigest[:16]
	}
	callID := fmt.Sprintf("external-%d-%s", sequence, shortDigest)
	input, _ := json.Marshal(map[string]string{"method": method, "url": target, "body_sha256": digest})
	request := model.NativePermissionRequest{
		// The host classifies this as an execute effect outside the project.
		// The synthetic scope is derived only from the exact URL so a
		// conversation grant cannot match a different external destination; the
		// real method, URL and body digest remain in Input and Reason.
		CallID: callID, Name: "external_http", Input: input, Resource: externalResourceScope(target),
		Reason: method + " " + target, Origin: w.OriginNative, Adapter: a.adapter, Action: "execute",
	}
	_ = a.pendingEvent(request, nil)
	decision, err := a.callback(ctx, request)
	if err != nil {
		_ = a.resolvedEvent(request, false, err.Error())
		return false, err.Error()
	}
	if !decision.Authorized() {
		reason := decision.Reason
		if reason == "" {
			reason = "the user denied this external effect"
		}
		_ = a.resolvedEvent(request, false, reason)
		return false, reason
	}
	_ = a.resolvedEvent(request, true, target)
	return true, ""
}

// externalResourceScope maps an external URL to an absolute outside-project
// scope. It is a request key/display hint, never a filesystem authorization;
// the proxy forwards only the exact URL captured in the request Input.
func externalResourceScope(target string) string {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return "/.cocogem-external/unknown"
	}
	segments := []string{"/.cocogem-external", parsed.Hostname()}
	for _, part := range strings.Split(parsed.EscapedPath(), "/") {
		if part != "" && part != "." && part != ".." {
			segments = append(segments, part)
		}
	}
	return path.Join(segments...)
}

func (a *nativeApprover) wait(ctx context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
	type result struct {
		decision model.NativePermissionDecision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		decision, err := a.callback(ctx, request)
		done <- result{decision, err}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return model.NativePermissionDecision{}, ctx.Err()
		case <-ticker.C:
			if a.live != nil {
				a.live.valid("permission_wait")
			}
			if a.gate != nil {
				_ = a.gate.activity(activityPermissionWait)
			}
		case value := <-done:
			if value.err != nil {
				return model.NativePermissionDecision{}, value.err
			}
			return value.decision, nil
		}
	}
}

func (a *nativeApprover) pendingEvent(request model.NativePermissionRequest, targets []string) error {
	if a.gate == nil {
		return nil
	}
	data := map[string]any{
		"tool_call_id": request.CallID, "name": request.Name, "origin": w.OriginNative,
		"status": "awaiting_permission", "action": request.Action, "resource": request.Resource,
		"reason": request.Reason, "adapter": request.Adapter, "targets": targets,
	}
	return a.gate.emit("native_permission", data)
}

func (a *nativeApprover) resolvedEvent(request model.NativePermissionRequest, allowed bool, detail string) error {
	if a.gate == nil {
		return nil
	}
	return a.gate.emit("native_permission_resolved", map[string]any{
		"tool_call_id": request.CallID, "name": request.Name, "origin": w.OriginNative,
		"allowed": allowed, "detail": detail,
	})
}

func firstRoot(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}

// classifyNativeCall returns the workspace action, a display resource, the
// resolved path targets and whether this is a public-network tool.
func classifyNativeCall(name string, input json.RawMessage, cwd string, roots []string) (action, primary string, targets []string, network bool) {
	action = w.ActionForNativeTool(name)
	var args map[string]any
	_ = json.Unmarshal(input, &args)
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
				return value
			}
		}
		return ""
	}
	switch strings.ToLower(name) {
	case "webfetch", "websearch", "web_search", "fetch":
		return "network", pick("url", "query"), nil, true
	case "bash", "shell", "run_shell_command", "exec", "command":
		command := pick("command", "cmd")
		primary = command
		tokens := shellPathTokens(command)
		if cwd == "" && len(roots) != 0 {
			cwd = roots[0]
		}
		for _, token := range tokens {
			resolved := resolveTarget(token, cwd)
			if resolved == "" {
				continue
			}
			targets = appendUnique(targets, resolved)
		}
		return "execute", primary, targets, false
	}
	target := pick("path", "file_path", "notebook_path", "directory", "cwd")
	if target == "" {
		target = "."
	}
	resolved := resolveTarget(target, cwd)
	if resolved != "" {
		targets = append(targets, resolved)
	}
	primary = resolved
	return action, primary, targets, false
}

// resolveTarget resolves one path relative to the project cwd. It never returns
// a lexical prefix result: the deepest existing ancestor is resolved.
func resolveTarget(target, cwd string) string {
	target = strings.TrimSpace(target)
	target = strings.Trim(target, `"'`)
	if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "git@") {
		return ""
	}
	if !filepath.IsAbs(target) {
		if cwd == "" {
			return ""
		}
		target = filepath.Join(cwd, target)
	}
	return resolvePhysicalPath(filepath.Clean(target))
}

// shellPathTokens extracts plausible filesystem arguments from a shell command.
// It is only used to decide which exact scope to show and to ask the host for;
// the OS sandbox remains the authority for what actually executes.
func shellPathTokens(command string) []string {
	if command == "" {
		return nil
	}
	replacer := strings.NewReplacer(";", " ", "|", " ", "&", " ", "<", " ", ">", " ", "\n", " ", "\t", " ", "(", " ", ")", " ", "\"", " ", "'", " ")
	tokens := strings.Fields(replacer.Replace(command))
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.Trim(token, `"'`)
		if token == "" || strings.HasPrefix(token, "-") || strings.Contains(token, "=") {
			continue
		}
		if strings.ContainsAny(token, "*?[]${}") {
			continue
		}
		if strings.HasPrefix(token, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				token = filepath.Join(home, strings.TrimPrefix(token, "~"))
			}
		}
		if filepath.IsAbs(token) || strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") || strings.Contains(token, string(filepath.Separator)) {
			out = appendUnique(out, token)
		}
		if len(out) >= 32 {
			break
		}
	}
	return out
}

func withinAnyRoot(target string, roots []string) bool {
	for _, root := range roots {
		if scopeCovers(root, target) {
			return true
		}
	}
	return false
}

// scopeCovers compares resolved paths, never string prefixes.
func scopeCovers(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	root = resolvePhysicalPath(root)
	target = resolvePhysicalPath(target)
	return w.ScopeContains(root, target)
}
