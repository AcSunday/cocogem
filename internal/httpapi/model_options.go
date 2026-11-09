package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
)

type ModelOptions = agent.ModelOptions

// modelOptions reads the CLI's local catalog; it never sends a model prompt.
func (s *Server) modelOptions(rw http.ResponseWriter, r *http.Request) {
	executor, selected := r.URL.Query().Get("executor"), r.URL.Query().Get("model")
	if executor != "opencode" || len(selected) > 150 || strings.HasPrefix(selected, "-") || strings.ContainsAny(selected, "\r\n\x00") {
		respond(rw, r, nil, errors.New("invalid OpenCode model options request"))
		return
	}
	if selected == "" {
		selected = "default"
	}
	provider, ok := s.config.Model.(agent.ModelOptionsProvider)
	if !ok {
		writeError(rw, r, 503, "model_unavailable", "执行器暂不支持模型能力查询")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	options, err := provider.ModelOptions(ctx, executor, selected)
	respond(rw, r, options, err)
}
