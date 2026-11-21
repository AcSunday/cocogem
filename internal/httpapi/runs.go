package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// submit validates and freezes one accepted submission. Explicit @member text
// and the Composer's selected recipients resolve deterministically, so sending a
// message never waits for a slow model pre-routing round trip.
func (s *Server) submit(rw http.ResponseWriter, r *http.Request) {
	var in w.RunInput
	if !decode(rw, r, &in) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 16 || len(key) > 100 {
		respond(rw, r, nil, errors.New("有效的 Idempotency-Key 是必需的"))
		return
	}
	if strings.TrimSpace(in.Message) == "" || len([]rune(in.Message)) > 16000 {
		respond(rw, r, nil, errors.New("问题必须为 1–16000 个字符"))
		return
	}
	executors := s.refreshExecutors(r.Context())
	hash := digest(in)
	var run w.Run
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		previous, oldHash, err := tx.Idempotent(r.PathValue("id"), key)
		if err == nil {
			if oldHash != hash {
				return w.ErrConflict
			}
			run = previous
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		th, err := tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		agents, err := tx.Agents(th.WorkspaceID)
		if err != nil {
			return err
		}
		if !s.modelConfigured(executors) {
			return &RequestError{503, "model_unavailable", "没有可用的本机 CLI，请检查 CLI 安装和现有连接配置"}
		}
		route, err := workflow.ResolveRouting(in, th, agents)
		if err != nil {
			return err
		}
		if route.Decision == "clarify" {
			return &RequestError{409, "routing_clarification", route.Clarification}
		}
		run, err = s.queueRunInTx(tx, th, in, route, key, hash)
		return err
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	writeJSON(rw, 202, run)
}

// queueRunInTx validates and freezes one accepted submission. Its caller owns
// the transaction so creating a conversation and queuing work commit together.
func (s *Server) queueRunInTx(tx *store.Tx, th w.Thread, in w.RunInput, route w.Routing, key, hash string) (w.Run, error) {
	var run w.Run
	ws, err := tx.Workspace(th.WorkspaceID)
	if err != nil {
		return run, err
	}
	agents, err := tx.Agents(ws.ID)
	if err != nil {
		return run, err
	}
	err = func() error {
		if in.ResearchScope == "web_discovery" && !s.engine.Web.SearchEnabled() {
			return &RequestError{503, "search_unavailable", "在线搜索尚未配置"}
		}
		if len(in.SourceURLs) > 10 || len(in.ArtifactRefs) > 5 || len(in.SourceIDs) > 5 {
			return errors.New("资料引用过多")
		}
		if err := validateSourceSelection(in.SourceIDs); err != nil {
			return err
		}
		for _, id := range in.SourceIDs {
			imported, err := tx.SourceImport(id)
			if err != nil || imported.ThreadID != th.ID || imported.WorkspaceID != ws.ID {
				return &RequestError{400, "invalid_source", "上传资料不属于当前会话"}
			}
			if imported.OriginalContent == "" {
				return &RequestError{400, "source_unavailable", "资料原件不可用，请重新添加文件"}
			}
		}
		for _, u := range in.SourceURLs {
			if _, err := tools.ValidateURL(u); err != nil {
				return err
			}
		}
		for _, ref := range in.ArtifactRefs {
			a, err := tx.Artifact(ref.ID, ref.Version)
			if err != nil || a.ThreadID != th.ID {
				return errors.New("产物引用不属于当前会话")
			}
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		if len(active) >= workflow.MaxActiveRuns {
			return &RequestError{429, "queue_full", "执行队列已满"}
		}
		if slices.ContainsFunc(active, func(r w.Run) bool { return r.ThreadID == th.ID }) {
			return w.ErrConflict
		}
		in.Targets = route.Targets
		// Ordinary single-member chats write directly to the bound project without
		// creating a Task or deriving a Candidate worktree. Only durable Tasks
		// (and parallel subtasks) use isolated Candidate worktrees; Plan-Only and
		// parallel multi-member passes stay read-only.
		writeBranch := ws.Root != "" && !in.PlanOnly && len(in.Targets) <= 1 && th.TaskID != ""
		var policy *w.ExecutionPolicy
		if s.engine != nil {
			limits := s.engine.Limits
			policy = &w.ExecutionPolicy{RunTimeout: limits.RunTimeout, StepTimeout: limits.StepTimeout, FirstResponse: limits.FirstResponse, Stall: limits.Stall, StallAction: limits.StallAction}
		}
		// A new run is explicitly under the delivery contract from acceptance,
		// so a queued cancellation or early failure never looks like legacy
		// history that exposes drafts. Only a successful finish fills it.
		run = w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Input: in, Routing: route, ExecutionPolicy: policy, Status: "queued", CreatedAt: w.Now(), CoordinatorID: th.CoordinatorID, SkillVersions: map[string]string{}, WriteBranch: writeBranch, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
		for _, skill := range skills.ListFor(ws.Root) {
			if ws.SkillIDs == nil || slices.Contains(ws.SkillIDs, skill.ID) {
				run.SkillVersions[skill.ID] = skill.Version
			}
		}
		steps, err := workflow.Plan(run.ID, in, agents)
		if err != nil {
			return err
		}
		for _, step := range steps {
			a := agents[slices.IndexFunc(agents, func(a w.Agent) bool { return a.ID == step.AgentID })]
			if err := s.validateExecutor(a.Executor); err != nil {
				return err
			}
			if err := w.ValidateAgent(a, nil, s.config.Models, tools.AllNames); err != nil {
				return err
			}
			if !slices.ContainsFunc(run.Agents, func(x w.Agent) bool { return x.ID == a.ID }) {
				run.Agents = append(run.Agents, a)
			}
			if !slices.Contains(th.AgentIDs, a.ID) {
				th.AgentIDs = append(th.AgentIDs, a.ID)
			}
		}
		if len(th.AgentIDs) > 10 {
			return errors.New("会话不能超过十个成员")
		}
		for _, a := range agents {
			if !a.Disabled && slices.Contains(th.AgentIDs, a.ID) && !slices.ContainsFunc(run.Agents, func(x w.Agent) bool { return x.ID == a.ID }) {
				run.Agents = append(run.Agents, a)
			}
		}
		run.SkillSnapshot, err = skills.SnapshotHashFor(ws.Root, ws.SkillIDs)
		if err != nil {
			return err
		}
		th.UpdatedAt = w.Now()
		th.Revision++
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		messageID := w.NewID()
		var task project.TaskView
		if th.TaskID != "" {
			task, err = s.projects.BindRunTask(tx, th, in, messageID, run.ID)
			if err != nil {
				return err
			}
			run.TaskID, run.TaskRevision, run.BudgetAccountID = task.Task.ID, task.Task.CurrentRevision, task.Budget.ID
		} else {
			run.TaskIntake = true
		}
		run.PolicyVersion = 1
		if err := s.projects.EnsureRunAuthorization(tx, &run, messageID); err != nil {
			return err
		}
		// The Source foreign key needs the Run row, while the startup manifest
		// must already include the Source. Publish and bind in this transaction.
		if err := tx.InsertRun(run, key, hash); err != nil {
			return err
		}
		if err := tx.PublishSourceImports(run, in.SourceIDs); err != nil {
			return err
		}
		if run.TaskID != "" {
			manifest, err := s.projects.PrepareContextInTx(tx, task.Task.ID, task.Task.CurrentRevision, project.ContextHostInput(run))
			if err != nil {
				return err
			}
			run.ContextRef = manifest.ID
			run.InputDigest = project.ContextInputDigest(task, manifest, messageID, in)
		}
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		message := w.Message{ID: messageID, ThreadID: th.ID, RunID: run.ID, Role: "user", Content: in.Message, Status: "completed", CreatedAt: w.Now(), AgentName: "你"}
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return tx.AppendEvent(run.ID, "", "", "run_queued", run)
	}()
	return run, err
}

// visibleRun keeps every child API from exposing a soft-deleted thread.
func visibleRun(tx *store.Tx, id string) (w.Run, error) {
	run, err := tx.Run(id)
	if err != nil {
		return run, err
	}
	if _, err := tx.Thread(run.ThreadID); err != nil {
		return w.Run{}, err
	}
	return run, nil
}

func (s *Server) run(rw http.ResponseWriter, r *http.Request) {
	var run w.Run
	var steps []w.Step
	var attempts []w.Attempt
	var cursor int64
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		run, err = visibleRun(tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		steps, err = tx.Steps(run.ID)
		if err != nil {
			return err
		}
		attempts, err = tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		cursor, err = tx.Cursor(run.ID)
		return err
	})
	respond(rw, r, RunView{Run: run, Steps: steps, Attempts: attempts, Cursor: cursor}, err)
}

func (s *Server) runContext(rw http.ResponseWriter, r *http.Request) {
	var view ContextView
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		run, err := visibleRun(tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if run.ContextRef == "" {
			return &RequestError{404, "context_unavailable", "该历史执行段没有项目上下文清单"}
		}
		view.Manifest, err = tx.ContextManifest(run.ContextRef)
		if err != nil {
			return err
		}
		if view.Manifest.TaskID != run.TaskID || view.Manifest.TaskRevision != run.TaskRevision {
			return w.ErrConflict
		}
		view.Appended, err = tx.ContextScopeEntries(run.ContextRef)
		if err != nil {
			return err
		}
		view.Receipts, err = tx.ReadReceipts(run.ContextRef)
		return err
	})
	respond(rw, r, view, err)
}

func (s *Server) cancel(rw http.ResponseWriter, r *http.Request) {
	if err := s.visibleRunRead(r, r.PathValue("id")); err != nil {
		respond(rw, r, nil, err)
		return
	}
	result, err := s.engine.Cancel(r.Context(), r.PathValue("id"))
	respond(rw, r, result, err)
}

func (s *Server) visibleRunRead(r *http.Request, id string) error {
	return s.store.Read(r.Context(), func(tx *store.Tx) error {
		_, err := visibleRun(tx, id)
		return err
	})
}

func (s *Server) resume(rw http.ResponseWriter, r *http.Request) {
	if err := s.visibleRunRead(r, r.PathValue("id")); err != nil {
		respond(rw, r, nil, err)
		return
	}
	result, err := s.engine.Resume(r.Context(), r.PathValue("id"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	writeJSON(rw, 202, result)
}

// RetryInput selects the exact failed member step; empty keeps legacy clients compatible.
type RetryInput struct {
	StepID string `json:"step_id,omitempty"`
}

// retry queues a linked execution; a member retry replaces that step's
// unfinished reply while the source run, steps and attempts stay terminal.
func (s *Server) retry(rw http.ResponseWriter, r *http.Request) {
	if err := s.visibleRunRead(r, r.PathValue("id")); err != nil {
		respond(rw, r, nil, err)
		return
	}
	var in RetryInput
	if !decode(rw, r, &in) {
		return
	}
	var result w.Run
	var err error
	if in.StepID != "" {
		result, err = s.engine.RetryStep(r.Context(), r.PathValue("id"), in.StepID, r.Header.Get("Idempotency-Key"))
	} else {
		result, err = s.engine.Retry(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	writeJSON(rw, 202, result)
}
