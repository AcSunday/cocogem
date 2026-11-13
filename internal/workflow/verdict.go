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

const maxEngineeringRounds = 3

type verdictInput struct {
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

func (e *Engine) verdictTools(ctx context.Context, run w.Run, step w.Step) []model.Tool {
	in := effectiveInput(run)
	if in.Workflow != "engineering" && in.Workflow != "research" && in.Workflow != "discussion" {
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
	if !canSubmitReviewVerdict(in, step, steps) {
		return nil
	}
	return []model.Tool{{
		Name:        "submit_review_verdict",
		Description: "Record the structured independent review or convergence verdict for this stage. Choose 'approved' when the upstream work is verified and needs no further rework round (converging the workflow early without wasted turns), or 'changes_requested' when concrete defects, missing evidence, or unresolved disputes require follow-up.",
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

func canSubmitReviewVerdict(in w.RunInput, step w.Step, steps []w.Step) bool {
	idx := slices.IndexFunc(steps, func(s w.Step) bool { return s.ID == step.ID })
	if idx < 0 {
		return false
	}
	switch in.Workflow {
	case "engineering":
		if step.Role == "reviewer" {
			return slices.ContainsFunc(steps[idx+1:], func(s w.Step) bool { return s.Status == "pending" })
		}
		if step.Role == "final_reviewer" {
			return engineeringRoundCount(steps) < maxEngineeringRounds && len(steps)+2 <= maxSteps
		}
	case "research":
		if step.Role == "critic" {
			return slices.ContainsFunc(steps[idx+1:], func(s w.Step) bool { return s.Status == "pending" && s.Role == "researcher" })
		}
	case "discussion":
		if step.Role == "critic" || step.Role == "synthesizer" {
			return slices.ContainsFunc(steps[idx+1:], func(s w.Step) bool { return s.Status == "pending" && s.Role == "checker" })
		}
	}
	return false
}

func engineeringRoundCount(steps []w.Step) int {
	rounds := 0
	for _, s := range steps {
		if s.Role == "engineer" && !s.Converged {
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

	convergedEarly := false
	appendedRound := false
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
		effective := effectiveInput(current)
		if !canSubmitReviewVerdict(effective, step, steps) {
			return errors.New("this step does not accept a review verdict")
		}

		step.ReviewVerdict = in.Verdict
		steps[idx] = step

		switch effective.Workflow {
		case "engineering":
			if step.Role == "reviewer" && in.Verdict == "approved" {
				for i := idx + 1; i < len(steps); i++ {
					if steps[i].Status != "pending" {
						continue
					}
					steps[i].Status = "skipped"
					steps[i].Converged = true
					steps[i].Error = ""
					if err := tx.SaveStep(steps[i]); err != nil {
						return err
					}
					if err := tx.AppendEvent(run.ID, steps[i].ID, steps[i].AgentID, "step_finished", steps[i]); err != nil {
						return err
					}
					convergedEarly = true
				}
			} else if step.Role == "final_reviewer" && in.Verdict == "changes_requested" {
				if engineeringRoundCount(steps) >= maxEngineeringRounds || len(steps)+2 > maxSteps {
					return errors.New("maximum engineering review rounds reached; publish the final engineering_report recording remaining issues and disputes")
				}
				engineerID := effective.Assignments["engineer"]
				var engineer w.Agent
				found := false
				for _, a := range current.Agents {
					if a.ID == engineerID && !a.Disabled {
						engineer = a
						found = true
						break
					}
				}
				if !found {
					return errors.New("assigned engineer is unavailable for another round")
				}
				// Demote this step to an intermediate review so it finishes as a
				// plain critique message instead of forcing premature report publication.
				step.Role = "reviewer"
				priorIDs := []string{}
				for i := 0; i <= idx; i++ {
					if steps[i].Role == "engineer" || steps[i].Role == "reviewer" || steps[i].Role == "final_reviewer" {
						priorIDs = append(priorIDs, steps[i].ID)
					}
				}
				rework := w.Step{
					ID:          w.NewID(),
					RunID:       run.ID,
					AgentID:     engineer.ID,
					AgentName:   engineer.DisplayName,
					Role:        "engineer",
					Instruction: "Read the latest reviewer critique in the handoff and address every remaining defect or failing test. Re-run the relevant tests and do not publish an artifact.",
					DependsOn:   slices.Clone(priorIDs),
					Status:      "pending",
				}
				finalDeps := append(slices.Clone(priorIDs), rework.ID)
				finalReview := w.Step{
					ID:          w.NewID(),
					RunID:       run.ID,
					AgentID:     step.AgentID,
					AgentName:   step.AgentName,
					Role:        "final_reviewer",
					Instruction: "Independently inspect the updated files, diffs, test evidence and prior review rounds in the handoff. Publish exactly one engineering_report with confirmed, disputed or insufficient_evidence findings, severities, exact file/diff/test source IDs and limitations. Never modify files.",
					DependsOn:   finalDeps,
					Status:      "pending",
				}
				if err := tx.SaveStep(rework); err != nil {
					return err
				}
				if err := tx.SaveStep(finalReview); err != nil {
					return err
				}
				appendedRound = true
			}

		case "research":
			if step.Role == "critic" && in.Verdict == "approved" {
				skippedIDs := map[string]bool{}
				for i := idx + 1; i < len(steps); i++ {
					if steps[i].Status != "pending" {
						continue
					}
					if steps[i].Role == "researcher" || steps[i].Role == "critic" {
						steps[i].Status = "skipped"
						steps[i].Converged = true
						steps[i].Error = ""
						skippedIDs[steps[i].ID] = true
						if err := tx.SaveStep(steps[i]); err != nil {
							return err
						}
						if err := tx.AppendEvent(run.ID, steps[i].ID, steps[i].AgentID, "step_finished", steps[i]); err != nil {
							return err
						}
						convergedEarly = true
					} else if steps[i].Role == "coordinator" && len(skippedIDs) > 0 {
						steps[i].DependsOn = slices.DeleteFunc(slices.Clone(steps[i].DependsOn), func(id string) bool {
							return skippedIDs[id]
						})
						if err := tx.SaveStep(steps[i]); err != nil {
							return err
						}
					}
				}
			}

		case "discussion":
			if step.Role == "critic" && in.Verdict == "approved" {
				allCriticsApproved := true
				for _, s := range steps {
					if s.Role == "critic" && s.ReviewVerdict != "approved" {
						allCriticsApproved = false
						break
					}
				}
				if allCriticsApproved {
					for i := idx + 1; i < len(steps); i++ {
						if steps[i].Status != "pending" {
							continue
						}
						if steps[i].Role == "synthesizer" {
							steps[i].Role = "coordinator"
							steps[i].Instruction = "Synthesize every independent view and cross-critique in the handoff and publish one final note artifact directly. Because all critics confirmed convergence without disputed misrepresentations, contributor check rounds are skipped."
							if err := tx.SaveStep(steps[i]); err != nil {
								return err
							}
						} else if steps[i].Role == "checker" || steps[i].Role == "coordinator" {
							steps[i].Status = "skipped"
							steps[i].Converged = true
							steps[i].Error = ""
							if err := tx.SaveStep(steps[i]); err != nil {
								return err
							}
							if err := tx.AppendEvent(run.ID, steps[i].ID, steps[i].AgentID, "step_finished", steps[i]); err != nil {
								return err
							}
							convergedEarly = true
						}
					}
				}
			} else if step.Role == "synthesizer" && in.Verdict == "approved" {
				skippedCheckers := map[string]bool{}
				for i := idx + 1; i < len(steps); i++ {
					if steps[i].Status != "pending" {
						continue
					}
					if steps[i].Role == "checker" {
						steps[i].Status = "skipped"
						steps[i].Converged = true
						steps[i].Error = ""
						skippedCheckers[steps[i].ID] = true
						if err := tx.SaveStep(steps[i]); err != nil {
							return err
						}
						if err := tx.AppendEvent(run.ID, steps[i].ID, steps[i].AgentID, "step_finished", steps[i]); err != nil {
							return err
						}
						convergedEarly = true
					} else if steps[i].Role == "coordinator" && len(skippedCheckers) > 0 {
						steps[i].DependsOn = slices.DeleteFunc(slices.Clone(steps[i].DependsOn), func(id string) bool {
							return skippedCheckers[id]
						})
						if err := tx.SaveStep(steps[i]); err != nil {
							return err
						}
					}
				}
			}
		}

		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.AppendEvent(run.ID, step.ID, step.AgentID, "review_verdict_submitted", map[string]any{
			"verdict":         in.Verdict,
			"summary":         in.Summary,
			"converged_early": convergedEarly,
			"appended_round":  appendedRound,
		})
	})
	if err != nil {
		return "", err
	}
	instruction := "Verdict recorded. Complete your clear user-facing review summary for this step now."
	if convergedEarly {
		instruction = "Verdict recorded and downstream redundant rework rounds skipped. Present your verified review summary clearly now to conclude this stage."
	} else if appendedRound {
		instruction = "Verdict recorded and another rework + review round has been scheduled. State the exact remaining defects and test evidence clearly now; do not call publish_artifact in this step."
	}
	encoded, err := json.Marshal(map[string]any{
		"accepted":        true,
		"verdict":         in.Verdict,
		"converged_early": convergedEarly,
		"appended_round":  appendedRound,
		"instruction":     instruction,
	})
	return string(encoded), err
}
