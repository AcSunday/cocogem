// Package model adapts provider protocols to a small tool-capable model interface.
package model

import (
	"context"
	"encoding/json"
)

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
	Model     string    `json:"model"`
	System    string    `json:"system"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream"`
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
