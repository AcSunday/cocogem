package project

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// derivedMessageDisclosure combines explicit evidence with everything the host
// can prove was exposed to the producing attempt. A body without refs is not a
// clean-room declaration: the attempt/session binding is the disclosure input.
func derivedMessageDisclosure(tx *store.Tx, sender w.Task, in SendTaskMessageInput, refs []string) (string, string, error) {
	materials, err := claimSources(tx, sender.WorkspaceID, refs)
	if err != nil {
		return "", "", err
	}
	if in.SenderAttemptID == "" {
		// No producing attempt means this is host-submitted input, not a claim
		// that a model-generated body had a clean room. Explicit references still
		// constrain it; an otherwise direct user question carries no derivation.
		if len(refs) == 0 {
			visibility, exposure := w.DisclosureForSources([]w.Source{{ID: "host_input", Visibility: "project"}})
			return visibility, exposure, nil
		}
		visibility, exposure := w.DisclosureForSources(materials)
		return visibility, exposure, nil
	}
	if sender.TransferRequestID != "" {
		return "", "", errorf("authority_revoked", "owner transfer freezes derived sends from the old owner")
	}
	attempt, err := tx.Attempt(in.SenderAttemptID)
	if err != nil || attempt.ExecutionSessionRef == "" {
		return "", "", errorf("disclosure_denied", "sender attempt has no verifiable execution exposure")
	}
	run, err := tx.Run(attempt.RunID)
	if err != nil || run.TaskID != sender.ID || run.TaskRevision != sender.CurrentRevision {
		return "", "", errorf("disclosure_denied", "sender attempt does not belong to the current sender task revision")
	}
	steps, err := tx.Steps(run.ID)
	if err != nil {
		return "", "", err
	}
	producerMemberID := ""
	for _, step := range steps {
		if step.ID == attempt.StepID && step.RunID == run.ID {
			producerMemberID = step.AgentID
		}
	}
	if producerMemberID == "" {
		return "", "", errorf("disclosure_denied", "sender attempt identity does not match a run participant")
	}
	session, err := tx.Session(attempt.ExecutionSessionRef)
	if err != nil || session.TaskID != sender.ID || session.MemberID != producerMemberID {
		return "", "", errorf("disclosure_denied", "sender session does not match the producing run participant")
	}
	if session.Status == "revoked" || session.Status == "quarantined" {
		return "", "", errorf("authority_revoked", "sender session exposure has been revoked")
	}
	// The live global permission epoch is the only current authority boundary.
	// Retired workspace grants are history and cannot keep an old exposure valid.
	settings, err := tx.Settings()
	if err != nil {
		return "", "", err
	}
	currentEpoch := max(1, settings.PermissionEpoch)
	if currentEpoch != session.VisibilityEpoch {
		return "", "", errorf("authority_revoked", "sender session visibility epoch is no longer current")
	}
	if session.ExposureDigest != exposureRefsDigest(session.ExposedRefs) {
		return "", "", errorf("disclosure_denied", "sender session exposure proof is inconsistent")
	}
	materials = append(materials, exposureMaterials(tx, sender, attempt, run, session)...)
	visibility, exposure := w.DisclosureForSources(materials)
	return visibility, exposure, nil
}

func exposureRefsDigest(refs []string) string {
	sum := sha256.Sum256([]byte(strings.Join(refs, "\n")))
	return hex.EncodeToString(sum[:])
}

func exposureMaterials(tx *store.Tx, sender w.Task, attempt w.Attempt, run w.Run, session w.ExecutionSession) []w.Source {
	materials := []w.Source{}
	unknown := false
	var manifest *w.ContextManifest
	contextItems := []w.ContextItem{}
	exposedContextItems := map[string]bool{}
	if run.ContextRef != "" {
		exact, err := tx.ContextManifest(run.ContextRef)
		if err != nil || exact.TaskID != sender.ID || exact.TaskRevision != run.TaskRevision {
			return append(materials, unknownExposure("invalid-context-exposure"))
		}
		manifest = &exact
		contextItems = append(contextItems, exact.Items...)
		appended, err := tx.ContextScopeEntries(exact.ID)
		if err != nil {
			return append(materials, unknownExposure("invalid-context-exposure"))
		}
		contextItems = append(contextItems, appended...)
		for _, item := range contextItems {
			exposedContextItems[item.Ref+":"+item.Digest] = true
		}
	}
	for _, id := range attempt.SourceIDs {
		source, err := tx.Source(id)
		if err != nil || source.WorkspaceID != sender.WorkspaceID {
			unknown = true
			continue
		}
		materials = append(materials, source)
	}
	for _, exposed := range session.ExposedRefs {
		switch {
		case strings.HasPrefix(exposed, "source_id:"):
			parts := strings.Split(exposed, ":")
			if len(parts) != 3 {
				unknown = true
				continue
			}
			source, err := tx.Source(parts[1])
			if err != nil || source.WorkspaceID != sender.WorkspaceID || capturedSourceDigest(source) != parts[2] {
				unknown = true
				continue
			}
			materials = append(materials, source)
		case strings.HasPrefix(exposed, "message:"):
			parts := strings.Split(exposed, ":")
			if len(parts) != 3 {
				unknown = true
				continue
			}
			material, ok := messageExposureMaterial(tx, sender.WorkspaceID, parts[1], parts[2])
			if !ok {
				unknown = true
				continue
			}
			materials = append(materials, material)
		case strings.HasPrefix(exposed, "artifact:"):
			ref, digest, ok := strings.Cut(strings.TrimPrefix(exposed, "artifact:"), ":")
			if !ok {
				unknown = true
				continue
			}
			derived, ok := artifactExposureMaterials(tx, sender.WorkspaceID, ref, digest)
			if !ok {
				unknown = true
				continue
			}
			materials = append(materials, derived...)
		case strings.HasPrefix(exposed, "context:"):
			if manifest == nil || exposed != "context:"+manifest.ID+":"+manifest.ScopeDigest {
				unknown = true
			}
		default:
			// Startup entries are recorded as ref:digest. Their disclosure is
			// resolved from the authenticated manifest below, never guessed from
			// the reference prefix or from the member's identity.
			if !exposedContextItems[exposed] {
				unknown = true
			}
		}
	}
	if manifest == nil {
		if unknown || len(session.ExposedRefs) == 0 {
			materials = append(materials, unknownExposure("unproven-legacy-exposure"))
		}
		return materials
	}
	for _, item := range contextItems {
		switch item.Kind {
		case "task", "work":
			visibility := item.Visibility
			if visibility == "" {
				visibility = "task_chain"
			}
			materials = append(materials, w.Source{ID: item.Ref, Visibility: visibility, ContentDigest: item.Digest})
		case "source":
			source, err := tx.Source(item.Ref)
			if err != nil || source.WorkspaceID != sender.WorkspaceID || capturedSourceDigest(source) != item.Digest {
				unknown = true
				continue
			}
			materials = append(materials, source)
		case "project", "memory", "claim", "dataset":
			material, ok := derivedContextMaterial(tx, sender.WorkspaceID, item)
			if !ok {
				unknown = true
				continue
			}
			materials = append(materials, material)
		default:
			unknown = true
		}
	}
	if unknown {
		materials = append(materials, unknownExposure("unproven-context-exposure"))
	}
	return materials
}

func capturedSourceDigest(source w.Source) string {
	if source.ContentDigest != "" {
		return source.ContentDigest
	}
	sum := sha256.Sum256([]byte(source.Content))
	return hex.EncodeToString(sum[:])
}

func messageExposureMaterial(tx *store.Tx, workspaceID, id, expectedDigest string) (w.Source, bool) {
	message, err := tx.Message(id)
	if err != nil {
		return w.Source{}, false
	}
	digest, _, _, err := tx.MessageDigest(id)
	if err != nil || digest != expectedDigest {
		return w.Source{}, false
	}
	material, _, ok := originalAttemptExposure(tx, workspaceID, message.RunID, message.StepID, message.AttemptID)
	return material, ok
}

func artifactExposureMaterials(tx *store.Tx, workspaceID, ref, expectedDigest string) ([]w.Source, bool) {
	id, rawVersion, ok := strings.Cut(ref, "@")
	version, err := strconv.Atoi(rawVersion)
	if !ok || err != nil || version < 1 {
		return nil, false
	}
	artifact, err := tx.Artifact(id, version)
	if err != nil {
		return nil, false
	}
	materials := []w.Source{{ID: "artifact:" + ref, ContentDigest: expectedDigest, Visibility: artifact.Visibility, ExposureDigest: artifact.ExposureDigest}}
	sources, err := claimSources(tx, workspaceID, artifact.SourceIDs)
	if err != nil {
		return nil, false
	}
	materials = append(materials, sources...)
	producer, _, ok := originalAttemptExposure(tx, workspaceID, artifact.RunID, artifact.StepID, "")
	if !ok {
		return nil, false
	}
	return append(materials, producer), true
}

// originalAttemptExposure attributes a committed message or artifact to the
// exact producing run and step. A project-visible label cannot erase a private
// exposure inherited from its producer.
func originalAttemptExposure(tx *store.Tx, workspaceID, runID, stepID, attemptID string) (w.Source, string, bool) {
	run, err := tx.Run(runID)
	if err != nil || run.WorkspaceID != workspaceID || run.TaskID == "" {
		return w.Source{}, "", false
	}
	attempts, err := tx.StepAttempts(stepID)
	if err != nil {
		return w.Source{}, "", false
	}
	matches := []w.Attempt{}
	for _, candidate := range attempts {
		if candidate.RunID != runID || candidate.ExecutionSessionRef == "" || attemptID != "" && candidate.ID != attemptID {
			continue
		}
		matches = append(matches, candidate)
	}
	if len(matches) != 1 {
		return w.Source{}, "", false
	}
	producer, ok, err := producerExposure(tx, workspaceID, run.TaskID, matches[0].ID)
	return producer, matches[0].ID, ok && err == nil
}

func derivedContextMaterial(tx *store.Tx, workspaceID string, item w.ContextItem) (w.Source, bool) {
	id, version, ok := strings.Cut(item.Ref, "@")
	if !ok {
		return w.Source{}, false
	}
	revision, err := strconv.Atoi(version)
	if err != nil || revision < 1 {
		return w.Source{}, false
	}
	material := w.Source{ID: item.Ref, ContentDigest: item.Digest}
	switch item.Kind {
	case "project":
		record, err := tx.ProjectRecord(id, revision)
		if err != nil || record.WorkspaceID != workspaceID {
			return w.Source{}, false
		}
		material.Visibility, material.ExposureDigest = record.Visibility, record.ExposureDigest
	case "memory":
		memories, err := tx.Memories(workspaceID)
		if err != nil {
			return w.Source{}, false
		}
		found := false
		for _, memory := range memories {
			if memory.ID == id && memory.Revision == revision {
				material.Visibility, material.ExposureDigest, found = memory.Visibility, memory.ExposureDigest, true
			}
		}
		if !found {
			return w.Source{}, false
		}
	case "claim":
		// ContextItem does not carry the owning task; the workspace-wide lookup
		// below avoids trusting a model-provided task ID.
		tasks, err := tx.Tasks(workspaceID)
		if err != nil {
			return w.Source{}, false
		}
		found := false
		for _, task := range tasks {
			claims, err := tx.Claims(task.ID)
			if err != nil {
				return w.Source{}, false
			}
			for _, claim := range claims {
				if claim.ID == id && claim.Revision == revision {
					material.Visibility, material.ExposureDigest, found = claim.Visibility, claim.ExposureDigest, true
				}
			}
		}
		if !found {
			return w.Source{}, false
		}
	case "dataset":
		tasks, err := tx.Tasks(workspaceID)
		if err != nil {
			return w.Source{}, false
		}
		found := false
		for _, task := range tasks {
			datasets, err := tx.Datasets(task.ID)
			if err != nil {
				return w.Source{}, false
			}
			for _, dataset := range datasets {
				if dataset.ID == id && dataset.Revision == revision {
					material.Visibility, material.ExposureDigest, found = dataset.Visibility, dataset.ExposureDigest, true
				}
			}
		}
		if !found {
			return w.Source{}, false
		}
	default:
		return w.Source{}, false
	}
	if material.Visibility == "" {
		material.Visibility = "private"
	}
	return material, true
}

func unknownExposure(id string) w.Source {
	return w.Source{ID: fmt.Sprintf("unknown:%s", id), Visibility: "private", ContentDigest: "unknown"}
}

// producerExposure folds an attempt's complete exposure into any derived
// record, including records whose author omitted explicit source references.
func producerExposure(tx *store.Tx, workspaceID, taskID, attemptID string) (w.Source, bool, error) {
	if attemptID == "" {
		return w.Source{}, false, nil
	}
	if taskID == "" {
		attempt, err := tx.Attempt(attemptID)
		if err != nil {
			return w.Source{}, false, errorf("disclosure_denied", "producer attempt is unavailable")
		}
		run, err := tx.Run(attempt.RunID)
		if err != nil {
			return w.Source{}, false, err
		}
		taskID = run.TaskID
	}
	task, err := tx.Task(taskID)
	if err != nil || task.WorkspaceID != workspaceID {
		return w.Source{}, false, errorf("disclosure_denied", "producer attempt task is outside this workspace")
	}
	visibility, exposure, err := derivedMessageDisclosure(tx, task, SendTaskMessageInput{
		SenderTaskID: task.ID, SenderAttemptID: attemptID,
	}, nil)
	if err != nil {
		return w.Source{}, false, err
	}
	return w.Source{ID: "attempt:" + attemptID, Visibility: visibility, ExposureDigest: exposure}, true, nil
}

// InheritAttemptDisclosure is used by artifact publication so explicit citations
// cannot launder private manifest, memory or later paged-read exposure.
func InheritAttemptDisclosure(tx *store.Tx, run w.Run, attemptID string, sources []w.Source) (string, string, error) {
	materials := slices.Clone(sources)
	if run.TaskID != "" && attemptID != "" {
		producer, ok, err := producerExposure(tx, run.WorkspaceID, run.TaskID, attemptID)
		if err != nil {
			return "", "", err
		}
		if ok {
			materials = append(materials, producer)
		}
	}
	visibility, exposure := w.DisclosureForSources(materials)
	return visibility, exposure, nil
}
