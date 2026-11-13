package nativecli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
)

type adapterSpec struct {
	name         string
	adapter      string
	minVersion   [3]int
	flags        []string
	resumeFlags  []string
	capabilities []string
}

// Version is diagnostic evidence and a minimum known-incompatible guard, never a whitelist.
var adapters = map[string]adapterSpec{
	"opencode": {name: "opencode", adapter: "opencode-server-events", minVersion: [3]int{2, 2, 5}, flags: []string{"--format", "--model", "--agent", "--standalone"}, resumeFlags: []string{"--session"}, capabilities: []string{"event_stream", "scoped_mcp", "tool_allowlist", "model_variants"}},
	"claude": {
		name: "claude", adapter: "claude-stream-json", minVersion: [3]int{2, 0, 0},
		flags:        []string{"--restricted", "--tools", "--strict-mcp-config", "--setting-sources", "--permission-prompts", "--no-session-persistence", "--settings", "--disable-slash-commands"},
		resumeFlags:  []string{"--resume"},
		capabilities: []string{"stream_json", "scoped_mcp", "ephemeral_session", "tool_allowlist"},
	},
	"codex": {
		name: "codex", adapter: "codex-exec-json", minVersion: [3]int{0, 100, 0},
		flags:        []string{"--ignore-user-config", "--ignore-rules", "--ephemeral", "--sandbox", "--json"},
		capabilities: []string{"stream_json", "scoped_mcp", "ephemeral_session", "tool_allowlist"},
	},
	"gemini": {
		name: "gemini", adapter: "gemini-stream-json", minVersion: [3]int{0, 50, 0},
		flags:        []string{"--extensions", "--allowed-mcp-server-names", "--policy", "--admin-policy", "--output-format", "--skip-trust", "--approval-mode"},
		capabilities: []string{"stream_json", "scoped_mcp", "policy_engine", "tool_allowlist"},
	},
	"pi": {
		name: "pi", adapter: "pi-json", minVersion: [3]int{1, 4, 0},
		flags:        []string{"--mode", "--print", "--no-session", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--no-builtin-tools", "--tools", "--thinking"},
		capabilities: []string{"stream_json", "scoped_mcp", "ephemeral_session", "tool_allowlist"},
	},
}

// probeWindow bounds one capability probe command. The OS verifies a CLI binary
// before the first launch after it was installed or updated; for a large
// single-file CLI that alone takes several seconds, longer on a busy machine.
const probeWindow = 30 * time.Second

// errProbeTimeout marks a probe command that did not answer inside its window.
var errProbeTimeout = errors.New("CLI capability probe timed out")

// Inspect checks installation and required isolation arguments without running
// a model turn. It never claims that the tool boundary has been proven.
func Inspect(ctx context.Context, id string) (agent.AdapterInfo, error) {
	info, err := check(ctx, id, nil)
	if err != nil {
		return info, err
	}
	return applyProbeCache(info), nil
}

func check(ctx context.Context, id string, command func(string, ...string) *exec.Cmd) (agent.AdapterInfo, error) {
	return checkWithin(ctx, id, command, probeWindow)
}

// checkWithin is check with an explicit window for each probe command.
func checkWithin(ctx context.Context, id string, command func(string, ...string) *exec.Cmd, window time.Duration) (agent.AdapterInfo, error) {
	spec, ok := adapters[id]
	info := agent.AdapterInfo{ID: id, Adapter: spec.adapter, Isolation: "unavailable"}
	if !ok {
		info.Reason = "不支持的 CLI"
		return info, errors.New(info.Reason)
	}
	injected := command != nil
	if command == nil {
		command = exec.Command
	}
	path := ""
	if !injected {
		resolved, err := exec.LookPath(id)
		if err != nil {
			info.Reason = "CLI 未安装或不在 PATH 中"
			return info, errors.New(info.Reason)
		}
		path = resolved
	} else {
		path = command(id, "--version").Path
	}
	info.Installed = path != ""
	if path != "" {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}
		if absolute, absErr := filepath.Abs(resolved); absErr == nil {
			resolved = absolute
		}
		stat, statErr := os.Stat(resolved)
		if statErr != nil || !stat.Mode().IsRegular() {
			info.Reason = "CLI 程序无法作为常规可执行文件检查"
			return info, errors.New(info.Reason)
		}
		info.Program = resolved
		info.Identity = programIdentity(resolved, stat)
	}

	version, help, err := probeCLI(ctx, id, command, window)
	if err != nil {
		info.Reason = "CLI 能力探测失败"
		if errors.Is(err, errProbeTimeout) {
			// A CLI that is still starting is not an incompatible installation.
			info.Reason = fmt.Sprintf("CLI 能力探测超时：%s 内没有返回版本或帮助信息，可稍后继续", window)
		}
		if ctx.Err() != nil {
			return info, ctx.Err()
		}
		return info, errors.New(info.Reason)
	}
	info.Version = version
	if id == "gemini" {
		if err := geminiPolicyBoundary(); err != nil {
			info.Reason = err.Error()
			return info, err
		}
	}
	if err := capabilityError(id, version, help); err != nil {
		info.Reason = err.Error()
		return info, err
	}
	info.Supported = true
	info.Isolation = "candidate"
	info.Capabilities = append([]string{}, spec.capabilities...)
	resumeSupported := len(spec.resumeFlags) != 0
	for _, flag := range spec.resumeFlags {
		resumeSupported = resumeSupported && strings.Contains(help, flag)
	}
	if resumeSupported {
		info.Capabilities = append(info.Capabilities, "managed_session", "session_resume_ack=supported")
	}
	info.Capabilities = append(info.Capabilities,
		"os="+runtime.GOOS,
		"file_isolation=unknown", "secret_environment_isolation=unknown",
		"network_isolation=unknown", "descendant_lifecycle=unknown",
		"detached_descendant_lifecycle=unknown", "background_job_budget_accounting=unknown",
		"native_effect_audit=partial", "git_metadata_isolation=unknown",
	)
	if !resumeSupported {
		info.Capabilities = append(info.Capabilities, "session_resume_ack=unknown")
	}
	return info, nil
}

func probeCLI(ctx context.Context, id string, command func(string, ...string) *exec.Cmd, window time.Duration) (string, string, error) {
	run := func(output *boundedBuffer, args ...string) error {
		probeCtx, cancel := context.WithTimeout(ctx, window)
		defer cancel()
		cmd := command(id, args...)
		cmd.Stdout, cmd.Stderr = output, output
		err := runProbe(probeCtx, cmd)
		if err != nil && ctx.Err() == nil && errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return errProbeTimeout
		}
		return err
	}
	version := &boundedBuffer{limit: 4096}
	if err := run(version, "--version"); err != nil {
		return "", "", err
	}
	help := &boundedBuffer{limit: 65536}
	if err := run(help, helpArgs(id)...); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(version.String()), help.String(), nil
}

func programIdentity(path string, info os.FileInfo) string {
	return fmt.Sprintf("%s|%d|%d|%s", path, info.Size(), info.ModTime().UnixNano(), inodeIdentity(info))
}

var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

func parseVersion(raw string) ([3]int, string, bool) {
	m := versionPattern.FindStringSubmatch(raw)
	if m == nil {
		return [3]int{}, "", false
	}
	var v [3]int
	for i := range 3 {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, "", false
		}
		v[i] = n
	}
	return v, m[0], true
}

func versionAtLeast(got, minimum [3]int) bool {
	for i := range 3 {
		if got[i] != minimum[i] {
			return got[i] > minimum[i]
		}
	}
	return true
}

// capabilityError blocks known-incompatible or isolation-incomplete installations.
func capabilityError(id, version, help string) error {
	spec, ok := adapters[id]
	if !ok {
		return errors.New("不支持的 CLI")
	}
	if strings.TrimSpace(version) == "" {
		return errors.New("CLI 版本探测失败")
	}
	if parsed, raw, recognized := parseVersion(version); recognized {
		if !versionAtLeast(parsed, spec.minVersion) {
			return fmt.Errorf("当前 %s 版本 %s 低于已验证的协议基线 %d.%d.%d；请升级 CLI", id, raw, spec.minVersion[0], spec.minVersion[1], spec.minVersion[2])
		}
	}
	for _, flag := range spec.flags {
		if !strings.Contains(help, flag) {
			return fmt.Errorf("%s CLI 缺少受限执行参数 %s；未知版本不会放宽工具隔离", id, flag)
		}
	}
	return nil
}
