package nativecli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// gateBroker is the per-attempt loopback endpoint a CLI-owned pre-effect gate
// calls. Gemini's private BeforeTool/AfterTool hooks and Pi's private
// tool_call/tool_result extension both post here.
//
// The broker only decides and narrows input; the CLI still performs the effect,
// so native tool results, events and session identity stay the CLI's own. An
// approved outside-project path therefore continues in the same call through a
// host-staged copy (read) or a staged mirror committed after the tool's real
// result (write) instead of being denied and re-asked.
//
// Idempotency: a decision is bound to (call id, name, exact normalized input).
// A repeated authorize for the same binding returns the stored decision instead
// of asking the host again; a repeated authorize with different arguments under
// the same call id is refused. finalize is idempotent, and two calls targeting
// the same outside file cannot both hold a pending commit.
type gateBroker struct {
	approver *nativeApprover
	sandbox  *nativeSandbox
	cwd      string
	staging  string
	adapter  string

	listener net.Listener
	server   *http.Server
	token    string

	aliases *gateAliases

	mu       sync.Mutex
	decided  map[string]*gateSettled
	commits  map[string]*gateCommit
	heldPath map[string]string
}

// gateSettled is one stored decision for an exact call binding.
type gateSettled struct {
	digest   [32]byte
	decision gateDecision
}

// gateCommit is the deferred outside-project write. staged holds the CLI's
// result; final is the exact approved target.
type gateCommit struct {
	callID string
	staged string
	final  string
}

type gateRequest struct {
	CallID  string          `json:"call_id"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Reason  string          `json:"reason"`
	IsError bool            `json:"is_error"`
}

// gateDecision is the neutral broker answer. Each adapter translates it into its
// own protocol shape.
type gateDecision struct {
	Allow  bool            `json:"allow"`
	Reason string          `json:"reason,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Action string          `json:"action,omitempty"`
}

func newGateBroker(approver *nativeApprover, sandbox *nativeSandbox, staging, cwd, adapter string) (*gateBroker, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, errors.New("generate native gate token")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("open native gate listener")
	}
	broker := &gateBroker{
		approver: approver, sandbox: sandbox, staging: staging, cwd: cwd, adapter: adapter,
		listener: listener, token: hex.EncodeToString(secret[:]),
		aliases: newGateAliases(),
		decided: map[string]*gateSettled{}, commits: map[string]*gateCommit{}, heldPath: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/gate/authorize", broker.serveAuthorize)
	mux.HandleFunc("/gate/finalize", broker.serveFinalize)
	mux.HandleFunc("/gate/gemini/authorize", broker.serveGeminiAuthorize)
	mux.HandleFunc("/gate/gemini/finalize", broker.serveGeminiFinalize)
	mux.HandleFunc("/gate/pi/tool_call", broker.servePiToolCall)
	broker.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	go func() { _ = broker.server.Serve(listener) }()
	return broker, nil
}

func (b *gateBroker) url() string { return "http://" + b.listener.Addr().String() }

// nativeAliases is the per-attempt identity map shared with the stream parser.
func (b *gateBroker) nativeAliases() *gateAliases {
	if b == nil {
		return nil
	}
	return b.aliases
}

func (b *gateBroker) close() {
	if b == nil {
		return
	}
	_ = b.server.Close()
	_ = b.listener.Close()
}

func (b *gateBroker) authorized(r *http.Request) bool {
	return r.Host == b.listener.Addr().String() && r.Header.Get("Origin") == "" &&
		subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.token)) == 1
}

func (b *gateBroker) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var request gateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || request.CallID == "" || request.Name == "" {
		http.Error(w, "invalid gate request", http.StatusBadRequest)
		return
	}
	writeGateJSON(w, b.authorize(r.Context(), request))
}

func (b *gateBroker) serveFinalize(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var request gateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || request.CallID == "" {
		http.Error(w, "invalid finalize request", http.StatusBadRequest)
		return
	}
	if err := b.finalize(request.CallID, request.IsError); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeGateJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

// callDigest binds a decision to the exact call identity and arguments.
func callDigest(name string, input json.RawMessage) [32]byte {
	return sha256.Sum256([]byte(name + "\x00" + string(input)))
}

// authorize admits one not-yet-executed native call and returns the exact input
// the CLI may execute.
func (b *gateBroker) authorize(ctx context.Context, request gateRequest) gateDecision {
	call, err := classifyNativeCallForGate(b.adapter, request.Name, request.Input, b.cwd)
	if err != nil {
		return gateDecision{Reason: err.Error()}
	}
	if call.HostTool {
		// Host MCP tools are authorized by the bridge's own tool permissions.
		return gateDecision{Allow: true, Input: request.Input, Action: call.Action}
	}
	digest := callDigest(request.Name, call.Normalized)
	b.mu.Lock()
	if settled := b.decided[request.CallID]; settled != nil {
		same := settled.digest == digest
		b.mu.Unlock()
		if !same {
			return gateDecision{Reason: "the native CLI reused a tool-call identity with different arguments; the call was refused"}
		}
		// A retried gate delivery returns the decision already made; it never
		// asks the host twice and never re-stages.
		return settled.decision
	}
	b.mu.Unlock()

	// The shared classifier derives an execute call's targets from the command
	// text, so an explicit outside `dir_path` would not appear in the request the
	// user is shown. Ask first with the exact directory in the normalized input,
	// and refuse an outside working directory the host did not approve for this
	// call (recorded in coordination.md as the shared classifier follow-up).
	authorization, authErr := b.approver.authorize(ctx, request.CallID, request.Name, call.Normalized, request.Reason)
	if authErr != nil {
		decision := gateDecision{}
		if ctx.Err() != nil {
			decision.Reason = "the authorization wait ended before any effect"
		} else {
			decision.Reason = authErr.Error()
		}
		// A denial caused by a host error is final for this binding: retrying
		// the same hook delivery must not turn it into an allow.
		b.store(request.CallID, digest, decision)
		return decision
	}
	if !authorization.Allow {
		reason := authorization.Reason
		if reason == "" {
			reason = "用户拒绝或授权已取消"
		}
		decision := gateDecision{Reason: reason}
		b.store(request.CallID, digest, decision)
		return decision
	}
	rewritten, commit, deny, err := b.scopeInput(ctx, request, call, authorization)
	if err != nil {
		decision := gateDecision{Reason: err.Error()}
		b.store(request.CallID, digest, decision)
		return decision
	}
	if deny != "" {
		decision := gateDecision{Reason: deny}
		b.store(request.CallID, digest, decision)
		return decision
	}
	if rewritten == nil {
		rewritten = request.Input
	}
	decision := gateDecision{Allow: true, Input: rewritten, Action: call.Action}
	if commit != nil {
		if !b.hold(commit) {
			refused := gateDecision{Reason: "another pending native call already owns this outside file; the write was not started"}
			b.store(request.CallID, digest, refused)
			return refused
		}
	}
	b.store(request.CallID, digest, decision)
	return decision
}

func (b *gateBroker) store(callID string, digest [32]byte, decision gateDecision) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.decided[callID] = &gateSettled{digest: digest, decision: decision}
}

// hold reserves the exact outside path for one pending write. A second call to
// the same path is refused so two tools cannot interleave into one file.
func (b *gateBroker) hold(commit *gateCommit) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if owner, taken := b.heldPath[commit.final]; taken && owner != commit.callID {
		return false
	}
	b.heldPath[commit.final] = commit.callID
	b.commits[commit.callID] = commit
	return true
}

// finalize commits or discards the staged result of one call. It is idempotent:
// a repeated finalize finds no pending commit and does nothing.
func (b *gateBroker) finalize(callID string, failed bool) error {
	b.mu.Lock()
	commit := b.commits[callID]
	if commit != nil {
		delete(b.commits, callID)
		if b.heldPath[commit.final] == callID {
			delete(b.heldPath, commit.final)
		}
	}
	if b.decided[callID] != nil {
		// The decision is consumed with the call: a later distinct effect needs a
		// new host decision, but a duplicate finalize changes nothing.
		delete(b.decided, callID)
	}
	b.mu.Unlock()
	if commit == nil {
		return nil
	}
	if failed {
		return nil
	}
	data, err := os.ReadFile(commit.staged)
	if err != nil {
		return errors.New("the staged result is unavailable; the outside file was not written")
	}
	if err := os.WriteFile(commit.final, data, 0600); err != nil {
		return errors.New("the approved outside write could not be committed")
	}
	return nil
}

// finalizeByPath resolves the pending commit of a tool that reported only its
// original path (Gemini's AfterTool receives the pre-rewrite input).
func (b *gateBroker) finalizeByPath(path string, failed bool) error {
	if path == "" {
		return nil
	}
	resolved := resolvePhysicalPath(path)
	b.mu.Lock()
	callID := b.heldPath[resolved]
	b.mu.Unlock()
	if callID == "" {
		return nil
	}
	return b.finalize(callID, failed)
}

// scopeInput turns one approved call into something the CLI cannot use to leave
// the approved boundary. Shell commands run inside a per-call seatbelt profile
// holding exactly the scopes the host approved.
func (b *gateBroker) scopeInput(ctx context.Context, request gateRequest, call gateCall, authorization nativeAuthorization) (json.RawMessage, *gateCommit, string, error) {
	if call.Spec.network {
		return request.Input, nil, "", nil
	}
	if call.Spec.commandKey != "" {
		return b.scopeCommand(ctx, request, call, authorization)
	}
	target := gateScopeTarget(call)
	if target == "" {
		return request.Input, nil, "", nil
	}
	if withinAnyRoot(target, b.approver.roots) {
		return request.Input, nil, "", nil
	}
	writable := call.Action == "write"
	scopes := authorization.ReadScopes
	if writable {
		scopes = authorization.WriteScopes
	}
	if !scopeApproved(scopes, target) {
		return nil, nil, "the host approved a different resource; this path was not executed", nil
	}
	if b.staging == "" {
		return nil, nil, "no private staging area is available for the approved outside path", nil
	}
	dir := filepath.Join(b.staging, safeToken(request.CallID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, "cannot prepare the approved outside path", nil
	}
	staged := filepath.Join(dir, filepath.Base(target))
	if data, err := os.ReadFile(target); err == nil {
		if err := os.WriteFile(staged, data, 0600); err != nil {
			return nil, nil, "cannot stage the approved outside path", nil
		}
	} else if writable {
		if err := os.WriteFile(staged, nil, 0600); err != nil {
			return nil, nil, "cannot stage the approved outside path", nil
		}
	} else {
		return nil, nil, "the approved outside path could not be read", nil
	}
	rewritten, err := rewriteNativePath(request.Input, call.PathKey, staged)
	if err != nil {
		return nil, nil, err.Error(), nil
	}
	if !writable {
		return rewritten, nil, "", nil
	}
	return rewritten, &gateCommit{callID: request.CallID, staged: staged, final: target}, "", nil
}

func (b *gateBroker) scopeCommand(ctx context.Context, request gateRequest, call gateCall, authorization nativeAuthorization) (json.RawMessage, *gateCommit, string, error) {
	if b.sandbox == nil || !b.sandbox.canWrap() {
		return nil, nil, "the native command boundary is unavailable on this platform", nil
	}
	var values map[string]any
	if err := json.Unmarshal(request.Input, &values); err != nil || values == nil {
		return nil, nil, "the native tool input is not an object", nil
	}
	command, _ := values[call.Spec.commandKey].(string)
	if strings.TrimSpace(command) == "" {
		return nil, nil, "the native command is empty", nil
	}
	cwd := b.cwd
	if call.Target != "" {
		if info, err := os.Stat(call.Target); err == nil && info.IsDir() {
			cwd = call.Target
		}
	}
	// The shared classifier derives an execute call's scope from the command
	// text, so an outside `dir_path` is not part of the request the user was
	// shown. Ask for exactly that directory as one extra single scope, in the
	// same call, and continue only if the host approves precisely it.
	if call.Target != "" && !withinAnyRoot(call.Target, b.approver.roots) &&
		!scopeApproved(append(append([]string{}, authorization.ReadScopes...), authorization.WriteScopes...), call.Target) {
		directory, err := json.Marshal(map[string]any{"file_path": call.Target})
		if err != nil {
			return nil, nil, "the command's working directory could not be requested", nil
		}
		extra, err := b.approver.authorize(ctx, request.CallID+"#cwd", "read_file", directory,
			"the command runs in this directory")
		if err != nil {
			return nil, nil, "the command's working directory authorization failed; the command was not executed", nil
		}
		if !extra.Allow || !scopeApproved(extra.ReadScopes, call.Target) {
			return nil, nil, "the command's outside working directory was not approved; the command was not executed", nil
		}
		// Add only the approved directory itself, never a parent or sibling. It
		// goes in the write list because the directory is the command's working
		// directory: the sandbox must be able to start there and the command may
		// create files in it, which is exactly what the user approved.
		authorization.ReadScopes = appendUnique(authorization.ReadScopes, call.Target)
		authorization.WriteScopes = appendUnique(authorization.WriteScopes, call.Target)
	}
	// The shared classifier reports one representative outside target, so a
	// command touching several outside paths could otherwise be approved for one
	// path and write another. Every outside path in the command must be covered
	// by this call's approved scopes, or nothing runs. The approved working
	// directory is included because the sandbox must be able to start there.
	approved := append(append([]string{}, authorization.ReadScopes...), authorization.WriteScopes...)
	for _, token := range shellPathTokens(command) {
		target := resolveTarget(token, cwd)
		if target == "" || withinAnyRoot(target, b.approver.roots) {
			continue
		}
		if !scopeApproved(approved, target) {
			return nil, nil, "the command touches an outside path this call was not approved for (" + target + "); it was not executed", nil
		}
	}
	profile, err := b.sandbox.profile("gate-"+safeToken(request.CallID), authorization.ReadScopes, authorization.WriteScopes)
	if err != nil {
		return nil, nil, "the per-call command boundary could not be prepared", nil
	}
	values[call.Spec.commandKey] = b.sandbox.wrap(profile, cwd, command)
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, nil, "the sandboxed command could not be encoded", nil
	}
	return encoded, nil, "", nil
}

func scopeApproved(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scopeCovers(scope, target) {
			return true
		}
	}
	return false
}
