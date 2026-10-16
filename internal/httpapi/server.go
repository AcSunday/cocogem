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
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Config contains public capabilities and server-only dependencies.
type Config struct {
	Host             string
	WebDir           string
	Models           []string
	Roots            []string
	Demo             bool
	ModelConfigured  bool
	SearchConfigured bool
	Model            model.Model
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
	config      Config
	store       *store.Store
	engine      *workflow.Engine
	auth        *authentication
	mu          sync.Mutex
	proposals   map[string]proposal
	routerSlots chan struct{}
	verified    map[string]string
}

// New validates static resources and configures the finite API surface.
func New(config Config, db *store.Store, engine *workflow.Engine) (*Server, error) {
	if _, err := os.Stat(filepath.Join(config.WebDir, "index.html")); err != nil {
		return nil, errors.New("frontend build missing: run make build")
	}
	s := &Server{config: config, store: db, engine: engine, auth: newAuthentication(), proposals: map[string]proposal{}, routerSlots: make(chan struct{}, 2), verified: map[string]string{}}
	return s, nil
}

// BootstrapToken is shown only in the local startup URL fragment.
func (s *Server) BootstrapToken() string { return s.auth.bootstrap }

// Handler serves the API and SPA from the same origin.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bootstrap", s.bootstrap)
	mux.HandleFunc("GET /api/v1/system", s.system)
	mux.HandleFunc("POST /api/v1/system/verify", s.verify)
	mux.HandleFunc("GET /api/v1/workspaces", s.workspaces)
	mux.HandleFunc("POST /api/v1/workspaces", s.createWorkspace)
	mux.HandleFunc("PATCH /api/v1/workspaces/{id}", s.updateWorkspace)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/agents", s.agents)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/agents", s.createAgent)
	mux.HandleFunc("PATCH /api/v1/agents/{id}", s.updateAgent)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/threads", s.threads)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/threads", s.createThread)
	mux.HandleFunc("GET /api/v1/threads/{id}", s.thread)
	mux.HandleFunc("PATCH /api/v1/threads/{id}", s.updateThread)
	mux.HandleFunc("POST /api/v1/threads/{id}/routing-preview", s.preview)
	mux.HandleFunc("POST /api/v1/threads/{id}/runs", s.submit)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.run)
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", s.cancel)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", s.events)
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
	cookie, _ := r.Cookie("cocogem_session")
	s.auth.mu.Lock()
	csrf := s.auth.sessions[cookie.Value]
	s.auth.mu.Unlock()
	state := func(configured bool) string {
		if s.config.Demo {
			return "verified"
		}
		if configured {
			return "configured"
		}
		return "not_configured"
	}
	s.mu.Lock()
	verified := map[string]string{}
	for k, v := range s.verified {
		verified[k] = v
	}
	s.mu.Unlock()
	writeJSON(rw, 200, SystemInfo{Demo: s.config.Demo, Models: append([]string{}, s.config.Models...), Roots: append([]string{}, s.config.Roots...), ModelStatus: state(s.config.ModelConfigured), SearchStatus: state(s.config.SearchConfigured), Verified: verified, CSRFToken: csrf, Limits: map[string]int{"members": 4, "queued_runs": 20, "tool_turns": 12}})
}
func (s *Server) verify(rw http.ResponseWriter, r *http.Request) {
	if s.config.Model == nil {
		writeError(rw, r, 503, "model_unavailable", "模型尚未配置")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_, err := s.config.Model.Complete(ctx, model.Request{Model: s.config.Models[0], System: "Reply with OK only.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Connection test using public text only."}}}}, MaxTokens: 16}, func(string) error { return nil })
	if err != nil {
		respond(rw, r, nil, errors.New("模型连接验证失败，请检查服务端配置"))
		return
	}
	s.mu.Lock()
	s.verified["model"] = w.Now()
	s.mu.Unlock()
	writeJSON(rw, 200, map[string]any{"model": "verified", "verified_at": w.Now()})
}

func (s *Server) workspaces(rw http.ResponseWriter, r *http.Request) {
	var values []w.Workspace
	err := s.store.Read(r.Context(), func(tx *store.Tx) error { var err error; values, err = tx.Workspaces(); return err })
	respond(rw, r, map[string]any{"items": values}, err)
}
func (s *Server) validateRoot(root string) error {
	if root == "" {
		return nil
	}
	if !slices.Contains(s.config.Roots, root) {
		return errors.New("资料目录不在服务端允许范围内")
	}
	return nil
}
func (s *Server) createWorkspace(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Name           string `json:"name"`
		Description    string `json:"description"`
		Root           string `json:"root"`
		ConsentVersion int    `json:"consent_version"`
	}
	if !decode(rw, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 100 || len(in.Description) > 4000 {
		respond(rw, r, nil, errors.New("工作空间名称或说明无效"))
		return
	}
	if err := s.validateRoot(in.Root); err != nil {
		respond(rw, r, nil, err)
		return
	}
	ws := w.Workspace{ID: w.NewID(), Name: in.Name, Description: in.Description, Root: in.Root, ConsentVersion: in.ConsentVersion, Revision: 1, CreatedAt: w.Now()}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for i, name := range []string{"布偶猫", "缅因猫", "橘猫"} {
			handles := []string{"ragdoll", "maine", "orange"}
			roles := []string{"研究资料，整理证据与不确定性。", "独立质疑结论，核对依据与遗漏。", "组织讨论，汇总观点并保留分歧。"}
			a := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: handles[i], Aliases: []string{}, RoleDescription: roles[i], Model: s.config.Models[0], Tools: tools.Names, Revision: 1}
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		return nil
	})
	respond(rw, r, ws, err)
}
func (s *Server) updateWorkspace(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Name           string `json:"name"`
		Description    string `json:"description"`
		Root           string `json:"root"`
		ConsentVersion int    `json:"consent_version"`
	}
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
		if err := s.validateRoot(in.Root); err != nil {
			return err
		}
		if ws.Root != in.Root || ws.ConsentVersion != in.ConsentVersion {
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
		ws.Root = in.Root
		ws.ConsentVersion = in.ConsentVersion
		ws.Revision++
		return tx.SaveWorkspace(ws)
	})
	respond(rw, r, ws, err)
}
func (s *Server) agents(rw http.ResponseWriter, r *http.Request) {
	var agents []w.Agent
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Workspace(r.PathValue("id")); err != nil {
			return err
		}
		var err error
		agents, err = tx.Agents(r.PathValue("id"))
		return err
	})
	respond(rw, r, map[string]any{"items": agents}, err)
}
func (s *Server) createAgent(rw http.ResponseWriter, r *http.Request) {
	var a w.Agent
	if !decode(rw, r, &a) {
		return
	}
	a.ID = w.NewID()
	a.WorkspaceID = r.PathValue("id")
	a.Revision = 1
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Workspace(a.WorkspaceID); err != nil {
			return err
		}
		peers, err := tx.Agents(a.WorkspaceID)
		if err != nil {
			return err
		}
		if err := w.ValidateAgent(a, peers, s.config.Models, tools.Names); err != nil {
			return err
		}
		return tx.SaveAgent(a)
	})
	respond(rw, r, a, err)
}
func (s *Server) updateAgent(rw http.ResponseWriter, r *http.Request) {
	var a w.Agent
	if !decode(rw, r, &a) {
		return
	}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		old, err := tx.Agent(r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := revision(r, old.Revision); err != nil {
			return err
		}
		a.ID = old.ID
		a.WorkspaceID = old.WorkspaceID
		a.Revision = old.Revision + 1
		peers, err := tx.Agents(old.WorkspaceID)
		if err != nil {
			return err
		}
		if err := w.ValidateAgent(a, peers, s.config.Models, tools.Names); err != nil {
			return err
		}
		return tx.SaveAgent(a)
	})
	respond(rw, r, a, err)
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
func (s *Server) thread(rw http.ResponseWriter, r *http.Request) {
	var th w.Thread
	var messages []w.Message
	var runs []w.Run
	var sources []w.Source
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		th, err = tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		messages, err = tx.Messages(th.ID)
		if err != nil {
			return err
		}
		runs, err = tx.Runs(th.ID)
		if err != nil {
			return err
		}
		sources, err = tx.Sources(th.ID)
		return err
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	page, err := paginate(r, messages)
	respond(rw, r, map[string]any{"thread": th, "messages": page, "runs": runs, "sources": sources}, err)
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
		ws, err := tx.Workspace(thread.WorkspaceID)
		if err != nil {
			return err
		}
		if !s.config.Demo && ws.ConsentVersion < 1 {
			return &RequestError{403, "consent_required", "请先在工作空间设置确认资料外发范围"}
		}
		agents, err = tx.Agents(thread.WorkspaceID)
		return err
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	route, err := workflow.ResolveRouting(r.Context(), s.config.Model, s.config.Demo, in, thread, agents)
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
		if !s.config.Demo && !s.config.ModelConfigured {
			return &RequestError{503, "model_unavailable", "真实模型尚未配置"}
		}
		if !s.config.Demo && ws.ConsentVersion < 1 {
			return &RequestError{403, "consent_required", "工作空间尚未确认资料外发范围"}
		}
		if p.Routing.Workflow == "research" && in.ResearchScope == "web_discovery" && !s.engine.Web.SearchEnabled() {
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
		run = w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Input: in, Routing: p.Routing, Status: "queued", CreatedAt: w.Now(), Demo: s.config.Demo}
		steps, err := workflow.Plan(run.ID, in, agents)
		if err != nil {
			return err
		}
		for _, step := range steps {
			a := agents[slices.IndexFunc(agents, func(a w.Agent) bool { return a.ID == step.AgentID })]
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
		cursor, err = tx.Cursor(run.ID)
		return err
	})
	respond(rw, r, map[string]any{"run": run, "steps": steps, "cursor": cursor}, err)
}
func (s *Server) cancel(rw http.ResponseWriter, r *http.Request) {
	result, err := s.engine.Cancel(r.Context(), r.PathValue("id"))
	respond(rw, r, result, err)
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
			sources, err = tx.Sources(a.ThreadID)
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
	if a.Demo {
		fmt.Fprint(rw, "> 离线演示：未验证真实模型效果。\n\n")
	}
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
