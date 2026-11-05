package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (s *Server) preview(rw http.ResponseWriter, r *http.Request) {
	var in w.RunInput
	if !decode(rw, r, &in) {
		return
	}
	action, err := w.NormalizeEngineeringAction(in.Workflow, in.EngineeringAction)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	in.EngineeringAction = action
	if strings.TrimSpace(in.Message) == "" || len([]rune(in.Message)) > 16000 {
		respond(rw, r, nil, errors.New("问题必须为 1–16000 个字符"))
		return
	}
	select {
	case s.routerSlots <- struct{}{}:
		defer func() { <-s.routerSlots }()
	default:
		writeError(rw, r, 429, "routing_busy", "路由解析繁忙，请稍后再试")
		return
	}
	var thread w.Thread
	var agents []w.Agent
	var settings w.Settings
	err = s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		thread, err = tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		_, err = tx.Workspace(thread.WorkspaceID)
		if err != nil {
			return err
		}
		if settings, err = tx.Settings(); err != nil {
			return err
		}
		agents, err = tx.Agents(thread.WorkspaceID)
		return err
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	effective, err := applySettingsDefaults(in, thread, agents, settings)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	route, err := workflow.ResolveRouting(r.Context(), s.config.Model, effective, thread, agents)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	if route.Decision != "clarify" {
		routeAction, actionErr := w.NormalizeEngineeringAction(route.Workflow, in.EngineeringAction)
		if actionErr != nil || routeAction == w.EngineeringDevelop && route.Workflow != "engineering" {
			if actionErr == nil {
				actionErr = errors.New("develop action requires the engineering workflow")
			}
			respond(rw, r, nil, actionErr)
			return
		}
		in.EngineeringAction = routeAction
	}
	if route.Decision == "clarify" {
		writeJSON(rw, 200, map[string]any{"routing": route})
		return
	}
	// Never hand out a proposal whose frozen assignments cannot form a plan.
	if _, err := workflow.Plan("preview", w.RunInput{Workflow: route.Workflow, Targets: route.Targets, Assignments: route.Assignments, EngineeringAction: in.EngineeringAction}, agents); err != nil {
		respond(rw, r, nil, err)
		return
	}
	id := w.NewID()
	s.mu.Lock()
	for key, p := range s.proposals {
		if time.Now().After(p.Expires) {
			delete(s.proposals, key)
		}
	}
	if len(s.proposals) >= 100 {
		s.mu.Unlock()
		writeError(rw, r, 429, "routing_capacity", "待提交建议过多")
		return
	}
	in.ProposalID = ""
	s.proposals[id] = proposal{ThreadID: thread.ID, Input: in, Routing: route, Digest: digest(in), Catalog: digest(routingCatalog(agents)), Expires: time.Now().Add(5 * time.Minute)}
	s.mu.Unlock()
	writeJSON(rw, 200, map[string]any{"proposal_id": id, "routing": route, "expires_in": 300})
}

func (s *Server) submit(rw http.ResponseWriter, r *http.Request) {
	var in w.RunInput
	if !decode(rw, r, &in) {
		return
	}
	action, err := w.NormalizeEngineeringAction(in.Workflow, in.EngineeringAction)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	in.EngineeringAction = action
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 16 || len(key) > 100 {
		respond(rw, r, nil, errors.New("有效的 Idempotency-Key 是必需的"))
		return
	}
	executors := s.refreshExecutors(r.Context())
	hash := digest(in)
	var run w.Run
	err = s.store.Write(r.Context(), func(tx *store.Tx) error {
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
		ws, err := tx.Workspace(th.WorkspaceID)
		if err != nil {
			return err
		}
		agents, err := tx.Agents(ws.ID)
		if err != nil {
			return err
		}
		s.mu.Lock()
		p, ok := s.proposals[in.ProposalID]
		s.mu.Unlock()
		original := in
		original.ProposalID = ""
		if !ok || p.ThreadID != th.ID || time.Now().After(p.Expires) || p.Digest != digest(original) || p.Catalog != digest(routingCatalog(agents)) {
			return &RequestError{409, "routing_proposal_stale", "路由建议已失效，请重新解析收件人"}
		}
		if !s.modelConfigured(executors) {
			return &RequestError{503, "model_unavailable", "没有可用的本机 CLI，请检查 CLI 安装和现有连接配置"}
		}
		if in.ResearchScope == "web_discovery" && !s.engine.Web.SearchEnabled() {
			return &RequestError{503, "search_unavailable", "在线搜索尚未配置"}
		}
		if (p.Routing.Workflow == "code_analysis" || p.Routing.Workflow == "engineering") && ws.Root == "" {
			return errors.New("代码分析与工程协作需要绑定允许的资料目录")
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
			if imported.ParseStatus != "parsed" || imported.Content == "" {
				return &RequestError{400, "source_not_parsed", "解析失败的资料不能进入任务，请查看失败原因或删除后重试"}
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
		in.Workflow = p.Routing.Workflow
		in.Targets = p.Routing.Targets
		// Global defaults were resolved and validated with the accepted routing.
		in.Assignments = p.Routing.Assignments
		routeAction, actionErr := w.NormalizeEngineeringAction(in.Workflow, in.EngineeringAction)
		if actionErr != nil || routeAction == w.EngineeringDevelop && in.Workflow != "engineering" {
			if actionErr == nil {
				actionErr = errors.New("develop action requires the engineering workflow")
			}
			return actionErr
		}
		in.EngineeringAction = routeAction
		var policy *w.ExecutionPolicy
		if s.engine != nil {
			limits := s.engine.Limits
			policy = &w.ExecutionPolicy{RunTimeout: limits.RunTimeout, StepTimeout: limits.StepTimeout, FirstResponse: limits.FirstResponse, Stall: limits.Stall, StallAction: limits.StallAction}
		}
		// A new run is explicitly under the delivery contract from acceptance,
		// so a queued cancellation or early failure never looks like legacy
		// history that exposes drafts. Only a successful finish fills it.
		run = w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Input: in, Routing: p.Routing, ExecutionPolicy: policy, Status: "queued", CreatedAt: w.Now(), CoordinatorID: th.CoordinatorID, SkillVersions: map[string]string{}, WriteBranch: in.EngineeringAction == w.EngineeringDevelop, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
		for _, skill := range skills.List() {
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
		if in.Workflow == "free" || in.Workflow == "" {
			for _, a := range agents {
				if !a.Disabled && slices.Contains(th.AgentIDs, a.ID) && !slices.ContainsFunc(run.Agents, func(x w.Agent) bool { return x.ID == a.ID }) {
					run.Agents = append(run.Agents, a)
				}
			}
			run.SkillSnapshot, err = skills.SnapshotHash(ws.SkillIDs)
			if err != nil {
				return err
			}
		}
		th.UpdatedAt = w.Now()
		th.Revision++
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		messageID := w.NewID()
		task, err := s.projects.EnsureThreadTask(tx, th, in, messageID, run.ID)
		if err != nil {
			return err
		}
		th.TaskID = task.Task.ID
		run.TaskID = task.Task.ID
		run.TaskRevision = task.Task.CurrentRevision
		run.PolicyVersion = 1
		run.BudgetAccountID = task.Budget.ID
		if err := s.projects.EnsureRunAuthorization(tx, &run, task.Revision.SourceMessageID); err != nil {
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
		manifest, err := s.projects.PrepareContextInTx(tx, task.Task.ID, task.Task.CurrentRevision, project.ContextHostInput(run))
		if err != nil {
			return err
		}
		run.ContextRef = manifest.ID
		run.InputDigest = project.ContextInputDigest(task, manifest, messageID, in)
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
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	writeJSON(rw, 202, run)
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

// retry queues a new run from a failed or canceled run without re-routing the
// original request. The source run stays terminal and linked by input.
func (s *Server) retry(rw http.ResponseWriter, r *http.Request) {
	if err := s.visibleRunRead(r, r.PathValue("id")); err != nil {
		respond(rw, r, nil, err)
		return
	}
	result, err := s.engine.Retry(r.Context(), r.PathValue("id"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	writeJSON(rw, 202, result)
}
