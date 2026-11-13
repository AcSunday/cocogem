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
	FailureProviderTransient    FailureKind = "provider_transient"
	FailureProcess              FailureKind = "process"
	FailureProcessUncertain     FailureKind = "process_uncertain"
	FailureUnknown              FailureKind = "unknown"
)

// Failure keeps a sanitized human reason next to the machine kind. Its Reason
// never contains CLI output, tool input or credentials.
type Failure struct {
	Kind   FailureKind
	Reason string
	// ToolFree is set only by an adapter whose protocol shows every tool request
	// before it runs, for a failure the CLI reported itself after a turn in
	// which it requested none. Replaying that invocation cannot repeat an effect.
	ToolFree bool
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

// ToolFree reports whether err proves that the native CLI ended its turn on a
// failure it reported itself, before requesting any tool. Every leaf of a
// joined error must carry that proof: a cancellation, an unverified process or
// an untyped error next to it leaves the outcome unproven.
func ToolFree(err error) bool {
	proven := false
	var walk func(error) bool
	walk = func(current error) bool {
		if current == nil {
			return false
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			leaves := joined.Unwrap()
			if len(leaves) == 0 {
				return false
			}
			for _, leaf := range leaves {
				if !walk(leaf) {
					return false
				}
			}
			return true
		}
		if failure, ok := current.(*Failure); ok {
			if !failure.ToolFree {
				return false
			}
			proven = true
			return true
		}
		if wrapped := errors.Unwrap(current); wrapped != nil {
			return walk(wrapped)
		}
		return false
	}
	return walk(err) && proven
}

// toolFreeFailure marks the terminal failure of an invocation whose adapter saw
// no tool request. Only a failure the CLI reported itself qualifies; anything
// inferred from stdio, or joined with other evidence, is returned unchanged.
func toolFreeFailure(err error) error {
	failure, ok := err.(*Failure)
	if !ok || !failure.confirmed {
		return err
	}
	marked := *failure
	marked.ToolFree = true
	return &marked
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
		case "rate_limited", "rate_limit", "rate_limit_exceeded", "quota_exceeded", "insufficient_quota", "usage_limit", "billing_error", "429":
			return FailureQuota, false
		case "network_error", "connection_error":
			return FailureNetwork, false
		case "server_error", "internal_error", "bad_gateway", "service_unavailable", "gateway_timeout", "overloaded_error", "500", "502", "503", "504":
			return FailureProviderTransient, false
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

// classifiedEndpointFailure classifies a turn the CLI ended on a provider error
// from the structured facts it reports: the HTTP status of the failed request
// and the CLI's own error category. Those outrank the error text, which is
// consulted only when neither is decisive. A known status is named in the
// reason so the cause is visible without echoing CLI output.
func classifiedEndpointFailure(terminalReason, subtype string, status int, errorCategory, detail string) error {
	kind, category, guidance := endpointFailure(terminalReason, subtype, status, errorCategory, detail)
	if status > 0 {
		return confirmedFailure(kind, "native CLI did not confirm success (result=%s, status=%d, category=%s); %s", safeToken(subtype), status, category, guidance)
	}
	return confirmedFailure(kind, "native CLI did not confirm success (result=%s, category=%s); %s", safeToken(subtype), category, guidance)
}

func endpointFailure(terminalReason, subtype string, status int, errorCategory, detail string) (FailureKind, string, string) {
	if kind := statusFailureKind(status); kind != "" {
		return kind, categoryFor(kind), guidanceFor(kind)
	}
	if kind, broad := structuredFailureKind(errorCategory); kind != "" {
		if broad && explicitOverflowDetail(detail) {
			kind = FailureContextOverflow
		}
		return kind, categoryFor(kind), guidanceFor(kind)
	}
	return classifyFailure(terminalReason, subtype, detail)
}

// statusFailureKind maps the HTTP status of a failed provider request. Statuses
// that do not identify a cause on their own, such as 400 or 404, stay open.
func statusFailureKind(status int) FailureKind {
	switch {
	case status == 401 || status == 403:
		return FailureAuthentication
	case status == 429:
		return FailureQuota
	case status == 408:
		return FailureNetwork
	case status >= 500 && status <= 599:
		return FailureProviderTransient
	default:
		return ""
	}
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
	// A CLI's own diagnosis of a cut or malformed response comes first: its
	// byte and timing counts would otherwise be read as HTTP status codes.
	case containsAny(lower, "empty or malformed response", "stream_truncated", "streamtruncated", "stream ended unexpectedly", "premature close"):
		return FailureNetwork, "incomplete_response", "the configured endpoint returned an incomplete or malformed response, often a proxy or gateway cutting the stream; the workflow may retry safe steps"
	case containsAny(lower, "openai-compatible upstream", "use /v1/chat/completions or /v1/responses", "unsupported api protocol"):
		return FailureProtocol, "protocol_mismatch", guidanceFor(FailureProtocol)
	case containsAny(lower, "authentication", "authenticate", "unauthorized", "not authorized", "invalid api key", "api key", "credential", "oauth", "log in", "login", "401", "403"):
		return FailureAuthentication, "authentication", guidanceFor(FailureAuthentication)
	case containsAny(lower, "quota", "rate limit", "rate_limit", "429", "credit", "balance", "billing", "usage limit"):
		return FailureQuota, "quota", guidanceFor(FailureQuota)
	case containsAny(lower, "temporarily unavailable", "service unavailable", "bad gateway", "gateway timeout", "overloaded", "internal server error", "502", "503", "504"):
		return FailureProviderTransient, "provider_transient", guidanceFor(FailureProviderTransient)
	case containsAny(lower, "permission", "denied", "disallowed"):
		return FailurePermission, "permissions", guidanceFor(FailurePermission)
	case containsAny(lower, "tool not found", "tool unavailable", "unknown tool", "mcp server"):
		return FailureToolUnavailable, "tool_unavailable", guidanceFor(FailureToolUnavailable)
	case containsAny(lower, "model", "404", "not found"):
		return FailureModelUnavailable, "model_unavailable", guidanceFor(FailureModelUnavailable)
	case containsAny(lower, "network", "connect", "timeout", "timed out", "fetch", "dns", "tls", "econnreset", "etimedout", "enotfound", "eai_again", "socket hang up"):
		return FailureNetwork, "connection", guidanceFor(FailureNetwork)
	}
	return FailureUnknown, "execution", "check the CLI locally"
}

func categoryFor(kind FailureKind) string {
	switch kind {
	case FailureProtocol:
		return "protocol_mismatch"
	case FailureNetwork, FailureProviderTransient:
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
	case FailureProviderTransient:
		return "the provider reported a temporary outage or rate limit; the workflow may retry safe read-only steps"
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
