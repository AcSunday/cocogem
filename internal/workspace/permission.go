package workspace

import (
	"path/filepath"
	"slices"
	"strings"
)

// Tool permission request statuses. A request is created before an effect that
// the current global mode does not admit, and stays pending until a user decides
// it, the permission epoch advances, or its run/attempt ends.
const (
	PermissionPending             = "pending"
	PermissionAllowedOnce         = "allowed_once"
	PermissionAllowedConversation = "allowed_conversation"
	PermissionDenied              = "denied"
	PermissionExpired             = "expired"
	PermissionCanceled            = "canceled"
)

// Decisions accepted from the user. They are the only values that can settle a
// pending request.
const (
	DecisionAllowOnce         = "allow_once"
	DecisionAllowConversation = "allow_conversation"
	DecisionDeny              = "deny"
)

// ValidDecision rejects unknown wire values before any state change.
func ValidDecision(decision string) bool {
	return slices.Contains([]string{DecisionAllowOnce, DecisionAllowConversation, DecisionDeny}, decision)
}

// Tool origins distinguish host-executed tools from CLI-owned tools.
const (
	OriginHost   = "host"
	OriginNative = "native"
)

// ToolPermissionRequest is the durable, user-facing authorization object for a
// single effect the member is not yet allowed to perform. Its normalized scope
// and argument digest are computed by the host; a model string never becomes a
// scope on its own.
type ToolPermissionRequest struct {
	ID                      string `json:"id"`
	ThreadID                string `json:"thread_id"`
	WorkspaceID             string `json:"workspace_id"`
	RunID                   string `json:"run_id"`
	StepID                  string `json:"step_id"`
	AttemptID               string `json:"attempt_id"`
	MemberID                string `json:"member_id"`
	MemberName              string `json:"member_name,omitempty"`
	ToolCallID              string `json:"tool_call_id"`
	Origin                  string `json:"origin"`
	Adapter                 string `json:"adapter,omitempty"`
	Action                  string `json:"action"`
	ToolName                string `json:"tool_name"`
	NormalizedResourceScope string `json:"normalized_resource_scope"`
	ArgumentsDigest         string `json:"arguments_digest"`
	Reason                  string `json:"reason"`
	// OperationSummary is the bounded, secret-free description shown with the
	// request (the command, destination and effect). It is display data computed
	// by the host from the exact arguments, never an authorization.
	OperationSummary string `json:"operation_summary,omitempty"`
	PermissionEpoch  int    `json:"permission_epoch"`
	// PreApproval marks a request created by request_tool_access. Its tool call
	// identity belongs to the request tool itself; the approval is instead
	// consumed by the next real call that matches run/step/attempt, action,
	// tool, scope and argument digest exactly once.
	PreApproval bool `json:"pre_approval,omitempty"`
	// Risk is a non-empty grading fact the adapter proved about the target. A
	// risky target never auto-runs; the user's decision acknowledges it.
	Risk       string `json:"risk,omitempty"`
	Status     string `json:"status"`
	RequestID  string `json:"request_id"`
	Revision   int    `json:"revision"`
	CreatedAt  string `json:"created_at"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

// Pending reports whether a decision is still required.
func (r ToolPermissionRequest) Pending() bool { return r.Status == PermissionPending }

// DecisionStatus maps a decision to the settled request status.
func DecisionStatus(decision string) string {
	switch decision {
	case DecisionAllowOnce:
		return PermissionAllowedOnce
	case DecisionAllowConversation:
		return PermissionAllowedConversation
	case DecisionDeny:
		return PermissionDenied
	}
	return ""
}

// ConversationGrant is a thread-scoped, member-independent authorization for
// one action and one normalized resource scope at one permission epoch.
type ConversationGrant struct {
	ID                      string `json:"id"`
	ThreadID                string `json:"thread_id"`
	Action                  string `json:"action"`
	ToolMatcher             string `json:"tool_matcher"`
	NormalizedResourceScope string `json:"normalized_resource_scope"`
	PermissionEpoch         int    `json:"permission_epoch"`
	SourceRequestID         string `json:"source_request_id"`
	Status                  string `json:"status"`
	CreatedAt               string `json:"created_at"`
	RevokedAt               string `json:"revoked_at,omitempty"`
	// AffectedActiveRuns is reported on revocation: executions still active in
	// the thread when the grant was withdrawn. They must be stopped and verified
	// by the caller; this is never claimed as a safe stop here.
	AffectedActiveRuns int `json:"affected_active_runs,omitempty"`
}

// Admission statuses. A reserved admission consumed a one-time authorization;
// it is never made available again, even after a crash.
const (
	AdmissionReserved  = "reserved"
	AdmissionStarted   = "started"
	AdmissionCompleted = "completed"
	AdmissionFailed    = "failed"
	AdmissionUnknown   = "unknown"
)

// ToolInvocationAdmission is the durable consumption/execution record that makes
// a one-time allowance at-most-once. It is unique per request and tool call.
type ToolInvocationAdmission struct {
	ID                  string `json:"id"`
	PermissionRequestID string `json:"permission_request_id"`
	GrantID             string `json:"grant_id,omitempty"`
	RunID               string `json:"run_id"`
	StepID              string `json:"step_id"`
	AttemptID           string `json:"attempt_id"`
	ToolCallID          string `json:"tool_call_id"`
	ArgumentsDigest     string `json:"arguments_digest"`
	PermissionEpoch     int    `json:"permission_epoch"`
	Status              string `json:"status"`
	ResultRef           string `json:"result_ref,omitempty"`
	ReservedAt          string `json:"reserved_at"`
	StartedAt           string `json:"started_at,omitempty"`
	SettledAt           string `json:"settled_at,omitempty"`
}

// NormalizeScope produces the host-owned scope string for one candidate path.
// The result is compared literally, so it must be the resolved real path (no
// relative segments and no symlink). A relative target resolves against root,
// never the server's process working directory. When the target does not exist,
// the deepest existing ancestor is resolved (following its symlinks) and the
// missing segments are appended, so `root/link/newdir/file` cannot keep a
// lexical prefix that hides an escape. An empty root or path yields "".
func NormalizeScope(root, target string) string {
	if root == "" || target == "" {
		return ""
	}
	resolvedRoot := resolveScopePath(root)
	if resolvedRoot == "" {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(resolvedRoot, target)
	}
	resolvedTarget := resolveScopePath(target)
	if resolvedTarget == "" {
		return ""
	}
	return filepath.Clean(resolvedTarget)
}

// resolveScopePath resolves a path to its real location, following symlinks on
// the deepest existing ancestor and keeping the trailing segments that do not
// exist yet. It never returns a partially lexical path that silently drops a
// symlinked component.
func resolveScopePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	// Walk up to the deepest existing ancestor, resolve it, then re-append the
	// missing segments in order.
	remaining := []string{}
	current := filepath.Clean(absolute)
	for {
		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without an existing ancestor: the path
			// cannot be resolved safely.
			return ""
		}
		remaining = append([]string{filepath.Base(current)}, remaining...)
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Clean(filepath.Join(append([]string{resolved}, remaining...)...))
		}
		current = parent
	}
}

// ScopeContains reports whether target is the root itself or below it. It works
// on already normalized scopes; a string prefix is never sufficient.
func ScopeContains(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ActionForNativeTool maps a CLI-owned tool name to the capability action used
// in requests and grants.
func ActionForNativeTool(name string) string {
	switch strings.ToLower(name) {
	case "write", "edit", "apply_patch", "write_file", "replace", "multiedit":
		return "write"
	case "bash", "shell", "run_shell_command", "exec", "command":
		return "execute"
	case "webfetch", "websearch", "web_search", "fetch":
		return "network"
	default:
		return "read"
	}
}
