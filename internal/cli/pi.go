package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func runPi(request Request, output io.Writer) (Result, error) {
	args := []string{"--mode", "json", "--print"}
	if request.SessionID != "" {
		args = append(args, "--session", request.SessionID)
	}
	// The prompt goes through stdin so @file mentions inside it are not
	// interpreted as attachments.
	cmd := exec.Command("pi", append(args, "--")...)
	cmd.Stdin = strings.NewReader(request.Prompt)
	parser := &piEvents{}
	result, err := runCLI("pi", cmd, parser.parse, output)
	if err == nil && !parser.finished {
		err = fmt.Errorf("pi ended without a confirmed terminal result")
	}
	return result, err
}

type piEvents struct {
	lastStop  string
	lastError string
	finished  bool
}

func (p *piEvents) parse(line []byte, output io.Writer) (string, error) {
	var event struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		Aborted    bool   `json:"aborted"`
		Success    bool   `json:"success"`
		FinalError string `json:"finalError"`
		Message    struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"message"`
		AssistantMessageEvent struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return "", err
	}
	if p.finished {
		return "", fmt.Errorf("pi emitted data after its terminal result")
	}
	switch {
	case event.Type == "session":
		return event.ID, nil
	case event.Type == "message_start" && event.Message.Role == "assistant":
		p.lastStop, p.lastError = "", ""
	case event.Type == "message_end" && event.Message.Role == "assistant":
		p.lastStop, p.lastError = event.Message.StopReason, event.Message.ErrorMessage
	case event.Type == "message_update" && event.AssistantMessageEvent.Type == "text_delta":
		if _, err := io.WriteString(output, event.AssistantMessageEvent.Delta); err != nil {
			return "", fmt.Errorf("write reply: %w", err)
		}
	case event.Type == "auto_retry_end" && !event.Success:
		return "", fmt.Errorf("request failed: %s", event.FinalError)
	case event.Type == "agent_settled":
		if event.Aborted {
			return "", fmt.Errorf("request aborted")
		}
		switch p.lastStop {
		case "stop":
		case "error", "aborted":
			return "", fmt.Errorf("request failed: %s", p.lastError)
		case "length":
			return "", fmt.Errorf("reply was truncated at the model output token limit")
		default:
			return "", fmt.Errorf("pi did not confirm a completed assistant reply")
		}
		if _, err := fmt.Fprintln(output); err != nil {
			return "", fmt.Errorf("write reply terminator: %w", err)
		}
		p.finished = true
	}
	return "", nil
}
