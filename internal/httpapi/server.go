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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
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
	// purgeExpired is a test seam for lifecycle tests; production uses
	// PurgeExpiredThreads.
	purgeExpired func(context.Context) (int, error)
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
	mux.HandleFunc("GET /api/v1/system/model-options", s.modelOptions)
	mux.HandleFunc("GET /api/v1/directories", s.directories)
	mux.HandleFunc("POST /api/v1/system/verify", s.verify)
	mux.HandleFunc("GET /api/v1/workspaces", s.workspaces)
	mux.HandleFunc("POST /api/v1/workspaces", s.createWorkspace)
	mux.HandleFunc("PATCH /api/v1/workspaces/{id}", s.updateWorkspace)
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}", s.deleteWorkspace)
	mux.HandleFunc("GET /api/v1/agents", s.globalAgents)
	mux.HandleFunc("POST /api/v1/agents", s.createGlobalAgent)
	mux.HandleFunc("GET /api/v1/settings", s.settings)
	mux.HandleFunc("PATCH /api/v1/settings", s.updateSettings)
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
	mux.HandleFunc("GET /api/v1/trash", s.trash)
	mux.HandleFunc("POST /api/v1/threads/{id}/restore", s.restoreThread)
	mux.HandleFunc("DELETE /api/v1/threads/{id}/permanent", s.permanentDeleteThread)
	mux.HandleFunc("POST /api/v1/threads/{id}/routing-preview", s.preview)
	mux.HandleFunc("POST /api/v1/threads/{id}/runs", s.submit)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.run)
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /api/v1/runs/{id}/resume", s.resume)
	mux.HandleFunc("POST /api/v1/runs/{id}/retry", s.retry)
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
	return decodeLimit(rw, r, out, 65536)
}

func decodeLimit(rw http.ResponseWriter, r *http.Request, out any, limit int64) bool {
	r.Body = http.MaxBytesReader(rw, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(rw, r, 413, "request_too_large", fmt.Sprintf("请求超过 %d KiB", limit/1024))
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
	if errors.Is(err, workflow.ErrQueueFull) {
		status, code = 429, "queue_full"
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
