package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// discussionCheck is the checker's whole final reply. It is committed as the
// step message, so the final coordinator receives the exact record in its
// handoff; a natural-language claim has no status and is rejected.
type discussionCheck struct {
	Status     string `json:"status"`
	Correction string `json:"correction"`
}

func parseDiscussionCheck(content string) (discussionCheck, error) {
	if len(content) > 4096 {
		return discussionCheck{}, errors.New("checker reply exceeds the 4096-byte receipt budget")
	}
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "```"))
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var check discussionCheck
	if err := dec.Decode(&check); err != nil {
		return discussionCheck{}, fmt.Errorf("checker reply must be one JSON object {status, correction}: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return discussionCheck{}, errors.New("checker reply must contain only the JSON object, with no trailing content")
	}
	switch check.Status {
	case "confirmed":
	case "disputed":
		if strings.TrimSpace(check.Correction) == "" {
			return discussionCheck{}, errors.New("a disputed check requires a correction")
		}
	default:
		return discussionCheck{}, fmt.Errorf("checker status %q must be confirmed or disputed", check.Status)
	}
	return check, nil
}

// discussionCheckRecord validates every committed checker receipt in the final
// handoff and renders the corrections the coordinator must consume. Disputed
// is a valid outcome: it stays in the handoff and the run continues.
func (e *Engine) discussionCheckRecord(ctx context.Context, run w.Run, step w.Step, handoff Handoff) (string, error) {
	roles := map[string]string{}
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, s := range steps {
			roles[s.ID] = s.Role
		}
		return nil
	}); err != nil {
		return "", err
	}
	want := 0
	for _, id := range step.DependsOn {
		if roles[id] == "checker" {
			want++
		}
	}
	lines := []string{}
	for _, message := range handoff.Messages {
		if roles[message.StepID] != "checker" {
			continue
		}
		check, err := parseDiscussionCheck(message.Content)
		if err != nil {
			return "", &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: fmt.Sprintf("invalid contributor check from step %s: %v", message.StepID, err)}
		}
		lines = append(lines, fmt.Sprintf("- %s (step %s): status=%s correction=%q", message.AgentName, message.StepID, check.Status, check.Correction))
	}
	if len(lines) != want {
		return "", &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: fmt.Sprintf("final delivery received %d valid contributor checks, want %d", len(lines), want)}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "\nContributor check records (parsed from the exact upstream handoff; the raw JSON messages above are the source):\n" + strings.Join(lines, "\n") + "\nApply every correction. Keep every disputed item visible as a genuine disagreement; agreement or confirmation is not source evidence.\n", nil
}
