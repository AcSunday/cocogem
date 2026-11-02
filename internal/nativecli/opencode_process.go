package nativecli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/proc"
)

// OpenCode loads MCP servers asynchronously. Warm the private server before
// starting a turn so the model receives the full tool menu on its first call.
func (p Provider) streamOpenCode(ctx context.Context, cmd *exec.Cmd, gate *bridge, live *liveness) (result error) {
	command := p.command
	if command == nil {
		command = exec.Command
	}
	dir, err := filepath.EvalSymlinks(cmd.Dir)
	if err != nil {
		return newFailure(FailureProcess, "cannot resolve OpenCode working directory")
	}
	cmd.Dir = dir
	cmd.Env = overrideEnv(cmd.Env, map[string]string{"PWD": dir})
	server := command("opencode", "serve", "--stdio", "--port", "0")
	server.Dir, server.Env = cmd.Dir, overrideEnv(cmd.Env, map[string]string{"OPENCODE_PASSWORD": gate.token})
	server.Stderr = &boundedBuffer{limit: 8192, truncate: true}
	lease, err := server.StdinPipe()
	if err != nil {
		return newFailure(FailureProcess, "open OpenCode server lease")
	}
	defer lease.Close()
	stdout, err := server.StdoutPipe()
	if err != nil {
		return newFailure(FailureProcess, "open OpenCode server output")
	}
	if err := configureProcess(server); err != nil {
		return err
	}
	if err := server.Start(); err != nil {
		return newFailure(FailureProcess, "start isolated OpenCode server")
	}
	join := watchCancel(ctx, func() { terminateProcess(server) })
	var identity proc.Identity
	var owned bool
	var drained <-chan struct{}
	defer func() {
		terminateProcess(server)
		_ = server.Wait()
		join()
		if drained != nil {
			<-drained
		}
		result = errors.Join(result, convergeProcessGroup(server, identity, owned))
	}()
	identity, owned, err = inspectOwnership(server.Process.Pid, server.Path)
	if err != nil || !owned {
		return newFailure(FailureProcessUncertain, "OpenCode server process ownership could not be recorded")
	}
	if err := gate.emit("native_process", map[string]any{"pid": identity.PID, "pgid": identity.PGID, "start": identity.Start, "executable": identity.Executable}); err != nil {
		return err
	}
	ready := make(chan string, 1)
	done := make(chan struct{})
	drained = done
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 65536)
		endpoint := ""
		if scanner.Scan() {
			var state struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(scanner.Bytes(), &state) == nil {
				endpoint = state.URL
			}
		}
		ready <- endpoint
		for scanner.Scan() {
		}
	}()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var endpoint string
	select {
	case endpoint = <-ready:
	case <-startup.Done():
		return newFailure(FailureProcess, "OpenCode server startup timed out")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil {
		return newFailure(FailureProtocol, "OpenCode returned an invalid private server address")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 15 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	if err := waitOpenCodeMCP(startup, client, endpoint, cmd.Dir, gate.token); err != nil {
		return err
	}
	selected := ""
	for i, arg := range cmd.Args {
		if arg == "--model" && i+1 < len(cmd.Args) {
			selected = cmd.Args[i+1]
		}
	}
	prompt, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		return newFailure(FailureHostInputLimit, "cannot read OpenCode task input")
	}
	return runOpenCodeTurn(ctx, client, endpoint, cmd.Dir, gate.token, selected, string(prompt), gate, live)
}

func waitOpenCodeMCP(ctx context.Context, client *http.Client, endpoint, dir, password string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/mcp?location[directory]="+url.QueryEscape(dir), nil)
		if err != nil {
			return newFailure(FailureProtocol, "invalid OpenCode MCP discovery request")
		}
		request.SetBasicAuth("opencode", password)
		response, err := client.Do(request)
		if err == nil {
			var state struct {
				Data []struct {
					Name   string
					Status struct{ Status string }
				} `json:"data"`
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&state)
			response.Body.Close()
			if response.StatusCode != http.StatusOK || err != nil {
				return newFailure(FailureProtocol, "OpenCode MCP discovery failed")
			}
			for _, server := range state.Data {
				if server.Name != "cocogem" {
					return newFailure(FailureProtocol, "OpenCode exposed an unexpected MCP server")
				}
				if server.Status.Status == "connected" {
					// OpenCode 2.2.5 reconciles its tool registry after a 100 ms
					// debounce following MCP.ToolsChanged. Keep the first turn behind it.
					timer := time.NewTimer(150 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				if strings.Contains(server.Status.Status, "fail") {
					return newFailure(FailureToolUnavailable, "OpenCode could not connect to the scoped MCP bridge")
				}
			}
		}
		select {
		case <-ctx.Done():
			return newFailure(FailureToolUnavailable, "OpenCode did not finish scoped MCP initialization")
		case <-ticker.C:
		}
	}
}
