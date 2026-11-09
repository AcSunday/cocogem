package project

import (
	"context"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Tasks returns durable work without expanding message or source bodies.
func (s *Service) Tasks(ctx context.Context, workspaceID string) ([]TaskView, error) {
	views := []TaskView{}
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		if _, err := tx.Workspace(workspaceID); err != nil {
			return err
		}
		tasks, err := tx.Tasks(workspaceID)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			view, err := s.taskInTx(tx, task.ID)
			if err != nil {
				return err
			}
			views = append(views, view)
		}
		return nil
	})
	return views, err
}

// Revisions returns immutable accepted goals, newest first.
func (s *Service) Revisions(ctx context.Context, taskID string) ([]w.TaskRevision, error) {
	var revisions []w.TaskRevision
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		if _, err := tx.Task(taskID); err != nil {
			return err
		}
		var err error
		revisions, err = tx.TaskRevisions(taskID)
		return err
	})
	return revisions, err
}

// Dependencies returns only edges whose consumer belongs to the requested task's workspace.
func (s *Service) Dependencies(ctx context.Context, taskID string) ([]w.TaskDependency, error) {
	dependencies := []w.TaskDependency{}
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		all, err := tx.Dependencies(task.WorkspaceID)
		if err != nil {
			return err
		}
		for _, dependency := range all {
			if dependency.ConsumerTaskID == taskID || dependency.ProducerTaskID == taskID {
				dependencies = append(dependencies, dependency)
			}
		}
		return nil
	})
	return dependencies, err
}

// Records returns the current active project-state projection.
func (s *Service) Records(ctx context.Context, workspaceID string) ([]w.ProjectRecord, error) {
	var records []w.ProjectRecord
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		if _, err := tx.Workspace(workspaceID); err != nil {
			return err
		}
		all, err := tx.ProjectRecords(workspaceID)
		if err != nil {
			return err
		}
		records = currentRecords(all)
		return nil
	})
	return records, err
}
