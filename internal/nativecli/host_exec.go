package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// hostExecutionBroker is the shared approve-then-execute helper for adapters
// whose CLI cannot widen a fixed sandbox profile. The adapter asks the broker
// for a decision, then calls RunAuthorized with the same exact operation. The
// host executes only the approved call under a per-call profile and returns the
// actual result, so the original invocation continues without replaying the CLI.
type hostExecutionBroker struct {
	gate     *bridge
	approver *nativeApprover
	sandbox  *nativeSandbox
	cwd      string
	roots    []string
}

func newHostExecutionBroker(gate *bridge) *hostExecutionBroker {
	broker := &hostExecutionBroker{gate: gate, cwd: gate.cwd}
	if gate.approver != nil {
		broker.approver = gate.approver
		broker.roots = append([]string{}, gate.approver.roots...)
	}
	broker.sandbox = gate.sandbox
	return broker
}

// Authorize routes one pre-effect call through the host ledger.
func (b *hostExecutionBroker) Authorize(ctx context.Context, callID, name string, input json.RawMessage) (nativeAuthorization, error) {
	if b == nil || b.approver == nil {
		return nativeAuthorization{Allow: true}, nil
	}
	return b.approver.authorize(ctx, callID, name, input, "")
}

// RunAuthorized executes the already-approved operation and returns the real
// output. A host execution error is an error result for the model, never a
// synthesized success.
func (b *hostExecutionBroker) RunAuthorized(ctx context.Context, authorization nativeAuthorization, name string, input json.RawMessage) (string, bool, error) {
	if authorization.Allow == false {
		return "permission_denied: the operation was not authorized", true, nil
	}
	switch strings.ToLower(name) {
	case "bash", "shell", "command", "run_command":
		return b.runCommand(ctx, authorization, input)
	case "read", "read_file":
		return b.runRead(ctx, authorization, input)
	case "write", "write_file":
		return b.runWrite(ctx, authorization, input)
	case "webfetch", "fetch":
		return b.runFetch(ctx, authorization, input)
	default:
		return "", true, fmt.Errorf("host execution does not implement native tool %s", safeToken(name))
	}
}

func (b *hostExecutionBroker) runCommand(ctx context.Context, authorization nativeAuthorization, raw json.RawMessage) (string, bool, error) {
	var input struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
		CWD     string `json:"cwd"`
	}
	if json.Unmarshal(raw, &input) != nil || strings.TrimSpace(input.Command) == "" && strings.TrimSpace(input.Cmd) == "" {
		return "", true, errors.New("host command input is invalid")
	}
	command := input.Command
	if command == "" {
		command = input.Cmd
	}
	cwd := input.CWD
	if cwd == "" {
		cwd = b.cwd
	}
	if b.sandbox != nil && b.sandbox.canWrap() {
		profile, err := b.sandbox.profile("host-"+safeToken(fmt.Sprint(time.Now().UnixNano())), authorization.ReadScopes, authorization.WriteScopes)
		if err != nil {
			return "", true, err
		}
		command = b.sandbox.wrap(profile, cwd, command)
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/bin/bash", "--noprofile", "--norc", "-c", command)
	cmd.Dir = cwd
	output := &boundedBuffer{limit: pluginOutputLimit, truncate: true}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return output.String(), true, nil
		}
		return "", true, errors.New("host command could not start")
	}
	return output.String(), false, nil
}

func (b *hostExecutionBroker) runRead(_ context.Context, authorization nativeAuthorization, raw json.RawMessage) (string, bool, error) {
	path := hostPath(raw)
	if path == "" || !b.pathAllowed(path, authorization.ReadScopes, false) {
		return "permission_denied: path is outside the approved read scope", true, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", true, errors.New("host read could not open the file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return "", true, errors.New("host read exceeded its bounded output")
	}
	return string(data), false, nil
}

func (b *hostExecutionBroker) runWrite(_ context.Context, authorization nativeAuthorization, raw json.RawMessage) (string, bool, error) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &input) != nil || input.Path == "" {
		return "", true, errors.New("host write input is invalid")
	}
	path := input.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.cwd, path)
	}
	if !b.pathAllowed(path, authorization.WriteScopes, true) {
		return "permission_denied: path is outside the approved write scope", true, nil
	}
	if err := os.WriteFile(path, []byte(input.Content), 0600); err != nil {
		return "", true, errors.New("host write failed")
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(input.Content), path), false, nil
}

func (b *hostExecutionBroker) runFetch(ctx context.Context, _ nativeAuthorization, raw json.RawMessage) (string, bool, error) {
	var input struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &input) != nil || !strings.HasPrefix(input.URL, "https://") && !strings.HasPrefix(input.URL, "http://") {
		return "", true, errors.New("host fetch URL is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
	if err != nil {
		return "", true, errors.New("host fetch request is invalid")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", true, errors.New("host fetch failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", true, fmt.Errorf("host fetch returned %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return "", true, errors.New("host fetch exceeded its bounded output")
	}
	return string(data), false, nil
}

func (b *hostExecutionBroker) pathAllowed(path string, scopes []string, write bool) bool {
	path = resolvePhysicalPath(path)
	if withinAnyRoot(path, b.roots) {
		return true
	}
	for _, scope := range scopes {
		if scopeCovers(scope, path) {
			return true
		}
	}
	return false
}

func hostPath(raw json.RawMessage) string {
	var input struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(raw, &input)
	if input.Path != "" {
		return input.Path
	}
	return input.FilePath
}

// wireHostEgressApproval makes a session's egress proxy ask the same host
// callback before forwarding an external mutating request.
func wireHostEgressApproval(proxy *egressProxy, gate *bridge) {
	if proxy == nil || gate == nil {
		return
	}
	proxy.approve = func(ctx context.Context, method, target, digest string) (bool, string) {
		if gate.approver == nil {
			return false, "external writes require a host approval callback"
		}
		return gate.approver.authorizeExternal(ctx, method, target, digest)
	}
}
