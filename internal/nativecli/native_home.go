package nativecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/AcSunday/cocogem/internal/model"
)

// Pi, Codex and Gemini share a native history or a model catalog across
// invocations of one managed session. Its directory lock lasts through exit.
func prepareManagedSessionDirectory(ctx context.Context, dir, executor string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("create private managed CLI session directory")
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil, errors.New("managed CLI session directory is not private")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, errors.New("secure private managed CLI session directory")
	}
	switch executor {
	case "pi", "codex", "gemini":
		return lockSessionDirectory(ctx, dir)
	default:
		return nil, nil
	}
}

// nativeStateRoot resolves the CLI's own persistent state directory. The host
// never overrides it with a temporary CODEX_HOME/CLAUDE_CONFIG_DIR: OAuth token
// refresh, prompt-cache hits and the user's own terminal history all live there,
// so moving it would fork the user's identity. Each CLI keeps its documented
// location, and a user-set environment variable wins.
//
// A reviewer's disposable verify_dir run opts out (SessionStorageRoot == "" and
// ephemeral flags) so a destroyed directory never pollutes that same history.
func nativeStateRoot(executor string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	switch executor {
	case "claude":
		if value := os.Getenv("CLAUDE_CONFIG_DIR"); value != "" {
			return value
		}
		return filepath.Join(home, ".claude")
	case "agy":
		return filepath.Join(home, ".gemini", "antigravity")
	case "codex":
		if value := os.Getenv("CODEX_HOME"); value != "" {
			return value
		}
		return filepath.Join(home, ".codex")
	case "gemini":
		if value := os.Getenv("GEMINI_CLI_HOME"); value != "" {
			return value
		}
		return filepath.Join(home, ".gemini")
	case "pi":
		if value := os.Getenv("PI_HOME"); value != "" {
			return value
		}
		return filepath.Join(home, ".pi")
	case "opencode":
		if value := os.Getenv("XDG_DATA_HOME"); value != "" {
			return filepath.Join(value, "opencode")
		}
		return filepath.Join(home, ".local", "share", "opencode")
	}
	return ""
}

// managedNativeSession reports whether this attempt may resume a native session
// in the CLI's own state directory. A one-shot reviewer run is explicitly
// ephemeral even when the adapter supports resume.
func managedNativeSession(request model.Request) bool {
	if request.SessionStorageRoot == "" {
		return false
	}
	if request.EphemeralSession {
		return false
	}
	return true
}
