package project

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
)

const (
	maxTopUsageTargets    = 10
	maxRecentUsageEntries = 50
)

// UsageQuery scopes the usage dashboard aggregation.
type UsageQuery struct {
	Range           string
	WorkspaceID     string
	Executor        string
	AgentID         string
	TZOffsetMinutes int
}

// UsageTotals summarizes real token counts, cache hits, completeness and any
// natively reported cost across matching attempts.
type UsageTotals struct {
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	ReportedCostUSD     float64 `json:"reported_cost_usd"`
	AttemptsTotal       int     `json:"attempts_total"`
	AttemptsKnown       int     `json:"attempts_known"`
	AttemptsUnknown     int     `json:"attempts_unknown"`
	AttemptsWithCost    int     `json:"attempts_with_cost"`
	AttemptsFailed      int     `json:"attempts_failed"`
}

// UsageBucket is one time-series bar (hourly for today, daily otherwise).
type UsageBucket struct {
	Key                 string  `json:"key"`
	Label               string  `json:"label"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	ReportedCostUSD     float64 `json:"reported_cost_usd"`
	Attempts            int     `json:"attempts"`
}

// UsageBreakdown aggregates consumption by model, executor, member or workspace.
type UsageBreakdown struct {
	Key                 string  `json:"key"`
	Label               string  `json:"label"`
	Secondary           string  `json:"secondary,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	ReportedCostUSD     float64 `json:"reported_cost_usd"`
	Attempts            int     `json:"attempts"`
	AttemptsKnown       int     `json:"attempts_known"`
	AttemptsWithCost    int     `json:"attempts_with_cost"`
}

// UsageTarget identifies one high-consumption conversation or task for direct
// click-through inspection.
type UsageTarget struct {
	WorkspaceID         string  `json:"workspace_id"`
	WorkspaceName       string  `json:"workspace_name"`
	ThreadID            string  `json:"thread_id"`
	TaskID              string  `json:"task_id,omitempty"`
	Title               string  `json:"title"`
	Deleted             bool    `json:"deleted,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	ReportedCostUSD     float64 `json:"reported_cost_usd"`
	Attempts            int     `json:"attempts"`
	AttemptsWithCost    int     `json:"attempts_with_cost"`
	LastAt              string  `json:"last_at"`
}

// UsageEntry is one bounded recent attempt row for the audit table.
type UsageEntry struct {
	AttemptID           string  `json:"attempt_id"`
	RunID               string  `json:"run_id"`
	WorkspaceID         string  `json:"workspace_id"`
	WorkspaceName       string  `json:"workspace_name"`
	ThreadID            string  `json:"thread_id"`
	TaskID              string  `json:"task_id,omitempty"`
	Title               string  `json:"title"`
	Deleted             bool    `json:"deleted,omitempty"`
	AgentID             string  `json:"agent_id,omitempty"`
	AgentName           string  `json:"agent_name,omitempty"`
	Executor            string  `json:"executor,omitempty"`
	Model               string  `json:"model,omitempty"`
	Status              string  `json:"status"`
	UsageKnown          bool    `json:"usage_known"`
	UsageCompleteness   string  `json:"usage_completeness,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	ReportedCostUSD     float64 `json:"reported_cost_usd,omitempty"`
	CostKnown           bool    `json:"cost_known,omitempty"`
	StartedAt           string  `json:"started_at"`
	FinishedAt          string  `json:"finished_at,omitempty"`
}

// UsageReport is the single-endpoint projection backing the Tokens usage view.
type UsageReport struct {
	Range       string            `json:"range"`
	Granularity string            `json:"granularity"`
	Totals      UsageTotals       `json:"totals"`
	Series      []UsageBucket     `json:"series"`
	ByModel     []UsageBreakdown  `json:"by_model"`
	ByExecutor  []UsageBreakdown  `json:"by_executor"`
	ByAgent     []UsageBreakdown  `json:"by_agent"`
	ByWorkspace []UsageBreakdown  `json:"by_workspace"`
	TopTargets  []UsageTarget     `json:"top_targets"`
	Recent      []UsageEntry      `json:"recent"`
	Workspaces  map[string]string `json:"workspaces"`
	Members     map[string]string `json:"members"`
	Executors   []string          `json:"executors"`
}

// UsageReport computes real token usage and natively reported cost in one
// consistent read transaction using a constant number of SQL queries.
func (s *Service) UsageReport(ctx context.Context, query UsageQuery) (UsageReport, error) {
	rng := normalizeUsageRange(query.Range)
	offset := query.TZOffsetMinutes
	if offset < -840 || offset > 840 {
		offset = 0
	}
	loc := time.FixedZone("client", offset*60)
	now := time.Now().UTC()
	if s.now != nil {
		if parsed, ok := parseTimestamp(s.now()); ok {
			now = parsed
		}
	}
	nowLocal := now.In(loc)
	granularity := "day"
	var cutoff time.Time
	hasCutoff := false
	switch rng {
	case "today":
		granularity = "hour"
		cutoff = time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
		hasCutoff = true
	case "7d":
		cutoff = time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -6)
		hasCutoff = true
	case "30d":
		cutoff = time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -29)
		hasCutoff = true
	}

	report := UsageReport{
		Range:       rng,
		Granularity: granularity,
		Series:      []UsageBucket{},
		ByModel:     []UsageBreakdown{},
		ByExecutor:  []UsageBreakdown{},
		ByAgent:     []UsageBreakdown{},
		ByWorkspace: []UsageBreakdown{},
		TopTargets:  []UsageTarget{},
		Recent:      []UsageEntry{},
		Workspaces:  map[string]string{},
		Members:     map[string]string{},
		Executors:   []string{},
	}

	var rows []store.UsageAttemptRow
	if err := s.store.Read(ctx, func(tx *store.Tx) error {
		workspaces, err := tx.Workspaces()
		if err != nil {
			return err
		}
		for _, ws := range workspaces {
			report.Workspaces[ws.ID] = ws.Name
		}
		agents, err := tx.GlobalAgents()
		if err != nil {
			return err
		}
		for _, a := range agents {
			report.Members[a.ID] = a.DisplayName
		}
		rows, err = tx.UsageAttempts()
		return err
	}); err != nil {
		return UsageReport{}, err
	}

	execSet := map[string]struct{}{}
	for _, row := range rows {
		if row.Executor != "" {
			execSet[row.Executor] = struct{}{}
		}
		if row.AgentID != "" && report.Members[row.AgentID] == "" && row.AgentName != "" {
			report.Members[row.AgentID] = row.AgentName
		}
	}
	for execName := range execSet {
		report.Executors = append(report.Executors, execName)
	}
	slices.Sort(report.Executors)

	bucketMap := map[string]*UsageBucket{}
	bucketOrder := []string{}
	addFixedBucket := func(key, label string) {
		b := &UsageBucket{Key: key, Label: label}
		bucketMap[key] = b
		bucketOrder = append(bucketOrder, key)
	}
	switch rng {
	case "today":
		for h := 0; h < 24; h++ {
			k := fmt.Sprintf("%02d:00", h)
			addFixedBucket(k, k)
		}
	case "7d":
		for i := 0; i < 7; i++ {
			d := cutoff.AddDate(0, 0, i)
			addFixedBucket(d.Format("2006-01-02"), d.Format("01-02"))
		}
	case "30d":
		for i := 0; i < 30; i++ {
			d := cutoff.AddDate(0, 0, i)
			addFixedBucket(d.Format("2006-01-02"), d.Format("01-02"))
		}
	}

	byModel := map[string]*UsageBreakdown{}
	byExecutor := map[string]*UsageBreakdown{}
	byAgent := map[string]*UsageBreakdown{}
	byWorkspace := map[string]*UsageBreakdown{}
	targets := map[string]*UsageTarget{}

	addBreakdown := func(m map[string]*UsageBreakdown, key, label, secondary string, row store.UsageAttemptRow, total int64) {
		item, ok := m[key]
		if !ok {
			item = &UsageBreakdown{Key: key, Label: label, Secondary: secondary}
			m[key] = item
		}
		item.InputTokens += row.InputTokens
		item.OutputTokens += row.OutputTokens
		item.CacheReadTokens += row.CacheReadTokens
		item.CacheCreationTokens += row.CacheCreationTokens
		item.TotalTokens += total
		item.Attempts++
		if row.UsageKnown {
			item.AttemptsKnown++
		}
		if row.CostKnown {
			item.ReportedCostUSD += row.ReportedCostUSD
			item.AttemptsWithCost++
		}
	}

	for _, row := range rows {
		if query.WorkspaceID != "" && row.WorkspaceID != query.WorkspaceID {
			continue
		}
		if query.Executor != "" && row.Executor != query.Executor {
			continue
		}
		if query.AgentID != "" && row.AgentID != query.AgentID {
			continue
		}
		ts, tsOK := parseTimestamp(row.StartedAt)
		if !tsOK && row.FinishedAt != "" {
			ts, tsOK = parseTimestamp(row.FinishedAt)
		}
		if hasCutoff {
			if !tsOK || ts.In(loc).Before(cutoff) {
				continue
			}
		}

		totalTokens := row.InputTokens + row.OutputTokens + row.CacheReadTokens + row.CacheCreationTokens
		report.Totals.InputTokens += row.InputTokens
		report.Totals.OutputTokens += row.OutputTokens
		report.Totals.CacheReadTokens += row.CacheReadTokens
		report.Totals.CacheCreationTokens += row.CacheCreationTokens
		report.Totals.TotalTokens += totalTokens
		report.Totals.AttemptsTotal++
		if row.UsageKnown {
			report.Totals.AttemptsKnown++
		} else {
			report.Totals.AttemptsUnknown++
		}
		if row.CostKnown {
			report.Totals.ReportedCostUSD += row.ReportedCostUSD
			report.Totals.AttemptsWithCost++
		}
		if row.Status == "failed" || row.Status == "canceled" || row.Status == "interrupted" || row.Status == "uncertain" || row.Status == "blocked" {
			report.Totals.AttemptsFailed++
		}

		if tsOK {
			localTS := ts.In(loc)
			var bKey, bLabel string
			if granularity == "hour" {
				bKey = fmt.Sprintf("%02d:00", localTS.Hour())
				bLabel = bKey
			} else {
				bKey = localTS.Format("2006-01-02")
				bLabel = localTS.Format("01-02")
			}
			bucket, ok := bucketMap[bKey]
			if !ok && rng == "all" {
				bucket = &UsageBucket{Key: bKey, Label: bLabel}
				bucketMap[bKey] = bucket
				bucketOrder = append(bucketOrder, bKey)
			}
			if bucket != nil {
				bucket.InputTokens += row.InputTokens
				bucket.OutputTokens += row.OutputTokens
				bucket.CacheReadTokens += row.CacheReadTokens
				bucket.CacheCreationTokens += row.CacheCreationTokens
				bucket.TotalTokens += totalTokens
				if row.CostKnown {
					bucket.ReportedCostUSD += row.ReportedCostUSD
				}
				bucket.Attempts++
			}
		}

		execKey := strings.TrimSpace(row.Executor)
		if execKey == "" {
			execKey = "unknown"
		}
		modelLabel := strings.TrimSpace(row.ResolvedModel)
		if modelLabel == "" {
			modelLabel = strings.TrimSpace(row.Model)
		}
		if modelLabel == "" {
			modelLabel = "unknown"
		}
		addBreakdown(byModel, execKey+":"+modelLabel, modelLabel, execKey, row, totalTokens)
		addBreakdown(byExecutor, execKey, execKey, "", row, totalTokens)

		agentKey := row.AgentID
		if agentKey == "" {
			agentKey = "unknown"
		}
		agentLabel := report.Members[row.AgentID]
		if agentLabel == "" {
			agentLabel = row.AgentName
		}
		if agentLabel == "" {
			agentLabel = agentKey
		}
		addBreakdown(byAgent, agentKey, agentLabel, "", row, totalTokens)

		wsLabel := report.Workspaces[row.WorkspaceID]
		if wsLabel == "" {
			wsLabel = row.WorkspaceID
		}
		addBreakdown(byWorkspace, row.WorkspaceID, wsLabel, "", row, totalTokens)

		targetTitle := strings.TrimSpace(row.TaskTitle)
		if targetTitle == "" {
			targetTitle = strings.TrimSpace(row.ThreadTitle)
		}
		targetKey := "thread:" + row.ThreadID
		if row.TaskID != "" {
			targetKey = "task:" + row.TaskID
		}
		target, ok := targets[targetKey]
		if !ok {
			target = &UsageTarget{
				WorkspaceID:   row.WorkspaceID,
				WorkspaceName: wsLabel,
				ThreadID:      row.ThreadID,
				TaskID:        row.TaskID,
				Title:         targetTitle,
				Deleted:       row.ThreadDeletedAt != "",
				LastAt:        row.StartedAt,
			}
			targets[targetKey] = target
		}
		target.InputTokens += row.InputTokens
		target.OutputTokens += row.OutputTokens
		target.CacheReadTokens += row.CacheReadTokens
		target.CacheCreationTokens += row.CacheCreationTokens
		target.TotalTokens += totalTokens
		target.Attempts++
		if row.CostKnown {
			target.ReportedCostUSD += row.ReportedCostUSD
			target.AttemptsWithCost++
		}
		if row.StartedAt > target.LastAt {
			target.LastAt = row.StartedAt
		}

		if len(report.Recent) < maxRecentUsageEntries {
			report.Recent = append(report.Recent, UsageEntry{
				AttemptID:           row.AttemptID,
				RunID:               row.RunID,
				WorkspaceID:         row.WorkspaceID,
				WorkspaceName:       wsLabel,
				ThreadID:            row.ThreadID,
				TaskID:              row.TaskID,
				Title:               targetTitle,
				Deleted:             row.ThreadDeletedAt != "",
				AgentID:             row.AgentID,
				AgentName:           agentLabel,
				Executor:            row.Executor,
				Model:               modelLabel,
				Status:              row.Status,
				UsageKnown:          row.UsageKnown,
				UsageCompleteness:   row.UsageCompleteness,
				InputTokens:         row.InputTokens,
				OutputTokens:        row.OutputTokens,
				CacheReadTokens:     row.CacheReadTokens,
				CacheCreationTokens: row.CacheCreationTokens,
				TotalTokens:         totalTokens,
				ReportedCostUSD:     row.ReportedCostUSD,
				CostKnown:           row.CostKnown,
				StartedAt:           row.StartedAt,
				FinishedAt:          row.FinishedAt,
			})
		}
	}

	if rng == "all" {
		slices.Sort(bucketOrder)
	}
	for _, key := range bucketOrder {
		report.Series = append(report.Series, *bucketMap[key])
	}

	report.ByModel = sortedBreakdowns(byModel)
	report.ByExecutor = sortedBreakdowns(byExecutor)
	report.ByAgent = sortedBreakdowns(byAgent)
	report.ByWorkspace = sortedBreakdowns(byWorkspace)

	targetList := make([]UsageTarget, 0, len(targets))
	for _, item := range targets {
		targetList = append(targetList, *item)
	}
	slices.SortFunc(targetList, func(a, b UsageTarget) int {
		if c := cmp.Compare(b.TotalTokens, a.TotalTokens); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Attempts, a.Attempts); c != 0 {
			return c
		}
		return cmp.Compare(b.LastAt, a.LastAt)
	})
	if len(targetList) > maxTopUsageTargets {
		targetList = targetList[:maxTopUsageTargets]
	}
	report.TopTargets = targetList

	return report, nil
}

func normalizeUsageRange(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "today":
		return "today"
	case "7d", "":
		return "7d"
	case "30d":
		return "30d"
	case "all":
		return "all"
	default:
		return "7d"
	}
}

func parseTimestamp(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func sortedBreakdowns(m map[string]*UsageBreakdown) []UsageBreakdown {
	out := make([]UsageBreakdown, 0, len(m))
	for _, item := range m {
		out = append(out, *item)
	}
	slices.SortFunc(out, func(a, b UsageBreakdown) int {
		if c := cmp.Compare(b.TotalTokens, a.TotalTokens); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Attempts, a.Attempts); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return out
}
