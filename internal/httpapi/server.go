package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Config contains public capabilities and server-only dependencies.
type Config struct {
	Host             string
	AuthFile         string
	WebDir           string
	Models           []string
	Roots            []string
	ModelConfigured  bool
	SearchConfigured bool
	SearchProvider   string
	Model            model.Model
	Executors        []ExecutorInfo
	Discover         func(context.Context) []ExecutorInfo
	Limits           map[string]int
}
type proposal struct {
	ThreadID string
	Input    w.RunInput
	Routing  workflow.Routing
	Digest   string
	Catalog  string
	Expires  time.Time
}

// Server owns routing previews and local authentication, never provider credentials.
type Server struct {
	config          Config
	store           *store.Store
	engine          *workflow.Engine
	auth            *authentication
	mu              sync.Mutex
	proposals       map[string]proposal
	routerSlots     chan struct{}
	verified        map[string]string
	verifiedProgram map[string]string
}

// New validates static resources and configures the finite API surface.
func New(config Config, db *store.Store, engine *workflow.Engine) (*Server, error) {
	if _, err := os.Stat(filepath.Join(config.WebDir, "index.html")); err != nil {
		return nil, errors.New("frontend build missing: run make build")
	}
	auth, err := newAuthentication(config.AuthFile)
	if err != nil {
		return nil, fmt.Errorf("initialize local authentication: %w", err)
	}
	s := &Server{config: config, store: db, engine: engine, auth: auth, proposals: map[string]proposal{}, routerSlots: make(chan struct{}, 2), verified: map[string]string{}, verifiedProgram: map[string]string{}}
	return s, nil
}

// Handler serves the API and SPA from the same origin.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/status", s.authStatus)
	mux.HandleFunc("POST /auth/setup", s.authenticate)
	mux.HandleFunc("POST /auth/login", s.authenticate)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/system", s.system)
	mux.HandleFunc("GET /api/v1/directories", s.directories)
	mux.HandleFunc("POST /api/v1/system/verify", s.verify)
	mux.HandleFunc("GET /api/v1/workspaces", s.workspaces)
	mux.HandleFunc("POST /api/v1/workspaces", s.createWorkspace)
	mux.HandleFunc("PATCH /api/v1/workspaces/{id}", s.updateWorkspace)
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}", s.deleteWorkspace)
	mux.HandleFunc("GET /api/v1/agents", s.globalAgents)
	mux.HandleFunc("POST /api/v1/agents", s.createGlobalAgent)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/agents", s.agents)
	mux.HandleFunc("PUT /api/v1/workspaces/{id}/members/{agentID}", s.linkMember)
	mux.HandleFunc("PATCH /api/v1/workspaces/{id}/members/{agentID}", s.updateMember)
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}/members/{agentID}", s.unlinkMember)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/agents", s.createAgent)
	mux.HandleFunc("PATCH /api/v1/agents/{id}", s.updateAgent)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/threads", s.threads)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/threads", s.createThread)
	mux.HandleFunc("GET /api/v1/threads/{id}", s.thread)
	mux.HandleFunc("PATCH /api/v1/threads/{id}", s.updateThread)
	mux.HandleFunc("DELETE /api/v1/threads/{id}", s.deleteThread)
	mux.HandleFunc("POST /api/v1/threads/{id}/routing-preview", s.preview)
	mux.HandleFunc("POST /api/v1/threads/{id}/runs", s.submit)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.run)
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /api/v1/runs/{id}/resume", s.resume)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.events)
	mux.HandleFunc("GET /api/v1/runs/{id}/event-history", s.eventHistory)
	mux.HandleFunc("GET /api/v1/runs/{id}/messages/{messageID}/content", s.messageContent)
	mux.HandleFunc("GET /api/v1/sources/{id}", s.source)
	mux.HandleFunc("GET /api/v1/threads/{id}/artifacts", s.artifacts)
	mux.HandleFunc("GET /api/v1/artifacts/{id}/versions/{version}", s.artifact)
	mux.HandleFunc("PATCH /api/v1/artifacts/{id}/versions/{version}", s.feedback)
	mux.HandleFunc("GET /api/v1/artifacts/{id}/versions/{version}/export", s.export)
	mux.HandleFunc("/api/", func(rw http.ResponseWriter, r *http.Request) { writeError(rw, r, 404, "not_found", "接口不存在") })
	files := http.FileServer(http.Dir(s.config.WebDir))
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			writeError(rw, r, 405, "method_not_allowed", "不支持的请求方法")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/favicon.svg" {
			files.ServeHTTP(rw, r)
			return
		}
		http.ServeFile(rw, r, filepath.Join(s.config.WebDir, "index.html"))
	})
	return s.middleware(mux)
}

func writeJSON(rw http.ResponseWriter, status int, value any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(value)
}
func writeError(rw http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(rw, status, map[string]any{"error": map[string]any{"code": code, "message": message, "request_id": w.NewID(), "details": map[string]any{}}})
}
func decode(rw http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(rw, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(rw, r, 413, "request_too_large", "请求超过 64 KiB")
		} else {
			writeError(rw, r, 400, "invalid_json", "请求字段或 JSON 格式无效")
		}
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeError(rw, r, 400, "invalid_json", "请求必须是单个 JSON 对象")
		return false
	}
	return true
}
func respond(rw http.ResponseWriter, r *http.Request, value any, err error) {
	if err == nil {
		writeJSON(rw, 200, value)
		return
	}
	status, code := 400, "invalid_request"
	var requestError *RequestError
	if errors.As(err, &requestError) {
		writeError(rw, r, requestError.Status, requestError.Code, requestError.Message)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		status, code = 404, "not_found"
	}
	if errors.Is(err, w.ErrConflict) {
		status, code = 409, "conflict"
	}
	if errors.Is(err, workflow.ErrRecoveryUncertain) {
		status, code = 409, "recovery_uncertain"
	}
	if strings.Contains(err.Error(), "capability_blocked:") {
		status, code = 503, "capability_blocked"
	}
	if strings.Contains(err.Error(), "connection_blocked:") {
		status, code = 503, "connection_blocked"
	}
	if strings.Contains(err.Error(), "connection_changed:") {
		status, code = 409, "connection_changed"
	}
	if errors.Is(err, errRevision) {
		status, code = 412, "revision_conflict"
	}
	writeError(rw, r, status, code, err.Error())
}

var errRevision = errors.New("配置已更新，请刷新后重新编辑")

// RequestError keeps actionable API status independent of internal error strings.
type RequestError struct {
	Status  int
	Code    string
	Message string
}

func (e *RequestError) Error() string { return e.Message }

func revision(r *http.Request, expected int) error {
	if r.Header.Get("If-Match") != strconv.Itoa(expected) {
		return errRevision
	}
	return nil
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Server) system(rw http.ResponseWriter, r *http.Request) {
	s.auth.mu.Lock()
	session, _ := s.auth.session(r)
	s.auth.mu.Unlock()
	state := func(configured bool) string {
		if configured {
			return "configured"
		}
		return "not_configured"
	}
	executors := s.refreshExecutors(r.Context())
	s.mu.Lock()
	filterVerifications(s.verified, s.verifiedProgram, executors)
	verified := map[string]string{}
	for k, v := range s.verified {
		verified[k] = v
	}
	limits := map[string]int{"members": 4, "queued_runs": 20, "handoffs": 12, "steps": 32, "handoff_depth": 4}
	if s.engine != nil {
		limits["run_seconds"] = int(s.engine.Limits.RunTimeout.Seconds())
		limits["step_seconds"] = int(s.engine.Limits.StepTimeout.Seconds())
		limits["first_response_seconds"] = int(s.engine.Limits.FirstResponse.Seconds())
		limits["stall_seconds"] = int(s.engine.Limits.Stall.Seconds())
	}
	for key, value := range s.config.Limits {
		// tool_calls is retired; an older overlay must not advertise it again.
		if key == "tool_calls" {
			continue
		}
		limits[key] = value
	}
	executorCopy := append([]ExecutorInfo{}, executors...)
	for i := range executorCopy {
		for key := range verified {
			if strings.HasPrefix(key, executorCopy[i].ID+":") {
				executorCopy[i].Protocol = "verified"
			}
		}
	}
	s.mu.Unlock()
	writeJSON(rw, 200, SystemInfo{Models: append([]string{}, s.config.Models...), Roots: append([]string{}, s.config.Roots...), ModelStatus: state(s.modelConfigured(executorCopy)), SearchStatus: state(s.config.SearchConfigured), SearchProvider: s.config.SearchProvider, Verified: verified, CSRFToken: session.csrf, Limits: limits, Executors: executorCopy, Skills: skills.List()})
}

func (s *Server) refreshExecutors(ctx context.Context) []ExecutorInfo {
	if s.config.Discover == nil {
		return s.executorSnapshot()
	}
	discovered := s.config.Discover(ctx)
	s.mu.Lock()
	s.config.Executors = discovered
	s.mu.Unlock()
	return discovered
}

func (s *Server) modelConfigured(executors []ExecutorInfo) bool {
	if s.config.ModelConfigured {
		return true
	}
	for _, executor := range executors {
		if executor.Installed && executor.Supported {
			return true
		}
	}
	return false
}
func (s *Server) verify(rw http.ResponseWriter, r *http.Request) {
	var in VerifyInput
	if !decode(rw, r, &in) {
		return
	}
	if s.config.Model == nil {
		writeError(rw, r, 503, "model_unavailable", "执行器尚不可用")
		return
	}
	if in.Model == "" {
		in.Model = "default"
	}
	s.refreshExecutors(r.Context())
	if len(s.executorSnapshot()) > 0 {
		if err := s.validateExecutor(in.Executor); err != nil {
			respond(rw, r, nil, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	_, err := s.config.Model.Complete(ctx, model.Request{Executor: in.Executor, Model: in.Model, System: "Reply with OK only. Do not call tools.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Connection test using public text only."}}}}, MaxTokens: 16}, func(string) error { return nil })
	if err != nil {
		respond(rw, r, nil, fmt.Errorf("CLI 验证失败：%w", err))
		return
	}
	key := in.Executor + ":" + in.Model
	if in.Executor == "" {
		key = "model"
	}
	at := w.Now()
	s.mu.Lock()
	if s.verified == nil {
		s.verified = map[string]string{}
	}
	if s.verifiedProgram == nil {
		s.verifiedProgram = map[string]string{}
	}
	s.verified[key] = at
	s.verifiedProgram[key] = ""
	for i := range s.config.Executors {
		if s.config.Executors[i].ID == in.Executor {
			s.config.Executors[i].Protocol = "verified"
			s.config.Executors[i].Reason = ""
			s.verifiedProgram[key] = executorSignature(s.config.Executors[i])
		}
	}
	executors := append([]ExecutorInfo{}, s.config.Executors...)
	s.mu.Unlock()
	writeJSON(rw, 200, VerifyReply{Executor: in.Executor, Model: in.Model, VerifiedAt: at, Executors: executors})
}

func executorSignature(item ExecutorInfo) string {
	return item.Version + "|" + item.Adapter + "|" + item.Isolation + "|" + item.Identity
}

// filterVerifications drops a connection result once its program or adapter changes.
func filterVerifications(verified, programs map[string]string, executors []ExecutorInfo) {
	current := map[string]string{}
	for _, item := range executors {
		current[item.ID] = executorSignature(item)
	}
	for key := range verified {
		executor, _, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		signature, exists := current[executor]
		stored := programs[key]
		if !exists || (stored != "" && stored != signature) {
			delete(verified, key)
			delete(programs, key)
		}
	}
}

func (s *Server) executorSnapshot() []ExecutorInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ExecutorInfo{}, s.config.Executors...)
}

func (s *Server) validateExecutor(id string) error {
	executors := s.executorSnapshot()
	if len(executors) == 0 {
		return nil
	}
	for _, executor := range executors {
		if executor.ID == id {
			if !executor.Installed {
				return &RequestError{503, "executor_unavailable", "所选 CLI 尚未安装"}
			}
			if !executor.Supported {
				return &RequestError{503, "executor_unsupported", executor.Reason}
			}
			return nil
		}
	}
	return &RequestError{400, "executor_required", "请选择本机 CLI；历史 API 配置需要在成员设置中明确切换"}
}

func (s *Server) workspaces(rw http.ResponseWriter, r *http.Request) {
	var values []w.Workspace
	err := s.store.Read(r.Context(), func(tx *store.Tx) error { var err error; values, err = tx.Workspaces(); return err })
	respond(rw, r, map[string]any{"items": values}, err)
}
func (s *Server) validateRoot(root string) (string, error) {
	return tools.ResolveSourceRoot(root, s.config.Roots)
}
func (s *Server) createWorkspace(rw http.ResponseWriter, r *http.Request) {
	var in WorkspaceInput
	if !decode(rw, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 100 || len(in.Description) > 4000 {
		respond(rw, r, nil, errors.New("工作空间名称或说明无效"))
		return
	}
	root, err := s.validateRoot(in.Root)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	if _, err := skills.Catalogue(in.SkillIDs); err != nil {
		respond(rw, r, nil, err)
		return
	}
	ws := w.Workspace{ID: w.NewID(), Name: in.Name, Description: in.Description, Root: root, SkillIDs: in.SkillIDs, Revision: 1, CreatedAt: w.Now()}
	err = s.store.Write(r.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return s.initializeMembers(tx, ws.ID)
	})
	respond(rw, r, ws, err)
}
func (s *Server) updateWorkspace(rw http.ResponseWriter, r *http.Request) {
	var in WorkspaceInput
	if !decode(rw, r, &in) {
		return
	}
	var ws w.Workspace
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		var err error
		ws, err = tx.Workspace(r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := revision(r, ws.Revision); err != nil {
			return err
		}
		if strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 100 || len(in.Description) > 4000 {
			return errors.New("工作空间名称或说明无效")
		}
		root, err := s.validateRoot(in.Root)
		if err != nil {
			return err
		}
		if ws.Root != root {
			active, err := tx.Active()
			if err != nil {
				return err
			}
			if slices.ContainsFunc(active, func(run w.Run) bool { return run.WorkspaceID == ws.ID }) {
				return w.ErrConflict
			}
		}
		ws.Name = in.Name
		ws.Description = in.Description
		ws.Root = root
		if _, err := skills.Catalogue(in.SkillIDs); err != nil {
			return err
		}
		ws.SkillIDs = in.SkillIDs
		ws.Revision++
		return tx.SaveWorkspace(ws)
	})
	respond(rw, r, ws, err)
}

func queryLimit(r *http.Request, fallback, maximum int) (int, error) {
	text := r.URL.Query().Get("limit")
	if text == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(text)
	if err != nil || value < 1 || value > maximum {
		return 0, errors.New("每页数量无效")
	}
	return value, nil
}

func paginate[T any](r *http.Request, values []T) (map[string]any, error) {
	limit := 30
	if text := r.URL.Query().Get("limit"); text != "" {
		v, err := strconv.Atoi(text)
		if err != nil || v < 1 || v > 100 {
			return nil, errors.New("invalid page limit")
		}
		limit = v
	}
	offset := 0
	if text := r.URL.Query().Get("cursor"); text != "" {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(text)
		if decodeErr != nil {
			return nil, errors.New("invalid cursor")
		}
		v, err := strconv.Atoi(string(decoded))
		if err != nil || v < 0 || v > len(values) {
			return nil, errors.New("invalid cursor")
		}
		offset = v
	}
	end := min(len(values), offset+limit)
	next := ""
	if end < len(values) {
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return map[string]any{"items": values[offset:end], "next_cursor": next}, nil
}
func (s *Server) threads(rw http.ResponseWriter, r *http.Request) {
	var threads []w.Thread
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Workspace(r.PathValue("id")); err != nil {
			return err
		}
		var err error
		threads, err = tx.Threads(r.PathValue("id"))
		return err
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	page, err := paginate(r, threads)
	respond(rw, r, page, err)
}
func validateThread(th w.Thread, agents []w.Agent) error {
	if strings.TrimSpace(th.Title) == "" || len([]rune(th.Title)) > 150 || len(th.AgentIDs) < 1 || len(th.AgentIDs) > 4 {
		return errors.New("会话标题或成员数量无效")
	}
	if !slices.Contains(th.AgentIDs, th.CoordinatorID) {
		return errors.New("协调者必须是会话成员")
	}
	seen := map[string]bool{}
	for _, id := range th.AgentIDs {
		if seen[id] || !slices.ContainsFunc(agents, func(a w.Agent) bool { return a.ID == id && !a.Disabled }) {
			return errors.New("成员重复或不可用")
		}
		seen[id] = true
	}
	if !slices.Contains([]string{"free", "discussion", "research", "code_analysis"}, th.Workflow) {
		return errors.New("不支持的默认流程")
	}
	return nil
}
func (s *Server) createThread(rw http.ResponseWriter, r *http.Request) {
	var th w.Thread
	if !decode(rw, r, &th) {
		return
	}
	th.ID = w.NewID()
	th.WorkspaceID = r.PathValue("id")
	th.Revision = 1
	th.UpdatedAt = w.Now()
	if th.Workflow == "" {
		th.Workflow = "free"
	}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		agents, err := tx.Agents(th.WorkspaceID)
		if err != nil {
			return err
		}
		if err := validateThread(th, agents); err != nil {
			return err
		}
		return tx.SaveThread(th)
	})
	respond(rw, r, th, err)
}
func (s *Server) updateThread(rw http.ResponseWriter, r *http.Request) {
	var th w.Thread
	if !decode(rw, r, &th) {
		return
	}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		old, err := tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := revision(r, old.Revision); err != nil {
			return err
		}
		th.ID = old.ID
		th.WorkspaceID = old.WorkspaceID
		th.Revision = old.Revision + 1
		th.UpdatedAt = w.Now()
		agents, err := tx.Agents(th.WorkspaceID)
		if err != nil {
			return err
		}
		if err := validateThread(th, agents); err != nil {
			return err
		}
		return tx.SaveThread(th)
	})
	respond(rw, r, th, err)
}

// errRunActive keeps deletion from racing a queued or running collaboration.
var errRunActive = &RequestError{409, "run_active", "有协作正在进行，请先停止再删除"}

// errOwnedProcess keeps a recorded child's ownership evidence until it is settled.
var errOwnedProcess = &RequestError{409, "owned_process_alive", "本平台记录的 CLI 进程可能仍在运行，请先继续或核实该任务再删除"}

func (s *Server) ownedProcessAlive(r *http.Request, threadID string) (bool, error) {
	if s.engine == nil {
		return false, nil
	}
	return s.engine.OwnedProcessAlive(r.Context(), threadID)
}

func (s *Server) deleteThread(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if live, liveErr := s.ownedProcessAlive(r, id); liveErr != nil {
		respond(rw, r, nil, liveErr)
		return
	} else if live {
		respond(rw, r, nil, errOwnedProcess)
		return
	}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Thread(id); err != nil {
			return err
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		if slices.ContainsFunc(active, func(run w.Run) bool { return run.ThreadID == id }) {
			return errRunActive
		}
		return tx.DeleteThread(id)
	})
	if err == nil {
		s.dropProposals(func(p proposal) bool { return p.ThreadID == id })
	}
	respond(rw, r, DeleteReply{ID: id}, err)
}

func (s *Server) deleteWorkspace(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.engine != nil {
		var list []w.Thread
		if err := s.store.Read(r.Context(), func(tx *store.Tx) error {
			var err error
			list, err = tx.Threads(id)
			return err
		}); err != nil {
			respond(rw, r, nil, err)
			return
		}
		for _, thread := range list {
			if live, liveErr := s.ownedProcessAlive(r, thread.ID); liveErr != nil {
				respond(rw, r, nil, liveErr)
				return
			} else if live {
				respond(rw, r, nil, errOwnedProcess)
				return
			}
		}
	}
	threads := map[string]bool{}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Workspace(id); err != nil {
			return err
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		if slices.ContainsFunc(active, func(run w.Run) bool { return run.WorkspaceID == id }) {
			return errRunActive
		}
		list, err := tx.Threads(id)
		if err != nil {
			return err
		}
		for _, th := range list {
			threads[th.ID] = true
		}
		return tx.DeleteWorkspace(id)
	})
	if err == nil {
		s.dropProposals(func(p proposal) bool { return threads[p.ThreadID] })
	}
	respond(rw, r, DeleteReply{ID: id}, err)
}

func (s *Server) dropProposals(match func(proposal) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, p := range s.proposals {
		if match(p) {
			delete(s.proposals, key)
		}
	}
}

func (s *Server) thread(rw http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r, store.DefaultMessagePage, store.MaxMessagePage)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var view ThreadView
	err = s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		view.Thread, err = tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		page, err := tx.PagedMessages(view.Thread.ID, r.URL.Query().Get("cursor"), limit)
		if err != nil {
			return err
		}
		view.Messages = Page[w.Message]{Items: page.Items, NextCursor: page.NextCursor}
		view.Runs, err = tx.Runs(view.Thread.ID)
		if err != nil {
			return err
		}
		view.Sources, err = tx.SourceMetadata(view.Thread.ID)
		return err
	})
	if errors.Is(err, store.ErrInvalidCursor) {
		writeError(rw, r, 400, "invalid_cursor", "分页 cursor 无效")
		return
	}
	respond(rw, r, view, err)
}

func (s *Server) preview(rw http.ResponseWriter, r *http.Request) {
	var in w.RunInput
	if !decode(rw, r, &in) {
		return
	}
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
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		thread, err = tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		_, err = tx.Workspace(thread.WorkspaceID)
		if err != nil {
			return err
		}
		agents, err = tx.Agents(thread.WorkspaceID)
		return err
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	route, err := workflow.ResolveRouting(r.Context(), s.config.Model, in, thread, agents)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	if route.Decision == "clarify" {
		writeJSON(rw, 200, map[string]any{"routing": route})
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
	s.proposals[id] = proposal{ThreadID: thread.ID, Input: in, Routing: route, Digest: digest(in), Catalog: digest(agents), Expires: time.Now().Add(5 * time.Minute)}
	s.mu.Unlock()
	writeJSON(rw, 200, map[string]any{"proposal_id": id, "routing": route, "expires_in": 300})
}

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
		if !ok || p.ThreadID != th.ID || time.Now().After(p.Expires) || p.Digest != digest(original) || p.Catalog != digest(agents) {
			return &RequestError{409, "routing_proposal_stale", "路由建议已失效，请重新解析收件人"}
		}
		if !s.modelConfigured(executors) {
			return &RequestError{503, "model_unavailable", "没有可用的本机 CLI，请检查 CLI 安装和现有连接配置"}
		}
		if in.ResearchScope == "web_discovery" && !s.engine.Web.SearchEnabled() {
			return &RequestError{503, "search_unavailable", "在线搜索尚未配置"}
		}
		if p.Routing.Workflow == "code_analysis" && ws.Root == "" {
			return errors.New("代码分析需要绑定允许的资料目录")
		}
		if len(in.SourceURLs) > 10 || len(in.ArtifactRefs) > 5 {
			return errors.New("资料引用过多")
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
		if len(active) >= 21 {
			return &RequestError{429, "queue_full", "执行队列已满"}
		}
		if slices.ContainsFunc(active, func(r w.Run) bool { return r.ThreadID == th.ID }) {
			return w.ErrConflict
		}
		in.Workflow = p.Routing.Workflow
		in.Targets = p.Routing.Targets
		in.Assignments = p.Routing.Assignments
		var policy *w.ExecutionPolicy
		if s.engine != nil {
			limits := s.engine.Limits
			policy = &w.ExecutionPolicy{RunTimeout: limits.RunTimeout, StepTimeout: limits.StepTimeout, FirstResponse: limits.FirstResponse, Stall: limits.Stall, StallAction: limits.StallAction}
		}
		run = w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Input: in, Routing: p.Routing, ExecutionPolicy: policy, Status: "queued", CreatedAt: w.Now(), CoordinatorID: th.CoordinatorID, SkillVersions: map[string]string{}}
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
			if err := w.ValidateAgent(a, nil, s.config.Models, tools.Names); err != nil {
				return err
			}
			if !slices.ContainsFunc(run.Agents, func(x w.Agent) bool { return x.ID == a.ID }) {
				run.Agents = append(run.Agents, a)
			}
			if !slices.Contains(th.AgentIDs, a.ID) {
				th.AgentIDs = append(th.AgentIDs, a.ID)
			}
		}
		if len(th.AgentIDs) > 4 {
			return errors.New("会话不能超过四个成员")
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
		if err := tx.InsertRun(run, key, hash); err != nil {
			return err
		}
		message := w.Message{ID: w.NewID(), ThreadID: th.ID, RunID: run.ID, Role: "user", Content: in.Message, Status: "completed", CreatedAt: w.Now(), AgentName: "你"}
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

func (s *Server) run(rw http.ResponseWriter, r *http.Request) {
	var run w.Run
	var steps []w.Step
	var attempts []w.Attempt
	var cursor int64
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		run, err = tx.Run(r.PathValue("id"))
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
func (s *Server) cancel(rw http.ResponseWriter, r *http.Request) {
	result, err := s.engine.Cancel(r.Context(), r.PathValue("id"))
	respond(rw, r, result, err)
}
func (s *Server) resume(rw http.ResponseWriter, r *http.Request) {
	result, err := s.engine.Resume(r.Context(), r.PathValue("id"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	writeJSON(rw, 202, result)
}
func (s *Server) events(rw http.ResponseWriter, r *http.Request) {
	afterText := r.URL.Query().Get("after")
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		afterText = header
	}
	var after int64
	if afterText != "" {
		v, err := strconv.ParseInt(afterText, 10, 64)
		if err != nil || v < 0 {
			respond(rw, r, nil, errors.New("事件 cursor 无效"))
			return
		}
		after = v
	}
	if err := s.store.Read(r.Context(), func(tx *store.Tx) error { _, err := tx.Run(r.PathValue("id")); return err }); err != nil {
		respond(rw, r, nil, err)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(rw)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		s.auth.mu.Lock()
		_, authenticated := s.auth.session(r)
		s.auth.mu.Unlock()
		if !authenticated {
			return
		}
		wake := s.store.Changed()
		var events []w.Event
		var terminal bool
		err := s.store.Read(r.Context(), func(tx *store.Tx) error {
			run, err := tx.Run(r.PathValue("id"))
			if err != nil {
				return err
			}
			terminal = w.Terminal(run.Status)
			events, err = tx.Events(run.ID, after)
			return err
		})
		if err != nil {
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(20 * time.Second))
		for _, ev := range events {
			b, _ := json.Marshal(ev)
			if _, err := fmt.Fprintf(rw, "id: %d\ndata: %s\n\n", ev.Seq, b); err != nil {
				return
			}
			after = ev.Seq
		}
		if err := controller.Flush(); err != nil {
			return
		}
		if len(events) == 500 {
			continue
		}
		if terminal {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(rw, ": heartbeat\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func (s *Server) eventHistory(rw http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if text := r.URL.Query().Get("after"); text != "" {
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil || value < 0 {
			respond(rw, r, nil, errors.New("事件 cursor 无效"))
			return
		}
		after = value
	}
	limit, err := queryLimit(r, store.DefaultEventLimit, store.MaxEventLimit)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var page store.EventPage
	err = s.store.Read(r.Context(), func(tx *store.Tx) error {
		run, err := tx.Run(r.PathValue("id"))
		if err != nil {
			return err
		}
		page, err = tx.EventPage(run.ID, after, limit)
		return err
	})
	respond(rw, r, page, err)
}

func (s *Server) messageContent(rw http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r, store.DefaultContentChunks, store.MaxContentChunks)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var page store.MessageContentPage
	err = s.store.Read(r.Context(), func(tx *store.Tx) error {
		run, err := tx.Run(r.PathValue("id"))
		if err != nil {
			return err
		}
		message, err := tx.Message(r.PathValue("messageID"))
		if err != nil {
			return err
		}
		// A message is addressable only through its own run and thread; never
		// let a cursor or an ID reach another run's body.
		if message.RunID != run.ID || message.ThreadID != run.ThreadID {
			return store.ErrNotFound
		}
		page, err = tx.MessageContent(message.ID, r.URL.Query().Get("cursor"), limit)
		return err
	})
	if errors.Is(err, store.ErrInvalidCursor) {
		writeError(rw, r, 400, "invalid_cursor", "正文 cursor 无效")
		return
	}
	respond(rw, r, page, err)
}

func (s *Server) source(rw http.ResponseWriter, r *http.Request) {
	var source w.Source
	err := s.store.Read(r.Context(), func(tx *store.Tx) error { var err error; source, err = tx.Source(r.PathValue("id")); return err })
	respond(rw, r, source, err)
}
func (s *Server) artifacts(rw http.ResponseWriter, r *http.Request) {
	var artifacts []w.Artifact
	err := s.store.Read(r.Context(), func(tx *store.Tx) error { var err error; artifacts, err = tx.Artifacts(r.PathValue("id")); return err })
	respond(rw, r, map[string]any{"items": artifacts}, err)
}
func artifactVersion(r *http.Request) (int, error) {
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || v < 1 {
		return 0, errors.New("invalid artifact version")
	}
	return v, nil
}
func (s *Server) artifact(rw http.ResponseWriter, r *http.Request) {
	v, err := artifactVersion(r)
	var a w.Artifact
	if err == nil {
		err = s.store.Read(r.Context(), func(tx *store.Tx) error { var err error; a, err = tx.Artifact(r.PathValue("id"), v); return err })
	}
	respond(rw, r, a, err)
}
func (s *Server) feedback(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Feedback string `json:"feedback"`
	}
	if !decode(rw, r, &in) {
		return
	}
	v, err := artifactVersion(r)
	var a w.Artifact
	if err == nil {
		err = s.store.Write(r.Context(), func(tx *store.Tx) error {
			var err error
			a, err = tx.Artifact(r.PathValue("id"), v)
			if err != nil {
				return err
			}
			if err := revision(r, a.Revision); err != nil {
				return err
			}
			if !slices.Contains([]string{"unreviewed", "accepted", "dismissed"}, in.Feedback) {
				return errors.New("无效的人工标记")
			}
			a.Feedback = in.Feedback
			a.FeedbackAt = w.Now()
			a.Revision++
			return tx.SaveArtifact(a)
		})
	}
	respond(rw, r, a, err)
}
func (s *Server) export(rw http.ResponseWriter, r *http.Request) {
	v, err := artifactVersion(r)
	var a w.Artifact
	var sources []w.Source
	var run w.Run
	if err == nil {
		err = s.store.Read(r.Context(), func(tx *store.Tx) error {
			var err error
			a, err = tx.Artifact(r.PathValue("id"), v)
			if err != nil {
				return err
			}
			run, err = tx.Run(a.RunID)
			if err != nil {
				return err
			}
			sources, err = tx.SourceMetadata(a.ThreadID)
			return err
		})
	}
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	rw.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	rw.Header().Set("Content-Disposition", `attachment; filename="cocogem-report.md"`)
	fmt.Fprintf(rw, "# %s\n\nVersion: %d · %s · Feedback: %s\n\n", a.Title, a.Version, a.CreatedAt, a.Feedback)
	fmt.Fprintf(rw, "## 原始问题\n%s\n\n%s\n\n## 成员\n", run.Input.Message, a.Body)
	if len(a.Findings) > 0 {
		fmt.Fprint(rw, "\n## 结构化发现\n")
		for _, finding := range a.Findings {
			fmt.Fprintf(rw, "\n### %s\n状态：%s · 严重度：%s\n\n%s\n\n来源：%s\n", finding.Title, finding.Status, finding.Severity, finding.Detail, strings.Join(finding.SourceIDs, ", "))
		}
	}
	for _, agent := range run.Agents {
		fmt.Fprintf(rw, "- %s (@%s), model: %s\n", agent.DisplayName, agent.Handle, agent.Model)
	}
	fmt.Fprint(rw, "\n## 来源\n")
	for _, source := range sources {
		if slices.Contains(a.SourceIDs, source.ID) {
			fmt.Fprintf(rw, "- [%s] %s %s %s L%d-%d (%s)\n", source.ID, source.Title, source.URL, source.Path, source.StartLine, source.EndLine, source.CapturedAt)
		}
	}
	fmt.Fprint(rw, "\n## 限制\n")
	for _, limit := range a.Limitations {
		fmt.Fprintf(rw, "- %s\n", limit)
	}
}
