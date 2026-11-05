package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// DisclosureForSources derives the most restrictive visibility and an auditable
// digest without including source bodies or credentials.
func DisclosureForSources(sources []Source) (string, string) {
	visibility := "task_chain"
	if len(sources) > 0 {
		visibility = "project"
	}
	rank := map[string]int{"project": 0, "project-visible": 0, "task_chain": 1, "role": 2, "private": 3}
	type entry struct {
		ID         string `json:"id"`
		Digest     string `json:"digest"`
		Visibility string `json:"visibility"`
		Exposure   string `json:"exposure"`
	}
	entries := make([]entry, 0, len(sources))
	for _, source := range sources {
		current := source.Visibility
		if current == "" {
			current = "task_chain"
		}
		if rank[current] > rank[visibility] || rank[current] == 0 && current != "project" && current != "project-visible" {
			visibility = current
		}
		if rank[current] == 0 && current != "project" && current != "project-visible" {
			visibility = "private"
		}
		digest := source.ContentDigest
		if digest == "" {
			digest = source.SHA256
		}
		entries = append(entries, entry{ID: source.ID, Digest: digest, Visibility: current, Exposure: source.ExposureDigest})
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.ID, b.ID) })
	encoded, _ := json.Marshal(entries)
	sum := sha256.Sum256(encoded)
	return visibility, hex.EncodeToString(sum[:])
}
