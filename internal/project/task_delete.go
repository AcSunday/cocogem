package project

import (
	"context"
	"sort"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TaskDeletion reports every identity removed by a permanent task delete,
// including descendants. ThreadIDs is internal to proposal cleanup and never
// serialized.
type TaskDeletion struct {
	Deleted   []string `json:"deleted"`
	Count     int      `json:"count"`
	ThreadIDs []string `json:"-"`
}

var unreconciledEffect = map[string]bool{
	"executing": true,
	"partial":   true,
	"uncertain": true,
}

// PermanentDeleteTasks removes selected tasks, their owned subtasks, their
// task-scoped records and the conversations created for them. It refuses to
// delete a task tree that is still executing, has unresolved effects, shares a
// conversation with another task, or is a valid dependency producer for work
// outside the deletion set.
func (s *Service) PermanentDeleteTasks(ctx context.Context, ids []string) (TaskDeletion, error) {
	var out TaskDeletion
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		tasks, err := tx.AllTasks()
		if err != nil {
			return err
		}
		byID := make(map[string]w.Task, len(tasks))
		children := make(map[string][]string, len(tasks))
		for _, task := range tasks {
			byID[task.ID] = task
			if task.ParentID != "" {
				children[task.ParentID] = append(children[task.ParentID], task.ID)
			}
		}

		selected := make(map[string]bool)
		for _, raw := range ids {
			id := strings.TrimSpace(raw)
			if id == "" {
				continue
			}
			if _, ok := byID[id]; !ok {
				return errorf("not_found", "任务不存在或已被删除")
			}
			selected[id] = true
		}
		if len(selected) == 0 {
			return errorf("invalid_request", "请选择至少一个任务")
		}
		if len(selected) > 200 {
			return errorf("invalid_request", "一次最多删除 200 个任务")
		}

		deletion := make(map[string]w.Task, len(selected))
		stack := make([]string, 0, len(selected))
		for id := range selected {
			stack = append(stack, id)
		}
		for len(stack) > 0 {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, ok := deletion[id]; ok {
				continue
			}
			deletion[id] = byID[id]
			stack = append(stack, children[id]...)
		}

		// A selected child can still be referenced by an ancestor's active run.
		// Guard the selected tree and every ancestor, but not unrelated siblings.
		guard := make(map[string]bool, len(deletion))
		for id := range deletion {
			guard[id] = true
		}
		for id := range deletion {
			for parent := byID[id].ParentID; parent != ""; parent = byID[parent].ParentID {
				guard[parent] = true
			}
		}
		for id := range guard {
			if byID[id].Status == "active" {
				return errorf("conflict", "任务仍在执行，请先暂停或取消")
			}
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		for _, run := range active {
			if guard[run.TaskID] {
				return errorf("conflict", "任务仍有执行中的运行，请先暂停或取消")
			}
		}
		for id := range guard {
			effects, err := tx.Effects(id)
			if err != nil {
				return err
			}
			for _, effect := range effects {
				if unreconciledEffect[effect.Status] {
					return errorf("conflict", "任务存在未核实的副作用，暂时不能删除")
				}
			}
		}

		workspaces := make(map[string]bool)
		for _, task := range deletion {
			workspaces[task.WorkspaceID] = true
		}
		for workspaceID := range workspaces {
			dependencies, err := tx.Dependencies(workspaceID)
			if err != nil {
				return err
			}
			for _, dependency := range dependencies {
				_, producerSelected := deletion[dependency.ProducerTaskID]
				_, consumerSelected := deletion[dependency.ConsumerTaskID]
				if dependency.Valid && producerSelected && !consumerSelected {
					return errorf("conflict", "还有其他任务依赖所选任务，请先处理依赖关系")
				}
			}
		}

		threads := make(map[string]bool)
		for _, task := range deletion {
			if task.ThreadID != "" {
				threads[task.ThreadID] = true
			}
		}
		for _, task := range tasks {
			if threads[task.ThreadID] {
				if _, selected := deletion[task.ID]; !selected {
					return errorf("conflict", "所选任务与其他任务共享会话，不能单独删除")
				}
			}
		}

		deleted := make([]string, 0, len(deletion))
		for id := range deletion {
			deleted = append(deleted, id)
		}
		sort.Strings(deleted)
		if err := tx.DeleteTaskRows(deleted); err != nil {
			return err
		}
		threadIDs := make([]string, 0, len(threads))
		for threadID := range threads {
			threadIDs = append(threadIDs, threadID)
		}
		sort.Strings(threadIDs)
		for _, threadID := range threadIDs {
			if err := tx.DeleteThread(threadID); err != nil {
				return err
			}
		}
		out = TaskDeletion{Deleted: deleted, Count: len(deleted), ThreadIDs: threadIDs}
		return nil
	})
	return out, err
}
