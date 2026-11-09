package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (s *Server) startProjectTask(rw http.ResponseWriter, r *http.Request) {
	run, err := s.startTaskRun(r.Context(), r.PathValue("id"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	writeJSON(rw, http.StatusAccepted, run)
}

// Launch is once per task. Opening or retrying this endpoint returns the same
// execution, including a failed/uncertain one, instead of replaying its effects.
func (s *Server) startTaskRun(ctx context.Context, taskID string) (w.Run, error) {
	executors := s.refreshExecutors(ctx)
	var run w.Run
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		if previous, err := tx.LatestTaskRun(task.ID); err == nil {
			run = previous
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if task.Status != "ready" && task.Status != "draft" {
			return &RequestError{409, "task_not_ready", "请先恢复任务，再开始执行"}
		}
		if !s.modelConfigured(executors) {
			return &RequestError{503, "model_unavailable", "没有可用的本机 CLI，请检查现有连接配置"}
		}
		agents, err := tx.Agents(task.WorkspaceID)
		if err != nil {
			return err
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		var th w.Thread
		if task.ThreadID == "" {
			revision, err := tx.TaskRevision(task.ID, task.CurrentRevision)
			if err != nil {
				return err
			}
			title := task.Title
			if title == "" {
				title = strings.SplitN(revision.Goal, "\n", 2)[0]
				title = string([]rune(title)[:min(100, len([]rune(title)))])
			}
			th = w.Thread{ID: w.NewID(), WorkspaceID: task.WorkspaceID, TaskID: task.ID, Title: title, CoordinatorID: task.OwnerMemberID, Workflow: "free", Revision: 1, UpdatedAt: w.Now(), Internal: task.ParentID != ""}
			applyThreadDefaults(&th, agents, settings)
			if !slices.Contains(th.AgentIDs, th.CoordinatorID) {
				th.AgentIDs = append([]string{th.CoordinatorID}, th.AgentIDs...)
			}
			if err := validateThread(th, agents); err != nil {
				return err
			}
			if err := tx.SaveThread(th); err != nil {
				return err
			}
			task.ThreadID, task.UpdatedAt = th.ID, w.Now()
			if task.Status == "draft" {
				task.Status = "ready"
			}
			if err := tx.SaveTask(task, task.CurrentRevision); err != nil {
				return err
			}
		} else {
			th, err = tx.Thread(task.ThreadID)
			if err != nil {
				return err
			}
			if th.TaskID != task.ID {
				return w.ErrConflict
			}
		}
		revision, err := tx.TaskRevision(task.ID, task.CurrentRevision)
		if err != nil {
			return err
		}
		in := w.RunInput{Message: revision.Goal, Workflow: th.Workflow, ResearchScope: "auto"}
		if in.Workflow == "" || in.Workflow == "free" {
			in.Workflow = "free"
			in.Targets = []string{th.CoordinatorID}
		}
		route := w.Routing{Decision: "discuss", Workflow: "free", Targets: []string{th.CoordinatorID}, Explanation: "交给任务负责人推进"}
		if in.Workflow != "free" {
			route, err = workflow.ResolveRouting(ctx, nil, in, th, agents)
			if err != nil {
				return err
			}
		}
		if route.Decision == "clarify" {
			return errors.New(route.Clarification)
		}
		in.Workflow, in.Targets, in.Assignments = route.Workflow, route.Targets, route.Assignments
		in, err = applySettingsDefaults(in, th, agents, settings)
		if err != nil {
			return err
		}
		route.Assignments = in.Assignments
		run, err = s.queueRunInTx(tx, th, in, route, "task-launch-"+task.ID, digest(in))
		return err
	})
	return run, err
}
