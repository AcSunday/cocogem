package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
)

func runGemini(request Request, output io.Writer) (Result, error) {
	args := []string{"--output-format", "stream-json", "--approval-mode", "plan"}
	if request.SessionID != "" {
		args = append(args, "--resume="+request.SessionID)
	}
	cmd := exec.Command("gemini", append(args, "--prompt="+request.Prompt)...)
	return runCLI("gemini", cmd, parseGeminiEvent, output)
}

func parseGeminiEvent(line []byte, output io.Writer) (string, error) {
	var event struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Role      string `json:"role"`
		Content   string `json:"content"`
		Status    string `json:"status"`
		Error     struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return "", err
	}
	switch {
	case event.Type == "init":
		return event.SessionID, nil
	case event.Type == "message" && event.Role == "assistant":
		if _, err := io.WriteString(output, event.Content); err != nil {
			return "", fmt.Errorf("write reply: %w", err)
		}
	case event.Type == "result" && event.Status == "error":
		return "", fmt.Errorf("request failed: %s", event.Error.Message)
	case event.Type == "result" && event.Status == "success":
		if _, err := fmt.Fprintln(output); err != nil {
			return "", fmt.Errorf("write reply terminator: %w", err)
		}
	}
	return "", nil
}
