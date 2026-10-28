package httpapi

import (
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// SystemInfo exposes capabilities and verification without server credentials.
type SystemInfo struct {
	Models         []string          `json:"models"`
	Roots          []string          `json:"roots"`
	ModelStatus    string            `json:"model_status"`
	SearchStatus   string            `json:"search_status"`
	SearchProvider string            `json:"search_provider,omitempty"`
	Verified       map[string]string `json:"verified"`
	CSRFToken      string            `json:"csrf_token"`
	Limits         map[string]int    `json:"limits"`
	Executors      []ExecutorInfo    `json:"executors,omitempty"`
	Skills         []skills.Info     `json:"skills,omitempty"`
}

// ExecutorInfo distinguishes installation, adapter/isolation, and model connection.
type ExecutorInfo struct {
	ID           string   `json:"id"`
	Installed    bool     `json:"installed"`
	Version      string   `json:"version"`
	Supported    bool     `json:"supported"`
	Reason       string   `json:"reason"`
	Adapter      string   `json:"adapter"`
	Isolation    string   `json:"isolation"`
	Protocol     string   `json:"protocol"`
	Capabilities []string `json:"capabilities,omitempty"`
	CheckedAt    string   `json:"checked_at,omitempty"`
	// Identity binds a verification to the exact installed program, not just a version.
	Identity string `json:"-"`
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

// MessageContentPage is one bounded slice of a message body.
type MessageContentPage = store.MessageContentPage

// EventPage is one bounded slice of a run's persisted event history.
type EventPage = store.EventPage

// RunView associates the plan, execution attempts and exact event cursor.
type RunView struct {
	Run      w.Run       `json:"run"`
	Steps    []w.Step    `json:"steps"`
	Attempts []w.Attempt `json:"attempts"`
	Cursor   int64       `json:"cursor"`
}

// RoutingPreview describes dispatch intent without starting execution.
type RoutingPreview struct {
	ProposalID string           `json:"proposal_id,omitempty"`
	Routing    workflow.Routing `json:"routing"`
	ExpiresIn  int              `json:"expires_in,omitempty"`
}

// WorkspaceInput contains only user-editable fields.
type WorkspaceInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Root        string   `json:"root"`
	SkillIDs    []string `json:"skill_ids"`
}

// ThreadUpdate edits the supplied fields; omitted settings keep their stored values.
type ThreadUpdate struct {
	Title         string    `json:"title"`
	AgentIDs      *[]string `json:"agent_ids,omitempty"`
	CoordinatorID *string   `json:"coordinator_id,omitempty"`
	Workflow      *string   `json:"workflow,omitempty"`

	// Legacy full-thread fields accept old clients; they are ignored because the
	// path and If-Match remain authoritative for identity and revision.
	ID          string `json:"id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Revision    int    `json:"revision,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// AuthStatus exposes whether the local workspace password has been initialized.
type AuthStatus struct {
	Initialized bool `json:"initialized"`
}

// PasswordInput sets or verifies the local workspace password.
type PasswordInput struct {
	Password string `json:"password"`
}

// SessionReply returns the session CSRF token; the session secret stays HttpOnly.
type SessionReply struct {
	CSRFToken string `json:"csrf_token"`
}

// FeedbackInput edits only human feedback, never immutable report content.
type FeedbackInput struct {
	Feedback string `json:"feedback"`
}

// VerifyReply records explicit provider verification time.
type VerifyReply struct {
	Executor   string         `json:"executor,omitempty"`
	Model      string         `json:"model"`
	VerifiedAt string         `json:"verified_at"`
	Executors  []ExecutorInfo `json:"executors,omitempty"`
}

// DeleteReply confirms which object was permanently removed.
type DeleteReply struct {
	ID string `json:"id"`
}
