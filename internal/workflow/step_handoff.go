package workflow

import (
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// prepareStepHandoff freezes the exact upstream inputs a dependent step may
// read. The planning root remains a scheduling dependency only.
func prepareStepHandoff(
	tx *store.Tx,
	run w.Run,
	step w.Step,
	agentID string,
	all []w.Message,
	sourceSnapshot []w.Source,
) (Handoff, error) {
	if len(step.DependsOn) == 0 && len(step.ContextRefs) == 0 {
		return Handoff{}, nil
	}
	artifacts, err := tx.ArtifactsMetadata(run.ThreadID)
	if err != nil {
		return Handoff{}, err
	}
	// The planning root is a scheduling dependency only. Its message and
	// artifacts are withheld from the handoff so a planner that answered
	// the task anyway cannot seed the independent views.
	planningRoot := planningRootStep(run)
	dependentArtifacts := []w.Artifact{}
	for _, artifact := range artifacts {
		if artifact.RunID != run.ID || artifact.StepID == planningRoot || !slices.Contains(step.DependsOn, artifact.StepID) {
			continue
		}
		preview, err := tx.ArtifactPreview(artifact.ID, artifact.Version, 6000)
		if err != nil {
			return Handoff{}, err
		}
		dependentArtifacts = append(dependentArtifacts, preview)
	}
	messages := all
	if planningRoot != "" {
		messages = slices.DeleteFunc(slices.Clone(all), func(m w.Message) bool { return m.StepID == planningRoot })
	}
	handoff := prepareHandoff(step, run.Input.Message, messages, dependentArtifacts, sourceSnapshot)
	if err := addReferencedInputs(tx, step, run.ThreadID, &handoff); err != nil {
		return Handoff{}, err
	}
	if err := sealHandoff(tx, &handoff); err != nil {
		return Handoff{}, err
	}
	if err := tx.AppendEvent(run.ID, step.ID, agentID, "handoff_prepared", handoff); err != nil {
		return Handoff{}, err
	}
	return handoff, nil
}
