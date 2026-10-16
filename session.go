package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validateSessionID(id string) error {
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("session ID must be a UUID: %q", id)
	}
	return nil
}

func sessionPath(dir, cli string) (string, error) {
	if cli != "claude" && cli != "codex" && cli != "gemini" {
		return "", fmt.Errorf("unsupported CLI: %q", cli)
	}
	return filepath.Join(dir, cli+".json"), nil
}

func loadSession(dir, cli string) (string, error) {
	path, err := sessionPath(dir, cli)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("load %s session (run a new conversation first): %w", cli, err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("decode %s session: %w", cli, err)
	}
	if result.CLI != cli {
		return "", fmt.Errorf("saved session belongs to %q, not %q", result.CLI, cli)
	}
	if err := validateSessionID(result.SessionID); err != nil {
		return "", err
	}
	return result.SessionID, nil
}

func saveSession(dir string, result Result) error {
	path, err := sessionPath(dir, result.CLI)
	if err != nil {
		return err
	}
	if err := validateSessionID(result.SessionID); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(result)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write session: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close session: %w", closeErr)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}
