package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
)

func runClaude(request Request, output io.Writer) (Result, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if request.SessionID != "" {
		args = append(args, "--resume", request.SessionID)
	}
	cmd := exec.Command("claude", append(args, "--", request.Prompt)...)
	return runCLI("claude", cmd, parseClaudeEvent, output)
}

func parseClaudeEvent(line []byte, output io.Writer) (string, error) {
	var event struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		SessionID string          `json:"session_id"`
		IsError   bool            `json:"is_error"`
		Result    string          `json:"result"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return "", err
	}
	if event.Type == "system" && event.Subtype == "init" {
		return event.SessionID, nil
	}
	if event.Type == "result" && event.IsError {
		return "", fmt.Errorf("request failed: %s", event.Result)
	}
	if event.Type != "assistant" {
		return "", nil
	}
	var message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(event.Message, &message); err != nil {
		return "", fmt.Errorf("parse claude message: %w", err)
	}
	for _, block := range message.Content {
		if block.Type != "text" {
			continue
		}
		if _, err := fmt.Fprintln(output, block.Text); err != nil {
			return "", fmt.Errorf("write reply: %w", err)
		}
	}
	return "", nil
}
