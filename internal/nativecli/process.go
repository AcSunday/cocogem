package nativecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

type boundedBuffer struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int
	truncate bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data.Len()+len(p) > b.limit {
		if b.truncate {
			_, _ = b.data.Write(p[:max(0, b.limit-b.data.Len())])
			return len(p), nil
		}
		return 0, errors.New("CLI output exceeds budget")
	}
	return b.data.Write(p)
}
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func runProbe(ctx context.Context, cmd *exec.Cmd) error {
	if err := configureProcess(cmd); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { terminateProcess(cmd) })
	defer stop()
	return cmd.Wait()
}

func streamProcess(ctx context.Context, executor string, cmd *exec.Cmd, gate *bridge) error {
	if err := configureProcess(cmd); err != nil {
		return err
	}
	cmd.Stderr = &boundedBuffer{limit: 8192, truncate: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("open native CLI output")
	}
	if err := cmd.Start(); err != nil {
		return errors.New("start native CLI: executable unavailable")
	}
	stop := context.AfterFunc(ctx, func() { terminateProcess(cmd) })
	defer stop()
	var streamErr error
	switch executor {
	case "codex":
		streamErr = parseCodex(stdout, gate)
	case "gemini":
		streamErr = parseGemini(stdout, gate)
	default:
		streamErr = parseClaude(stdout, gate)
	}
	if streamErr != nil {
		terminateProcess(cmd)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if streamErr != nil {
		return streamErr
	}
	if waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			return fmt.Errorf("native CLI exited unsuccessfully (code %d); check its login and model availability", exit.ExitCode())
		}
		return errors.New("native CLI process did not settle")
	}
	return nil
}

func parseClaude(reader io.Reader, gate *bridge) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	initialized, finished := false, false
	sessionID := ""
	bytesRead, events, textBytes := 0, 0, 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		bytesRead += len(line)
		events++
		if bytesRead > 8*1024*1024 || events > 2048 {
			return errors.New("native CLI event budget exceeded")
		}
		var event struct {
			Type      string          `json:"type"`
			Subtype   string          `json:"subtype"`
			SessionID string          `json:"session_id"`
			IsError   bool            `json:"is_error"`
			Result    string          `json:"result"`
			Errors    []string        `json:"errors"`
			Tools     []string        `json:"tools"`
			Message   json.RawMessage `json:"message"`
			Usage     tokenUsage      `json:"usage"`
		}
		if err := json.Unmarshal(line, &event); err != nil || event.Type == "" {
			return errors.New("malformed native CLI event")
		}
		if finished {
			return errors.New("native CLI emitted data after its terminal result")
		}
		if initialized && event.SessionID != "" && event.SessionID != sessionID {
			return errors.New("native CLI changed its session identity")
		}
		switch event.Type {
		case "system":
			if event.Subtype == "init" {
				if initialized || !sessionPattern.MatchString(event.SessionID) {
					return errors.New("native CLI session initialization is invalid")
				}
				initialized = true
				sessionID = event.SessionID
				for _, name := range event.Tools {
					if !gate.allowedNativeName(name) {
						return errors.New("native CLI exposed a tool outside this step's permissions")
					}
				}
				if err := gate.emit("native_session", map[string]any{"executor": "claude", "session_id": sessionID, "mode": "fresh", "cwd": "isolated"}); err != nil {
					return err
				}
			}
		case "assistant":
			if !initialized {
				return errors.New("native CLI replied before session initialization")
			}
			var message struct {
				Content []struct {
					Type string `json:"type"`
					Name string `json:"name"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(event.Message, &message) != nil {
				return errors.New("native CLI returned an invalid assistant message")
			}
			for _, block := range message.Content {
				if block.Type == "tool_use" && !gate.allowedNativeName(block.Name) {
					return errors.New("native CLI requested an unapproved native tool")
				}
				if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
					textBytes += len(block.Text)
					if textBytes > 256*1024 {
						return errors.New("native CLI visible output exceeds budget")
					}
					if err := gate.emit("text", map[string]any{"text": block.Text + "\n"}); err != nil {
						return err
					}
				}
			}
		case "result":
			if !initialized || event.IsError || event.Subtype != "success" {
				return nativeFailure(event.Subtype, event.Result+" "+strings.Join(event.Errors, " "))
			}
			finished = true
			if err := gate.recordUsage(event.Usage); err != nil {
				return err
			}
		case "error":
			return errors.New("native CLI reported an error")
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("native CLI output could not be read within budget")
	}
	if !finished {
		return errors.New("native CLI ended without a confirmed terminal result")
	}
	return nil
}

func nativeFailure(subtype, detail string) error {
	if !regexp.MustCompile(`^[a-z_]{1,80}$`).MatchString(subtype) {
		subtype = "unconfirmed"
	}
	category := "execution"
	lower := strings.ToLower(detail)
	for _, group := range []struct {
		name  string
		words []string
	}{{"authentication", []string{"auth", "login", "log in", "token", "401", "403", "credential", "api key"}}, {"quota", []string{"quota", "rate limit", "429", "credit", "balance", "billing"}}, {"permissions", []string{"permission", "denied", "disallowed"}}, {"tool_unavailable", []string{"tool not found", "tool unavailable", "unknown tool", "mcp server"}}, {"model_unavailable", []string{"model", "404", "not found"}}, {"connection", []string{"network", "connect", "timeout", "fetch", "dns", "tls"}}} {
		for _, word := range group.words {
			if strings.Contains(lower, word) {
				category = group.name
				break
			}
		}
		if category != "execution" {
			break
		}
	}
	guidance := "check the CLI locally"
	if category == "authentication" {
		guidance = "refresh the CLI login locally, then retry"
	}
	return fmt.Errorf("native CLI did not confirm success (result=%s, category=%s); %s", subtype, category, guidance)
}

var sessionPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (b *bridge) allowedNativeName(name string) bool {
	const prefix = "mcp__cocogem__"
	return strings.HasPrefix(name, prefix) && b.schemas[strings.TrimPrefix(name, prefix)] != nil
}

func capabilities(id, version, help string) bool {
	flags := []string{"--restricted", "--tools", "--strict-mcp-config", "--setting-sources", "--permission-prompts", "--no-session-persistence", "--settings", "--disable-slash-commands"}
	switch id {
	case "codex":
		// shortcut: inspect native registry changes before enabling other Codex minor versions.
		if !regexp.MustCompile(`^codex-cli 0\.162\.\d+$`).MatchString(version) {
			return false
		}
		flags = []string{"--ignore-user-config", "--ignore-rules", "--ephemeral", "--sandbox", "--json"}
	case "gemini":
		// shortcut: inspect settings and policy precedence before enabling other Gemini minor versions.
		if !regexp.MustCompile(`^0\.64\.\d+$`).MatchString(version) {
			return false
		}
		flags = []string{"--extensions", "--allowed-mcp-server-names", "--policy", "--admin-policy", "--output-format", "--skip-trust"}
	case "claude":
	default:
		return false
	}
	if version == "" {
		return false
	}
	for _, flag := range flags {
		if !strings.Contains(help, flag) {
			return false
		}
	}
	return true
}
