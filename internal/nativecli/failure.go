package nativecli

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// FailureKind is a machine-readable native failure category. Recoverability is
// decided by the workflow from this kind, never from the human-readable reason.
type FailureKind string

const (
	FailureContextOverflow      FailureKind = "context_overflow"
	FailureAuthentication       FailureKind = "authentication"
	FailureQuota                FailureKind = "quota"
	FailureProtocol             FailureKind = "protocol"
	FailureNetwork              FailureKind = "network"
	FailureHostInputLimit       FailureKind = "host_input_limit"
	FailureFirstResponseTimeout FailureKind = "first_response_timeout"
	FailureStallTimeout         FailureKind = "stall_timeout"
	FailurePermission           FailureKind = "permissions"
	FailureToolUnavailable      FailureKind = "tool_unavailable"
	FailureModelUnavailable     FailureKind = "model_unavailable"
	FailureProcess              FailureKind = "process"
	FailureProcessUncertain     FailureKind = "process_uncertain"
	FailureUnknown              FailureKind = "unknown"
)

// Failure keeps a sanitized human reason next to the machine kind. Its Reason
// never contains CLI output, tool input or credentials.
type Failure struct {
	Kind   FailureKind
	Reason string
	// confirmed marks a failure read from explicit protocol error fields rather
	// than inferred from stdio that a cancellation may have truncated.
	confirmed bool
}

func (f *Failure) Error() string { return string(f.Kind) + ": " + f.Reason }

// FailureKindOf reports the machine kind wrapped anywhere in err, or "".
func FailureKindOf(err error) FailureKind {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return ""
}

func newFailure(kind FailureKind, format string, args ...any) error {
	return &Failure{Kind: kind, Reason: fmt.Sprintf(format, args...)}
}

// structuredFailureKind maps an exact or namespace-suffixed protocol code to a
// machine kind. broad marks codes such as invalid_request that do not by
// themselves identify the cause and may be refined by an explicit detail.
func structuredFailureKind(code string) (kind FailureKind, broad bool) {
	normalized := strings.ToLower(strings.TrimSpace(code))
	if normalized == "" {
		return "", false
	}
	candidates := []string{normalized}
	parts := strings.FieldsFunc(normalized, func(r rune) bool { return r == '.' || r == ':' || r == '/' })
	if len(parts) != 0 {
		// Accept both namespace suffixes and a leading code followed by detail.
		candidates = append(candidates, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[len(parts)-1]))
	}
	for _, candidate := range candidates {
		switch candidate {
		case "prompt_too_long", "context_length_exceeded", "context_overflow":
			return FailureContextOverflow, false
		case "authentication_error", "authentication_failed", "unauthenticated", "unauthorized", "invalid_api_key", "login_required", "401", "403":
			return FailureAuthentication, false
		case "rate_limited", "rate_limit_exceeded", "quota_exceeded", "insufficient_quota", "usage_limit", "429":
			return FailureQuota, false
		case "network_error", "connection_error":
			return FailureNetwork, false
		case "permission_denied", "permission_error":
			return FailurePermission, false
		case "tool_not_found", "tool_unavailable", "unknown_tool":
			return FailureToolUnavailable, false
		case "model_not_found", "model_unavailable":
			return FailureModelUnavailable, false
		case "protocol_error", "invalid_request", "invalid_request_error", "bad_request":
			return FailureProtocol, true
		}
	}
	return "", false
}

// terminalFailureKind trusts only documented protocol codes, matched exactly or
// by namespace suffix; it never infers a kind from code substrings.
func terminalFailureKind(reason string) FailureKind {
	kind, _ := structuredFailureKind(reason)
	return kind
}

// overflowDetailPatterns are the only text fallbacks that prove an overflow:
// standard provider error codes or the canonical maximum-context message.
// Broad phrases such as "context window", "input length" or "too many tokens"
// also occur in network, configuration and unsupported-parameter errors.
var overflowDetailPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bprompt_too_long\b`),
	regexp.MustCompile(`(?i)\bcontext_length_exceeded\b`),
	regexp.MustCompile(`(?i)\bprompt is too long\b`),
	regexp.MustCompile(`(?i)\bmaximum context length\b[^.]{0,80}?(?:\d|exceeded|requested)`),
}

func explicitOverflowDetail(detail string) bool {
	for _, pattern := range overflowDetailPatterns {
		if pattern.MatchString(detail) {
			return true
		}
	}
	return false
}

var terminalPattern = regexp.MustCompile(`^[a-z0-9_.-]{1,80}$`)

// safeToken keeps protocol identifiers diagnostic without echoing payloads.
func safeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !terminalPattern.MatchString(value) {
		return "unconfirmed"
	}
	return value
}

func classifiedNativeFailure(terminalReason, subtype, detail string) error {
	kind, category, guidance := classifyFailure(terminalReason, subtype, detail)
	return confirmedFailure(kind, "native CLI did not confirm success (result=%s, category=%s); %s", safeToken(subtype), category, guidance)
}

func confirmedFailure(kind FailureKind, format string, args ...any) error {
	return &Failure{Kind: kind, Reason: fmt.Sprintf(format, args...), confirmed: true}
}

func nativeFailure(subtype, detail string) error {
	return classifiedNativeFailure("", subtype, detail)
}

func classifyFailure(terminalReason, subtype, detail string) (FailureKind, string, string) {
	// Documented structured codes are authoritative. A broad invalid_request
	// code can still be refined by an explicit overflow detail; structured
	// authentication/network results can never be overridden that way.
	for _, code := range []string{terminalReason, subtype} {
		kind, broad := structuredFailureKind(code)
		if kind == "" {
			continue
		}
		if broad && explicitOverflowDetail(detail) {
			return FailureContextOverflow, "context_overflow", guidanceFor(FailureContextOverflow)
		}
		return kind, categoryFor(kind), guidanceFor(kind)
	}
	if explicitOverflowDetail(detail) {
		return FailureContextOverflow, "context_overflow", guidanceFor(FailureContextOverflow)
	}
	lower := strings.ToLower(detail)
	switch {
	case containsAny(lower, "openai-compatible upstream", "use /v1/chat/completions or /v1/responses", "unsupported api protocol"):
		return FailureProtocol, "protocol_mismatch", guidanceFor(FailureProtocol)
	case containsAny(lower, "authentication", "authenticate", "unauthorized", "not authorized", "invalid api key", "api key", "credential", "oauth", "log in", "login", "401", "403"):
		return FailureAuthentication, "authentication", guidanceFor(FailureAuthentication)
	case containsAny(lower, "quota", "rate limit", "rate_limit", "429", "credit", "balance", "billing", "usage limit"):
		return FailureQuota, "quota", guidanceFor(FailureQuota)
	case containsAny(lower, "permission", "denied", "disallowed"):
		return FailurePermission, "permissions", guidanceFor(FailurePermission)
	case containsAny(lower, "tool not found", "tool unavailable", "unknown tool", "mcp server"):
		return FailureToolUnavailable, "tool_unavailable", guidanceFor(FailureToolUnavailable)
	case containsAny(lower, "model", "404", "not found"):
		return FailureModelUnavailable, "model_unavailable", guidanceFor(FailureModelUnavailable)
	case containsAny(lower, "network", "connect", "timeout", "fetch", "dns", "tls"):
		return FailureNetwork, "connection", guidanceFor(FailureNetwork)
	}
	return FailureUnknown, "execution", "check the CLI locally"
}

func categoryFor(kind FailureKind) string {
	switch kind {
	case FailureProtocol:
		return "protocol_mismatch"
	case FailureNetwork:
		return "connection"
	default:
		return string(kind)
	}
}

func guidanceFor(kind FailureKind) string {
	switch kind {
	case FailureAuthentication:
		return "check the CLI's existing credentials and connection settings locally, then retry"
	case FailureProtocol:
		return "the configured upstream does not accept this native CLI API protocol; check its existing endpoint and provider mapping"
	case FailureContextOverflow:
		return "the native session's context window is exhausted; retry only with a smaller referenced input"
	case FailureQuota:
		return "the CLI account reported a quota or rate limit; check its existing plan locally"
	case FailureNetwork:
		return "the CLI could not reach its configured endpoint; check local network and connection settings"
	case FailureHostInputLimit:
		return "reduce the required host input or retrieve evidence by exact reference"
	case FailureFirstResponseTimeout:
		return "the CLI produced no validated response before the first-response deadline"
	case FailureStallTimeout:
		return "the CLI produced no validated activity before the stall deadline"
	default:
		return "check the CLI locally"
	}
}

func containsAny(value string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(value, word) {
			return true
		}
	}
	return false
}
