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

// ErrBudgetExhausted prevents a new automatic execution segment without
// discarding the durable account or silently resetting its usage.
var ErrBudgetExhausted = errors.New("workspace: automatic continuation budget exhausted")

// ErrRepairLimit leaves repair intent queued until the corresponding policy is expanded.
var ErrRepairLimit = errors.New("workspace: automatic repair policy limit reached")

// ErrExecutionBusy leaves a queued run intact until a shared execution slot is released.
var ErrExecutionBusy = errors.New("workspace: execution capacity occupied")

// Workspace owns members, conversations and an optional allowed source directory.
type Workspace struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	Description     string                `json:"description"`
	Root            string                `json:"root"`
	Revision        int                   `json:"revision"`
	ProjectRevision int                   `json:"project_revision,omitempty"`
	ExecutionMode   string                `json:"execution_mode,omitempty"`
	BudgetOverride  *BudgetPolicyOverride `json:"budget_override,omitempty"`
	CreatedAt       string                `json:"created_at"`
	SkillIDs        []string              `json:"skill_ids"`
}

// AvatarChoices are legacy built-in avatars retained so old profiles can be
// edited and cleared. New avatars are uploaded image data URIs only; avatars
// never enter model prompts.
var AvatarChoices = []string{"🐱", "🦊", "🦉", "🐼", "🐰", "🐻", "🐧", "🐯"}

// Context budget and thinking-level limits are shared by CRUD, run snapshots
// and native invocation. ContextSize is an estimated token budget; empty
// follows the CLI's configured context window.
const MinContextTokens = 1024

// ThinkingLevels returns fixed effort choices; OpenCode discovers its model
// variants at runtime instead of assuming the same choices for every model.
func ThinkingLevels(executor string) []string {
	switch executor {
	case "claude":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "codex":
		return []string{"low", "medium", "high", "xhigh", "ultra", "max"}
	case "gemini":
		// The installed Gemini CLI exposes thinkingLevel LOW/HIGH through its
		// scoped modelConfigs.generateContentConfig, not a CLI flag.
		return []string{"low", "high"}
	case "pi":
		return []string{"low", "medium", "high", "xhigh", "max"}
	default:
		return nil
	}
}

// ValidateExecutionOptions checks options before persistence and again before
// dispatch. An unsupported combination is an explicit error, never ignored.
func ValidateExecutionOptions(executor string, contextSize int, thinking string) error {
	if contextSize != 0 && contextSize < MinContextTokens {
		return fmt.Errorf("context_size must be 0 (CLI default) or at least %d estimated tokens", MinContextTokens)
	}
	if thinking == "" {
		return nil
	}
	if executor == "opencode" {
		if strings.TrimSpace(thinking) != thinking || len(thinking) > 100 || strings.ContainsAny(thinking, "#\r\n\x00") {
			return errors.New("invalid OpenCode thinking variant")
		}
		return nil
	}
	levels := ThinkingLevels(executor)
	if len(levels) == 0 {
		return errors.New("thinking_level is not supported for this executor; leave it empty to keep the CLI default")
	}
	if !slices.Contains(levels, thinking) {
		return fmt.Errorf("thinking_level must be one of %s", strings.Join(levels, ", "))
	}
	return nil
}

// Agent keeps identity independent of its presentation, model and current role.
type Agent struct {
	ID              string   `json:"id"`
	WorkspaceID     string   `json:"workspace_id"`
	DisplayName     string   `json:"display_name"`
	Handle          string   `json:"handle"`
	Aliases         []string `json:"aliases"`
	RoleDescription string   `json:"role_description"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Avatar          string   `json:"avatar,omitempty"`
	Executor        string   `json:"executor,omitempty"`
	Model           string   `json:"model"`
	// ContextSize is an optional host input budget in estimated tokens. Empty
	// (0) follows the CLI's configured context window.
	ContextSize   int    `json:"context_size,omitempty"`
	ThinkingLevel string `json:"thinking_level,omitempty"`
	// Tools is legacy audit data. It no longer limits a new execution; the
	// global execution mode, the task/role constraints and the conversation
	// grants decide what may run. Historical Run snapshots keep their copy.
	Tools      []string         `json:"tools"`
	Disabled   bool             `json:"disabled"`
	Revision   int              `json:"revision"`
	Membership *WorkspaceMember `json:"membership,omitempty"`
}

// WorkspaceMember records workspace membership and enablement. Tools is legacy
// audit data and never an active permission limit.
type WorkspaceMember struct {
	WorkspaceID string   `json:"workspace_id"`
	AgentID     string   `json:"agent_id"`
	Enabled     bool     `json:"enabled"`
	Tools       []string `json:"tools"`
	Revision    int      `json:"revision"`
}

// WorkflowRoles is the exact role vocabulary of each built-in workflow.
// Engineering is one workflow with an engineer and an independent reviewer;
// the engineer uses whatever read, write and test tools the member was granted.
var WorkflowRoles = map[string][]string{
	"discussion":    {"coordinator"},
	"research":      {"researcher", "critic", "coordinator"},
	"code_analysis": {"analyst", "reviewer"},
	"engineering":   {"engineer", "reviewer"},
}

// WorkflowActionRoles is kept for callers that still pass a legacy action.
// Engineering no longer has action-specific role subsets.
func WorkflowActionRoles(workflow, _ string) []string {
	return WorkflowRoles[workflow]
}

// RoleTools are the role capabilities a planned step needs. They project onto
// the runtime standard catalog, never onto a legacy member array: the engineer
// may use the catalog's write/test tools when the task admits them, and only the
// publishing roles require publish_artifact.
func RoleTools(workflow, _ string, role string) []string {
	return StandardRoleTools(role)
}

// RolesConflict reports whether two roles cannot be held by one member in the
// same plan. Independent review must never be performed by the author.
func RolesConflict(workflow, role, other string) bool {
	switch workflow {
	case "research":
		return role == "researcher" && other == "critic" || role == "critic" && other == "researcher"
	case "code_analysis":
		return role == "analyst" && other == "reviewer" || role == "reviewer" && other == "analyst"
	case "engineering":
		return role == "engineer" && other == "reviewer" || role == "reviewer" && other == "engineer"
	}
	return false
}

// Trash retention bounds. Zero means the default of 7 days.
const (
	DefaultRetentionDays = 7
	MinRetentionDays     = 1
	MaxRetentionDays     = 3650
)

// EffectiveRetentionDays returns the configured period or the compatible default.
func EffectiveRetentionDays(days int) int {
	if days == 0 {
		return DefaultRetentionDays
	}
	return days
}

// ValidateRetentionDays rejects ambiguous or unbounded retention periods.
func ValidateRetentionDays(days int) error {
	if days < MinRetentionDays || days > MaxRetentionDays {
		return fmt.Errorf("retention_days must be between %d and %d", MinRetentionDays, MaxRetentionDays)
	}
	return nil
}

// Settings holds device-wide defaults: collaboration, retention and the single
// execution permission policy shared by every workspace and by the Composer.
type Settings struct {
	Revision             int                          `json:"revision"`
	DefaultCoordinatorID string                       `json:"default_coordinator_id"`
	Assignments          map[string]map[string]string `json:"assignments"`
	RetentionDays        int                          `json:"retention_days,omitempty"`
	CollaborationBudget  BudgetPolicy                 `json:"collaboration_budget,omitempty"`
	// ExecutionMode is the only effective permission policy. Workspace bodies
	// keep their legacy mode as inert migration data.
	ExecutionMode string `json:"execution_mode,omitempty"`
	// PermissionEpoch advances only when an execution boundary changes. It
	// invalidates pending requests and conversation grants of earlier epochs.
	PermissionEpoch int `json:"permission_epoch,omitempty"`
}

// DefaultExecutionMode is the new-install and one-time migration default.
const DefaultExecutionMode = ExecutionProjectAutonomous

// NormalizeExecutionMode applies the confirmed default to a record that has no
// stored mode (new database or a database upgraded before the migration marker).
func NormalizeExecutionMode(settings Settings) Settings {
	if !ValidExecutionMode(settings.ExecutionMode) {
		settings.ExecutionMode = DefaultExecutionMode
	}
	if settings.PermissionEpoch < 1 {
		settings.PermissionEpoch = 1
	}
	return settings
}

// Thread is a shared, ordered conversation within one workspace. Status fields
// are a read projection of this thread's latest run, not persisted body fields.
// A soft-deleted thread keeps its identity and full history until purge.
type Thread struct {
	ID              string   `json:"id"`
	WorkspaceID     string   `json:"workspace_id"`
	Title           string   `json:"title"`
	AgentIDs        []string `json:"agent_ids"`
	CoordinatorID   string   `json:"coordinator_id"`
	Workflow        string   `json:"workflow"`
	TaskID          string   `json:"task_id,omitempty"`
	Internal        bool     `json:"internal,omitempty"`
	Revision        int      `json:"revision"`
	UpdatedAt       string   `json:"updated_at"`
	DeletedAt       string   `json:"deleted_at,omitempty"`
	PurgeAt         string   `json:"purge_at,omitempty"`
	LastRunID       string   `json:"last_run_id,omitempty"`
	LastRunStatus   string   `json:"last_run_status,omitempty"`
	ActiveRunID     string   `json:"active_run_id,omitempty"`
	ActiveRunStatus string   `json:"active_run_status,omitempty"`
}

// WorkflowIsReadOnly reports whether a workflow is read-only. Engineering is a
// single writable workflow; only the retired code_analysis workflow remains
// read-only so old plans can still be restored and inspected.
func WorkflowIsReadOnly(in RunInput) bool {
	return in.Workflow == "code_analysis"
}

// RunInput is a user's request, independent of a particular provider protocol.
type RunInput struct {
	Message       string            `json:"message"`
	Workflow      string            `json:"workflow"`
	Targets       []string          `json:"targets"`
	Assignments   map[string]string `json:"assignments"`
	SourceURLs    []string          `json:"source_urls"`
	SourceIDs     []string          `json:"source_ids"`
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

// WorkflowSelection records the collaboration method a free run's coordinator
// selected from its planning step. Run.Input stays immutable; the selected
// workflow and its exact participants are the effective plan input.
type WorkflowSelection struct {
	Workflow    string            `json:"workflow"`
	Targets     []string          `json:"targets"`
	Assignments map[string]string `json:"assignments"`
	Reason      string            `json:"reason,omitempty"`
	StepID      string            `json:"step_id"`
	SelectedAt  string            `json:"selected_at"`
}

// Run snapshots a bounded workflow and owns its single terminal status.
type Run struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	ThreadID    string   `json:"thread_id"`
	Input       RunInput `json:"input"`
	Routing     Routing  `json:"routing"`
	// SelectedWorkflow is written once by the coordinator's planning step.
	// Method records how a free run actually completed: direct, delegation, or
	// the selected workflow. Built-in runs leave Method empty.
	SelectedWorkflow         *WorkflowSelection `json:"selected_workflow,omitempty"`
	Method                   string             `json:"method,omitempty"`
	RetryOfStepID            string             `json:"retry_of_step_id,omitempty"`
	Agents                   []Agent            `json:"agents"`
	CoordinatorID            string             `json:"coordinator_id,omitempty"`
	SkillVersions            map[string]string  `json:"skill_versions,omitempty"`
	SkillSnapshot            string             `json:"skill_snapshot,omitempty"`
	Workspace                Workspace          `json:"workspace"`
	ExecutionPolicy          *ExecutionPolicy   `json:"execution_policy,omitempty"`
	RequestedExecutionMode   string             `json:"requested_execution_mode,omitempty"`
	EffectiveExecutionMode   string             `json:"effective_execution_mode,omitempty"`
	PermissionEpoch          int                `json:"permission_epoch,omitempty"`
	EnforcedCapabilities     []string           `json:"enforced_capabilities,omitempty"`
	UnenforcedConstraints    []string           `json:"unenforced_constraints,omitempty"`
	TaskID                   string             `json:"task_id,omitempty"`
	TaskIntake               bool               `json:"task_intake,omitempty"`
	TaskPromotionStepID      string             `json:"task_promotion_step_id,omitempty"`
	TaskPromotionDigest      string             `json:"task_promotion_digest,omitempty"`
	TaskRevision             int                `json:"task_revision,omitempty"`
	PolicyVersion            int                `json:"policy_version,omitempty"`
	BudgetAccountID          string             `json:"budget_account_id,omitempty"`
	GrantSnapshot            string             `json:"grant_snapshot_digest,omitempty"`
	GrantActions             []string           `json:"grant_actions,omitempty"`
	ContextRef               string             `json:"context_manifest_id,omitempty"`
	ExecutionRef             string             `json:"execution_ref,omitempty"`
	ExecutionRoot            string             `json:"execution_root,omitempty"`
	TaskDirectory            string             `json:"task_directory,omitempty"`
	OutputBaseline           map[string]string  `json:"output_baseline,omitempty"`
	InputDigest              string             `json:"input_snapshot_digest,omitempty"`
	ContinuedFromRun         string             `json:"continued_from_run_id,omitempty"`
	CollaborationMessageID   string             `json:"collaboration_message_id,omitempty"`
	AutomaticContinuation    bool               `json:"automatic_continuation,omitempty"`
	WriteBranch              bool               `json:"write_branch,omitempty"`
	BudgetReserved           bool               `json:"budget_reserved,omitempty"`
	BudgetStarted            bool               `json:"budget_started,omitempty"`
	BudgetIssueID            string             `json:"budget_issue_id,omitempty"`
	ProgressDigest           string             `json:"progress_digest,omitempty"`
	ProgressObserved         bool               `json:"progress_observed,omitempty"`
	ReservationReleaseReason string             `json:"reservation_release_reason,omitempty"`
	Status                   string             `json:"status"`
	FailureKind              string             `json:"failure_kind,omitempty"`
	Error                    string             `json:"error"`
	RecoveryCount            int                `json:"recovery_count,omitempty"`
	RecoveredAt              string             `json:"recovered_at,omitempty"`
	RecoveryNote             string             `json:"recovery_note,omitempty"`
	CreatedAt                string             `json:"created_at"`
	StartedAt                string             `json:"started_at,omitempty"`
	DeadlineAt               string             `json:"deadline_at,omitempty"`
	PolicyAdoptedAt          string             `json:"policy_adopted_at,omitempty"`
	FinishedAt               string             `json:"finished_at"`
	Delivery                 *Delivery          `json:"delivery,omitempty"`
}

// ProjectRoot is the candidate execution tree when one was prepared; source
// authorization continues to use Workspace.Root, the canonical user binding.
func (r Run) ProjectRoot() string {
	if r.ExecutionRoot != "" {
		return r.ExecutionRoot
	}
	if r.Workspace.Root != "" {
		return r.Workspace.Root
	}
	// A file task without a bound project uses its task directory as the root. An
	// empty root must never stand in for the user's HOME or the process cwd.
	return r.TaskDirectory
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
	RetryOfStepID  string   `json:"retry_of_step_id,omitempty"`
	RequestID      string   `json:"request_id,omitempty"`
	ContextRefs    []string `json:"context_refs,omitempty"`
	ContinuationID string   `json:"continuation_id,omitempty"`
	Converged      bool     `json:"converged,omitempty"`
	ReviewVerdict  string   `json:"review_verdict,omitempty"`
	Depth          int      `json:"depth,omitempty"`
	AttemptCount   int      `json:"attempt_count,omitempty"`
	StartedAt      string   `json:"started_at,omitempty"`
	DeadlineAt     string   `json:"deadline_at,omitempty"`
}

// Attempt records one execution try for a logical step; retries never rewrite
// the previous attempt's evidence.
type Attempt struct {
	ID                  string           `json:"id"`
	RunID               string           `json:"run_id"`
	StepID              string           `json:"step_id"`
	Number              int              `json:"number"`
	Status              string           `json:"status"`
	Reason              string           `json:"reason,omitempty"`
	Executor            string           `json:"executor,omitempty"`
	Model               string           `json:"model,omitempty"`
	ResolvedModel       string           `json:"resolved_model,omitempty"`
	ConnectionID        string           `json:"connection_identity,omitempty"`
	Adapter             string           `json:"adapter,omitempty"`
	CLIVersion          string           `json:"cli_version,omitempty"`
	CLIProgram          string           `json:"cli_program,omitempty"`
	CLIIdentity         string           `json:"cli_identity,omitempty"`
	SessionID           string           `json:"session_id,omitempty"`
	SessionMode         string           `json:"session_mode,omitempty"`
	ExecutionSessionRef string           `json:"execution_session_ref,omitempty"`
	SessionFingerprint  string           `json:"session_fingerprint,omitempty"`
	Recovery            string           `json:"recovery,omitempty"`
	RecoveredFrom       string           `json:"recovered_from,omitempty"`
	FailureKind         string           `json:"failure_kind,omitempty"`
	InputPlanDigest     string           `json:"input_plan_digest,omitempty"`
	CheckpointStart     string           `json:"checkpoint_start_digest,omitempty"`
	CheckpointDigest    string           `json:"checkpoint_digest,omitempty"`
	DraftArtifact       *Artifact        `json:"draft_artifact,omitempty"`
	SourceIDs           []string         `json:"source_ids,omitempty"`
	UsageKnown          bool             `json:"usage_known,omitempty"`
	UsageCompleteness   string           `json:"usage_completeness,omitempty"`
	InputTokens         int64            `json:"input_tokens,omitempty"`
	OutputTokens        int64            `json:"output_tokens,omitempty"`
	LivenessWarning     *LivenessWarning `json:"liveness_warning,omitempty"`
	Process             *ProcessRef      `json:"process,omitempty"`
	StartedAt           string           `json:"started_at"`
	LastProgress        string           `json:"last_progress_at,omitempty"`
	FinishedAt          string           `json:"finished_at,omitempty"`
	Error               string           `json:"error,omitempty"`
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
	// SHA256 is the exact file hash for file/diff sources. For host test
	// evidence ("test") it is the delivered-tree digest exposed only when the
	// bounded digest taken immediately before and after the test both completed
	// and matched. The engineering gate also compares it with the current tree,
	// so a tree the test rewrote has no qualifying digest and cannot verify
	// delivery.
	SHA256     string `json:"sha256"`
	Content    string `json:"content"`
	CapturedAt string `json:"captured_at"`
	Truncated  bool   `json:"truncated"`
	// ExitCode and TimedOut are set on host test evidence ("test"). A timed-out
	// or killed test uses -1 and never counts as a passing verification.
	ExitCode int  `json:"exit_code,omitempty"`
	TimedOut bool `json:"timed_out,omitempty"`

	// CaptureStepID attributes a capture to the logical step that requested
	// it, so one step's checkpoint ignores unrelated sibling captures.
	CaptureStepID string `json:"capture_step_id,omitempty"`
	// ContentDigest is the SHA-256 of the exact captured content, kept in
	// metadata projections that deliberately omit the body.
	ContentDigest   string `json:"content_digest,omitempty"`
	OriginalContent string `json:"original_content,omitempty"`
	OriginalDigest  string `json:"original_digest,omitempty"`
	MediaType       string `json:"media_type,omitempty"`
	FileSize        int    `json:"file_size,omitempty"`
	FileModifiedAt  string `json:"file_modified_at,omitempty"`
	ParseStatus     string `json:"parse_status,omitempty"`
	ParseError      string `json:"parse_error,omitempty"`
	ParseQuality    string `json:"parse_quality,omitempty"`
	ParserVersion   string `json:"parser_version,omitempty"`
	SourceLocator   string `json:"source_locator,omitempty"`
	Visibility      string `json:"visibility,omitempty"`
	ExposureDigest  string `json:"exposure_scope_digest,omitempty"`
}

// SourceImport is an uploaded original and its bounded parse result. Imports
// exist before a Run; successful publication freezes the same bytes as Source.
type SourceImport struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ThreadID        string `json:"thread_id"`
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	FileName        string `json:"file_name"`
	MediaType       string `json:"media_type"`
	Content         string `json:"content,omitempty"`
	ContentDigest   string `json:"content_digest,omitempty"`
	OriginalContent string `json:"original_content,omitempty"`
	OriginalDigest  string `json:"original_digest"`
	SHA256          string `json:"sha256"`
	CapturedAt      string `json:"captured_at"`
	Truncated       bool   `json:"truncated"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	ParseStatus     string `json:"parse_status"`
	ParseError      string `json:"parse_error,omitempty"`
	ParseQuality    string `json:"parse_quality,omitempty"`
	ParserVersion   string `json:"parser_version,omitempty"`
	SourceLocator   string `json:"source_locator,omitempty"`
	Visibility      string `json:"visibility"`
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
	ID             string         `json:"id"`
	Version        int            `json:"version"`
	Revision       int            `json:"revision"`
	ThreadID       string         `json:"thread_id"`
	RunID          string         `json:"run_id"`
	StepID         string         `json:"step_id"`
	Type           string         `json:"type"`
	Title          string         `json:"title"`
	Body           string         `json:"body"`
	Files          []ArtifactFile `json:"files,omitempty"`
	Findings       []Finding      `json:"findings"`
	SourceIDs      []string       `json:"source_ids"`
	Limitations    []string       `json:"limitations"`
	Feedback       string         `json:"feedback"`
	FeedbackAt     string         `json:"feedback_at"`
	Visibility     string         `json:"visibility,omitempty"`
	ExposureDigest string         `json:"exposure_scope_digest,omitempty"`
	CreatedAt      string         `json:"created_at"`
}

// ArtifactFile identifies immutable bytes captured before the deliverable commits.
type ArtifactFile struct {
	SourceID  string `json:"source_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int    `json:"size"`
	SHA256    string `json:"sha256"`
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
	return slices.Contains([]string{"succeeded", "failed", "canceled", "interrupted", "yielded"}, s)
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
	if a.Executor != "" && !slices.Contains([]string{"claude", "codex", "gemini", "pi", "opencode"}, a.Executor) {
		return errors.New("executor is not supported")
	}
	if a.Executor == "claude" || a.Executor == "codex" || a.Executor == "gemini" || a.Executor == "pi" || a.Executor == "opencode" {
		if len(a.Model) > 150 || strings.TrimSpace(a.Model) == "" || strings.HasPrefix(a.Model, "-") || strings.ContainsAny(a.Model, "\r\n\x00") {
			return errors.New("CLI model must be default or a model identifier")
		}
	} else if !slices.Contains(models, a.Model) {
		return errors.New("model is not configured")
	}
	if len(a.Aliases) > 12 || len(a.RoleDescription) > 8000 || len(a.Capabilities) > 20 {
		return errors.New("agent configuration exceeds limits")
	}
	if err := ValidateAvatar(a.Avatar); err != nil {
		return err
	}
	if err := ValidateExecutionOptions(a.Executor, a.ContextSize, a.ThinkingLevel); err != nil {
		return err
	}
	if a.Executor == "gemini" && a.ThinkingLevel != "" && (a.Model == "" || a.Model == "default") {
		return errors.New("Gemini thinking_level requires an explicit model so the scoped override can match it")
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
	// Member tool arrays are retired history. A new profile carries no tool
	// configuration; the runtime catalog is StandardToolCatalog(). Legacy values,
	// including unknown historical names, are preserved but never interpreted.
	return nil
}

// NormalizeSettings maps legacy engineering analyst/developer defaults to the
// single engineer role. It preserves an explicit engineer value and removes
// roles that no current workflow uses.
func NormalizeSettings(s Settings) Settings {
	roles := s.Assignments["engineering"]
	if roles == nil {
		return s
	}
	if roles["engineer"] == "" {
		if roles["developer"] != "" {
			roles["engineer"] = roles["developer"]
		} else if roles["analyst"] != "" {
			roles["engineer"] = roles["analyst"]
		}
	}
	delete(roles, "developer")
	delete(roles, "analyst")
	if len(roles) == 0 {
		delete(s.Assignments, "engineering")
	}
	return s
}

// ValidateSettings rejects unknown workflows and roles, unavailable global
// members, and exclusive roles assigned twice. Empty values are explicit resets.
func ValidateSettings(s Settings, agents []Agent) error {
	byID := map[string]Agent{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	available := func(id string) error {
		if id == "" {
			return nil
		}
		a, ok := byID[id]
		if !ok || a.Disabled {
			return errors.New("settings reference an unavailable member")
		}
		return nil
	}
	if err := available(s.DefaultCoordinatorID); err != nil {
		return err
	}
	for workflow, roles := range s.Assignments {
		allowed, ok := WorkflowRoles[workflow]
		if !ok {
			return fmt.Errorf("unsupported workflow in settings: %s", workflow)
		}
		for role, id := range roles {
			if !slices.Contains(allowed, role) {
				return fmt.Errorf("unsupported role for %s: %s", workflow, role)
			}
			if err := available(id); err != nil {
				return err
			}
		}
		if roles["researcher"] != "" && roles["researcher"] == roles["critic"] {
			return errors.New("researcher and critic must be different members")
		}
		if roles["analyst"] != "" && roles["analyst"] == roles["reviewer"] {
			return errors.New("analyst and reviewer must be different members")
		}
		if roles["engineer"] != "" && roles["engineer"] == roles["reviewer"] {
			return errors.New("engineer and reviewer must be different members")
		}
	}
	return nil
}
