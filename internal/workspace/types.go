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
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Root        string   `json:"root"`
	Revision    int      `json:"revision"`
	CreatedAt   string   `json:"created_at"`
	SkillIDs    []string `json:"skill_ids"`
}

// Agent keeps identity independent of its presentation, model and current role.
type Agent struct {
	ID              string           `json:"id"`
	WorkspaceID     string           `json:"workspace_id"`
	DisplayName     string           `json:"display_name"`
	Handle          string           `json:"handle"`
	Aliases         []string         `json:"aliases"`
	RoleDescription string           `json:"role_description"`
	Capabilities    []string         `json:"capabilities,omitempty"`
	Executor        string           `json:"executor,omitempty"`
	Model           string           `json:"model"`
	Tools           []string         `json:"tools"`
	Disabled        bool             `json:"disabled"`
	Revision        int              `json:"revision"`
	Membership      *WorkspaceMember `json:"membership,omitempty"`
}

// WorkspaceMember grants a global member a bounded set of workspace tools.
type WorkspaceMember struct {
	WorkspaceID string   `json:"workspace_id"`
	AgentID     string   `json:"agent_id"`
	Enabled     bool     `json:"enabled"`
	Tools       []string `json:"tools"`
	Revision    int      `json:"revision"`
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

// ExecutionPolicy is the parsed policy frozen when a run is accepted. A nil
// Run.ExecutionPolicy means a legacy run that has no policy yet; once set, a
// zero duration is an explicit "disabled", not an invitation to apply defaults.
type ExecutionPolicy struct {
	RunTimeout    time.Duration `json:"run_timeout_ns"`
	StepTimeout   time.Duration `json:"step_timeout_ns"`
	FirstResponse time.Duration `json:"first_response_ns"`
	Stall         time.Duration `json:"stall_ns"`
	StallAction   string        `json:"stall_action"`
}

// Run snapshots a bounded workflow and owns its single terminal status.
type Run struct {
	ID              string            `json:"id"`
	WorkspaceID     string            `json:"workspace_id"`
	ThreadID        string            `json:"thread_id"`
	Input           RunInput          `json:"input"`
	Routing         Routing           `json:"routing"`
	Agents          []Agent           `json:"agents"`
	CoordinatorID   string            `json:"coordinator_id,omitempty"`
	SkillVersions   map[string]string `json:"skill_versions,omitempty"`
	SkillSnapshot   string            `json:"skill_snapshot,omitempty"`
	Workspace       Workspace         `json:"workspace"`
	ExecutionPolicy *ExecutionPolicy  `json:"execution_policy,omitempty"`
	Status          string            `json:"status"`
	FailureKind     string            `json:"failure_kind,omitempty"`
	Error           string            `json:"error"`
	RecoveryCount   int               `json:"recovery_count,omitempty"`
	RecoveredAt     string            `json:"recovered_at,omitempty"`
	RecoveryNote    string            `json:"recovery_note,omitempty"`
	CreatedAt       string            `json:"created_at"`
	StartedAt       string            `json:"started_at,omitempty"`
	DeadlineAt      string            `json:"deadline_at,omitempty"`
	PolicyAdoptedAt string            `json:"policy_adopted_at,omitempty"`
	FinishedAt      string            `json:"finished_at"`
}

// Step describes one member's work and the exact upstream results it can consume.
type Step struct {
	ID             string   `json:"id"`
	RunID          string   `json:"run_id"`
	AgentID        string   `json:"agent_id"`
	AgentName      string   `json:"agent_name"`
	Role           string   `json:"role"`
	Instruction    string   `json:"instruction"`
	DependsOn      []string `json:"depends_on"`
	Status         string   `json:"status"`
	FailureKind    string   `json:"failure_kind,omitempty"`
	Error          string   `json:"error"`
	Kind           string   `json:"kind,omitempty"`
	FromStepID     string   `json:"from_step_id,omitempty"`
	FromAgentID    string   `json:"from_agent_id,omitempty"`
	FromAttemptID  string   `json:"from_attempt_id,omitempty"`
	RequestID      string   `json:"request_id,omitempty"`
	ContextRefs    []string `json:"context_refs,omitempty"`
	ContinuationID string   `json:"continuation_id,omitempty"`
	Depth          int      `json:"depth,omitempty"`
	AttemptCount   int      `json:"attempt_count,omitempty"`
	StartedAt      string   `json:"started_at,omitempty"`
	DeadlineAt     string   `json:"deadline_at,omitempty"`
}

// Attempt records one execution try for a logical step; retries never rewrite
// the previous attempt's evidence.
type Attempt struct {
	ID                string           `json:"id"`
	RunID             string           `json:"run_id"`
	StepID            string           `json:"step_id"`
	Number            int              `json:"number"`
	Status            string           `json:"status"`
	Reason            string           `json:"reason,omitempty"`
	Executor          string           `json:"executor,omitempty"`
	Model             string           `json:"model,omitempty"`
	ResolvedModel     string           `json:"resolved_model,omitempty"`
	ConnectionID      string           `json:"connection_identity,omitempty"`
	Adapter           string           `json:"adapter,omitempty"`
	CLIVersion        string           `json:"cli_version,omitempty"`
	CLIProgram        string           `json:"cli_program,omitempty"`
	CLIIdentity       string           `json:"cli_identity,omitempty"`
	SessionID         string           `json:"session_id,omitempty"`
	SessionMode       string           `json:"session_mode,omitempty"`
	Recovery          string           `json:"recovery,omitempty"`
	RecoveredFrom     string           `json:"recovered_from,omitempty"`
	FailureKind       string           `json:"failure_kind,omitempty"`
	InputPlanDigest   string           `json:"input_plan_digest,omitempty"`
	CheckpointStart   string           `json:"checkpoint_start_digest,omitempty"`
	CheckpointDigest  string           `json:"checkpoint_digest,omitempty"`
	DraftArtifact     *Artifact        `json:"draft_artifact,omitempty"`
	SourceIDs         []string         `json:"source_ids,omitempty"`
	UsageKnown        bool             `json:"usage_known,omitempty"`
	UsageCompleteness string           `json:"usage_completeness,omitempty"`
	InputTokens       int64            `json:"input_tokens,omitempty"`
	OutputTokens      int64            `json:"output_tokens,omitempty"`
	LivenessWarning   *LivenessWarning `json:"liveness_warning,omitempty"`
	Process           *ProcessRef      `json:"process,omitempty"`
	StartedAt         string           `json:"started_at"`
	LastProgress      string           `json:"last_progress_at,omitempty"`
	FinishedAt        string           `json:"finished_at,omitempty"`
	Error             string           `json:"error,omitempty"`
}

// LivenessWarning is the last observed silence warning for one attempt. It is
// a bounded snapshot for UIs seeding recent events, not a replayed history.
type LivenessWarning struct {
	State          string `json:"state"`
	SilenceSeconds int    `json:"silence_seconds"`
	LastEventType  string `json:"last_event_type"`
	ProcessAlive   bool   `json:"process_alive"`
	ToolActive     bool   `json:"tool_active"`
	ObservedAt     string `json:"observed_at"`
}

// ProcessRef is the persisted ownership proof for a native child process.
type ProcessRef struct {
	PID        int    `json:"pid"`
	PGID       int    `json:"pgid,omitempty"`
	Start      string `json:"start,omitempty"`
	Executable string `json:"executable,omitempty"`
}

// RunClaim serializes recovery and dispatch between live server instances.
type RunClaim struct {
	RunID      string `json:"run_id"`
	OwnerID    string `json:"owner_id"`
	OwnerPID   int    `json:"owner_pid"`
	OwnerStart string `json:"owner_start"`
	ClaimedAt  string `json:"claimed_at"`
}

// Content formats: empty and "inline" are the legacy complete body; new
// streamed messages store an immutable chunk series in message_chunks.
const (
	ContentFormatInline   = "inline"
	ContentFormatChunksV1 = "chunks_v1"

	MaxMessageChunkBytes   = 8 << 10
	MaxMessagePreviewBytes = 64 << 10
)

// Message is one stable timeline item, updated while streaming. Model fields
// snapshot the producing attempt so later configuration edits cannot relabel it.
type Message struct {
	ID               string `json:"id"`
	ThreadID         string `json:"thread_id"`
	RunID            string `json:"run_id"`
	StepID           string `json:"step_id"`
	AttemptID        string `json:"attempt_id,omitempty"`
	AgentID          string `json:"agent_id"`
	AgentName        string `json:"agent_name"`
	Model            string `json:"model,omitempty"`
	ResolvedModel    string `json:"resolved_model,omitempty"`
	Role             string `json:"role"`
	Content          string `json:"content"`
	ContentFormat    string `json:"content_format,omitempty"`
	ContentBytes     int    `json:"content_bytes,omitempty"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
	Status           string `json:"status"`
	CreatedAt        string `json:"created_at"`
}

// Format returns the stored format, with the legacy default made explicit.
func (m Message) Format() string {
	if m.ContentFormat == "" {
		return ContentFormatInline
	}
	return m.ContentFormat
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

	// CaptureStepID attributes a capture to the logical step that requested
	// it, so one step's checkpoint ignores unrelated sibling captures.
	CaptureStepID string `json:"capture_step_id,omitempty"`
	// ContentDigest is the SHA-256 of the exact captured content, kept in
	// metadata projections that deliberately omit the body.
	ContentDigest string `json:"content_digest,omitempty"`
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
		if len(a.Model) > 150 || strings.TrimSpace(a.Model) == "" || strings.HasPrefix(a.Model, "-") || strings.ContainsAny(a.Model, "\r\n\x00") {
			return errors.New("CLI model must be default or a model identifier")
		}
	} else if !slices.Contains(models, a.Model) {
		return errors.New("model is not configured")
	}
	if len(a.Aliases) > 12 || len(a.RoleDescription) > 8000 || len(a.Capabilities) > 20 {
		return errors.New("agent configuration exceeds limits")
	}
	for _, capability := range a.Capabilities {
		if strings.TrimSpace(capability) != capability || capability == "" || len([]rune(capability)) > 100 {
			return errors.New("invalid capability")
		}
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
