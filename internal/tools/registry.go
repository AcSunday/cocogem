package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Names is the finite v0.1 tool vocabulary, including the workflow-owned publisher.
var Names = []string{"list_files", "read_file", "search_content", "web_search", "fetch_url", "read_source", "publish_artifact"}

func object(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func text(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// Definitions returns schemas only for explicitly allowed capabilities.
func Definitions(names []string) []model.Tool {
	defs := map[string]model.Tool{
		"read_source":    {Name: "read_source", Description: "Read an exact captured source visible in this step's input snapshot. Does not refresh the original.", Schema: object(map[string]any{"source_id": text("Captured source UUID")}, "source_id")},
		"list_files":     {Name: "list_files", Description: "List files in the bound source directory; symbolic links and secrets are excluded.", Schema: object(map[string]any{"path": text("Relative directory; defaults to .")})},
		"read_file":      {Name: "read_file", Description: "Read a bounded text excerpt and obtain a captured source ID.", Schema: object(map[string]any{"path": text("Relative file path"), "start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1}}, "path")},
		"search_content": {Name: "search_content", Description: "Search literal text in allowed source files; results are navigation hints, not read-file evidence.", Schema: object(map[string]any{"path": text("Relative directory"), "query": text("Literal search text")}, "query")},
		"web_search":     {Name: "web_search", Description: "Search public web sources. Results are snippets; fetch original pages to verify claims.", Schema: object(map[string]any{"query": text("Search query")}, "query")},
		"fetch_url":      {Name: "fetch_url", Description: "Read a public text page and capture its exact source. Does not access logged-in pages.", Schema: object(map[string]any{"url": text("Public HTTP(S) URL")}, "url")},
		"publish_artifact": {Name: "publish_artifact", Description: "Submit this step's deliverable. Use captured source IDs only. Code findings require file evidence. Research reports include scope, methods, findings, disagreements, limitations and next steps in the body.", Schema: object(map[string]any{
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

// Scope limits a tool call to its workspace and exact evidence capture callback.
type Scope struct {
	Root    string
	Web     *Web
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
		result = source
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
		result = source
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
		result = map[string]any{"sources": sources}
	default:
		return "", errors.New("unknown or unsupported read-only tool")
	}
	b, err := json.Marshal(result)
	return string(b), err
}
