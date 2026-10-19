package httpapi

import (
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// SystemInfo exposes capabilities and verification without server credentials.
type SystemInfo struct {
	Demo         bool              `json:"demo"`
	Models       []string          `json:"models"`
	Roots        []string          `json:"roots"`
	ModelStatus  string            `json:"model_status"`
	SearchStatus string            `json:"search_status"`
	Verified     map[string]string `json:"verified"`
	CSRFToken    string            `json:"csrf_token"`
	Limits       map[string]int    `json:"limits"`
	Executors    []ExecutorInfo    `json:"executors,omitempty"`
}

// ExecutorInfo distinguishes a CLI installation from a verified invocation.
type ExecutorInfo struct {
	ID        string `json:"id"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
}

// VerifyInput names the precise CLI and model to verify using a public prompt.
type VerifyInput struct {
	Executor string `json:"executor,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Page carries a stable list contract and the next opaque cursor.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// ThreadView is a consistent conversation snapshot, with separately paged messages.
type ThreadView struct {
	Thread   w.Thread        `json:"thread"`
	Messages Page[w.Message] `json:"messages"`
	Runs     []w.Run         `json:"runs"`
	Sources  []w.Source      `json:"sources"`
}

// RunView associates the plan with its exact event cursor.
type RunView struct {
	Run    w.Run    `json:"run"`
	Steps  []w.Step `json:"steps"`
	Cursor int64    `json:"cursor"`
}

// RoutingPreview describes dispatch intent without starting execution.
type RoutingPreview struct {
	ProposalID string           `json:"proposal_id,omitempty"`
	Routing    workflow.Routing `json:"routing"`
	ExpiresIn  int              `json:"expires_in,omitempty"`
}

// WorkspaceInput contains only user-editable fields.
type WorkspaceInput struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Root           string `json:"root"`
	ConsentVersion int    `json:"consent_version"`
}

// BootstrapInput exchanges a single-use startup token for a local session.
type BootstrapInput struct {
	Token string `json:"token"`
}

// BootstrapReply returns the session CSRF token; the session secret stays HttpOnly.
type BootstrapReply struct {
	CSRFToken string `json:"csrf_token"`
}

// FeedbackInput edits only human feedback, never immutable report content.
type FeedbackInput struct {
	Feedback string `json:"feedback"`
}

// VerifyReply records explicit provider verification time.
type VerifyReply struct {
	Executor   string `json:"executor,omitempty"`
	Model      string `json:"model"`
	VerifiedAt string `json:"verified_at"`
}
