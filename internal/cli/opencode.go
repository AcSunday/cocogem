package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func runOpenCode(request Request, output io.Writer) (Result, error) {
	args := []string{"run", "--format", "json", "--standalone"}
	if request.SessionID != "" {
		// OpenCode --session creates a missing ID; Cocogem resume must fail
		// when the original native session no longer exists.
		check := exec.Command("opencode", "api", "session.get", "--param", "sessionID="+request.SessionID, "--standalone")
		check.Stdout, check.Stderr = io.Discard, os.Stderr
		if err := check.Run(); err != nil {
			return Result{CLI: "opencode"}, fmt.Errorf("OpenCode session is missing or unavailable: %w", err)
		}
		args = append(args, "--session", request.SessionID)
	}
	cmd := exec.Command("opencode", args...)
	cmd.Stdin = strings.NewReader(request.Prompt)
	parser := &openCodeEvents{}
	result, err := runCLI("opencode", cmd, parser.parse, output)
	if err == nil && !parser.finished {
		err = fmt.Errorf("opencode ended without a confirmed terminal result")
	}
	return result, err
}

type openCodeEvents struct {
	session  string
	finished bool
}

func (p *openCodeEvents) parse(line []byte, output io.Writer) (string, error) {
	var event struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionID"`
		Part      struct {
			Text   string `json:"text"`
			Reason string `json:"reason"`
		} `json:"part"`
		Error struct {
			Message string `json:"message"`
			Data    struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return "", err
	}
	if event.Type == "error" {
		return "", fmt.Errorf("request failed: %s %s", event.Error.Message, event.Error.Data.Message)
	}
	if p.finished || event.Type == "" || validateCLISessionID("opencode", event.SessionID) != nil || p.session != "" && p.session != event.SessionID {
		return "", fmt.Errorf("opencode emitted an invalid session or terminal event")
	}
	p.session = event.SessionID
	switch event.Type {
	case "text":
		if _, err := fmt.Fprintln(output, event.Part.Text); err != nil {
			return "", fmt.Errorf("write reply: %w", err)
		}
	case "step_finish":
		switch event.Part.Reason {
		case "stop":
			p.finished = true
		case "tool-calls":
		default:
			return "", fmt.Errorf("opencode did not complete the reply: %s", event.Part.Reason)
		}
	}
	return event.SessionID, nil
}
