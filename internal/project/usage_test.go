package project

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestUsageReportAggregatesTokensCacheCostFiltersAndBoundedQueries(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := New(db)
	// Fixed clock: 2026-11-22 10:30:00 +08:00 (02:30:00Z)
	svc.now = func() string { return "2026-11-22T02:30:00Z" }

	ws1 := w.Workspace{ID: "ws-1", Name: "Alpha", Revision: 1, CreatedAt: "2026-11-01T00:00:00Z"}
	ws2 := w.Workspace{ID: "ws-2", Name: "Beta", Revision: 1, CreatedAt: "2026-11-01T00:00:00Z"}
	ag1 := w.Agent{ID: "ag-1", DisplayName: "Architect", Handle: "architect", Executor: "claude", Model: "claude-sonnet-4-6", Revision: 1}
	ag2 := w.Agent{ID: "ag-2", DisplayName: "Builder", Handle: "builder", Executor: "codex", Model: "gpt-5.4", Revision: 1}
	th1 := w.Thread{ID: "th-1", WorkspaceID: "ws-1", Title: "Refactor auth", CoordinatorID: "ag-1", AgentIDs: []string{"ag-1"}, Revision: 1, UpdatedAt: "2026-11-22T02:00:00Z"}
	th2 := w.Thread{ID: "th-2", WorkspaceID: "ws-2", Title: "Deleted experiment", CoordinatorID: "ag-2", AgentIDs: []string{"ag-2"}, Revision: 1, UpdatedAt: "2026-11-20T05:00:00Z", DeletedAt: "2026-11-21T00:00:00Z"}

	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		for _, ws := range []w.Workspace{ws1, ws2} {
			if err := tx.SaveWorkspace(ws); err != nil {
				return err
			}
		}
		for _, ag := range []w.Agent{ag1, ag2} {
			if err := tx.SaveAgent(ag); err != nil {
				return err
			}
		}
		for _, th := range []w.Thread{th1, th2} {
			if err := tx.SaveThread(th); err != nil {
				return err
			}
		}
		// Run 1 (today 09:15 +08:00 = 01:15Z): Claude succeeded with cache + reported cost.
		if err := tx.InsertRun(w.Run{ID: "run-1", WorkspaceID: "ws-1", ThreadID: "th-1", Status: "succeeded", CreatedAt: "2026-11-22T01:15:00Z"}, "k1", "h1"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: "step-1", RunID: "run-1", AgentID: "ag-1", AgentName: "Architect", Status: "succeeded"}); err != nil {
			return err
		}
		if err := tx.SaveAttempt(w.Attempt{
			ID: "att-1", RunID: "run-1", StepID: "step-1", Number: 1, Status: "succeeded",
			Executor: "claude", Model: "claude-sonnet-4-6", ResolvedModel: "claude-sonnet-4-6",
			UsageKnown: true, UsageCompleteness: "known",
			InputTokens: 300, OutputTokens: 150, CacheReadTokens: 2000, CacheCreationTokens: 500,
			ReportedCostUSD: 0.0125, CostKnown: true,
			StartedAt: "2026-11-22T01:15:00Z", FinishedAt: "2026-11-22T01:16:00Z",
		}); err != nil {
			return err
		}
		// Attempt 2 on Run 1 (today 10:05 +08:00 = 02:05Z): failed attempt with unknown usage.
		if err := tx.SaveAttempt(w.Attempt{
			ID: "att-2", RunID: "run-1", StepID: "step-1", Number: 2, Status: "failed",
			Executor: "claude", Model: "claude-sonnet-4-6",
			UsageKnown: false, UsageCompleteness: "unknown",
			StartedAt: "2026-11-22T02:05:00Z", FinishedAt: "2026-11-22T02:05:30Z",
		}); err != nil {
			return err
		}
		// Run 2 (2 days ago, in trashed thread th-2): Codex failed attempt that still consumed tokens (no USD cost).
		if err := tx.InsertRun(w.Run{ID: "run-2", WorkspaceID: "ws-2", ThreadID: "th-2", Status: "failed", CreatedAt: "2026-11-20T05:00:00Z"}, "k2", "h2"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: "step-2", RunID: "run-2", AgentID: "ag-2", AgentName: "Builder", Status: "failed"}); err != nil {
			return err
		}
		if err := tx.SaveAttempt(w.Attempt{
			ID: "att-3", RunID: "run-2", StepID: "step-2", Number: 1, Status: "failed",
			Executor: "codex", Model: "gpt-5.4", ResolvedModel: "gpt-5.4",
			UsageKnown: true, UsageCompleteness: "known",
			InputTokens: 400, OutputTokens: 100, CacheReadTokens: 1600,
			StartedAt: "2026-11-20T05:00:00Z", FinishedAt: "2026-11-20T05:01:00Z",
		}); err != nil {
			return err
		}
		// Run 3 (40 days ago): outside 7d and 30d, visible in "all".
		if err := tx.InsertRun(w.Run{ID: "run-3", WorkspaceID: "ws-1", ThreadID: "th-1", Status: "succeeded", CreatedAt: "2026-10-10T01:00:00Z"}, "k3", "h3"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: "step-3", RunID: "run-3", AgentID: "ag-1", AgentName: "Architect", Status: "succeeded"}); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{
			ID: "att-4", RunID: "run-3", StepID: "step-3", Number: 1, Status: "succeeded",
			Executor: "claude", Model: "claude-sonnet-4-6",
			UsageKnown: true, UsageCompleteness: "known",
			InputTokens: 1000, OutputTokens: 500,
			StartedAt: "2026-10-10T01:00:00Z", FinishedAt: "2026-10-10T01:02:00Z",
		})
	}); err != nil {
		t.Fatal(err)
	}

	// Verify 7d report + bounded query count (<= 3 SQL queries total).
	before := db.QueryCount()
	rep7d, err := svc.UsageReport(t.Context(), UsageQuery{Range: "7d", TZOffsetMinutes: 480})
	if err != nil {
		t.Fatal(err)
	}
	if delta := db.QueryCount() - before; delta > 3 {
		t.Fatalf("UsageReport ran %d queries, want bounded <= 3", delta)
	}
	if rep7d.Granularity != "day" || len(rep7d.Series) != 7 {
		t.Fatalf("7d series = (%s, %d), want (day, 7)", rep7d.Granularity, len(rep7d.Series))
	}
	// att-1 (300+150+2000+500=2950) + att-2 (0, unknown) + att-3 (400+100+1600=2100) = 5050.
	if rep7d.Totals.TotalTokens != 5050 || rep7d.Totals.InputTokens != 700 || rep7d.Totals.OutputTokens != 250 ||
		rep7d.Totals.CacheReadTokens != 3600 || rep7d.Totals.CacheCreationTokens != 500 {
		t.Fatalf("unexpected 7d totals: %+v", rep7d.Totals)
	}
	if rep7d.Totals.AttemptsTotal != 3 || rep7d.Totals.AttemptsKnown != 2 || rep7d.Totals.AttemptsUnknown != 1 ||
		rep7d.Totals.AttemptsWithCost != 1 || rep7d.Totals.AttemptsFailed != 2 {
		t.Fatalf("unexpected 7d attempt counts: %+v", rep7d.Totals)
	}
	if math.Abs(rep7d.Totals.ReportedCostUSD-0.0125) > 1e-9 {
		t.Fatalf("reported_cost_usd = %v, want 0.0125", rep7d.Totals.ReportedCostUSD)
	}
	if len(rep7d.TopTargets) != 2 || rep7d.TopTargets[0].ThreadID != "th-1" || !rep7d.TopTargets[1].Deleted {
		t.Fatalf("unexpected top targets: %+v", rep7d.TopTargets)
	}

	// Verify "today" with UTC+8 puts att-1 in 09:00 and att-2 in 10:00, excludes att-3.
	repToday, err := svc.UsageReport(t.Context(), UsageQuery{Range: "today", TZOffsetMinutes: 480})
	if err != nil {
		t.Fatal(err)
	}
	if repToday.Granularity != "hour" || len(repToday.Series) != 24 {
		t.Fatalf("today series = (%s, %d), want (hour, 24)", repToday.Granularity, len(repToday.Series))
	}
	if repToday.Totals.AttemptsTotal != 2 || repToday.Series[9].TotalTokens != 2950 || repToday.Series[10].Attempts != 1 {
		t.Fatalf("unexpected today buckets: totals=%+v h09=%+v h10=%+v", repToday.Totals, repToday.Series[9], repToday.Series[10])
	}

	// Verify filters: executor=codex narrows totals to att-3 while keeping all executors listed.
	repCodex, err := svc.UsageReport(t.Context(), UsageQuery{Range: "all", Executor: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if repCodex.Totals.TotalTokens != 2100 || repCodex.Totals.AttemptsTotal != 1 || len(repCodex.Executors) != 2 {
		t.Fatalf("unexpected codex filtered report: totals=%+v executors=%v", repCodex.Totals, repCodex.Executors)
	}

	// Verify "all" includes the 40-day-old legacy attempt att-4.
	repAll, err := svc.UsageReport(t.Context(), UsageQuery{Range: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if repAll.Totals.TotalTokens != 6550 || repAll.Totals.AttemptsTotal != 4 {
		t.Fatalf("unexpected all-time totals: %+v", repAll.Totals)
	}
}
