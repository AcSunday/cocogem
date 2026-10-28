//go:build !darwin && !linux

package tools

import (
	"context"
	"fmt"
)

func prepareSandbox(context.Context, sandboxSpec) (sandboxPlan, error) {
	return sandboxPlan{}, fmt.Errorf("%w: this platform has no verified test sandbox", ErrSandboxUnavailable)
}
