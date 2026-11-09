package nativecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/AcSunday/cocogem/internal/model"
)

// piNativeSession owns the per-attempt objects for a project_autonomous Pi run.
type piNativeSession struct {
	request  model.Request
	gate     *bridge
	broker   *gateBroker
	sandbox  *nativeSandbox
	proxy    *egressProxy
	workdir  string
	launch   *connectionPlan
	probeURL string
}

func preparePiNativeSession(request model.Request, base, workdir string, gate *bridge, live *liveness, launch *connectionPlan, probeURL string) (*piNativeSession, error) {
	if request.WorkspaceRoot == "" {
		return nil, errors.New("project-autonomous native execution requires a bound project root")
	}
	sandbox, err := newNativeSandbox(base)
	if err != nil {
		return nil, err
	}
	sandbox.addRoots(request.WorkspaceRoot, request.WorkingDirectory)
	session := &piNativeSession{request: request, gate: gate, sandbox: sandbox, workdir: workdir, launch: launch, probeURL: probeURL}
	if probeURL == "" {
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
	approver := newNativeApprover(gate, live, request, "pi", sandbox)
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
	broker, err := newGateBroker(approver, sandbox, staging, request.WorkspaceRoot, "pi")
	if err != nil {
		session.close()
		return nil, err
	}
	session.broker = broker
	return session, nil
}

func (s *piNativeSession) close() {
	if s == nil {
		return
	}
	if s.broker != nil {
		s.broker.close()
	}
	if s.proxy != nil {
		s.proxy.close()
	}
}

func (s *piNativeSession) nativeArgs() ([]string, map[string]string, error) {
	return preparePiNative(s.request, s.workdir, s.gate, s.launch, s.broker, s.probeURL)
}

// streamPiNative runs Pi's RPC mode with the private gate extension.
func (p Provider) streamPiNative(ctx context.Context, cmd *exec.Cmd, gate *bridge, live *liveness, session *piNativeSession, prompt string) error {
	if session == nil {
		return errors.New("the Pi native session is unavailable")
	}
	defer session.close()
	return streamPiRPC(ctx, cmd, gate, live, prompt)
}
