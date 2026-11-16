package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// checkpointDigest is the on-disk state a continuing member must build on
// instead of restarting: the candidate's changed files, the last completed tool
// calls and the reason the previous attempt stopped.
//
// It is injected only when the native session cannot be resumed, so a resumed
// session keeps its own richer history.
type continuationCheckpoint struct {
	WorkRoot string   `json:"work_root,omitempty"`
	Changed  []string `json:"changed_files,omitempty"`
	Stat     string   `json:"diff_stat,omitempty"`
	Tools    []string `json:"recent_tool_calls,omitempty"`
	Reason   string   `json:"interruption_reason,omitempty"`
}

// buildCheckpointDigest inspects the current candidate state and the previous
// attempt's receipts. It never runs project code and never trusts a deleted or
// escaping path.
func (e *Engine) buildCheckpointDigest(ctx context.Context, run w.Run, step w.Step, previous *w.Attempt) (string, error) {
	if run.ExecutionRoot == "" {
		return "", nil
	}
	digest := continuationCheckpoint{WorkRoot: run.ExecutionRoot}
	if previous != nil {
		digest.Reason = diagnosticText(strings.TrimSpace(previous.Error))
	}
	// The candidate's changed set is host-observed state, not a model claim.
	if changed, stat, err := changedFiles(ctx, run.ExecutionRoot); err == nil {
		digest.Changed, digest.Stat = changed, stat
	}
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.StepID != step.ID || event.Type != "tool_finished" || len(digest.Tools) >= 10 {
				continue
			}
			digest.Tools = append(digest.Tools, string(event.Data))
		}
		return nil
	}); err != nil {
		return "", err
	}
	if len(digest.Changed) == 0 && len(digest.Tools) == 0 && digest.Reason == "" {
		return "", nil
	}
	encoded, err := json.Marshal(digest)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Checkpoint continuity (the host captured this state; verify the files before continuing, and do not restart from scratch):\n%s\n", encoded), nil
}

// changedFiles lists the candidate's changed paths and a bounded diff stat.
func changedFiles(ctx context.Context, root string) ([]string, string, error) {
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		names, err := gitOutput(ctx, root, "diff", "--name-only", "HEAD")
		if err != nil {
			return nil, "", err
		}
		stat, _ := gitOutput(ctx, root, "diff", "--stat", "HEAD")
		untracked, _ := gitOutput(ctx, root, "ls-files", "--others", "--exclude-standard")
		all := append(splitLines(names), splitLines(untracked)...)
		return uniqueStrings(all), stat, nil
	}
	return nil, "", nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	output, err := cmd.Output()
	return string(output), err
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func splitLines(value string) []string {
	out := []string{}
	for _, line := range strings.Split(value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
