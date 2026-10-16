package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func runCodex(request Request, output io.Writer) (Result, error) {
	args := []string{"exec", "--sandbox", "read-only"}
	if request.SessionID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check", "--")
	if request.SessionID != "" {
		args = append(args, request.SessionID)
	}
	cmd := exec.Command("codex", append(args, "-")...)
	cmd.Stdin = strings.NewReader(request.Prompt)
	return runCLI("codex", cmd, parseCodexEvent, output)
}

func parseCodexEvent(line []byte, output io.Writer) (string, error) {
	var event struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Item     struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return "", err
	}
	switch {
	case event.Type == "thread.started":
		return event.ThreadID, nil
	case event.Type == "item.completed" && event.Item.Type == "agent_message":
		if _, err := fmt.Fprintln(output, event.Item.Text); err != nil {
			return "", fmt.Errorf("write reply: %w", err)
		}
	case event.Type == "turn.failed":
		return "", fmt.Errorf("turn failed: %s", event.Error.Message)
	}
	return "", nil
}
