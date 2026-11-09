// Package model adapts provider protocols to a small tool-capable model interface.
package model

import (
	"context"
	"encoding/json"
	"time"
)

// MaxToolCalls bounds tool executions per step. It is shared by the Go loop,
// the native CLI bridge, the execution prompt and the product contract.
const MaxToolCalls = 60

// Block is visible text, a structured tool request or its associated result.
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Message is one completed exchange in a model invocation.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Tool describes a host-owned operation using a JSON schema.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}

// Request contains no credentials and is immutable during an API call.
type Request struct {
	Executor  string    `json:"-"`
	Model     string    `json:"model"`
	System    string    `json:"system"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream"`
	// Execution-policy fields never cross the provider wire.
	RequiredTools        []string      `json:"-"`
	ExpectedConnection   Connection    `json:"-"`
	FirstResponseTimeout time.Duration `json:"-"`
	StallTimeout         time.Duration `json:"-"`
	StallAction          string        `json:"-"`
	// ContextSize is the optional host-enforced input budget in estimated
	// tokens; 0 follows the CLI's configured context window. ThinkingLevel is
	// an explicit CLI effort option; empty keeps the CLI's own configured
	// default.
	ContextSize   int    `json:"-"`
	ThinkingLevel string `json:"-"`
	// NativeTools are the CLI-owned capabilities derived from the member's
	// effective grants. WorkspaceRoot bounds native file tools; NativeProxy is
	// the already-resolved public-web proxy for native web processes.
	NativeTools      []string `json:"-"`
	WorkspaceRoot    string   `json:"-"`
	WorkingDirectory string   `json:"-"`
	TrustedExecution bool     `json:"-"`
	NativeProxy      string   `json:"-"`
	// Managed native sessions live only under SessionStorageRoot. SessionID is
	// populated solely after an earlier runner acknowledged the exact session.
	ExecutionMode       string `json:"-"`
	SessionMode         string `json:"-"`
	SessionID           string `json:"-"`
	SessionStorageRoot  string `json:"-"`
	SessionContextEpoch int    `json:"-"`
	// AuthorizeNative is the host's pre-effect admission for a CLI-owned tool
	// call. A nil callback means no host approval is available: native
	// adapters must fail closed for every operation that is not already
	// enforced by a proven OS boundary.
	AuthorizeNative func(context.Context, NativePermissionRequest) (NativePermissionDecision, error) `json:"-"`
}

// NativePermissionRequest is one native tool call that has not produced its
// effect yet and must be admitted by the host. Input is the exact native tool
// input; Resource is the adapter's best-effort primary target and is never an
// authorization by itself.
type NativePermissionRequest struct {
	CallID   string          `json:"call_id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input,omitempty"`
	Resource string          `json:"resource,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Origin   string          `json:"origin"`
	Adapter  string          `json:"adapter"`
	Action   string          `json:"action"`
	// Risk is a non-empty grading fact the adapter proved about the target
	// (currently "hardlink"). It is not an authorization. The host must not
	// auto-allow such a call; a user decision must set AcknowledgedRisk.
	Risk string `json:"risk,omitempty"`
}

// Native permission decision kinds. They describe the user's decision, never a
// CLI-native "always" rule.
const (
	NativeScopeOnce         = "once"
	NativeScopeConversation = "conversation"
	NativeScopeDeny         = "deny"
)

// NativePermissionDecision is the host's answer for exactly one request.
//
// Scope is the decision kind (once/conversation/deny). ResourceScope is the
// host-normalized resource the user actually approved; it may narrow the
// adapter's execution but must never be widened from the raw native input.
// Adapters always answer the CLI with a single-call allow, never a CLI
// session-wide bypass.
type NativePermissionDecision struct {
	Allow         bool   `json:"allow"`
	Scope         string `json:"scope"`
	ResourceScope string `json:"resource_scope,omitempty"`
	Reason        string `json:"reason,omitempty"`
	// AcknowledgedRisk is true only when a human decision explicitly accepted a
	// request whose Risk was non-empty. An automatic in-project admission must
	// leave it false; native then refuses the effect.
	AcknowledgedRisk bool `json:"acknowledged_risk,omitempty"`
}

// Authorized reports whether this decision permits the one pending native call.
func (d NativePermissionDecision) Authorized() bool {
	return d.Allow && (d.Scope == NativeScopeOnce || d.Scope == NativeScopeConversation || d.Scope == "")
}

// Connection records the resolved model and non-secret upstream identity selected
// before an execution attempt.
type Connection struct {
	ResolvedModel string `json:"resolved_model"`
	Identity      string `json:"identity"`
}

// Response confirms the provider's termination and actual observed usage.
type Response struct {
	Blocks       []Block
	StopReason   string
	InputTokens  int
	OutputTokens int
	UsageKnown   bool
}

// Model implementations stream only visible text; the host executes tools.
type Model interface {
	Complete(context.Context, Request, func(string) error) (Response, error)
}
