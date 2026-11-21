//go:build linux

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// probeLinuxBubblewrap runs one minimal, harmless scratch command through
// bubblewrap and reports whether the host kernel actually allows the namespace
// it needs. A plain LookPath is not evidence: Ubuntu 24.04 CI kernels commonly
// disable unprivileged user namespaces through AppArmor, and pretending the
// boundary exists would be a false isolation claim.
//
// The probe result is a capability fact, never an authorization on its own.
func probeLinuxBubblewrap(ctx context.Context) error {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("%w: linux_bubblewrap_missing: bwrap is not installed", ErrSandboxUnavailable)
	}
	scratch, err := os.MkdirTemp("", "cocogem-bwrap-probe-*")
	if err != nil {
		return fmt.Errorf("%w: linux_bwrap_probe_unavailable: a private probe directory could not be created", ErrSandboxUnavailable)
	}
	defer os.RemoveAll(scratch)
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, bwrap,
		"--die-with-parent", "--unshare-all", "--new-session",
		"--ro-bind", "/", "/",
		"--bind", scratch, scratch,
		"--tmpfs", "/tmp",
		"/bin/echo", "cocogem-bwrap-probe",
	)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "cocogem-bwrap-probe") {
		return fmt.Errorf("%w: linux_bwrap_userns_unavailable: the host kernel refused an unprivileged bubblewrap namespace, so no verified Linux test sandbox is available; run_test requests an approval instead of claiming isolation", ErrSandboxUnavailable)
	}
	return nil
}

// prepareSandbox fails closed on Linux. Bubblewrap was never verified against a
// real Linux host, and an unverified namespace setup must not be presented as a
// strong isolation boundary; cross-compilation is not verification. Reintroduce
// the bwrap path only together with real-host boundary tests, and gate it on
// the runtime probe above: a probe success is still not a full boundary test.
func prepareSandbox(ctx context.Context, spec sandboxSpec) (sandboxPlan, error) {
	if err := probeLinuxBubblewrap(ctx); err != nil {
		return sandboxPlan{}, err
	}
	return sandboxPlan{}, fmt.Errorf("%w: linux_bwrap_userns_unavailable: the kernel permits a probe namespace, but this cross-build has not passed the required real-host boundary tests; run_test stays blocked rather than run with an unverified boundary", ErrSandboxUnavailable)
}

// prepareCommandSandbox fails closed for the same reason as prepareSandbox.
func prepareCommandSandbox(ctx context.Context, spec sandboxSpec) (sandboxPlan, error) {
	return prepareSandbox(ctx, spec)
}
