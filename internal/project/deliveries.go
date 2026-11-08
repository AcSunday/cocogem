package project

import (
	"context"
	"strconv"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TaskDeliveries is the narrow, source-free projection TaskDetail needs. It
// carries this task's runs plus exactly the artifacts and messages a successful
// delivery named, so the detail page never loads a full ThreadView (and its
// sources) just to show acceptance candidates.
type TaskDeliveries struct {
	TaskID    string       `json:"task_id"`
	Runs      []w.Run      `json:"runs"`
	Artifacts []w.Artifact `json:"artifacts"`
	Messages  []w.Message  `json:"messages"`
}

// Deliveries projects one task's runs and the exact delivered versions.
func (s *Service) Deliveries(ctx context.Context, taskID string) (TaskDeliveries, error) {
	out := TaskDeliveries{TaskID: taskID, Runs: []w.Run{}, Artifacts: []w.Artifact{}, Messages: []w.Message{}}
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		out, err = s.deliveriesInTx(tx, task)
		return err
	})
	return out, err
}

func (s *Service) deliveriesInTx(tx *store.Tx, task w.Task) (TaskDeliveries, error) {
	out := TaskDeliveries{TaskID: task.ID, Runs: []w.Run{}, Artifacts: []w.Artifact{}, Messages: []w.Message{}}
	runs, err := tx.TaskRuns(task.ID)
	if err != nil {
		return out, err
	}
	out.Runs = runs
	seenArtifact := map[string]bool{}
	seenMessage := map[string]bool{}
	for _, run := range runs {
		if run.Status != "succeeded" || run.Delivery == nil {
			continue
		}
		for _, ref := range run.Delivery.ArtifactRefs {
			key := ref.ID + "@v" + strconv.Itoa(ref.Version)
			if seenArtifact[key] {
				continue
			}
			artifact, err := tx.Artifact(ref.ID, ref.Version)
			if err != nil || artifact.RunID != run.ID || artifact.ThreadID != task.ThreadID {
				continue
			}
			seenArtifact[key] = true
			out.Artifacts = append(out.Artifacts, artifact)
		}
		for _, id := range run.Delivery.MessageIDs {
			if seenMessage[id] {
				continue
			}
			message, err := tx.Message(id)
			if err != nil || message.ThreadID != task.ThreadID || message.RunID != run.ID {
				continue
			}
			seenMessage[id] = true
			out.Messages = append(out.Messages, message)
		}
	}
	return out, nil
}
