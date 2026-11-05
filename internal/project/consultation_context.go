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

func (s *Service) addConsultationWork(tx *store.Tx, task w.Task, revision w.TaskRevision, entries []w.ContextEntry) ([]w.ContextEntry, error) {
	requester, err := tx.Task(task.ParentID)
	if err != nil {
		return nil, errorf("disclosure_denied", "consultation requester is unavailable")
	}
	for _, ref := range revision.InputRefs {
		raw, ok := strings.CutPrefix(ref, "work:")
		if !ok {
			return nil, errorf("invalid_request", "consultation only accepts exact original work references")
		}
		id, rawRevision, ok := strings.Cut(raw, "@")
		version, parseErr := strconv.Atoi(rawRevision)
		if !ok || parseErr != nil || version < 1 {
			return nil, errorf("invalid_request", "consultation work reference must include an exact revision")
		}
		original, err := tx.Task(id)
		if err != nil || original.WorkspaceID != task.WorkspaceID {
			return nil, errorf("not_found", "original consultation work was not found")
		}
		originalRevision, err := tx.TaskRevision(id, version)
		if err != nil {
			return nil, err
		}
		visibility := "private"
		if requester.OwnerMemberID != "" && requester.OwnerMemberID == task.OwnerMemberID && task.OwnerMemberID == original.OwnerMemberID {
			visibility = "role"
		}
		completions, err := tx.Completions(id)
		if err != nil {
			return nil, err
		}
		exactCompletions := []w.CompletionRecord{}
		for _, completion := range completions {
			if completion.TaskRevision == version {
				exactCompletions = append(exactCompletions, completion)
			}
		}
		effects, err := tx.Effects(id)
		if err != nil {
			return nil, err
		}
		original.CurrentRevision = version
		work := struct {
			Task        w.Task               `json:"task"`
			Revision    w.TaskRevision       `json:"revision"`
			Completions []w.CompletionRecord `json:"completions"`
			Effects     []w.EffectRecord     `json:"effects"`
		}{original, originalRevision, exactCompletions, effects}
		encoded, err := json.Marshal(work)
		if err != nil {
			return nil, err
		}
		entries = append(entries, w.ContextEntry{Ref: ref, Kind: "work", Digest: contextplan.Digest(string(encoded)), Visibility: visibility, Required: true, Content: string(encoded)})

		sources, err := tx.Sources(original.ThreadID)
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			shared, ok := consultationSourceVisibility(requester, original, task, source.Visibility)
			if !ok {
				continue
			}
			entries = append(entries, w.ContextEntry{Ref: source.ID, Kind: "source", Digest: contextplan.Digest(source.Content), Visibility: shared})
		}
		claims, err := tx.Claims(id)
		if err != nil {
			return nil, err
		}
		for _, claim := range claims {
			shared, ok := consultationSourceVisibility(requester, original, task, claim.Visibility)
			if !ok {
				continue
			}
			encoded, err := json.Marshal(claim)
			if err != nil {
				return nil, err
			}
			entries = append(entries, w.ContextEntry{Ref: fmt.Sprintf("claim:%s@%d", claim.ID, claim.Revision), Kind: "claim", Digest: contextplan.Digest(string(encoded)), Visibility: shared, Content: string(encoded)})
		}
	}
	return entries, nil
}

func consultationSourceVisibility(requester, original, consultation w.Task, visibility string) (string, bool) {
	switch visibility {
	case "project", "project-visible":
		return "project", true
	case "role":
		if requester.OwnerMemberID != "" && requester.OwnerMemberID == consultation.OwnerMemberID && consultation.OwnerMemberID == original.OwnerMemberID {
			return "role", true
		}
	}
	return "", false
}
