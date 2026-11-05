package project

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func claimSources(tx *store.Tx, workspaceID string, refs []string) ([]w.Source, error) {
	sources := make([]w.Source, 0, len(refs))
	for _, id := range refs {
		source, err := tx.Source(id)
		if err != nil {
			return nil, err
		}
		if source.WorkspaceID != workspaceID {
			return nil, errorf("disclosure_denied", "source belongs to another workspace")
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func locatorMatches(source w.Source, locator string) bool {
	if locator == "" {
		return !source.Truncated
	}
	parts := strings.Split(locator, ":")
	if len(parts) != 2 {
		return false
	}
	number, err := strconv.Atoi(parts[1])
	if err != nil || number < 1 {
		return false
	}
	switch parts[0] {
	case "page":
		return source.Kind == "pdf" && source.SourceLocator == "page:1" && number == 1
	case "row", "line":
		if source.Kind == "csv" && parts[0] != "row" || source.Kind != "csv" && parts[0] != "line" {
			return false
		}
		prefix := strconv.Itoa(number) + ":"
		for _, line := range strings.Split(source.Content, "\n") {
			if strings.HasPrefix(line, prefix) {
				return true
			}
		}
	}
	return false
}

func (s *Service) SaveClaim(ctx context.Context, in w.Claim) (w.Claim, error) {
	if in.WorkspaceID == "" || in.TaskID == "" || in.ID == "" || in.Revision < 1 || strings.TrimSpace(in.Statement) == "" {
		return in, errorf("invalid_request", "claim requires workspace, task, identity, revision and statement")
	}
	if !slices.Contains([]string{"proposed", "supported", "contradicted", "mixed", "unresolved"}, in.Status) {
		return in, errorf("invalid_request", "unknown claim status")
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(in.TaskID)
		if err != nil || task.WorkspaceID != in.WorkspaceID {
			return errorf("not_found", "claim task was not found in this workspace")
		}
		in.SupportingRefs = []string{}
		in.ContradictingRefs = []string{}
		refs := []string{}
		completeSupport := false
		for _, evidence := range in.Evidence {
			if !slices.Contains([]string{"supports", "contradicts", "contextualizes", "fails_to_detect"}, evidence.Relation) {
				return errorf("invalid_request", "unknown claim evidence relation")
			}
			source, err := tx.Source(evidence.SourceID)
			if err != nil {
				return err
			}
			if source.WorkspaceID != in.WorkspaceID {
				return errorf("disclosure_denied", "claim evidence belongs to another workspace")
			}
			if evidence.Completeness != "complete" && evidence.Completeness != "partial" && evidence.Completeness != "unavailable" {
				return errorf("invalid_request", "claim evidence requires complete, partial or unavailable locator quality")
			}
			if evidence.Completeness != "unavailable" && !locatorMatches(source, evidence.Locator) {
				return errorf("evidence_stale", "claim evidence locator does not match the captured source")
			}
			if evidence.Relation == "supports" && evidence.Completeness == "complete" && strings.TrimSpace(evidence.Reviewer) != "" {
				completeSupport = true
			}
			if evidence.Relation == "supports" {
				in.SupportingRefs = append(in.SupportingRefs, evidence.SourceID)
			}
			if evidence.Relation == "contradicts" {
				in.ContradictingRefs = append(in.ContradictingRefs, evidence.SourceID)
			}
			refs = append(refs, evidence.SourceID)
		}
		if in.Status == "supported" && !completeSupport {
			return errorf("insufficient_evidence", "supported claims require complete located evidence and a semantic reviewer")
		}
		sources, err := claimSources(tx, in.WorkspaceID, refs)
		if err != nil {
			return err
		}
		producer, ok, err := producerExposure(tx, in.WorkspaceID, in.TaskID, in.ProducerAttemptID)
		if err != nil {
			return err
		}
		if ok {
			sources = append(sources, producer)
		}
		in.Visibility, in.ExposureDigest = w.DisclosureForSources(sources)
		in.CreatedAt = s.now()
		return tx.SaveClaim(in)
	})
	return in, err
}

func (s *Service) SaveDataset(ctx context.Context, in w.DatasetManifest) (w.DatasetManifest, error) {
	if in.WorkspaceID == "" || in.TaskID == "" || in.ID == "" || in.Revision < 1 || len(in.SourceRefs) == 0 {
		return in, errorf("invalid_request", "dataset manifest requires identity, revision and exact sources")
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(in.TaskID)
		if err != nil || task.WorkspaceID != in.WorkspaceID {
			return errorf("not_found", "dataset task was not found in this workspace")
		}
		sources, err := claimSources(tx, in.WorkspaceID, in.SourceRefs)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if in.ContentDigest != "" && source.SHA256 != "" && in.ContentDigest != source.SHA256 {
				return errorf("evidence_stale", "dataset digest does not match its source")
			}
		}
		producer, ok, err := producerExposure(tx, in.WorkspaceID, in.TaskID, in.ProducerAttemptID)
		if err != nil {
			return err
		}
		if ok {
			sources = append(sources, producer)
		}
		in.Visibility, in.ExposureDigest = w.DisclosureForSources(sources)
		in.CreatedAt = s.now()
		return tx.SaveDataset(in)
	})
	return in, err
}

func (s *Service) SaveMemory(ctx context.Context, in w.MemoryRecord) (w.MemoryRecord, error) {
	if in.WorkspaceID == "" || in.ID == "" || in.Revision < 1 || strings.TrimSpace(in.Content) == "" || len(in.SourceRefs) == 0 {
		return in, errorf("invalid_request", "memory requires identity, content, revision and sources")
	}
	if !slices.Contains([]string{"candidate", "accepted", "rejected", "superseded", "stale"}, in.ClaimStatus) {
		return in, errorf("invalid_request", "unknown memory claim status")
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		sources, err := claimSources(tx, in.WorkspaceID, in.SourceRefs)
		if err != nil {
			return err
		}
		producer, ok, err := producerExposure(tx, in.WorkspaceID, "", in.ProducerAttemptID)
		if err != nil {
			return err
		}
		if ok {
			sources = append(sources, producer)
		}
		in.Visibility, in.ExposureDigest = w.DisclosureForSources(sources)
		in.CreatedAt = s.now()
		return tx.SaveMemory(in)
	})
	return in, err
}

func (s *Service) Claims(ctx context.Context, taskID string) ([]w.Claim, error) {
	var claims []w.Claim
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		claims, err = tx.Claims(taskID)
		return err
	})
	return claims, err
}

func (s *Service) Datasets(ctx context.Context, taskID string) ([]w.DatasetManifest, error) {
	var datasets []w.DatasetManifest
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		datasets, err = tx.Datasets(taskID)
		return err
	})
	return datasets, err
}

func (s *Service) Memories(ctx context.Context, workspaceID string) ([]w.MemoryRecord, error) {
	var memories []w.MemoryRecord
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		memories, err = tx.Memories(workspaceID)
		return err
	})
	return memories, err
}

func datasetForSource(run w.Run, source w.Source) (w.DatasetManifest, bool) {
	if run.TaskID == "" || source.Kind != "csv" {
		return w.DatasetManifest{}, false
	}
	visibility, exposure := w.DisclosureForSources([]w.Source{source})
	schema := ""
	if line, _, ok := strings.Cut(source.Content, "\n"); ok {
		schema = line
	}
	return w.DatasetManifest{
		ID: source.ID + "-dataset", WorkspaceID: run.WorkspaceID, TaskID: run.TaskID, Revision: 1,
		SourceRefs: []string{source.ID}, Schema: schema, AcquiredAt: source.CapturedAt,
		ProcessingSteps: []string{source.ParserVersion}, MissingRules: "not_observed",
		ContentDigest: source.SHA256, Location: source.Path, Visibility: visibility, ExposureDigest: exposure,
		CreatedAt: w.Now(),
	}, true
}

// SaveDatasetForSource records the real CSV capture in the same transaction.
func SaveDatasetForSource(tx *store.Tx, run w.Run, source w.Source) error {
	dataset, ok := datasetForSource(run, source)
	if !ok {
		return nil
	}
	if _, err := tx.Dataset(dataset.ID, dataset.Revision); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return tx.SaveDataset(dataset)
}
