package nativecli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	capabilities []string
}

// Version is diagnostic evidence and a minimum known-incompatible guard, never a whitelist.
var adapters = map[string]adapterSpec{
	"claude": {
		name: "claude", adapter: "claude-stream-json", minVersion: [3]int{2, 0, 0},
		flags:        []string{"--restricted", "--tools", "--strict-mcp-config", "--setting-sources", "--permission-prompts", "--no-session-persistence", "--settings", "--disable-slash-commands"},
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
}

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

	version, help, err := probeCLI(ctx, id, command)
	if err != nil {
		info.Reason = "CLI 能力探测失败"
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
	return info, nil
}

func probeCLI(ctx context.Context, id string, command func(string, ...string) *exec.Cmd) (string, string, error) {
	version := &boundedBuffer{limit: 4096}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	cmd := command(id, "--version")
	cmd.Stdout, cmd.Stderr = version, version
	err := runProbe(probeCtx, cmd)
	cancel()
	if err != nil {
		return "", "", err
	}
	help := &boundedBuffer{limit: 65536}
	helpCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	probe := command(id, helpArgs(id)...)
	probe.Stdout, probe.Stderr = help, help
	err = runProbe(helpCtx, probe)
	stop()
	if err != nil {
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
