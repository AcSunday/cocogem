// Package workspace defines the persisted collaboration objects and their invariants.
package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ErrConflict indicates a stale revision or incompatible active operation.
var ErrConflict = errors.New("workspace: conflict")

// Workspace owns members, conversations and an optional allowed source directory.
type Workspace struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Root           string `json:"root"`
	ConsentVersion int    `json:"consent_version"`
	Revision       int    `json:"revision"`
	CreatedAt      string `json:"created_at"`
}

// Agent keeps identity independent of its presentation, model and current role.
type Agent struct {
	ID              string   `json:"id"`
	WorkspaceID     string   `json:"workspace_id"`
	DisplayName     string   `json:"display_name"`
	Handle          string   `json:"handle"`
	Aliases         []string `json:"aliases"`
	RoleDescription string   `json:"role_description"`
	Executor        string   `json:"executor,omitempty"`
	Model           string   `json:"model"`
	Tools           []string `json:"tools"`
	Disabled        bool     `json:"disabled"`
	Revision        int      `json:"revision"`
}

// Thread is a shared, ordered conversation within one workspace.
type Thread struct {
	ID            string   `json:"id"`
	WorkspaceID   string   `json:"workspace_id"`
	Title         string   `json:"title"`
	AgentIDs      []string `json:"agent_ids"`
	CoordinatorID string   `json:"coordinator_id"`
	Workflow      string   `json:"workflow"`
	Revision      int      `json:"revision"`
	UpdatedAt     string   `json:"updated_at"`
}

// RunInput is a user's request, independent of a particular provider protocol.
type RunInput struct {
	Message       string            `json:"message"`
	Workflow      string            `json:"workflow"`
	Targets       []string          `json:"targets"`
	Assignments   map[string]string `json:"assignments"`
	SourceURLs    []string          `json:"source_urls"`
	ArtifactRefs  []ArtifactRef     `json:"artifact_refs"`
	ResearchScope string            `json:"research_scope"`
	ProposalID    string            `json:"proposal_id"`
	RetryOfRunID  string            `json:"retry_of_run_id"`
}

// ArtifactRef identifies an immutable version, not whichever version is newest.
type ArtifactRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// RoutingEvidence binds a semantic assignment to a quote in the current user input.
type RoutingEvidence struct {
	AgentID string `json:"agent_id"`
	Quote   string `json:"quote"`
}

// Routing records the accepted recipient decision independently of provider wire data.
type Routing struct {
	Decision      string            `json:"decision"`
	Targets       []string          `json:"targets"`
	Workflow      string            `json:"workflow"`
	Assignments   map[string]string `json:"assignments"`
	Explanation   string            `json:"explanation"`
	Clarification string            `json:"clarification"`
	Evidence      []RoutingEvidence `json:"evidence"`
	ModelCall     bool              `json:"model_call"`
	UsageKnown    bool              `json:"usage_known"`
	InputTokens   int               `json:"input_tokens"`
	OutputTokens  int               `json:"output_tokens"`
}

// Run snapshots a bounded workflow and owns its single terminal status.
type Run struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	ThreadID    string    `json:"thread_id"`
	Input       RunInput  `json:"input"`
	Routing     Routing   `json:"routing"`
	Agents      []Agent   `json:"agents"`
	Workspace   Workspace `json:"workspace"`
	Status      string    `json:"status"`
	Error       string    `json:"error"`
	CreatedAt   string    `json:"created_at"`
	FinishedAt  string    `json:"finished_at"`
	Demo        bool      `json:"demo"`
}

// Step describes one member's work and the exact upstream results it can consume.
type Step struct {
	ID          string   `json:"id"`
	RunID       string   `json:"run_id"`
	AgentID     string   `json:"agent_id"`
	AgentName   string   `json:"agent_name"`
	Role        string   `json:"role"`
	Instruction string   `json:"instruction"`
	DependsOn   []string `json:"depends_on"`
	Status      string   `json:"status"`
	Error       string   `json:"error"`
}

// Message is one stable timeline item, updated while streaming.
type Message struct {
	ID        string `json:"id"`
	ThreadID  string `json:"thread_id"`
	RunID     string `json:"run_id"`
	StepID    string `json:"step_id"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// Source captures what was actually read; refreshing creates a new source.
type Source struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ThreadID    string `json:"thread_id"`
	RunID       string `json:"run_id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	SHA256      string `json:"sha256"`
	Content     string `json:"content"`
	CapturedAt  string `json:"captured_at"`
	Truncated   bool   `json:"truncated"`
}

// Finding preserves evidence and disagreement in typed reports.
type Finding struct {
	Title     string   `json:"title"`
	Detail    string   `json:"detail"`
	Status    string   `json:"status"`
	Severity  string   `json:"severity,omitempty"`
	SourceIDs []string `json:"source_ids"`
}

// Artifact is a versioned deliverable; feedback is separate from its content.
type Artifact struct {
	ID          string    `json:"id"`
	Version     int       `json:"version"`
	Revision    int       `json:"revision"`
	ThreadID    string    `json:"thread_id"`
	RunID       string    `json:"run_id"`
	StepID      string    `json:"step_id"`
	Type        string    `json:"type"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Findings    []Finding `json:"findings"`
	SourceIDs   []string  `json:"source_ids"`
	Limitations []string  `json:"limitations"`
	Feedback    string    `json:"feedback"`
	FeedbackAt  string    `json:"feedback_at"`
	CreatedAt   string    `json:"created_at"`
	Demo        bool      `json:"demo"`
}

// Event is the provider-neutral, replayable envelope consumed by the UI.
type Event struct {
	SchemaVersion int             `json:"schema_version"`
	RunID         string          `json:"run_id"`
	Seq           int64           `json:"seq"`
	Type          string          `json:"type"`
	OccurredAt    string          `json:"occurred_at"`
	StepID        string          `json:"step_id,omitempty"`
	AgentID       string          `json:"agent_id,omitempty"`
	Data          json.RawMessage `json:"data"`
}

// NewID returns a cryptographically random UUID v4.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("uuid entropy: %w", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Now returns an unambiguous persisted timestamp.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Terminal is the authoritative run-status classifier.
func Terminal(s string) bool {
	return slices.Contains([]string{"succeeded", "failed", "canceled", "interrupted"}, s)
}

var handlePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// ValidateAgent rejects ambiguous explicit names and unavailable model/tool choices.
func ValidateAgent(a Agent, peers []Agent, models, tools []string) error {
	if strings.TrimSpace(a.DisplayName) == "" || len([]rune(a.DisplayName)) > 60 {
		return errors.New("display_name must contain 1–60 characters")
	}
	if !handlePattern.MatchString(a.Handle) {
		return errors.New("handle must match [a-z][a-z0-9-]{1,31}")
	}
	if a.Executor != "" && !slices.Contains([]string{"claude", "codex", "gemini"}, a.Executor) {
		return errors.New("executor is not supported")
	}
	if a.Executor == "claude" || a.Executor == "codex" || a.Executor == "gemini" {
		if a.Executor == "codex" && a.Model == "default" {
			return errors.New("Codex 网页执行需要明确填写模型 ID，不能沿用用户配置默认值")
		}
		if len(a.Model) > 150 || strings.TrimSpace(a.Model) == "" || strings.HasPrefix(a.Model, "-") || strings.ContainsAny(a.Model, "\r\n\x00") {
			return errors.New("CLI model must be default or a model identifier")
		}
	} else if !slices.Contains(models, a.Model) {
		return errors.New("model is not configured")
	}
	if len(a.Aliases) > 12 || len(a.RoleDescription) > 8000 {
		return errors.New("agent configuration exceeds limits")
	}
	names := map[string]bool{a.Handle: true}
	for _, name := range a.Aliases {
		if strings.TrimSpace(name) != name || name == "" || len([]rune(name)) > 60 {
			return errors.New("invalid alias")
		}
		key := strings.ToLower(name)
		if names[key] {
			return errors.New("duplicate alias")
		}
		names[key] = true
	}
	for _, peer := range peers {
		if peer.ID == a.ID {
			continue
		}
		for _, name := range append([]string{peer.Handle}, peer.Aliases...) {
			if names[strings.ToLower(name)] {
				return errors.New("handle or alias is already assigned")
			}
		}
	}
	for _, name := range a.Tools {
		if !slices.Contains(tools, name) {
			return fmt.Errorf("unsupported tool: %s", name)
		}
	}
	return nil
}
