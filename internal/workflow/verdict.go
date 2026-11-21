package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// maxReworkRounds is the hard anti-ping-pong bound: the same reviewer may ask
// the same author for changes at most twice before the disagreement must be
// delivered as-is (or escalated to the coordinator / ask_question).
const maxReworkRounds = 2

type verdictInput struct {
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

// verdictTools offers the structured review verdict to independent review
// steps only. A responder or coordinator never reviews itself.
func (e *Engine) verdictTools(ctx context.Context, run w.Run, step w.Step) []model.Tool {
	if step.Role != "reviewer" && step.Role != "critic" {
		return nil
	}
	if len(step.DependsOn) == 0 && step.FromStepID == "" {
		return nil
	}
	var steps []w.Step
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var readErr error
		steps, readErr = tx.Steps(run.ID)
		return readErr
	}); err != nil {
		return nil
	}
	if !canSubmitReviewVerdict(step, steps) {
		return nil
	}
	return []model.Tool{{
		Name:        "submit_review_verdict",
		Description: "Record the structured independent review verdict. Choose 'approved' when the reviewed work is verified and needs no further rework, or 'changes_requested' when concrete defects or missing evidence require one more bounded repair round. After two changes_requested rounds on the same issue the host stops further rework; deliver the remaining issues and disagreements honestly instead.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []string{"approved", "changes_requested"},
					"description": "'approved' when verified with no blocking objection; 'changes_requested' when concrete rework or verification is still needed.",
				},
				"summary": map[string]any{
					"type":        "string",
					"minLength":   1,
					"maxLength":   2000,
					"description": "Concise factual basis for this verdict.",
				},
			},
			"required":             []string{"verdict", "summary"},
			"additionalProperties": false,
		},
	}}
}

func canSubmitReviewVerdict(step w.Step, steps []w.Step) bool {
	if step.Role != "reviewer" && step.Role != "critic" {
		return false
	}
	return slices.ContainsFunc(steps, func(s w.Step) bool { return s.ID == step.ID })
}

// reviewRoundCount counts the changes_requested verdicts one reviewer already
// issued against one author in this run.
func reviewRoundCount(steps []w.Step, reviewerID, authorID string) int {
	rounds := 0
	for _, s := range steps {
		if s.AgentID == reviewerID && s.FromAgentID == authorID && s.ReviewVerdict == "changes_requested" {
			rounds++
		}
	}
	return rounds
}

func (e *Engine) submitReviewVerdict(ctx context.Context, run w.Run, sender w.Step, raw json.RawMessage) (string, error) {
	var in verdictInput
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	in.Verdict = strings.TrimSpace(in.Verdict)
	in.Summary = strings.TrimSpace(in.Summary)
	if in.Verdict != "approved" && in.Verdict != "changes_requested" {
		return "", errors.New("verdict must be approved or changes_requested")
	}
	if in.Summary == "" || utf8.RuneCountInString(in.Summary) > 2000 {
		return "", errors.New("summary must be 1 to 2000 characters")
	}

	reachedLimit := false
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return errors.New("run is no longer active")
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(steps, func(s w.Step) bool { return s.ID == sender.ID })
		if idx < 0 || steps[idx].Status != "running" {
			return errors.New("reviewing step is no longer running")
		}
		step := steps[idx]
		if step.ReviewVerdict != "" {
			if step.ReviewVerdict == in.Verdict {
				return nil
			}
			return errors.New("a different review verdict was already recorded for this step")
		}
		if in.Verdict == "changes_requested" && reviewRoundCount(steps, step.AgentID, step.FromAgentID) >= maxReworkRounds {
			// Record the disagreement honestly instead of rewriting it to approved;
			// the anti-ping-pong valve in collaboration.go blocks a 3rd rework relay.
			reachedLimit = true
			in.Summary += "（已达 2 轮返工上限，保留分歧按现状交付）"
		}
		step.ReviewVerdict = in.Verdict
		steps[idx] = step
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.AppendEvent(run.ID, step.ID, step.AgentID, "review_verdict_submitted", map[string]any{
			"verdict":       in.Verdict,
			"summary":       in.Summary,
			"rework_limit":  reachedLimit,
			"rounds_before": reviewRoundCount(steps[:idx], step.AgentID, step.FromAgentID),
		})
	})
	if err != nil {
		return "", err
	}
	instruction := "Verdict recorded. Complete your clear user-facing review summary for this step now."
	if reachedLimit {
		instruction = "The two-round rework limit is reached. Record the remaining defects and disagreements in your final reply, do not request another rework round, and deliver the current state honestly."
	} else if in.Verdict == "approved" {
		instruction = "Verdict recorded as approved. Present your verified review summary clearly now to conclude this stage."
	}
	encoded, err := json.Marshal(map[string]any{
		"accepted":     true,
		"verdict":      in.Verdict,
		"rework_limit": reachedLimit,
		"instruction":  instruction,
	})
	return string(encoded), err
}
