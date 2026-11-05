package project

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// AnalyzeCSVInput binds a deterministic computation to one execution segment.
// The original filesystem path is deliberately absent: analysis consumes only
// the content-addressed capture already authorized for this attempt.
type AnalyzeCSVInput struct {
	TaskID    string
	RunID     string
	StepID    string
	AttemptID string
	SourceID  string
	Column    string
	RequestID string
}

type CSVStatistics struct {
	SchemaVersion int     `json:"schema_version"`
	SourceID      string  `json:"source_id"`
	SourceDigest  string  `json:"source_digest"`
	Column        string  `json:"column"`
	Rows          int     `json:"rows"`
	Numeric       int     `json:"numeric"`
	Missing       int     `json:"missing"`
	Invalid       int     `json:"invalid"`
	Minimum       float64 `json:"minimum"`
	Maximum       float64 `json:"maximum"`
	Sum           float64 `json:"sum"`
	Mean          float64 `json:"mean"`
	ChartedValues int     `json:"charted_values"`
}

type CSVAnalysisView struct {
	Execution  w.WorkspaceExecution `json:"execution"`
	Dataset    w.DatasetManifest    `json:"dataset"`
	Statistics w.Source             `json:"statistics"`
	Chart      w.Artifact           `json:"chart"`
}

// AnalyzeCSV performs bounded, in-process computation without a shell, network,
// original-file read or write. It is not presented as a general notebook or as
// evidence that arbitrary native data-analysis jobs are isolated.
func (s *Service) AnalyzeCSV(ctx context.Context, in AnalyzeCSVInput) (CSVAnalysisView, error) {
	var out CSVAnalysisView
	if in.TaskID == "" || in.RunID == "" || in.SourceID == "" || in.Column == "" || in.RequestID == "" || len(in.Column) > 256 {
		return out, errorf("invalid_request", "CSV analysis requires task, run, source, bounded column and request ID")
	}
	var source w.Source
	var run w.Run
	if err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		source, err = tx.Source(in.SourceID)
		if err != nil {
			return err
		}
		run, err = tx.Run(in.RunID)
		return err
	}); err != nil {
		return out, err
	}
	if run.TaskID != in.TaskID || source.WorkspaceID != run.WorkspaceID || source.Kind != "csv" || source.ParseStatus != "parsed" || source.OriginalContent == "" {
		return out, errorf("invalid_request", "CSV analysis requires a parsed capture in the producing run's workspace")
	}
	raw, err := base64.StdEncoding.DecodeString(source.OriginalContent)
	if err != nil || len(raw) > 32<<20 {
		return out, errorf("evidence_stale", "CSV original capture is unavailable or exceeds the analysis budget")
	}
	digest := sha256.Sum256(raw)
	originalDigest := hex.EncodeToString(digest[:])
	if source.OriginalDigest != originalDigest || source.SHA256 != originalDigest {
		return out, errorf("evidence_stale", "CSV original digest does not match its immutable capture")
	}
	key := Digest(struct{ Run, Request string }{in.RunID, in.RequestID})
	statsID := "csv-stats-" + key
	chartID := "csv-chart-" + key
	executionID := "csv-analysis-" + key
	// Reject request-ID reuse before doing new computation, including when the
	// conflicting column would itself be invalid or contain no numeric values.
	if err := s.store.Read(ctx, func(tx *store.Tx) error {
		existing, readErr := tx.Source(statsID)
		if errors.Is(readErr, store.ErrNotFound) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		var previous CSVStatistics
		if json.Unmarshal([]byte(existing.Content), &previous) != nil || previous.SchemaVersion != 1 || previous.SourceID != source.ID || previous.SourceDigest != originalDigest || previous.Column != in.Column {
			return errorf("conflict", "CSV analysis request ID was reused for different input")
		}
		return nil
	}); err != nil {
		return out, err
	}
	stats, values, err := csvStatistics(source.ID, originalDigest, in.Column, raw)
	if err != nil {
		return out, err
	}
	encoded, err := json.Marshal(stats)
	if err != nil {
		return out, err
	}
	now := s.now()
	err = s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(in.TaskID)
		if err != nil || task.WorkspaceID != run.WorkspaceID || task.CurrentRevision != run.TaskRevision {
			return errorf("authority_revoked", "CSV analysis no longer belongs to the current task revision")
		}
		if err := csvSourceVisibleToTask(tx, task, source); err != nil {
			return err
		}
		if existing, readErr := tx.Source(statsID); readErr == nil {
			if existing.Content != string(encoded) {
				return errorf("conflict", "CSV analysis request ID was reused for different input")
			}
			chart, err := tx.Artifact(chartID, 1)
			if err != nil {
				return err
			}
			execution, err := tx.Execution(executionID)
			if err != nil {
				return err
			}
			dataset, err := latestCSVDataset(tx, task.ID, source.ID+"-dataset")
			if err != nil {
				return err
			}
			out = CSVAnalysisView{Execution: execution, Dataset: dataset, Statistics: existing, Chart: chart}
			return nil
		} else if !errors.Is(readErr, store.ErrNotFound) {
			return readErr
		}
		visibility, exposure, err := analysisDisclosure(tx, run, in.AttemptID, source)
		if err != nil {
			return err
		}
		statistics := w.Source{
			ID: statsID, WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID,
			Kind: "analysis", Title: "CSV statistics: " + in.Column, Content: string(encoded), ContentDigest: Digest(string(encoded)),
			CaptureStepID: in.StepID, CapturedAt: now, ParseStatus: "parsed", ParseQuality: "deterministic_statistics",
			ParserVersion: "csv-stats-v1", SourceLocator: "row:N; column:" + in.Column,
			Visibility: visibility, ExposureDigest: exposure,
		}
		if err := tx.SaveSource(statistics); err != nil {
			return err
		}
		chart := w.Artifact{
			ID: chartID, Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: in.StepID,
			Type: "data_chart", Title: "CSV chart: " + in.Column, Body: csvChartSVG(in.Column, stats, values),
			Findings: []w.Finding{}, SourceIDs: []string{source.ID, statistics.ID},
			Limitations: []string{fmt.Sprintf("statistics include all %d data rows; chart plots at most 100 numeric values", stats.Rows), "missing and non-numeric cells are excluded from numeric aggregates and counted separately"},
			Visibility:  visibility, ExposureDigest: exposure, CreatedAt: now,
		}
		if err := tx.SaveArtifact(chart); err != nil {
			return err
		}
		dataset, err := nextCSVDataset(tx, task.ID, run, source, statistics, now)
		if err != nil {
			return err
		}
		execution := w.WorkspaceExecution{
			ID: executionID, WorkspaceID: run.WorkspaceID, TaskID: task.ID, Kind: "isolated_csv_v1",
			CanonicalRoot: "research://" + task.ID + "/" + in.RequestID, OwnerMemberID: run.CoordinatorID,
			LeaseOwner: in.AttemptID, LeaseEpoch: 1, Status: "released",
			Observation: "immutable_capture_only; no_shell; no_network; no_original_file_access; external_effects=none",
			CreatedAt:   now, UpdatedAt: now,
		}
		if err := tx.SaveExecution(execution); err != nil {
			return err
		}
		out = CSVAnalysisView{Execution: execution, Dataset: dataset, Statistics: statistics, Chart: chart}
		return nil
	})
	return out, err
}

func csvSourceVisibleToTask(tx *store.Tx, task w.Task, source w.Source) error {
	sourceRun, err := tx.Run(source.RunID)
	if err != nil || sourceRun.TaskID == "" {
		return errorf("disclosure_denied", "CSV capture has no verifiable task scope")
	}
	sourceTask, err := tx.Task(sourceRun.TaskID)
	if err != nil || sourceTask.WorkspaceID != task.WorkspaceID || sourceTask.CollaborationScopeID != task.CollaborationScopeID {
		return errorf("disclosure_denied", "CSV capture is outside this task's collaboration scope")
	}
	switch source.Visibility {
	case "project", "project-visible":
		return nil
	case "private":
		if sourceTask.ID == task.ID {
			return nil
		}
	case "role":
		if sourceTask.ID == task.ID && sourceTask.OwnerMemberID == task.OwnerMemberID {
			return nil
		}
	case "", "task_chain":
		return nil
	}
	return errorf("disclosure_denied", "CSV capture visibility does not permit this analysis")
}

func analysisDisclosure(tx *store.Tx, run w.Run, attemptID string, source w.Source) (string, string, error) {
	if attemptID == "" {
		visibility, exposure := w.DisclosureForSources([]w.Source{source})
		return visibility, exposure, nil
	}
	return InheritAttemptDisclosure(tx, run, attemptID, []w.Source{source})
}

func latestCSVDataset(tx *store.Tx, taskID, id string) (w.DatasetManifest, error) {
	items, err := tx.Datasets(taskID)
	if err != nil {
		return w.DatasetManifest{}, err
	}
	latest := w.DatasetManifest{}
	for _, item := range items {
		if item.ID == id && item.Revision > latest.Revision {
			latest = item
		}
	}
	if latest.ID == "" {
		return latest, store.ErrNotFound
	}
	return latest, nil
}

func nextCSVDataset(tx *store.Tx, taskID string, run w.Run, source, statistics w.Source, now string) (w.DatasetManifest, error) {
	dataset, ok := datasetForSource(run, source)
	if !ok {
		return dataset, errorf("invalid_request", "CSV source has no dataset manifest")
	}
	latest, err := latestCSVDataset(tx, taskID, dataset.ID)
	if err == nil {
		dataset = latest
		dataset.Revision++
	} else if !errors.Is(err, store.ErrNotFound) {
		return dataset, err
	}
	dataset.TaskID = taskID
	dataset.ProducerAttemptID = ""
	dataset.ProcessingSteps = append(dataset.ProcessingSteps, "csv-stats-v1:"+statistics.ID)
	dataset.Visibility, dataset.ExposureDigest = statistics.Visibility, statistics.ExposureDigest
	dataset.CreatedAt = now
	return dataset, tx.SaveDataset(dataset)
}

func csvStatistics(sourceID, sourceDigest, column string, raw []byte) (CSVStatistics, []float64, error) {
	stats := CSVStatistics{SchemaVersion: 1, SourceID: sourceID, SourceDigest: sourceDigest, Column: column}
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return stats, nil, errorf("invalid_request", "CSV header cannot be parsed: %v", err)
	}
	columnIndex := -1
	for i, name := range header {
		if name == column {
			if columnIndex >= 0 {
				return stats, nil, errorf("invalid_request", "CSV column name is ambiguous")
			}
			columnIndex = i
		}
	}
	if columnIndex < 0 {
		return stats, nil, errorf("not_found", "CSV column was not found in the captured header")
	}
	values := []float64{}
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return stats, nil, errorf("invalid_request", "CSV row cannot be parsed: %v", readErr)
		}
		stats.Rows++
		if stats.Rows > 5000 {
			return stats, nil, errorf("invalid_request", "CSV exceeds the 5000-row analysis budget")
		}
		if columnIndex >= len(record) || strings.TrimSpace(record[columnIndex]) == "" {
			stats.Missing++
			continue
		}
		value, parseErr := strconv.ParseFloat(strings.TrimSpace(record[columnIndex]), 64)
		if parseErr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			stats.Invalid++
			continue
		}
		if stats.Numeric == 0 || value < stats.Minimum {
			stats.Minimum = value
		}
		if stats.Numeric == 0 || value > stats.Maximum {
			stats.Maximum = value
		}
		stats.Numeric++
		stats.Sum += value
		if len(values) < 100 {
			values = append(values, value)
		}
	}
	if stats.Numeric == 0 {
		return stats, nil, errorf("insufficient_evidence", "CSV column has no finite numeric observations")
	}
	stats.Mean = stats.Sum / float64(stats.Numeric)
	stats.ChartedValues = len(values)
	return stats, values, nil
}

func csvChartSVG(column string, stats CSVStatistics, values []float64) string {
	const width, height, padding = 800.0, 320.0, 36.0
	var out strings.Builder
	out.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 320" role="img" aria-label="CSV numeric values">`)
	out.WriteString("<title>" + html.EscapeString("CSV numeric values: "+column) + "</title>")
	out.WriteString(`<rect width="800" height="320" fill="white"/><line x1="36" y1="284" x2="780" y2="284" stroke="#334155"/>`)
	span := stats.Maximum - stats.Minimum
	for i, value := range values {
		x := padding
		if len(values) > 1 {
			x += (width - 2*padding) * float64(i) / float64(len(values)-1)
		}
		y := height - padding
		if span > 0 {
			y -= (height - 2*padding) * (value - stats.Minimum) / span
		}
		fmt.Fprintf(&out, `<circle cx="%s" cy="%s" r="3" fill="#2563eb"><title>row %d: %s</title></circle>`, strconv.FormatFloat(x, 'f', 4, 64), strconv.FormatFloat(y, 'f', 4, 64), i+2, strconv.FormatFloat(value, 'g', -1, 64))
	}
	fmt.Fprintf(&out, `<text x="36" y="24" font-family="sans-serif" font-size="14">n=%d min=%s max=%s mean=%s</text></svg>`, stats.Numeric, strconv.FormatFloat(stats.Minimum, 'g', -1, 64), strconv.FormatFloat(stats.Maximum, 'g', -1, 64), strconv.FormatFloat(stats.Mean, 'g', -1, 64))
	return out.String()
}
