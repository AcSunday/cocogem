package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/project"
)

func (s *Server) usageReport(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rng := strings.TrimSpace(q.Get("range"))
	if rng != "" && rng != "today" && rng != "7d" && rng != "30d" && rng != "all" {
		writeError(rw, r, http.StatusBadRequest, "invalid_request", "range must be today, 7d, 30d or all")
		return
	}
	tzOffset := 0
	if raw := strings.TrimSpace(q.Get("tz_offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < -840 || parsed > 840 {
			writeError(rw, r, http.StatusBadRequest, "invalid_request", "tz_offset must be an integer between -840 and 840")
			return
		}
		tzOffset = parsed
	}
	value, err := s.projects.UsageReport(r.Context(), project.UsageQuery{
		Range:           rng,
		WorkspaceID:     strings.TrimSpace(q.Get("workspace_id")),
		Executor:        strings.TrimSpace(q.Get("executor")),
		AgentID:         strings.TrimSpace(q.Get("agent_id")),
		TZOffsetMinutes: tzOffset,
	})
	respond(rw, r, value, err)
}
