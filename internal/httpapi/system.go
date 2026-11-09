package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

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
		parallel := s.engine.MaxParallelRuns
		if parallel <= 0 {
			parallel = workflow.DefaultMaxParallelRuns
		}
		limits["parallel_runs"] = parallel
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
	if err := w.ValidateExecutionOptions(in.Executor, in.ContextSize, in.ThinkingLevel); err != nil {
		respond(rw, r, nil, err)
		return
	}
	if in.Executor == "gemini" && in.ThinkingLevel != "" && in.Model == "default" {
		respond(rw, r, nil, errors.New("Gemini thinking_level requires an explicit model"))
		return
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
	_, err := s.config.Model.Complete(ctx, model.Request{Executor: in.Executor, Model: in.Model, ContextSize: in.ContextSize, ThinkingLevel: in.ThinkingLevel, System: "Reply with OK only. Do not call tools.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Connection test using public text only."}}}}, MaxTokens: 16}, func(string) error { return nil })
	if err != nil {
		respond(rw, r, nil, fmt.Errorf("CLI 验证失败：%w", err))
		return
	}
	key := verificationKey(in)
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
	writeJSON(rw, 200, VerifyReply{Executor: in.Executor, Model: in.Model, ContextSize: in.ContextSize, ThinkingLevel: in.ThinkingLevel, VerifiedAt: at, Executors: executors})
}

func verificationKey(in VerifyInput) string {
	key := in.Executor + ":" + in.Model
	if in.ContextSize > 0 {
		key += fmt.Sprintf(":context=%d", in.ContextSize)
	}
	if in.ThinkingLevel != "" {
		key += ":thinking=" + in.ThinkingLevel
	}
	return key
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
