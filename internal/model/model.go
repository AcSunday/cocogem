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
	NativeTools   []string `json:"-"`
	WorkspaceRoot string   `json:"-"`
	NativeProxy   string   `json:"-"`
	// Managed native sessions live only under SessionStorageRoot. SessionID is
	// populated solely after an earlier runner acknowledged the exact session.
	ExecutionMode       string `json:"-"`
	SessionMode         string `json:"-"`
	SessionID           string `json:"-"`
	SessionStorageRoot  string `json:"-"`
	SessionContextEpoch int    `json:"-"`
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
