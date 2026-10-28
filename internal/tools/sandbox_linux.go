//go:build linux

package tools

import (
	"context"
	"fmt"
)

// prepareSandbox fails closed on Linux. Bubblewrap was never verified against a
// real Linux host, and an unverified namespace setup must not be presented as a
// strong isolation boundary; cross-compilation is not verification. Reintroduce
// the bwrap path only together with real-host boundary tests.
func prepareSandbox(context.Context, sandboxSpec) (sandboxPlan, error) {
	return sandboxPlan{}, fmt.Errorf("%w: the Linux test sandbox is not verified on a real host; run_test is blocked rather than run with an unverified boundary", ErrSandboxUnavailable)
}
