package agent

import "context"

type toolCallKey struct{}

// WithToolCall binds the exact tool-call identity to the execution context so
// the permission admission can consume and record the right allowance.
func WithToolCall(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallKey{}, id)
}

// ToolCallID returns the bound tool-call identity, or "" outside a call.
func ToolCallID(ctx context.Context) string {
	value, _ := ctx.Value(toolCallKey{}).(string)
	return value
}
