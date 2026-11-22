package store

// UsageAttemptRow is a lightweight projection of one attempt joined with its
// run, step, thread and task metadata for usage reporting. It never reads
// message bodies, source payloads or event streams.
type UsageAttemptRow struct {
	AttemptID           string
	RunID               string
	StepID              string
	Status              string
	Executor            string
	Model               string
	ResolvedModel       string
	UsageKnown          bool
	UsageCompleteness   string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReportedCostUSD     float64
	CostKnown           bool
	StartedAt           string
	FinishedAt          string
	WorkspaceID         string
	ThreadID            string
	TaskID              string
	AgentID             string
	AgentName           string
	ThreadTitle         string
	ThreadDeletedAt     string
	TaskTitle           string
}

// UsageAttempts reads all persisted attempts with their owning context in one
// SQL query. Historical attempts written before cache/cost fields existed
// safely default those values to zero.
func (t *Tx) UsageAttempts() ([]UsageAttemptRow, error) {
	if t.queries != nil {
		t.queries.Add(1)
	}
	const query = `SELECT
		a.id,
		a.run_id,
		a.step_id,
		a.status,
		COALESCE(json_extract(a.body,'$.executor'),''),
		COALESCE(json_extract(a.body,'$.model'),''),
		COALESCE(json_extract(a.body,'$.resolved_model'),''),
		COALESCE(json_extract(a.body,'$.usage_known'),0),
		COALESCE(json_extract(a.body,'$.usage_completeness'),''),
		COALESCE(json_extract(a.body,'$.input_tokens'),0),
		COALESCE(json_extract(a.body,'$.output_tokens'),0),
		COALESCE(json_extract(a.body,'$.cache_read_tokens'),0),
		COALESCE(json_extract(a.body,'$.cache_creation_tokens'),0),
		COALESCE(json_extract(a.body,'$.reported_cost_usd'),0.0),
		COALESCE(json_extract(a.body,'$.cost_known'),0),
		COALESCE(json_extract(a.body,'$.started_at'),r.created_at),
		COALESCE(json_extract(a.body,'$.finished_at'),''),
		r.workspace_id,
		r.thread_id,
		r.task_id,
		COALESCE(json_extract(s.body,'$.agent_id'),''),
		COALESCE(json_extract(s.body,'$.agent_name'),''),
		COALESCE(json_extract(th.body,'$.title'),''),
		COALESCE(json_extract(th.body,'$.deleted_at'),''),
		COALESCE(json_extract(tk.body,'$.title'),'')
	FROM attempts a
	JOIN runs r ON r.id = a.run_id
	JOIN steps s ON s.id = a.step_id
	LEFT JOIN threads th ON th.id = r.thread_id
	LEFT JOIN tasks tk ON tk.id = r.task_id AND r.task_id <> ''
	ORDER BY COALESCE(json_extract(a.body,'$.started_at'),r.created_at) DESC, a.id DESC`
	rows, err := t.tx.QueryContext(t.ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageAttemptRow{}
	for rows.Next() {
		var row UsageAttemptRow
		var knownInt, costKnownInt int
		if err := rows.Scan(
			&row.AttemptID,
			&row.RunID,
			&row.StepID,
			&row.Status,
			&row.Executor,
			&row.Model,
			&row.ResolvedModel,
			&knownInt,
			&row.UsageCompleteness,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.ReportedCostUSD,
			&costKnownInt,
			&row.StartedAt,
			&row.FinishedAt,
			&row.WorkspaceID,
			&row.ThreadID,
			&row.TaskID,
			&row.AgentID,
			&row.AgentName,
			&row.ThreadTitle,
			&row.ThreadDeletedAt,
			&row.TaskTitle,
		); err != nil {
			return nil, err
		}
		row.UsageKnown = knownInt != 0
		row.CostKnown = costKnownInt != 0
		out = append(out, row)
	}
	return out, rows.Err()
}
