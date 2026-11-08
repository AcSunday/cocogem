package project

import (
	"slices"
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// externalSubjectDigest resolves browser/provider supplied references against
// the task's own immutable thread records. The caller's digest is deliberately
// ignored: candidates must resolve to exact artifacts or successful task
// deliveries, each proved by task identity + successful run + exact version.
func externalSubjectDigest(tx *store.Tx, view TaskView, in RecordCompletionInput) (string, error) {
	candidates := make([]any, 0, len(in.CandidateRefs))
	seenCandidates := map[string]bool{}
	for _, ref := range in.CandidateRefs {
		if seenCandidates[ref] {
			return "", errorf("invalid_request", "duplicate candidate reference")
		}
		seenCandidates[ref] = true
		if strings.HasPrefix(ref, "message:") {
			candidate, err := completionMessage(tx, view.Task, ref)
			if err != nil {
				return "", err
			}
			candidates = append(candidates, candidate)
			continue
		}
		artifact, err := completionArtifact(tx, view.Task, ref)
		if err != nil {
			return "", err
		}
		candidates = append(candidates, artifact)
	}
	seenEvidence := map[string]bool{}
	for _, result := range in.Results {
		for _, ref := range result.Evidence {
			if seenEvidence[ref] {
				continue
			}
			seenEvidence[ref] = true
			if strings.HasPrefix(ref, "message:") {
				if _, err := completionMessage(tx, view.Task, ref); err != nil {
					return "", err
				}
				continue
			}
			if strings.HasPrefix(ref, "source:") {
				// New human acceptance uses only exact delivered artifacts and
				// messages. Captured sources remain part of the shared source/
				// sidebar/download infrastructure and old records stay readable, but
				// they are no longer offered or accepted as new acceptance evidence.
				return "", errorf("invalid_request", "new acceptance evidence must reference an exact delivered artifact version or delivery message; captured sources are historical only")
			}
			if _, err := completionArtifact(tx, view.Task, ref); err != nil {
				return "", err
			}
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	return Digest(candidates), nil
}

// Only an exact final delivery from this task is an acceptance candidate.
// Discussion before promotion and partial/failed attempts remain history.
func completionMessage(tx *store.Tx, task w.Task, ref string) (any, error) {
	id := strings.TrimPrefix(ref, "message:")
	message, err := tx.MessageMeta(id)
	if err != nil || message.ThreadID != task.ThreadID || message.Status != "completed" || message.Role != "assistant" {
		return nil, errorf("invalid_request", "candidate is not a completed task reply")
	}
	run, err := tx.Run(message.RunID)
	if err != nil || run.TaskID != task.ID || run.Status != "succeeded" || run.Delivery == nil || !slices.Contains(run.Delivery.MessageIDs, id) {
		return nil, errorf("invalid_request", "candidate is not an exact final delivery of this task")
	}
	digest, _, _, err := tx.MessageDigest(id)
	if err != nil {
		return nil, err
	}
	return struct {
		ID     string `json:"message_id"`
		Digest string `json:"content_digest"`
	}{id, digest}, nil
}

// completionArtifact proves that an exact artifact version was delivered by a
// successful run of this task. Checking only the thread would accept artifacts
// from before the task existed, from another task in the same thread, from a
// failed run, or an undelivered version — all of which are rejected here.
func completionArtifact(tx *store.Tx, task w.Task, ref string) (w.Artifact, error) {
	if !strings.HasPrefix(ref, "artifact:") {
		return w.Artifact{}, errorf("invalid_request", "external completion requires an exact artifact or source reference")
	}
	identity := strings.TrimPrefix(ref, "artifact:")
	id, versionText, found := strings.Cut(identity, "@v")
	version, err := strconv.Atoi(versionText)
	if !found || id == "" || versionText == "" || err != nil || version < 1 || strings.Contains(versionText, "+") {
		return w.Artifact{}, errorf("invalid_request", "artifact reference must be artifact:<id>@v<positive-version>")
	}
	artifact, err := tx.Artifact(id, version)
	if err != nil || artifact.ThreadID != task.ThreadID {
		return w.Artifact{}, errorf("invalid_request", "candidate artifact does not belong to the task thread")
	}
	run, err := tx.Run(artifact.RunID)
	if err != nil || run.TaskID != task.ID {
		return w.Artifact{}, errorf("invalid_request", "candidate artifact was not produced by this task")
	}
	if run.Status != "succeeded" || run.Delivery == nil {
		return w.Artifact{}, errorf("invalid_request", "candidate artifact was not produced by a successful run of this task")
	}
	if !slices.Contains(run.Delivery.ArtifactRefs, w.ArtifactRef{ID: artifact.ID, Version: artifact.Version}) {
		return w.Artifact{}, errorf("invalid_request", "candidate artifact version was not delivered by this task")
	}
	return artifact, nil
}

// DeliveryCandidates lists the acceptance candidates of the current task: exact
// artifact versions and exact delivery messages, each proved by a successful run
// of this task. Old-generation, other-task and failed-run results never appear.
func (s *Service) DeliveryCandidates(tx *store.Tx, task w.Task) ([]w.Artifact, []w.Message, error) {
	deliveries, err := s.deliveriesInTx(tx, task)
	if err != nil {
		return nil, nil, err
	}
	return deliveries.Artifacts, deliveries.Messages, nil
}
