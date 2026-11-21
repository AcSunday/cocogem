//go:build linux

package tools

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestLinuxBubblewrapProbeReportsHonestly is the CI regression: a probe never
// claims a working sandbox it could not start, and the reported kind names the
// actual AppArmor/userns gap rather than a generic failure.
func TestLinuxBubblewrapProbeReportsHonestly(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := probeLinuxBubblewrap(ctx)
	if err != nil {
		if !errors.Is(err, ErrSandboxUnavailable) {
			t.Fatalf("probe error is not a fail-closed capability gap: %v", err)
		}
		return
	}
	// A successful probe is only a kernel capability fact; the full boundary
	// tests must still gate run_test.
	if _, err := prepareSandbox(ctx, sandboxSpec{}); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("unverified cross-build was admitted: %v", err)
	}
}
