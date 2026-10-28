package workspace

// Delivery is the authoritative final result of a successful run. The nullable
// Run.Delivery distinguishes "not recorded" from an empty delivery: legacy
// history is never backfilled by guessing, and a run without a delivery can
// never be treated as if it had one.
type Delivery struct {
	MessageIDs   []string      `json:"message_ids"`
	ArtifactRefs []ArtifactRef `json:"artifact_refs"`
}

// IsDeliveredArtifact reports whether this exact artifact version belongs to the
// run's committed delivery. IDs and versions are matched together, never by
// artifact identity or by latest version. A recorded delivery is authoritative
// only once the run succeeded, so a new failed or canceled run cannot expose a
// draft. Legacy terminal runs recorded no delivery at all, so their existing
// artifacts stay visible instead of being hidden by a guessed migration.
func IsDeliveredArtifact(run Run, artifact Artifact) bool {
	if artifact.RunID != run.ID {
		return false
	}
	if run.Delivery == nil {
		return Terminal(run.Status)
	}
	if run.Status != "succeeded" {
		return false
	}
	for _, ref := range run.Delivery.ArtifactRefs {
		if ref.ID == artifact.ID && ref.Version == artifact.Version {
			return true
		}
	}
	return false
}

// FinalArtifacts filters artifacts already scoped to one thread down to the
// exact versions delivered by this run. HTTP and UI consumers must use it
// instead of inventing a "latest artifact" or "last step" rule.
func FinalArtifacts(run Run, artifacts []Artifact) []Artifact {
	final := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if IsDeliveredArtifact(run, artifact) {
			final = append(final, artifact)
		}
	}
	return final
}
