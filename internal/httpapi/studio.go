package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// StudioSnapshot aggregates live workspace activity for the Pixel Cat R&D
// Studio view in a single consistent read transaction.
type StudioSnapshot struct {
	WorkspaceID        string                `json:"workspace_id"`
	WorkspaceName      string                `json:"workspace_name"`
	UpdatedAt          string                `json:"updated_at"`
	Threads            []StudioThread        `json:"threads"`
	Agents             []StudioAgentActivity `json:"agents"`
	Relays             []StudioRelay         `json:"relays"`
	PendingPermissions []StudioPermission    `json:"pending_permissions"`
	Artifacts          []StudioArtifact      `json:"artifacts"`
}

// StudioThread summarizes a workspace conversation for the Studio thread filter.
type StudioThread struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Status             string `json:"status"`
	TaskID             string `json:"task_id,omitempty"`
	ActiveRunID        string `json:"active_run_id,omitempty"`
	PendingPermissions int    `json:"pending_permissions"`
	UpdatedAt          string `json:"updated_at"`
}

// StudioAgentActivity projects one workspace member's live state into a Studio station.
type StudioAgentActivity struct {
	AgentID         string `json:"agent_id"`
	DisplayName     string `json:"display_name"`
	Handle          string `json:"handle"`
	Avatar          string `json:"avatar"`
	Executor        string `json:"executor"`
	Model           string `json:"model"`
	Enabled         bool   `json:"enabled"`
	State           string `json:"state"`
	Station         string `json:"station"`
	ThreadID        string `json:"thread_id,omitempty"`
	ThreadTitle     string `json:"thread_title,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	StepID          string `json:"step_id,omitempty"`
	Role            string `json:"role,omitempty"`
	ActivitySummary string `json:"activity_summary,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

// StudioRelay describes an active or recent peer-to-peer handoff between two members.
type StudioRelay struct {
	FromAgentID string `json:"from_agent_id"`
	ToAgentID   string `json:"to_agent_id"`
	ThreadID    string `json:"thread_id"`
	RunID       string `json:"run_id"`
	StepID      string `json:"step_id"`
	Role        string `json:"role"`
	Status      string `json:"status"`
}

// StudioPermission pairs a pending permission request with display metadata.
type StudioPermission struct {
	Request     w.ToolPermissionRequest `json:"request"`
	ThreadTitle string                  `json:"thread_title"`
	AgentName   string                  `json:"agent_name"`
}

// StudioArtifact summarizes an exact accepted workspace deliverable.
type StudioArtifact struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	ThreadID    string `json:"thread_id"`
	ThreadTitle string `json:"thread_title"`
	RunID       string `json:"run_id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
}

func isActiveRunStatus(status string) bool {
	switch status {
	case "queued", "running", "waiting", "waiting_permission", "pausing":
		return true
	default:
		return false
	}
}

func studioStationForStep(role, status string) (string, string) {
	lower := strings.ToLower(strings.TrimSpace(role))
	if status == "waiting_permission" || status == "waiting" {
		return "waiting_permission", "gate"
	}
	if status == "failed" || status == "interrupted" {
		return "failed", "coding"
	}
	switch {
	case strings.Contains(lower, "coord") || strings.Contains(lower, "plan") || strings.Contains(lower, "synth") || strings.Contains(lower, "summar") || strings.Contains(lower, "汇总") || strings.Contains(lower, "协调"):
		return "planning", "whiteboard"
	case strings.Contains(lower, "review") || strings.Contains(lower, "critic") || strings.Contains(lower, "challeng") || strings.Contains(lower, "audit") || strings.Contains(lower, "复核") || strings.Contains(lower, "质疑"):
		return "reviewing", "review"
	case strings.Contains(lower, "research") || strings.Contains(lower, "search") || strings.Contains(lower, "analy") || strings.Contains(lower, "研究") || strings.Contains(lower, "检索"):
		return "researching", "research"
	default:
		return "coding", "coding"
	}
}

func studioStatePriority(state string) int {
	switch state {
	case "waiting_permission":
		return 6
	case "coding", "reviewing", "researching", "planning":
		return 5
	case "failed":
		return 3
	case "completed":
		return 2
	default:
		return 1
	}
}

func summarizeInstruction(text string) string {
	clean := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(clean)
	if len(runes) > 72 {
		return string(runes[:71]) + "…"
	}
	return clean
}

func studioArtifacts(tx *store.Tx, workspaceID string, threads map[string]w.Thread, runs []w.Run) ([]StudioArtifact, error) {
	judgments, err := tx.WorkspaceCompletedTaskJudgments(workspaceID)
	if err != nil {
		return nil, err
	}
	acceptedByTask := map[string]map[string]bool{}
	for _, judgment := range judgments {
		task := judgment.Task
		if _, visible := threads[task.ThreadID]; !visible || acceptedByTask[task.ID] != nil {
			continue
		}
		refs := map[string]bool{}
		if judgment.Completion.Accepted(task, judgment.Revision) {
			for _, ref := range judgment.Completion.CandidateRefs {
				refs[ref] = true
			}
		}
		acceptedByTask[task.ID] = refs
	}

	artifacts := make([]StudioArtifact, 0, 16)
	for _, run := range runs {
		refs := acceptedByTask[run.TaskID]
		thread, visible := threads[run.ThreadID]
		if len(refs) == 0 || !visible || run.Status != "succeeded" || run.Delivery == nil || thread.TaskID != run.TaskID {
			continue
		}
		for _, ref := range run.Delivery.ArtifactRefs {
			candidate := fmt.Sprintf("artifact:%s@v%d", ref.ID, ref.Version)
			if !refs[candidate] {
				continue
			}
			artifact, err := tx.Artifact(ref.ID, ref.Version)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if artifact.RunID != run.ID || artifact.ThreadID != thread.ID {
				continue
			}
			delete(refs, candidate)
			artifacts = append(artifacts, StudioArtifact{
				ID:          artifact.ID,
				Version:     artifact.Version,
				ThreadID:    thread.ID,
				ThreadTitle: thread.Title,
				RunID:       run.ID,
				Type:        artifact.Type,
				Title:       artifact.Title,
				CreatedAt:   artifact.CreatedAt,
			})
			if len(artifacts) == cap(artifacts) {
				return artifacts, nil
			}
		}
	}
	return artifacts, nil
}

func (s *Server) workspaceStudio(rw http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	var snapshot StudioSnapshot
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		ws, err := tx.Workspace(workspaceID)
		if err != nil {
			return err
		}
		agents, err := tx.Agents(workspaceID)
		if err != nil {
			return err
		}
		threads, err := tx.Threads(workspaceID)
		if err != nil {
			return err
		}
		runs, err := tx.WorkspaceRuns(workspaceID)
		if err != nil {
			return err
		}

		threadByID := make(map[string]w.Thread, len(threads))
		for _, th := range threads {
			threadByID[th.ID] = th
		}

		// Map latest run and active run per thread.
		latestRunByThread := make(map[string]w.Run, len(threads))
		activeRuns := make([]w.Run, 0)
		for _, run := range runs {
			if _, ok := threadByID[run.ThreadID]; !ok {
				continue
			}
			prev, exists := latestRunByThread[run.ThreadID]
			if !exists || run.CreatedAt > prev.CreatedAt {
				latestRunByThread[run.ThreadID] = run
			}
			if isActiveRunStatus(run.Status) {
				activeRuns = append(activeRuns, run)
			}
		}

		// Collect pending permissions across active/recent threads.
		pendingByThread := map[string]int{}
		pendingByAgent := map[string]StudioPermission{}
		pendingList := make([]StudioPermission, 0)
		agentNames := make(map[string]string, len(agents))
		for _, ag := range agents {
			agentNames[ag.ID] = ag.DisplayName
		}

		// Inspect active runs plus the most recent run of up to 12 threads so
		// recently completed handoffs and pending approvals remain visible.
		inspectedRuns := map[string]w.Run{}
		for _, run := range activeRuns {
			inspectedRuns[run.ID] = run
		}
		limitThreads := threads
		if len(limitThreads) > 12 {
			limitThreads = limitThreads[:12]
		}
		for _, th := range limitThreads {
			if run, ok := latestRunByThread[th.ID]; ok {
				inspectedRuns[run.ID] = run
			}
		}

		// Keep deterministic order (newest run first).
		runList := make([]w.Run, 0, len(inspectedRuns))
		for _, run := range inspectedRuns {
			runList = append(runList, run)
		}
		sort.Slice(runList, func(i, j int) bool {
			return runList[i].CreatedAt > runList[j].CreatedAt
		})

		activityByAgent := make(map[string]StudioAgentActivity, len(agents))
		for _, ag := range agents {
			enabled := !ag.Disabled && (ag.Membership == nil || ag.Membership.Enabled)
			activityByAgent[ag.ID] = StudioAgentActivity{
				AgentID:     ag.ID,
				DisplayName: ag.DisplayName,
				Handle:      ag.Handle,
				Avatar:      ag.Avatar,
				Executor:    ag.Executor,
				Model:       ag.Model,
				Enabled:     enabled,
				State:       "idle",
				Station:     "idle",
			}
		}

		relays := make([]StudioRelay, 0)
		seenPerm := map[string]struct{}{}

		for _, run := range runList {
			th := threadByID[run.ThreadID]
			runActive := isActiveRunStatus(run.Status)

			if runActive {
				reqs, err := tx.RunPendingPermissionRequests(run.ID)
				if err != nil {
					return err
				}
				for _, req := range reqs {
					if _, dup := seenPerm[req.ID]; dup {
						continue
					}
					seenPerm[req.ID] = struct{}{}
					pendingByThread[run.ThreadID]++
					agentName := req.MemberName
					if agentName == "" {
						agentName = agentNames[req.MemberID]
					}
					item := StudioPermission{
						Request:     req,
						ThreadTitle: th.Title,
						AgentName:   agentName,
					}
					pendingList = append(pendingList, item)
					if req.MemberID != "" {
						if _, exists := pendingByAgent[req.MemberID]; !exists {
							pendingByAgent[req.MemberID] = item
						}
					}
				}
			}

			steps, err := tx.Steps(run.ID)
			if err != nil {
				return err
			}
			for _, step := range steps {
				if step.FromAgentID != "" && step.AgentID != "" && step.FromAgentID != step.AgentID {
					if runActive || step.Status == "running" || step.Status == "completed" {
						relays = append(relays, StudioRelay{
							FromAgentID: step.FromAgentID,
							ToAgentID:   step.AgentID,
							ThreadID:    run.ThreadID,
							RunID:       run.ID,
							StepID:      step.ID,
							Role:        step.Role,
							Status:      step.Status,
						})
					}
				}
				current, ok := activityByAgent[step.AgentID]
				if !ok {
					continue
				}
				var state, station string
				switch step.Status {
				case "running", "queued":
					state, station = studioStationForStep(step.Role, step.Status)
				case "waiting", "waiting_permission":
					state, station = "waiting_permission", "gate"
				case "failed", "interrupted":
					if runActive || run.ID == latestRunByThread[run.ThreadID].ID {
						state, station = "failed", "coding"
					} else {
						continue
					}
				case "completed":
					if runActive {
						state, station = "completed", "idle"
					} else {
						continue
					}
				default:
					continue
				}
				if studioStatePriority(state) > studioStatePriority(current.State) ||
					(studioStatePriority(state) == studioStatePriority(current.State) && step.StartedAt > current.UpdatedAt) {
					current.State = state
					current.Station = station
					current.ThreadID = run.ThreadID
					current.ThreadTitle = th.Title
					current.RunID = run.ID
					current.StepID = step.ID
					current.Role = step.Role
					current.ActivitySummary = summarizeInstruction(step.Instruction)
					if step.StartedAt != "" {
						current.UpdatedAt = step.StartedAt
					} else {
						current.UpdatedAt = run.CreatedAt
					}
					activityByAgent[step.AgentID] = current
				}
			}
		}

		// Pending permissions always elevate the requesting agent to the gate station.
		for agentID, perm := range pendingByAgent {
			if current, ok := activityByAgent[agentID]; ok {
				current.State = "waiting_permission"
				current.Station = "gate"
				current.ThreadID = perm.Request.ThreadID
				current.ThreadTitle = perm.ThreadTitle
				current.RunID = perm.Request.RunID
				current.StepID = perm.Request.StepID
				current.ActivitySummary = summarizeInstruction(perm.Request.OperationSummary)
				if current.ActivitySummary == "" {
					current.ActivitySummary = summarizeInstruction(perm.Request.Reason)
				}
				if current.ActivitySummary == "" {
					current.ActivitySummary = perm.Request.ToolName
				}
				current.UpdatedAt = perm.Request.CreatedAt
				activityByAgent[agentID] = current
			}
		}

		artifacts, err := studioArtifacts(tx, workspaceID, threadByID, runs)
		if err != nil {
			return err
		}

		studioThreads := make([]StudioThread, 0, len(limitThreads))
		for _, th := range limitThreads {
			status := th.ActiveRunStatus
			if status == "" {
				status = th.LastRunStatus
			}
			if status == "" {
				status = "idle"
			}
			st := StudioThread{
				ID:                 th.ID,
				Title:              th.Title,
				Status:             status,
				TaskID:             th.TaskID,
				PendingPermissions: pendingByThread[th.ID],
				UpdatedAt:          th.UpdatedAt,
			}
			if run, ok := latestRunByThread[th.ID]; ok && isActiveRunStatus(run.Status) {
				st.ActiveRunID = run.ID
				st.Status = run.Status
			}
			studioThreads = append(studioThreads, st)
		}

		studioAgents := make([]StudioAgentActivity, 0, len(agents))
		for _, ag := range agents {
			if item, ok := activityByAgent[ag.ID]; ok {
				studioAgents = append(studioAgents, item)
			}
		}

		if len(relays) > 12 {
			relays = relays[:12]
		}

		snapshot = StudioSnapshot{
			WorkspaceID:        ws.ID,
			WorkspaceName:      ws.Name,
			UpdatedAt:          w.Now(),
			Threads:            studioThreads,
			Agents:             studioAgents,
			Relays:             relays,
			PendingPermissions: pendingList,
			Artifacts:          artifacts,
		}
		return nil
	})
	respond(rw, r, snapshot, err)
}
