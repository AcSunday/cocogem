package nativecli

import "strings"

// Native tool-call identities.
//
// One real CLI tool call must carry exactly one identity through the whole host
// lifecycle: the approval request the user decided on, the `tool_started`
// event, the `awaiting_permission` projection and the terminal
// `tool_finished`/denied event. The host settles one admission per identity, so
// an adapter that prefixes the approval id but emits the CLI's raw id (or vice
// versa) leaves a pending request and a finished effect with different keys.
//
// The canonical form is `<adapter>:<cli identity>`; every adapter builds and
// reads ids only through these helpers, and the CLI's own id is recoverable
// without trimming guesses.
const (
	nativeIDGemini = "gemini:"
	nativeIDPi     = "pi:"
)

// nativeToolID returns the canonical host identity for one CLI tool call.
func nativeToolID(adapter, cliID string) string {
	cliID = strings.TrimSpace(cliID)
	if cliID == "" {
		return ""
	}
	prefix := nativeIDPi
	if adapter == "gemini" {
		prefix = nativeIDGemini
	}
	if strings.HasPrefix(cliID, prefix) {
		return cliID
	}
	return prefix + cliID
}

// nativeToolIDMatches reports whether two ids denote the same host call. It
// compares canonical forms, never a trimmed prefix guess.
func nativeToolIDMatches(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return strings.TrimPrefix(a, nativeIDGemini) == strings.TrimPrefix(strings.TrimPrefix(b, nativeIDGemini), nativeIDPi) ||
		strings.TrimPrefix(a, nativeIDPi) == strings.TrimPrefix(strings.TrimPrefix(b, nativeIDPi), nativeIDGemini)
}
