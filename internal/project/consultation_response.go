package project

import (
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// ValidateConsultationResponse rechecks the actual producing attempt, including
// reads made after startup, against the requester's current task audience.
func (s *Service) ValidateConsultationResponse(tx *store.Tx, run w.Run, responseRef string) error {
	if run.TaskID == "" || responseRef == "" {
		return nil
	}
	consultation, err := tx.Task(run.TaskID)
	if err != nil {
		return err
	}
	if consultation.Kind != "consultation" {
		return nil
	}
	material, ok := consultationResponseMaterial(tx, run, responseRef)
	if !ok {
		return errorf("disclosure_denied", "consultation response is not an exact committed delivery with verifiable producing exposure")
	}
	requester, err := tx.Task(consultation.ParentID)
	if err != nil {
		return errorf("disclosure_denied", "consultation requester is unavailable")
	}
	switch material.Visibility {
	case "private":
		return errorf("disclosure_denied", "consultation response inherited private work that cannot cross tasks")
	case "role":
		if requester.OwnerMemberID == "" || requester.OwnerMemberID != consultation.OwnerMemberID {
			return errorf("disclosure_denied", "consultation response inherited role-scoped work for another member")
		}
	case "task_chain":
		if requester.CollaborationScopeID != consultation.CollaborationScopeID {
			return errorf("disclosure_denied", "consultation response inherited an unrelated task chain")
		}
	case "project", "project-visible":
		return nil
	default:
		return errorf("disclosure_denied", "consultation response has unknown disclosure scope")
	}
	return nil
}

func consultationResponseMaterial(tx *store.Tx, run w.Run, responseRef string) (w.Source, bool) {
	if strings.HasPrefix(responseRef, "artifact:") {
		id, rawVersion, ok := strings.Cut(strings.TrimPrefix(responseRef, "artifact:"), "@")
		version, err := strconv.Atoi(rawVersion)
		if !ok || err != nil || version < 1 {
			return w.Source{}, false
		}
		delivered := false
		if run.Delivery != nil {
			for _, ref := range run.Delivery.ArtifactRefs {
				if ref.ID == id && ref.Version == version {
					delivered = true
				}
			}
		}
		artifact, err := tx.Artifact(id, version)
		if !delivered || err != nil || artifact.RunID != run.ID {
			return w.Source{}, false
		}
		producer, _, ok := originalAttemptExposure(tx, run.WorkspaceID, run.ID, artifact.StepID, "")
		if !ok {
			return w.Source{}, false
		}
		sources, err := claimSources(tx, run.WorkspaceID, artifact.SourceIDs)
		if err != nil {
			return w.Source{}, false
		}
		materials := append([]w.Source{{ID: responseRef, Visibility: artifact.Visibility, ExposureDigest: artifact.ExposureDigest}, producer}, sources...)
		visibility, exposure := w.DisclosureForSources(materials)
		return w.Source{ID: responseRef, Visibility: visibility, ExposureDigest: exposure}, true
	}
	message, err := tx.Message(responseRef)
	delivered := false
	if run.Delivery != nil {
		for _, id := range run.Delivery.MessageIDs {
			if id == responseRef {
				delivered = true
			}
		}
	}
	if err != nil || !delivered || message.RunID != run.ID || message.Status != "completed" {
		return w.Source{}, false
	}
	material, _, ok := originalAttemptExposure(tx, run.WorkspaceID, run.ID, message.StepID, message.AttemptID)
	return material, ok
}
