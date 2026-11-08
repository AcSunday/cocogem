package nativecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/AcSunday/cocogem/internal/model"
)

// geminiNativeSession owns the per-attempt objects for a project_autonomous
// Gemini run: the host approval gate, the OS sandbox each shell call is rewritten
// into, the egress proxy that command sandbox reaches, and the private staging
// area for approved outside paths.
//
// The whole CLI process is not sandboxed: it needs the model connection and the
// loopback MCP bridge. The boundary is per call, applied where the effect is.
type geminiNativeSession struct {
	request model.Request
	gate    *bridge
	broker  *gateBroker
	sandbox *nativeSandbox
	proxy   *egressProxy
	workdir string
}

func prepareGeminiNativeSession(request model.Request, base, workdir string, gate *bridge, live *liveness, launch *connectionPlan, probeURL string) (*geminiNativeSession, error) {
	if request.WorkspaceRoot == "" {
		return nil, errors.New("project-autonomous native execution requires a bound project root")
	}
	sandbox, err := newNativeSandbox(base)
	if err != nil {
		return nil, err
	}
	sandbox.addRoots(request.WorkspaceRoot, request.WorkingDirectory)
	session := &geminiNativeSession{request: request, gate: gate, sandbox: sandbox, workdir: workdir}
	if probeURL == "" {
		// Real turns get the host egress proxy so dependency retrieval keeps
		// working while arbitrary external writes stay refused by the proxy.
		proxy, proxyErr := newEgressProxy(func(host, method string) {
			_ = gate.notice("native_egress_blocked", "sandboxed command attempted "+method+" "+host+"; that external write was not authorized")
		})
		if proxyErr != nil {
			return nil, errors.New("native egress policy unavailable: " + proxyErr.Error())
		}
		session.proxy = proxy
		sandbox.proxyURL, sandbox.proxyAddr = proxy.url(), proxy.addr()
		if err := sandbox.setCA(proxy.caPEM); err != nil {
			proxy.close()
			return nil, err
		}
	}
	staging := filepath.Join(base, "staged")
	if err := os.MkdirAll(staging, 0700); err != nil {
		session.close()
		return nil, errors.New("create private staging directory")
	}
	approver := newNativeApprover(gate, live, request, "gemini", sandbox)
	gate.approver = approver
	gate.sandbox = sandbox
	// After the approver is bound, external mutating requests must ask the host
	// instead of being silently blocked, matching the Claude/OpenCode behaviour.
	wireHostEgressApproval(session.proxy, gate)
	// Every root the sandbox can write must be scanned: an inside file whose
	// inode also links outside would let a path rule modify the outside
	// original. An unscanned root is unverified and fails closed.
	scan := gateHardlinkScan(sandbox, request.WorkspaceRoot, request.WorkingDirectory)
	if err := applyGateHardlinkPolicy(gate, approver, sandbox, scan); err != nil {
		session.close()
		return nil, err
	}
	broker, err := newGateBroker(approver, sandbox, staging, request.WorkspaceRoot, "gemini")
	if err != nil {
		session.close()
		return nil, err
	}
	session.broker = broker
	// The stream parser and the hook broker must share one identity map without
	// changing the shared bridge type.
	attachGateAliases(gate, broker.nativeAliases())
	return session, nil
}

func (s *geminiNativeSession) close() {
	if s == nil {
		return
	}
	detachGateAliases(s.gate)
	if s.broker != nil {
		s.broker.close()
	}
	if s.proxy != nil {
		s.proxy.close()
	}
}

// nativeArgs builds the CLI invocation after the session exists (the gate hook
// needs the broker endpoint).
func (s *geminiNativeSession) nativeArgs() ([]string, map[string]string, error) {
	return prepareGeminiNative(s.request, s.workdir, s.gate, s.broker)
}

// streamGeminiNative runs the headless CLI whose private hook consults the host
// gate before every real tool effect.
func (p Provider) streamGeminiNative(ctx context.Context, cmd *exec.Cmd, gate *bridge, live *liveness, session *geminiNativeSession) error {
	if session == nil {
		return errors.New("the Gemini native session is unavailable")
	}
	defer session.close()
	return streamProcessWithClaude(ctx, "gemini", cmd, gate, live, nil)
}
