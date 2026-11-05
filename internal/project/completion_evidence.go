package project

import (
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// externalSubjectDigest resolves browser/provider supplied references against
// the task's own immutable thread records. The caller's digest is deliberately
// ignored: only the host adapter may attest a subject that is not an Artifact.
func externalSubjectDigest(tx *store.Tx, view TaskView, in RecordCompletionInput) (string, error) {
	candidates := make([]w.Artifact, 0, len(in.CandidateRefs))
	seenCandidates := map[string]bool{}
	for _, ref := range in.CandidateRefs {
		if seenCandidates[ref] {
			return "", errorf("invalid_request", "duplicate candidate reference")
		}
		seenCandidates[ref] = true
		artifact, err := completionArtifact(tx, view.Task.ThreadID, ref)
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
			if strings.HasPrefix(ref, "source:") {
				source, err := tx.Source(strings.TrimPrefix(ref, "source:"))
				if err != nil || source.ThreadID != view.Task.ThreadID {
					return "", errorf("invalid_request", "evidence source does not belong to the task thread")
				}
				continue
			}
			if _, err := completionArtifact(tx, view.Task.ThreadID, ref); err != nil {
				return "", err
			}
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	return Digest(candidates), nil
}

func completionArtifact(tx *store.Tx, threadID, ref string) (w.Artifact, error) {
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
	if err != nil || artifact.ThreadID != threadID {
		return w.Artifact{}, errorf("invalid_request", "candidate artifact does not belong to the task thread")
	}
	return artifact, nil
}
