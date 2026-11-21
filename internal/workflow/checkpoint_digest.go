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

// continuationCheckpoint is the unified Continuity Capsule (<=1.5K tokens)
// injected whenever a member starts a fresh session to continue existing work:
// RetryStep checkpoint recovery, Stop-corrupted session self-healing, context
// overflow rollover, or promote_to_task work-root transitions.
//
// Only this capsule itself is bounded (~4.5 KB / <=1.5K tokens); overall
// conversation context remains unbounded via read_context / read_source / files.
type continuationCheckpoint struct {
	Goal      string   `json:"goal,omitempty"`
	WorkRoot  string   `json:"work_root,omitempty"`
	Changed   []string `json:"changed_files,omitempty"`
	Stat      string   `json:"diff_stat,omitempty"`
	Tools     []string `json:"recent_tool_calls,omitempty"`
	DraftTail string   `json:"unfinished_draft_tail,omitempty"`
	Reason    string   `json:"interruption_reason,omitempty"`
	Hint      string   `json:"paging_hint,omitempty"`
}

const maxContinuityCapsuleBytes = 4500

// buildCheckpointDigest builds the unified Continuity Capsule across both code
// and non-code runs, supporting RetryStep (new step ID via RetryOfStepID),
// Resume, Stop self-healing and context-overflow rollover.
func (e *Engine) buildCheckpointDigest(ctx context.Context, run w.Run, step w.Step, previous *w.Attempt, previousDraft string) (string, error) {
	workRoot := run.ProjectRoot()
	digest := continuationCheckpoint{
		Goal:     boundedRunes(strings.TrimSpace(run.Input.Message), 300),
		WorkRoot: workRoot,
		Hint:     "Use read_context, read_source, or read_file to inspect full history or files on demand.",
	}
	if previous != nil {
		digest.Reason = boundedRunes(diagnosticText(strings.TrimSpace(previous.Error)), 300)
	}
	if tail := strings.TrimSpace(previousDraft); tail != "" {
		runes := []rune(tail)
		if len(runes) > 400 {
			tail = "…" + string(runes[len(runes)-400:])
		}
		digest.DraftTail = tail
	}
	// Resolve baseline ref when a Candidate worktree exists so agent commits
	// inside the candidate are still reflected in changed_files / diff_stat.
	baseRef := "HEAD"
	if run.ExecutionRef != "" {
		_ = e.Store.Read(ctx, func(tx *store.Tx) error {
			if execRec, err := tx.Execution(run.ExecutionRef); err == nil {
				if execRec.BaseSnapshotOID != "" {
					baseRef = execRec.BaseSnapshotOID
				} else if execRec.BaseRevision != "" {
					baseRef = execRec.BaseRevision
				}
			}
			return nil
		})
	}
	if workRoot != "" {
		if changed, stat, err := changedFilesSince(ctx, workRoot, baseRef); err == nil {
			if len(changed) > 30 {
				changed = changed[:30]
			}
			digest.Changed = changed
			digest.Stat = boundedRunes(strings.TrimSpace(stat), 800)
		}
	}
	// Inspect tool receipts from either the current step OR the retried source step.
	sourceRunID := run.ID
	sourceStepID := step.ID
	if step.RetryOfStepID != "" && run.ContinuedFromRun != "" {
		sourceRunID = run.ContinuedFromRun
		sourceStepID = step.RetryOfStepID
	}
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		if run.TaskID != "" {
			if task, err := tx.Task(run.TaskID); err == nil {
				if rev, err := tx.TaskRevision(task.ID, task.CurrentRevision); err == nil && strings.TrimSpace(rev.Goal) != "" {
					digest.Goal = boundedRunes(strings.TrimSpace(rev.Goal), 300)
				}
			}
		}
		events, err := tx.Events(sourceRunID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.StepID != sourceStepID || event.Type != "tool_finished" {
				continue
			}
			digest.Tools = append(digest.Tools, boundedRunes(string(event.Data), 180))
		}
		if len(digest.Tools) > 8 {
			digest.Tools = digest.Tools[len(digest.Tools)-8:]
		}
		// If RetryStep had no `previous` attempt on the new step ID, pull the
		// failure reason from the retried step's last attempt.
		if digest.Reason == "" && sourceStepID != step.ID {
			if attempts, err := tx.StepAttempts(sourceStepID); err == nil && len(attempts) > 0 {
				digest.Reason = boundedRunes(diagnosticText(strings.TrimSpace(attempts[len(attempts)-1].Error)), 300)
			}
		}
		return nil
	}); err != nil {
		return "", err
	}
	if len(digest.Changed) == 0 && len(digest.Tools) == 0 && digest.Reason == "" && digest.DraftTail == "" {
		return "", nil
	}
	encoded, err := json.Marshal(digest)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxContinuityCapsuleBytes {
		digest.Stat = boundedRunes(digest.Stat, 300)
		digest.DraftTail = boundedRunes(digest.DraftTail, 200)
		if len(digest.Tools) > 4 {
			digest.Tools = digest.Tools[len(digest.Tools)-4:]
		}
		encoded, _ = json.Marshal(digest)
	}
	return fmt.Sprintf("Continuity capsule (the host captured this state; verify existing files before continuing and do not restart from scratch):\n%s\n", encoded), nil
}

func boundedRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// changedFilesSince lists changed paths and a bounded diff stat relative to baseRef.
func changedFilesSince(ctx context.Context, root, baseRef string) ([]string, string, error) {
	if baseRef == "" {
		baseRef = "HEAD"
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		names, err := gitOutput(ctx, root, "diff", "--name-only", baseRef)
		if err != nil {
			return nil, "", err
		}
		stat, _ := gitOutput(ctx, root, "diff", "--stat", baseRef)
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
