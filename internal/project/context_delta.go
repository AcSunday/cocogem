package project

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/contextplan"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// ResolveContextItem returns the exact body admitted by a manifest. Required
// records come from the immutable rendered plan: their live database projection
// may already have advanced when a run starts. Exact reference-only evidence is
// still read from its immutable durable record and checked against the digest.
func ResolveContextItem(tx *store.Tx, manifest w.ContextManifest, task w.Task, item w.ContextItem) (string, error) {
	if item.Mode == "inline" {
		header := fmt.Sprintf("\n[%s %s digest=%s]\n", item.Kind, item.Ref, item.Digest)
		start := strings.Index(manifest.Brief, header)
		if start < 0 {
			return "", fmt.Errorf("inline context item %s is missing from its immutable manifest", item.Ref)
		}
		start += len(header)
		end := len(manifest.Brief)
		if next := strings.Index(manifest.Brief[start:], "\n["); next >= 0 {
			end = start + next
		}
		content := strings.TrimSuffix(manifest.Brief[start:end], "\n")
		if contextplan.Digest(content) != item.Digest {
			return "", fmt.Errorf("inline context item %s does not match its manifest digest", item.Ref)
		}
		return content, nil
	}
	encode := func(value any) (string, error) {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		if contextplan.Digest(string(encoded)) != item.Digest {
			return "", fmt.Errorf("context item %s changed before its session delta was prepared", item.Ref)
		}
		return string(encoded), nil
	}
	if item.Kind == "source" {
		source, err := tx.Source(item.Ref)
		if err != nil || source.WorkspaceID != task.WorkspaceID || contextplan.Digest(source.Content) != item.Digest {
			return "", fmt.Errorf("context source %s is unavailable at its admitted digest", item.Ref)
		}
		return source.Content, nil
	}
	identity, rawRevision, ok := strings.Cut(item.Ref, "@")
	revision, revisionErr := strconv.Atoi(rawRevision)
	if !ok || revisionErr != nil || revision < 1 {
		return "", fmt.Errorf("context item %s has no exact revision", item.Ref)
	}
	switch item.Kind {
	case "task":
		id := strings.TrimPrefix(identity, "task:")
		revisionRecord, err := tx.TaskRevision(id, revision)
		if err != nil || id != task.ID || identity != "task:"+id {
			return "", fmt.Errorf("task revision for %s is unavailable", item.Ref)
		}
		return encode(revisionRecord)
	case "work":
		id := strings.TrimPrefix(identity, "work:")
		related, err := tx.Task(id)
		if err != nil || related.WorkspaceID != task.WorkspaceID || related.CurrentRevision != revision || identity != "work:"+id {
			return "", fmt.Errorf("work record for %s is unavailable", item.Ref)
		}
		completions, err := tx.Completions(id)
		if err != nil {
			return "", err
		}
		effects, err := tx.Effects(id)
		if err != nil {
			return "", err
		}
		return encode(struct {
			Task        w.Task               `json:"task"`
			Completions []w.CompletionRecord `json:"completions"`
			Effects     []w.EffectRecord     `json:"effects"`
		}{Task: related, Completions: completions, Effects: effects})
	case "project":
		id := strings.TrimPrefix(identity, "project:")
		record, err := tx.ProjectRecord(id, revision)
		if err != nil || record.WorkspaceID != task.WorkspaceID || identity != "project:"+id {
			return "", fmt.Errorf("project record for %s is unavailable", item.Ref)
		}
		return encode(record)
	case "memory":
		id := strings.TrimPrefix(identity, "memory:")
		memories, err := tx.Memories(task.WorkspaceID)
		if err != nil {
			return "", err
		}
		for _, memory := range memories {
			if identity == "memory:"+id && memory.ID == id && memory.Revision == revision {
				return encode(memory)
			}
		}
	case "claim", "dataset":
		prefix := item.Kind + ":"
		id := strings.TrimPrefix(identity, prefix)
		tasks, err := tx.Tasks(task.WorkspaceID)
		if err != nil {
			return "", err
		}
		for _, related := range tasks {
			if item.Kind == "claim" {
				claims, err := tx.Claims(related.ID)
				if err != nil {
					return "", err
				}
				for _, claim := range claims {
					if identity == prefix+id && claim.ID == id && claim.Revision == revision {
						return encode(claim)
					}
				}
			} else {
				datasets, err := tx.Datasets(related.ID)
				if err != nil {
					return "", err
				}
				for _, dataset := range datasets {
					if identity == prefix+id && dataset.ID == id && dataset.Revision == revision {
						return encode(dataset)
					}
				}
			}
		}
	}
	return "", fmt.Errorf("context item %s has no authorized durable snapshot", item.Ref)
}
