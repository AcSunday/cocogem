package cli

import (
	"fmt"
	"io"
	"strings"
)

type Request struct {
	Prompt    string
	SessionID string
}

type Result struct {
	CLI       string `json:"cli"`
	SessionID string `json:"session_id"`
}

// Invoke streams a reply and returns the CLI's session ID for later calls.
func Invoke(cli string, request Request, output io.Writer) (Result, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return Result{}, fmt.Errorf("prompt must not be empty")
	}
	if request.SessionID != "" {
		if err := validateCLISessionID(cli, request.SessionID); err != nil {
			return Result{}, err
		}
	}
	if output == nil {
		return Result{}, fmt.Errorf("output writer must not be nil")
	}
	var result Result
	var err error
	switch cli {
	case "claude":
		result, err = runClaude(request, output)
	case "codex":
		result, err = runCodex(request, output)
	case "gemini":
		result, err = runGemini(request, output)
	case "pi":
		result, err = runPi(request, output)
	case "opencode":
		result, err = runOpenCode(request, output)
	default:
		return Result{}, fmt.Errorf("unsupported CLI: %q", cli)
	}
	sameSession := strings.EqualFold(result.SessionID, request.SessionID)
	if cli == "opencode" {
		sameSession = result.SessionID == request.SessionID
	}
	if err == nil && request.SessionID != "" && !sameSession {
		return result, fmt.Errorf("%s resumed a different session: got %s, want %s", cli, result.SessionID, request.SessionID)
	}
	return result, err
}
