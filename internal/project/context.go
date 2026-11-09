package project

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AcSunday/cocogem/internal/contextplan"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// PrepareContextInTx derives the eligible scope from authoritative records,
// then persists the exact brief shared by every attempt in this Run segment.
// A retry must name its original accepted revision; it cannot silently move to
// a newer goal while reusing the old execution plan.
func (s *Service) PrepareContextInTx(tx *store.Tx, taskID string, taskRevision, hostInput int) (w.ContextManifest, error) {
	task, err := tx.Task(taskID)
	if err != nil {
		return w.ContextManifest{}, err
	}
	if taskRevision < 1 || task.CurrentRevision != taskRevision {
		return w.ContextManifest{}, errorf("revision_conflict", "task revision changed; create a new accepted execution segment")
	}
	revision, err := tx.TaskRevision(taskID, taskRevision)
	if err != nil {
		return w.ContextManifest{}, err
	}
	entries := []w.ContextEntry{}
	addRequired := func(ref, kind, content string) error {
		entries = append(entries, w.ContextEntry{Ref: ref, Kind: kind, Digest: contextplan.Digest(content), Visibility: "task_chain", Required: true, Content: content})
		return nil
	}
	revisionJSON, err := json.Marshal(revision)
	if err != nil {
		return w.ContextManifest{}, err
	}
	if err := addRequired(fmt.Sprintf("task:%s@%d", task.ID, taskRevision), "task", string(revisionJSON)); err != nil {
		return w.ContextManifest{}, err
	}

	records, err := tx.ProjectRecords(task.WorkspaceID)
	if err != nil {
		return w.ContextManifest{}, err
	}
	for _, record := range currentRecords(records) {
		if record.Visibility != "project" && record.Visibility != "project-visible" {
			continue
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return w.ContextManifest{}, err
		}
		if err := addRequired(fmt.Sprintf("project:%s@%d", record.ID, record.Revision), "project", string(encoded)); err != nil {
			return w.ContextManifest{}, err
		}
	}

	tasks, err := tx.Tasks(task.WorkspaceID)
	if err != nil {
		return w.ContextManifest{}, err
	}
	now := s.now()
	grantEpoch := 0
	grants, err := tx.Grants(task.WorkspaceID)
	if err != nil {
		return w.ContextManifest{}, err
	}
	for _, grant := range grants {
		if grant.SubjectMemberID == task.OwnerMemberID && grant.Status == "active" && (grant.ExpiresAt == "" || grant.ExpiresAt > now) {
			grantEpoch = max(grantEpoch, grant.Epoch)
		}
	}
	for _, related := range tasks {
		if related.CollaborationScopeID != task.CollaborationScopeID {
			continue
		}
		canShare := func(visibility string) bool {
			switch visibility {
			case "private":
				return related.ID == task.ID
			case "role":
				return related.ID == task.ID && related.OwnerMemberID == task.OwnerMemberID
			default:
				return true
			}
		}
		completions, err := tx.Completions(related.ID)
		if err != nil {
			return w.ContextManifest{}, err
		}
		effects, err := tx.Effects(related.ID)
		if err != nil {
			return w.ContextManifest{}, err
		}
		questions, err := tx.Questions(related.ID)
		if err != nil {
			return w.ContextManifest{}, err
		}
		work := struct {
			Task        w.Task               `json:"task"`
			Completions []w.CompletionRecord `json:"completions"`
			Effects     []w.EffectRecord     `json:"effects"`
			Questions   []w.Question         `json:"questions"`
		}{Task: related, Completions: completions, Effects: effects, Questions: questions}
		encoded, err := json.Marshal(work)
		if err != nil {
			return w.ContextManifest{}, err
		}
		if err := addRequired(fmt.Sprintf("work:%s@%d", related.ID, related.CurrentRevision), "work", string(encoded)); err != nil {
			return w.ContextManifest{}, err
		}
		sources, err := tx.Sources(related.ThreadID)
		if err != nil {
			return w.ContextManifest{}, err
		}
		for _, source := range sources {
			if !canShare(source.Visibility) {
				continue
			}
			visibility := source.Visibility
			if visibility == "" {
				visibility = "task_chain"
			}
			entries = append(entries, w.ContextEntry{Ref: source.ID, Kind: "source", Digest: contextplan.Digest(source.Content), Visibility: visibility})
		}
		claims, err := tx.Claims(related.ID)
		if err != nil {
			return w.ContextManifest{}, err
		}
		for _, claim := range claims {
			if !canShare(claim.Visibility) {
				continue
			}
			encoded, err := json.Marshal(claim)
			if err != nil {
				return w.ContextManifest{}, err
			}
			entries = append(entries, w.ContextEntry{Ref: fmt.Sprintf("claim:%s@%d", claim.ID, claim.Revision), Kind: "claim", Digest: contextplan.Digest(string(encoded)), Visibility: claim.Visibility, Content: string(encoded)})
		}
		datasets, err := tx.Datasets(related.ID)
		if err != nil {
			return w.ContextManifest{}, err
		}
		for _, dataset := range datasets {
			if !canShare(dataset.Visibility) {
				continue
			}
			encoded, err := json.Marshal(dataset)
			if err != nil {
				return w.ContextManifest{}, err
			}
			entries = append(entries, w.ContextEntry{Ref: fmt.Sprintf("dataset:%s@%d", dataset.ID, dataset.Revision), Kind: "dataset", Digest: contextplan.Digest(string(encoded)), Visibility: dataset.Visibility, Content: string(encoded)})
		}
	}
	if task.Kind == "consultation" {
		entries, err = s.addConsultationWork(tx, task, revision, entries)
		if err != nil {
			return w.ContextManifest{}, err
		}
	}
	memories, err := tx.Memories(task.WorkspaceID)
	if err != nil {
		return w.ContextManifest{}, err
	}
	latestMemories := map[string]w.MemoryRecord{}
	for _, memory := range memories {
		if current, ok := latestMemories[memory.ID]; !ok || memory.Revision > current.Revision {
			latestMemories[memory.ID] = memory
		}
	}
	for _, memory := range latestMemories {
		if memory.ClaimStatus != "accepted" {
			continue
		}
		visible := memory.Visibility == "project" || memory.Visibility == "project-visible" ||
			memory.Visibility == "task_chain" && (memory.Scope == task.ID || memory.Scope == task.CollaborationScopeID) ||
			memory.Visibility == "private" && memory.Scope == task.ID
		if !visible {
			continue
		}
		encoded, err := json.Marshal(memory)
		if err != nil {
			return w.ContextManifest{}, err
		}
		entries = append(entries, w.ContextEntry{Ref: fmt.Sprintf("memory:%s@%d", memory.ID, memory.Revision), Kind: "memory", Digest: contextplan.Digest(string(encoded)), Visibility: memory.Visibility, Content: string(encoded)})
	}
	manifest, err := contextplan.Build(w.EligibleScope{
		WorkspaceID: task.WorkspaceID, TaskID: task.ID, TaskRevision: taskRevision,
		CollaborationScopeID: task.CollaborationScopeID, GrantEpoch: grantEpoch, Entries: entries,
	}, w.ContextBudget{HostInput: hostInput, WindowSource: "unknown"})
	if err != nil {
		return w.ContextManifest{}, err
	}
	manifest.ID = w.NewID()
	manifest.CreatedAt = now
	if err := tx.SaveContextManifest(manifest); err != nil {
		return w.ContextManifest{}, err
	}
	return manifest, nil
}

// ContextHostInput chooses the strictest declared member budget for a Run-wide
// startup manifest. Zero members retain the conservative platform default.
func ContextHostInput(run w.Run) int {
	hostInput := 0
	for _, member := range run.Agents {
		if member.ContextSize > 0 && (hostInput == 0 || member.ContextSize < hostInput) {
			hostInput = member.ContextSize
		}
	}
	return hostInput
}

// ContextInputDigest binds both the accepted task and rendered scope to a Run.
func ContextInputDigest(view TaskView, manifest w.ContextManifest, messageID string, in w.RunInput) string {
	return Digest(struct {
		TaskRevision   w.TaskRevision `json:"task_revision"`
		Message        string         `json:"message"`
		Input          w.RunInput     `json:"input"`
		ContextID      string         `json:"context_manifest_id"`
		ScopeDigest    string         `json:"scope_digest"`
		RenderedDigest string         `json:"rendered_digest"`
	}{TaskRevision: view.Revision, Message: messageID, Input: in, ContextID: manifest.ID, ScopeDigest: manifest.ScopeDigest, RenderedDigest: manifest.RenderedDigest})
}

// SourceContextRef is the manifest key used by the scoped read_source tool.
func SourceContextRef(source w.Source) string { return strings.TrimSpace(source.ID) }
