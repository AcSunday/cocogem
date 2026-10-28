package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Names is the finite v0.1 tool vocabulary, including the workflow-owned publisher.
var Names = []string{"list_files", "read_file", "search_content", "web_search", "fetch_url", "read_source", "publish_artifact"}

func object(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func text(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// Definitions returns schemas only for explicitly allowed capabilities.
func Definitions(names []string) []model.Tool {
	defs := map[string]model.Tool{
		"read_source":    {Name: "read_source", Description: "Page an exact captured source visible in this step's input snapshot. Does not refresh the original.", Schema: object(map[string]any{"source_id": text("Captured source UUID"), "cursor": text("Opaque next_cursor from the previous page"), "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": w.MaxTextPageBytes}}, "source_id")},
		"list_files":     {Name: "list_files", Description: "List files in the bound source directory; symbolic links and secrets are excluded.", Schema: object(map[string]any{"path": text("Relative directory; defaults to .")})},
		"read_file":      {Name: "read_file", Description: "Read a bounded text excerpt and obtain a captured source ID.", Schema: object(map[string]any{"path": text("Relative file path"), "start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1}}, "path")},
		"search_content": {Name: "search_content", Description: "Search literal text in allowed source files; results are navigation hints, not read-file evidence.", Schema: object(map[string]any{"path": text("Relative directory"), "query": text("Literal search text")}, "query")},
		"web_search":     {Name: "web_search", Description: "Search public web sources. Results are snippets; fetch original pages to verify claims.", Schema: object(map[string]any{"query": text("Search query")}, "query")},
		"fetch_url":      {Name: "fetch_url", Description: "Read a public original text page and capture its exact source. Search-engine result pages are captured as leads, not originals.", Schema: object(map[string]any{"url": text("Public HTTP(S) URL")}, "url")},
		"publish_artifact": {Name: "publish_artifact", Description: "Submit this step's deliverable. Use captured source IDs only; for native CLI observations obtain server-assigned IDs via list_sources and never invent them. Code findings require file evidence. Supported findings require a read original source; search snippets, search result pages and native search leads are not evidence. Native web output is a CLI-processed observation, not raw page text. Research reports include scope, methods, findings, disagreements, limitations and next steps in the body.", Schema: object(map[string]any{
			"type": map[string]any{"type": "string", "enum": []string{"note", "research_report", "code_analysis"}}, "title": text("Title"), "body": text("Markdown body"), "source_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "limitations": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"findings": map[string]any{"type": "array", "items": object(map[string]any{"title": text("Finding title"), "detail": text("Evidence and reasoning"), "status": map[string]any{"type": "string", "enum": []string{"supported", "confirmed", "disputed", "insufficient_evidence"}}, "severity": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}}, "source_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "title", "detail", "status", "source_ids")}}, "type", "title", "body", "findings", "source_ids", "limitations")},
	}
	out := []model.Tool{}
	for _, name := range names {
		if def, ok := defs[name]; ok {
			out = append(out, def)
		}
	}
	return out
}

// Decode rejects unknown fields, trailing JSON and oversized tool input.
func Decode(raw json.RawMessage, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("tool input must be a JSON object")
	}
	if len(raw) > 65536 {
		return errors.New("tool input exceeds 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid tool arguments")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("tool input must contain exactly one JSON value")
	}
	return nil
}

// WebClient supplies source capture and search to the host's tool boundary.
type WebClient interface {
	SearchEnabled() bool
	Fetch(context.Context, string) (w.Source, error)
	Search(context.Context, string) ([]w.Source, error)
}

// Scope limits a tool call to its workspace and exact evidence capture callback.
type Scope struct {
	Root    string
	Web     WebClient
	Capture func(w.Source) (w.Source, error)
}

// Execute returns a JSON observation for a registered read-only operation.
func Execute(ctx context.Context, name string, raw json.RawMessage, scope Scope) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var result any
	switch name {
	case "list_files":
		var in struct {
			Path string `json:"path"`
		}
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		entries, truncated, err := ListFiles(ctx, scope.Root, in.Path)
		if err != nil {
			return "", err
		}
		result = map[string]any{"entries": entries, "truncated": truncated}
	case "read_file":
		var in struct {
			Path  string `json:"path"`
			Start int    `json:"start_line"`
			End   int    `json:"end_line"`
		}
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		if in.Path == "" {
			return "", errors.New("path is required")
		}
		source, err := ReadFile(ctx, scope.Root, in.Path, in.Start, in.End)
		if err != nil {
			return "", err
		}
		source, err = scope.Capture(source)
		if err != nil {
			return "", err
		}
		result = previewSource(source, w.MaxTextPageBytes)
	case "search_content":
		var in struct {
			Path  string `json:"path"`
			Query string `json:"query"`
		}
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		hits, truncated, err := SearchContent(ctx, scope.Root, in.Path, in.Query)
		if err != nil {
			return "", err
		}
		result = map[string]any{"hits": hits, "truncated": truncated}
	case "fetch_url":
		var in struct {
			URL string `json:"url"`
		}
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		source, err := scope.Web.Fetch(ctx, in.URL)
		if err != nil {
			return "", err
		}
		source, err = scope.Capture(source)
		if err != nil {
			return "", err
		}
		result = previewSource(source, w.MaxTextPageBytes)
	case "web_search":
		var in struct {
			Query string `json:"query"`
		}
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		sources, err := scope.Web.Search(ctx, in.Query)
		if err != nil {
			return "", err
		}
		for i, s := range sources {
			sources[i], err = scope.Capture(s)
			if err != nil {
				return "", err
			}
		}
		result = searchPreview(sources)
	default:
		return "", errors.New("unknown or unsupported read-only tool")
	}
	b, err := json.Marshal(result)
	return string(b), err
}

// sourceValue flattens capture metadata so a caller can still decode the
// legacy w.Source shape while extra bounded-preview fields stay explicit.
func sourceValue(source w.Source) map[string]any {
	return map[string]any{"id": source.ID, "workspace_id": source.WorkspaceID, "thread_id": source.ThreadID, "run_id": source.RunID, "kind": source.Kind, "title": source.Title, "url": source.URL, "path": source.Path, "start_line": source.StartLine, "end_line": source.EndLine, "sha256": source.SHA256, "captured_at": source.CapturedAt, "truncated": source.Truncated, "content": ""}
}

// previewSource returns one legal JSON observation: a bounded UTF-8 preview,
// the exact source ID, and a cursor for read_source. source_truncated is the
// capture's own limit; preview_truncated only means this page is smaller.
func previewSource(source w.Source, maxBytes int) map[string]any {
	value := sourceValue(source)
	value["source_id"] = source.ID
	value["source_truncated"] = source.Truncated
	page, err := w.PageText(w.TextRef{Kind: "source", ID: source.ID}, source.Content, "", maxBytes)
	if err != nil {
		preview := source.Content
		if len(preview) > maxBytes {
			end := maxBytes
			for end > 0 && !utf8.RuneStart(preview[end]) {
				end--
			}
			preview = preview[:end]
		}
		value["content"] = preview
		value["preview_truncated"] = len(preview) < len(source.Content)
		value["has_more"] = len(preview) < len(source.Content)
		return value
	}
	value["content"] = page.Content
	value["offset"] = page.Offset
	value["total_bytes"] = page.TotalBytes
	value["preview_truncated"] = page.HasMore
	value["has_more"] = page.HasMore
	if page.NextCursor != "" {
		value["next_cursor"] = page.NextCursor
	}
	return value
}

// searchPreview keeps the aggregate observation under the MCP wire budget by
// shrinking per-source previews structurally, never by cutting serialized JSON.
func searchPreview(sources []w.Source) map[string]any {
	budget := w.MaxTextPageBytes
	for {
		entries := make([]map[string]any, 0, len(sources))
		truncated := false
		for _, source := range sources {
			entry := previewSource(source, budget)
			if has, _ := entry["has_more"].(bool); has {
				truncated = true
			}
			entries = append(entries, entry)
		}
		result := map[string]any{"sources": entries, "truncated": truncated}
		encoded, err := json.Marshal(result)
		if err == nil && len(encoded) <= 60000 {
			return result
		}
		if budget > 1 {
			budget /= 2
			continue
		}
		for _, entry := range entries {
			entry["content"] = ""
			entry["has_more"] = true
			entry["preview_truncated"] = true
		}
		result["truncated"] = true
		result["content_omitted"] = true
		return result
	}
}
