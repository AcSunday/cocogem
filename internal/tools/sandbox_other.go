//go:build !darwin && !linux

package tools

import (
	"context"
	"fmt"
)

func prepareSandbox(context.Context, sandboxSpec) (sandboxPlan, error) {
	return sandboxPlan{}, fmt.Errorf("%w: this platform has no verified test sandbox", ErrSandboxUnavailable)
}

// prepareCommandSandbox fails closed on platforms without a verified boundary.
func prepareCommandSandbox(ctx context.Context, spec sandboxSpec) (sandboxPlan, error) {
	return prepareSandbox(ctx, spec)
}
