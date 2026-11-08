package workspace

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Project execution modes are stable API values. Full access is an explicit
// trust decision, not an isolation guarantee.
const (
	ExecutionControlled        = "controlled"
	ExecutionProjectAutonomous = "project_autonomous"
	ExecutionFullAccess        = "full_access"
)

// ValidExecutionMode rejects implicit privilege upgrades and unknown modes.
func ValidExecutionMode(mode string) bool {
	return slices.Contains([]string{ExecutionControlled, ExecutionProjectAutonomous, ExecutionFullAccess}, mode)
}

// BudgetPolicy is resolved when a root task is created. Later setting changes
// never rewrite this snapshot or any running Run's historical policy.
type BudgetPolicy struct {
	AutoContinuations  int `json:"auto_continuations"`
	ParallelBranches   int `json:"parallel_branches"`
	WriterBranches     int `json:"writer_branches"`
	FixRounds          int `json:"fix_rounds"`
	NoProgressFailures int `json:"no_progress_failures"`
}

// DefaultBudgetPolicy returns the confirmed product defaults.
func DefaultBudgetPolicy() BudgetPolicy {
	return BudgetPolicy{AutoContinuations: 20, ParallelBranches: 4, WriterBranches: 2, FixRounds: 3, NoProgressFailures: 2}
}

// ValidateBudgetPolicy applies technical bounds without silently truncating a
// user's requested values. The limits protect local SQLite and scheduler state.
func ValidateBudgetPolicy(p BudgetPolicy) error {
	values := []struct {
		name  string
		value int
		max   int
	}{
		{"auto_continuations", p.AutoContinuations, 10000},
		{"parallel_branches", p.ParallelBranches, 64},
		{"writer_branches", p.WriterBranches, 64},
		{"fix_rounds", p.FixRounds, 1000},
		{"no_progress_failures", p.NoProgressFailures, 1000},
	}
	for _, item := range values {
		if item.value < 1 || item.value > item.max {
			return fmt.Errorf("%s must be an integer from 1 to %d", item.name, item.max)
		}
	}
	if p.WriterBranches > p.ParallelBranches {
		return errors.New("writer_branches must not exceed parallel_branches")
	}
	return nil
}

// BudgetPolicyOverride distinguishes inheritance from an explicit number.
type BudgetPolicyOverride struct {
	AutoContinuations  *int `json:"auto_continuations,omitempty"`
	ParallelBranches   *int `json:"parallel_branches,omitempty"`
	WriterBranches     *int `json:"writer_branches,omitempty"`
	FixRounds          *int `json:"fix_rounds,omitempty"`
	NoProgressFailures *int `json:"no_progress_failures,omitempty"`
}

// ResolveBudget applies each present override to the supplied defaults.
func ResolveBudget(defaults BudgetPolicy, override *BudgetPolicyOverride) (BudgetPolicy, error) {
	resolved := defaults
	if override != nil {
		if override.AutoContinuations != nil {
			resolved.AutoContinuations = *override.AutoContinuations
		}
		if override.ParallelBranches != nil {
			resolved.ParallelBranches = *override.ParallelBranches
		}
		if override.WriterBranches != nil {
			resolved.WriterBranches = *override.WriterBranches
		}
		if override.FixRounds != nil {
			resolved.FixRounds = *override.FixRounds
		}
		if override.NoProgressFailures != nil {
			resolved.NoProgressFailures = *override.NoProgressFailures
		}
	}
	return resolved, ValidateBudgetPolicy(resolved)
}

// BudgetSources records where each resolved value came from.
type BudgetSources struct {
	AutoContinuations  string `json:"auto_continuations"`
	ParallelBranches   string `json:"parallel_branches"`
	WriterBranches     string `json:"writer_branches"`
	FixRounds          string `json:"fix_rounds"`
	NoProgressFailures string `json:"no_progress_failures"`
}

// BudgetAccount is the single cumulative ledger shared by a root task and all
// owned descendants, revisions, consultations, retries and resumed Runs.
type BudgetAccount struct {
	ID                 string         `json:"id"`
	RootTaskID         string         `json:"root_task_id"`
	Policy             BudgetPolicy   `json:"policy"`
	Sources            BudgetSources  `json:"sources"`
	Revision           int            `json:"revision"`
	UsedContinuations  int            `json:"used_continuations"`
	ReservedRuns       int            `json:"reserved_runs"`
	ActiveBranches     int            `json:"active_branches"`
	ActiveWriters      int            `json:"active_writers"`
	FixRoundsByIssue   map[string]int `json:"fix_rounds_by_issue"`
	NoProgressByBranch map[string]int `json:"no_progress_by_branch"`
	UpdatedAt          string         `json:"updated_at"`
}

// RemainingContinuations excludes both started work and durable reservations.
func (a BudgetAccount) RemainingContinuations() int {
	return a.Policy.AutoContinuations - a.UsedContinuations - a.ReservedRuns
}

// AcceptanceCriterion identifies a user outcome, not a number of finished steps.
type AcceptanceCriterion struct {
	CriterionID          string   `json:"criterion_id"`
	Statement            string   `json:"statement"`
	Required             bool     `json:"required"`
	Kind                 string   `json:"kind"`
	EvidenceRequirements []string `json:"evidence_requirements"`
	EvaluationPolicy     string   `json:"evaluation_policy"`
	ApplicableWhen       string   `json:"applicable_when,omitempty"`
}

// TaskRevision is immutable. A changed goal creates a new revision; changing a
// plan alone cannot remove criteria or expand the authorized action scope.
type TaskRevision struct {
	TaskID             string                `json:"task_id"`
	Revision           int                   `json:"revision"`
	Goal               string                `json:"goal"`
	Scope              []string              `json:"scope"`
	NonGoals           []string              `json:"non_goals"`
	ConstraintRefs     []string              `json:"constraint_refs"`
	Acceptance         []AcceptanceCriterion `json:"acceptance"`
	InputRefs          []string              `json:"input_refs"`
	AllowedActions     []string              `json:"allowed_actions"`
	SourceMessageID    string                `json:"source_message_id"`
	AcceptedBy         string                `json:"accepted_by"`
	AcceptanceEvidence string                `json:"acceptance_evidence"`
	CreatedAt          string                `json:"created_at"`
	Digest             string                `json:"digest"`
}

// Task is a durable identity whose current revision and evaluation projection
// may advance without rewriting historical work or completion records.
type Task struct {
	ID                   string `json:"id"`
	WorkspaceID          string `json:"workspace_id"`
	ParentID             string `json:"parent_id,omitempty"`
	ThreadID             string `json:"thread_id"`
	Title                string `json:"title,omitempty"`
	CurrentRevision      int    `json:"current_revision"`
	OwnerMemberID        string `json:"owner_member_id"`
	OwnerRevision        int    `json:"owner_revision"`
	RootTaskID           string `json:"root_task_id"`
	RootControlEpoch     int    `json:"root_control_epoch"`
	BudgetAccountID      string `json:"budget_account_id"`
	CollaborationScopeID string `json:"collaboration_scope_id"`
	Status               string `json:"status"`
	Kind                 string `json:"kind,omitempty"`
	WaitGeneration       int    `json:"wait_generation,omitempty"`
	Priority             int    `json:"priority"`
	WaitingReason        string `json:"waiting_reason,omitempty"`
	CurrentRunID         string `json:"current_run_id,omitempty"`
	PendingOwnerID       string `json:"pending_owner_id,omitempty"`
	TransferRequestID    string `json:"transfer_request_id,omitempty"`
	EvaluationGeneration int    `json:"evaluation_generation"`
	EvaluationStatus     string `json:"evaluation_status"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

// ValidTaskStatus includes explicit waiting and paused states instead of
// overloading Run failure with long-lived task state.
func ValidTaskStatus(status string) bool {
	return slices.Contains([]string{"draft", "ready", "active", "waiting", "paused", "completed", "canceled"}, status)
}

// CanTransitionTask is the control-plane state machine. Revalidation may reopen
// a completed task only when a new accepted revision or stale evidence requires it.
func CanTransitionTask(from, to string) bool {
	if from == to {
		return true
	}
	transitions := map[string][]string{
		"draft":     {"ready", "canceled"},
		"ready":     {"active", "waiting", "paused", "canceled"},
		"active":    {"ready", "waiting", "completed", "paused", "canceled"},
		"waiting":   {"ready", "paused", "canceled"},
		"paused":    {"ready", "canceled"},
		"completed": {"ready", "waiting"},
		"canceled":  {},
	}
	return slices.Contains(transitions[from], to)
}

// ProjectRecord is one immutable version of a project goal, decision or rule.
type ProjectRecord struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	Revision       int      `json:"revision"`
	Kind           string   `json:"kind"`
	Content        string   `json:"content"`
	Status         string   `json:"status"`
	Applicability  string   `json:"applicability"`
	SourceRefs     []string `json:"source_refs"`
	SourceMessage  string   `json:"source_message_id,omitempty"`
	AcceptedBy     string   `json:"accepted_by"`
	Supersedes     string   `json:"supersedes,omitempty"`
	Visibility     string   `json:"visibility"`
	ExposureDigest string   `json:"exposure_scope_digest"`
	CreatedAt      string   `json:"created_at"`
}

// TaskDependency pins an exact result or follows the currently accepted one.
type TaskDependency struct {
	ID                   string `json:"id"`
	ProducerTaskID       string `json:"producer_task_id"`
	ConsumerTaskID       string `json:"consumer_task_id"`
	CompletionRef        string `json:"completion_ref,omitempty"`
	ArtifactRef          string `json:"artifact_ref,omitempty"`
	Mode                 string `json:"mode"`
	ValidUntil           string `json:"valid_until,omitempty"`
	Valid                bool   `json:"valid"`
	ConsumedTaskRevision int    `json:"consumed_task_revision"`
	ConsumedDigest       string `json:"consumed_digest"`
	CreatedAt            string `json:"created_at"`
}

// CriterionResult separates semantic judgment from current validity.
type CriterionResult struct {
	CriterionID string   `json:"criterion_id"`
	Result      string   `json:"result"`
	Evidence    []string `json:"evidence"`
	Limitations []string `json:"limitations"`
}

// CompletionRecord is bound to exact inputs and an evaluation generation.
type CompletionRecord struct {
	ID                   string            `json:"id"`
	TaskID               string            `json:"task_id"`
	TaskRevision         int               `json:"task_revision"`
	EvaluationGeneration int               `json:"evaluation_generation"`
	InputSnapshotDigest  string            `json:"input_snapshot_digest"`
	SubjectDigest        string            `json:"subject_digest"`
	CandidateRefs        []string          `json:"candidate_refs"`
	Results              []CriterionResult `json:"results"`
	JudgeMemberID        string            `json:"judge_member_id"`
	EvidenceProducer     string            `json:"evidence_producer"`
	Validity             string            `json:"validity"`
	Unresolved           []string          `json:"unresolved"`
	CreatedAt            string            `json:"created_at"`
}

// Accepted reports whether every required criterion passed under the current
// revision/generation and no unresolved effect invalidates the judgment.
func (c CompletionRecord) Accepted(task Task, revision TaskRevision) bool {
	if c.TaskID != task.ID || c.TaskRevision != task.CurrentRevision ||
		c.EvaluationGeneration != task.EvaluationGeneration || c.Validity != "current" ||
		c.InputSnapshotDigest != revision.Digest {
		return false
	}
	byID := map[string]CriterionResult{}
	for _, result := range c.Results {
		byID[result.CriterionID] = result
	}
	for _, criterion := range revision.Acceptance {
		if criterion.Required && byID[criterion.CriterionID].Result != "passed" {
			return false
		}
	}
	return true
}

// Grant is a persistent authorization, independent of native CLI credentials.
type Grant struct {
	ID              string   `json:"id"`
	WorkspaceID     string   `json:"workspace_id"`
	SubjectMemberID string   `json:"subject_member_id"`
	Mode            string   `json:"mode"`
	Actions         []string `json:"actions"`
	Resources       []string `json:"resources"`
	ExternalActions bool     `json:"external_actions"`
	SourceMessageID string   `json:"source_message_id"`
	Revision        int      `json:"revision"`
	Epoch           int      `json:"epoch"`
	Status          string   `json:"status"`
	ExpiresAt       string   `json:"expires_at,omitempty"`
	CreatedAt       string   `json:"created_at"`
	RevokedAt       string   `json:"revoked_at,omitempty"`
}

// AuthorizedActions computes an upper bound; environment enforcement is checked
// separately by the execution package before admitting an action.
func AuthorizedActions(global, project, task, step, run []string) []string {
	sets := [][]string{global, project, task, step, run}
	counts := map[string]int{}
	for _, set := range sets {
		for _, action := range set {
			counts[action]++
		}
	}
	out := []string{}
	for action, count := range counts {
		if count == len(sets) {
			out = append(out, action)
		}
	}
	slices.Sort(out)
	return out
}

// WorkspaceExecution owns an isolated candidate directory and its durable lease.
type WorkspaceExecution struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	TaskID         string `json:"task_id"`
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	CanonicalRoot  string `json:"canonical_root"`
	BaseRevision   string `json:"base_revision"`
	TreeDigest     string `json:"tree_digest"`
	OwnerMemberID  string `json:"owner_member_id"`
	LeaseOwner     string `json:"lease_owner"`
	LeaseEpoch     int    `json:"lease_epoch"`
	LeaseExpiresAt string `json:"lease_expires_at"`
	Status         string `json:"status"`
	Observation    string `json:"observation_completeness"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// ExecutionSession is disposable native context with explicit exposure and
// revocation metadata; WorkRecord remains the canonical recovery source.
type ExecutionSession struct {
	ID              string   `json:"id"`
	TaskID          string   `json:"task_id"`
	MemberID        string   `json:"member_id"`
	Role            string   `json:"role"`
	ExecutionBranch string   `json:"execution_branch"`
	Fingerprint     string   `json:"fingerprint"`
	ContextEpoch    int      `json:"context_epoch"`
	VisibilityEpoch int      `json:"visibility_epoch"`
	ExposedRefs     []string `json:"exposed_refs"`
	// AcknowledgedExposedRefs is the last input snapshot the runner actually
	// accepted. ExposedRefs also includes host reads after that acknowledgement.
	AcknowledgedExposedRefs []string `json:"acknowledged_exposed_refs,omitempty"`
	ExposureDigest          string   `json:"exposure_scope_digest"`
	NativeSessionID         string   `json:"native_session_id,omitempty"`
	Adapter                 string   `json:"adapter"`
	Acknowledgement         string   `json:"acknowledgement"`
	Status                  string   `json:"status"`
	CreatedAt               string   `json:"created_at"`
	UpdatedAt               string   `json:"updated_at"`
}

// EffectRecord tracks only host-managed effects. Arbitrary native commands use
// command receipts and tree diffs with an explicit observation completeness.
type EffectRecord struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	IdempotencyKey string `json:"idempotency_key"`
	RequestDigest  string `json:"request_digest"`
	GrantID        string `json:"grant_id"`
	GrantEpoch     int    `json:"grant_epoch"`
	Status         string `json:"status"`
	BeforeDigest   string `json:"before_digest,omitempty"`
	AfterDigest    string `json:"after_digest,omitempty"`
	RemoteID       string `json:"remote_id,omitempty"`
	Observation    string `json:"observation"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// ValidEffectStatus covers uncertain and reconciled outcomes explicitly.
func ValidEffectStatus(status string) bool {
	return slices.Contains([]string{"prepared", "executing", "applied", "failed_no_effect", "partial", "uncertain", "reconciled_applied", "reconciled_not_applied", "manual_resolution"}, status)
}

// TaskActionReceipt makes pause, resume, start and cancellation replay-safe.
type TaskActionReceipt struct {
	TaskID           string `json:"task_id"`
	RequestID        string `json:"request_id"`
	Action           string `json:"action"`
	RequestDigest    string `json:"request_digest"`
	Task             Task   `json:"task"`
	PendingOwnerID   string `json:"pending_owner_id,omitempty"`
	OwnerRevision    int    `json:"owner_revision,omitempty"`
	TaskRevision     int    `json:"task_revision,omitempty"`
	RootControlEpoch int    `json:"root_control_epoch,omitempty"`
	ExecutionRef     string `json:"execution_ref,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

// InboxItem is an idempotent steering request applied at a declared boundary.
type InboxItem struct {
	ID                string `json:"id"`
	TaskID            string `json:"task_id"`
	SourceMessageID   string `json:"source_message_id"`
	ExpectedRevision  int    `json:"expected_revision"`
	Kind              string `json:"kind"`
	Body              string `json:"body"`
	ApplicationPolicy string `json:"application_policy"`
	Status            string `json:"status"`
	AppliedRevision   int    `json:"applied_revision,omitempty"`
	RunID             string `json:"run_id,omitempty"`
	FollowUpTaskID    string `json:"follow_up_task_id,omitempty"`
	IdempotencyKey    string `json:"idempotency_key"`
	RequestDigest     string `json:"request_digest"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// Question binds a blocking choice to one exact task revision.
type Question struct {
	ID            string   `json:"id"`
	TaskID        string   `json:"task_id"`
	TaskRevision  int      `json:"task_revision"`
	BlockingScope string   `json:"blocking_scope"`
	Body          string   `json:"body"`
	Options       []string `json:"options"`
	Recommended   string   `json:"recommended,omitempty"`
	Status        string   `json:"status"`
	Answer        string   `json:"answer,omitempty"`
	AnswerRef     string   `json:"answer_ref,omitempty"`
	CreatedAt     string   `json:"created_at"`
	ResolvedAt    string   `json:"resolved_at,omitempty"`
}

// CollaborationMessage is an immutable task-to-task envelope. Sender identity,
// exposure and budget ownership are always host-bound.
type CollaborationMessage struct {
	ID                     string   `json:"id"`
	SchemaVersion          int      `json:"schema_version"`
	Kind                   string   `json:"kind"`
	Body                   string   `json:"body"`
	BodyDigest             string   `json:"body_digest"`
	SenderTaskID           string   `json:"sender_task_id"`
	SenderTaskRevision     int      `json:"sender_task_revision"`
	SenderAttemptID        string   `json:"sender_attempt_id,omitempty"`
	SenderMemberID         string   `json:"sender_member_id"`
	TargetTaskID           string   `json:"target_task_id"`
	ResolvedTargetMemberID string   `json:"resolved_target_member_id"`
	TargetOwnerRevision    int      `json:"target_owner_revision"`
	RelatedTaskRevision    int      `json:"related_task_revision,omitempty"`
	ReplyTo                string   `json:"reply_to,omitempty"`
	CorrelationID          string   `json:"correlation_id"`
	RequestID              string   `json:"request_id"`
	RequestDigest          string   `json:"request_digest"`
	ContextRefs            []string `json:"context_refs"`
	DisclosureScope        string   `json:"disclosure_scope"`
	ExposureScopeDigest    string   `json:"exposure_scope_digest"`
	BudgetAccountID        string   `json:"budget_account_id"`
	RootControlEpoch       int      `json:"root_control_epoch"`
	CreatedAt              string   `json:"created_at"`
	ExpiresAt              string   `json:"expires_at,omitempty"`
}

// DeliveryReceipt records observable delivery, not recipient acceptance or work completion.
type DeliveryReceipt struct {
	MessageID    string `json:"message_id"`
	TargetTaskID string `json:"target_task_id"`
	Status       string `json:"status"`
	Attempt      int    `json:"attempt"`
	ResponseRef  string `json:"response_ref,omitempty"`
	Detail       string `json:"detail,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

// Wakeup is a durable predicate registration with compare-and-swap generation.
type Wakeup struct {
	ID                string `json:"id"`
	TaskID            string `json:"task_id"`
	TaskRevision      int    `json:"task_revision"`
	WaitGeneration    int    `json:"wait_generation"`
	Predicate         string `json:"predicate"`
	ObservationCursor string `json:"observation_cursor"`
	WakeSource        string `json:"wake_source"`
	EventID           string `json:"event_id"`
	Status            string `json:"status"`
	SatisfiedBy       string `json:"satisfied_by,omitempty"`
	YieldedRunID      string `json:"yielded_run_id"`
	ContinuationRunID string `json:"continuation_run_id,omitempty"`
	// ContinuedFromRun is read-only legacy data; new wakeups never use the
	// ambiguous thread-last-run fallback.
	ContinuedFromRun string `json:"continued_from_run_id,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// WakeEvent retains an observation that may arrive before its task registers a
// predicate. Payloads are bounded by the host API; no model expression is evaluated.
type WakeEvent struct {
	WakeSource  string `json:"wake_source"`
	EventID     string `json:"event_id"`
	SatisfiedBy string `json:"satisfied_by"`
	ObservedAt  string `json:"observed_at"`
}

// HandoffSummary preserves conditions and counterevidence across members and CLIs.
type HandoffSummary struct {
	TaskRevision      int      `json:"task_revision"`
	ClaimOrResult     string   `json:"claim_or_result"`
	Applicability     string   `json:"applicability"`
	SupportingRefs    []string `json:"supporting_refs"`
	ContradictingRefs []string `json:"contradicting_refs"`
	Limitations       []string `json:"limitations"`
	Unresolved        []string `json:"unresolved"`
	OmittedTopics     []string `json:"omitted_topics"`
	SourceVersions    []string `json:"source_versions"`
	AuthorMemberID    string   `json:"author_member_id"`
	Visibility        string   `json:"visibility,omitempty"`
	ExposureDigest    string   `json:"exposure_scope_digest,omitempty"`
}

// ValidateHandoffSummary checks structure and citations, not semantic sufficiency.
func ValidateHandoffSummary(v HandoffSummary) error {
	if v.TaskRevision < 1 || strings.TrimSpace(v.ClaimOrResult) == "" || v.AuthorMemberID == "" {
		return errors.New("handoff summary requires task revision, result and author")
	}
	if len(v.SourceVersions) == 0 {
		return errors.New("handoff summary requires exact source versions")
	}
	return nil
}

// MemoryRecord inherits disclosure constraints from every source and producer.
type MemoryRecord struct {
	ID                string   `json:"id"`
	WorkspaceID       string   `json:"workspace_id"`
	ProducerAttemptID string   `json:"producer_attempt_id,omitempty"`
	Revision          int      `json:"revision"`
	Scope             string   `json:"scope"`
	Kind              string   `json:"kind"`
	Content           string   `json:"content"`
	SourceRefs        []string `json:"source_refs"`
	SourceLocator     string   `json:"source_locator"`
	ClaimStatus       string   `json:"claim_status"`
	ValidFrom         string   `json:"valid_from"`
	ReviewAfter       string   `json:"review_after,omitempty"`
	Supersedes        string   `json:"supersedes,omitempty"`
	AcceptedBy        string   `json:"accepted_by"`
	Visibility        string   `json:"visibility"`
	ExposureDigest    string   `json:"exposure_scope_digest"`
	CreatedAt         string   `json:"created_at"`
}

// ClaimEvidence binds a semantic review to a verifiable source location.
type ClaimEvidence struct {
	SourceID     string `json:"source_id"`
	Relation     string `json:"relation"`
	Locator      string `json:"locator"`
	Completeness string `json:"completeness"`
	Reviewer     string `json:"reviewer"`
}

// Claim preserves support, contradiction and applicability without source voting.
type Claim struct {
	ID                string          `json:"id"`
	WorkspaceID       string          `json:"workspace_id"`
	TaskID            string          `json:"task_id"`
	ProducerAttemptID string          `json:"producer_attempt_id,omitempty"`
	Revision          int             `json:"revision"`
	Statement         string          `json:"statement"`
	Applicability     string          `json:"applicability"`
	Status            string          `json:"status"`
	SupportingRefs    []string        `json:"supporting_refs"`
	ContradictingRefs []string        `json:"contradicting_refs"`
	Method            string          `json:"method"`
	Assumptions       []string        `json:"assumptions"`
	Limitations       []string        `json:"limitations"`
	AsOf              string          `json:"as_of"`
	ReviewAfter       string          `json:"review_after,omitempty"`
	Evidence          []ClaimEvidence `json:"evidence"`
	Visibility        string          `json:"visibility,omitempty"`
	ExposureDigest    string          `json:"exposure_scope_digest"`
	CreatedAt         string          `json:"created_at"`
}

// DatasetManifest separates observed time from when data became available.
type DatasetManifest struct {
	ID                string   `json:"id"`
	WorkspaceID       string   `json:"workspace_id"`
	TaskID            string   `json:"task_id"`
	ProducerAttemptID string   `json:"producer_attempt_id,omitempty"`
	Revision          int      `json:"revision"`
	SourceRefs        []string `json:"source_refs"`
	Schema            string   `json:"schema"`
	TimeRange         string   `json:"time_range"`
	AcquiredAt        string   `json:"acquired_at"`
	AvailableAt       string   `json:"available_at,omitempty"`
	ProcessingSteps   []string `json:"processing_steps"`
	MissingRules      string   `json:"missing_rules"`
	ContentDigest     string   `json:"content_digest"`
	Location          string   `json:"location"`
	Visibility        string   `json:"visibility,omitempty"`
	ExposureDigest    string   `json:"exposure_scope_digest,omitempty"`
	CreatedAt         string   `json:"created_at"`
}

// ContextEntry is an authorized candidate for selection. Required entries carry
// current effective state; large evidence remains an exact ref for paged reads.
type ContextEntry struct {
	Ref        string `json:"ref"`
	Kind       string `json:"kind"`
	Digest     string `json:"digest"`
	Visibility string `json:"visibility"`
	Required   bool   `json:"required"`
	Content    string `json:"content,omitempty"`
}

// EligibleScope is computed by the host before retrieval or prompt selection.
type EligibleScope struct {
	WorkspaceID          string         `json:"workspace_id"`
	TaskID               string         `json:"task_id"`
	TaskRevision         int            `json:"task_revision"`
	CollaborationScopeID string         `json:"collaboration_scope_id"`
	GrantEpoch           int            `json:"grant_epoch"`
	Entries              []ContextEntry `json:"entries"`
}

// ContextBudget separates host input from an uncertain native model window.
type ContextBudget struct {
	HostInput         int    `json:"host_input"`
	KnownModelWindow  int    `json:"known_model_window,omitempty"`
	WindowSource      string `json:"window_source"`
	NativeOverhead    int    `json:"native_overhead_estimate"`
	ToolGrowthReserve int    `json:"tool_growth_reserve"`
	OutputReserve     int    `json:"output_reserve"`
	SafetyMargin      int    `json:"safety_margin"`
}

// ContextItem records whether content was inlined or only authorized for exact retrieval.
type ContextItem struct {
	Ref        string `json:"ref"`
	Kind       string `json:"kind"`
	Digest     string `json:"digest"`
	Visibility string `json:"visibility,omitempty"`
	Mode       string `json:"mode"`
	Required   bool   `json:"required"`
	Reason     string `json:"reason"`
}

// ContextManifest is an immutable host-submission record, not a claim about model attention.
type ContextManifest struct {
	ID             string        `json:"id"`
	TaskID         string        `json:"task_id"`
	TaskRevision   int           `json:"task_revision"`
	AttemptID      string        `json:"attempt_id,omitempty"`
	PolicyVersion  int           `json:"policy_version"`
	Items          []ContextItem `json:"items"`
	Brief          string        `json:"brief"`
	ScopeDigest    string        `json:"scope_digest"`
	RenderedDigest string        `json:"rendered_digest"`
	WorkDigest     string        `json:"work_digest"`
	// ReuseKey permits deterministic brief reuse only for the exact policy,
	// authorized source snapshot and rendered bytes. It is not a model cache hit.
	ReuseKey          string `json:"context_reuse_key"`
	HostInputBytes    int    `json:"host_input_bytes"`
	InlineItems       int    `json:"inline_items"`
	ReferenceItems    int    `json:"reference_items"`
	HostInputEstimate int    `json:"host_input_estimate"`
	ModelWindowKnown  bool   `json:"model_window_known"`
	NativeObservation string `json:"native_observation"`
	CreatedAt         string `json:"created_at"`
}

// ReadReceipt proves one exact host-provided page, not semantic understanding.
type ReadReceipt struct {
	ID         string `json:"id"`
	ManifestID string `json:"manifest_id"`
	Ref        string `json:"ref"`
	Digest     string `json:"digest"`
	Offset     int    `json:"offset"`
	Bytes      int    `json:"bytes"`
	Complete   bool   `json:"complete"`
	CreatedAt  string `json:"created_at"`
}
