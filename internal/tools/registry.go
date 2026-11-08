package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Names is the default read-only vocabulary for new members, including the
// workflow-owned publisher. Engineering write/test tools are deliberately not
// default grants, so adding them cannot expand existing or new-install rights.
var Names = []string{"list_files", "read_file", "view_image", "search_content", "web_search", "fetch_url", "read_source", "publish_artifact", "publish_file"}

// EngineeringNames are writable/executable host tools. They are valid grants,
// but the workflow action and step role must also authorize them.
var EngineeringNames = []string{"write_file", "patch_file", "run_test"}

// AllNames is the complete grant vocabulary accepted by member configuration.
var AllNames = append(append(append([]string{}, Names...), EngineeringNames...), "run_command")

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
		"run_command":    {Name: "run_command", Description: "Execute an argv command in this task's persistent working directory using explicitly authorized full access. Use Python, document/image libraries, converters and installed tools as needed; inspect available programs first. This is not a sandbox: local files, network and existing credentials may be accessible. Work on copies, preserve inputs, obey the user's objective and publish actual output files with publish_file. No shell is inserted; use an explicit shell argv only when needed. Captures exit code and bounded output. Never rerun an uncertain external write.", Schema: object(map[string]any{"argv": map[string]any{"type": "array", "minItems": 1, "maxItems": 128, "items": text("An exact argv element")}, "cwd": text("Relative directory under the task working directory; default ."), "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 600}}, "argv")},
		"view_image":     {Name: "view_image", Description: "Return actual image pixels to the model, with a captured source ID. Supply either a path in the bound project/task directory or an authorized image source_id. Do not infer image contents from filenames. PNG, JPEG and GIF are decoded; use available conversion tools for other image formats.", Schema: object(map[string]any{"path": text("Relative image path, or an absolute path inside this task/project"), "source_id": text("Authorized image source ID")})},
		"publish_file":   {Name: "publish_file", Description: "Deliver real files created in this task's working directory. The host snapshots the bytes, computes hashes and adds download links to the final deliverable. Accepts any file format; a filename alone is not a delivery. Verify the file opens and the requested content/format is correct before publication. Intermediate authors leave files in the task directory and return exact paths to the final publisher.", Schema: object(map[string]any{"paths": map[string]any{"type": "array", "minItems": 1, "maxItems": 10, "items": text("Relative output file path under the task working directory")}, "title": text("Deliverable title"), "body": text("What was delivered, checks and remaining limitations")}, "paths", "title", "body")},
		"read_source":    {Name: "read_source", Description: "Page an exact captured source visible in this step's input snapshot. Does not refresh the original.", Schema: object(map[string]any{"source_id": text("Captured source UUID"), "cursor": text("Opaque next_cursor from the previous page"), "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": w.MaxTextPageBytes}}, "source_id")},
		"list_files":     {Name: "list_files", Description: "List files in the bound source directory; symbolic links and secrets are excluded.", Schema: object(map[string]any{"path": text("Relative directory; defaults to .")})},
		"read_file":      {Name: "read_file", Description: "Read a bounded, line-numbered text excerpt and obtain a captured source ID. Text files, CSV/TSV, HTML, text PDF, and DOCX/XLSX/PPTX are extracted on the host; legacy .doc, RTF and ODT use an installed host converter. Images, OCR, encrypted files and unsupported formats return a specific error. The returned sha256 is the SHA-256 of the complete file, not just the excerpt, so it can be passed as expected_sha256 to patch_file or write_file.", Schema: object(map[string]any{"path": text("Relative file path"), "start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1}}, "path")},
		"search_content": {Name: "search_content", Description: "Search literal text in allowed source files; results are navigation hints, not read-file evidence.", Schema: object(map[string]any{"path": text("Relative directory"), "query": text("Literal search text")}, "query")},
		"web_search":     {Name: "web_search", Description: "Search public web sources. Results are snippets; fetch original pages to verify claims.", Schema: object(map[string]any{"query": text("Search query")}, "query")},
		"fetch_url":      {Name: "fetch_url", Description: "Read a public original text page and capture its exact source. Search-engine result pages are captured as leads, not originals.", Schema: object(map[string]any{"url": text("Public HTTP(S) URL")}, "url")},
		"write_file":     {Name: "write_file", Description: "Create or replace one UTF-8 text file inside the bound project (at most 32 KiB). Replacing an existing file requires its current expected_sha256; creating a new path requires that path to be absent. Symlinks, hard links, sensitive paths, non-regular files, oversized content and stale hashes are rejected. Returns the new SHA-256 and a captured diff source ID. Retrying the exact same content is idempotent. Use patch_file, when granted, for a targeted edit of a larger existing file. This tool is only available to the developer role in an authorized develop action.", Schema: object(map[string]any{"path": text("Relative file path inside the bound project"), "content": text("Complete UTF-8 file content"), "expected_sha256": text("Current complete-file SHA-256 from read_file; required when the file already exists, empty for a new path")}, "path", "content")},
		"patch_file":     {Name: "patch_file", Description: "Replace one exact occurrence of old_text with new_text in an existing UTF-8 file. old_text must occur exactly once and the file must match expected_sha256, the complete-file SHA-256 from read_file. Full files may be up to 1 MiB; old_text and new_text together may be at most 32 KiB. Symlinks, hard links, sensitive paths, non-regular files, ambiguous or missing matches and stale hashes are rejected. Returns the new SHA-256 and a captured diff source ID. An identical retry that provably was already applied returns changed=false and is not applied again. This tool is only available to the developer role in an authorized develop action.", Schema: object(map[string]any{"path": text("Relative file path inside the bound project"), "expected_sha256": text("Current complete-file SHA-256 from read_file"), "old_text": text("Exact fragment to replace; must occur exactly once"), "new_text": text("Replacement fragment")}, "path", "expected_sha256", "old_text", "new_text")},
		"run_test":       {Name: "run_test", Description: "Run the project's own tests under the host OS sandbox. No shell is used and no network, user home or credentials are available. toolchain=go runs go test -count=1 ./...; toolchain=npm runs npm test. target is a relative project directory. A nonzero exit is returned as a tool error with captured evidence; only exit code 0 verifies tests. Other toolchains are not supported.", Schema: object(map[string]any{"toolchain": map[string]any{"type": "string", "enum": []string{"go", "npm"}}, "target": text("Relative project directory; defaults to ."), "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 600, "description": "Bounded execution limit; defaults to 120 seconds"}}, "toolchain")},
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
	Root          string
	WorkDir       string
	ExecutionMode string
	Web           WebClient
	Capture       func(w.Source) (w.Source, error)
	// ApprovedRoots are exact, host-normalized targets the user approved for this
	// one call (a file or a directory outside the workspace). A tool may resolve
	// its target under one of them and nowhere else. They never widen the
	// workspace root itself.
	ApprovedRoots []string
	// ApprovedCommand marks a command whose exact invocation the user approved in
	// a mode that does not run commands autonomously. It runs under the project
	// sandbox with writes limited to the project and any approved roots.
	ApprovedCommand bool
}

// Execute returns a JSON observation for a registered host operation. run_test
// applies its own bounded timeout instead of the short read-tool budget.
func Execute(ctx context.Context, name string, raw json.RawMessage, scope Scope) (string, error) {
	if len(scope.ApprovedRoots) > 0 && rebindApprovedTarget(name, &raw, &scope) {
		// The scope now points at exactly the approved location.
	} else if name == "read_file" || name == "list_files" || name == "search_content" {
		var args map[string]json.RawMessage
		if json.Unmarshal(raw, &args) == nil {
			var path string
			_ = json.Unmarshal(args["path"], &path)
			for _, root := range []string{scope.Root, scope.WorkDir} {
				if root == "" {
					continue
				}
				local := path
				if filepath.IsAbs(local) {
					local, _ = filepath.Rel(root, local)
				}
				if local == "" {
					local = "."
				}
				if !filepath.IsLocal(local) {
					continue
				}
				if _, err := os.Lstat(filepath.Join(root, local)); err != nil {
					continue
				}
				scope.Root = root
				args["path"], _ = json.Marshal(local)
				raw, _ = json.Marshal(args)
				break
			}
		}
	}
	if name != "run_test" && name != "run_command" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	var result any
	switch name {
	case "run_command":
		return ExecuteCommand(ctx, raw, scope)
	case "view_image":
		var in struct {
			Path     string `json:"path"`
			SourceID string `json:"source_id"`
		}
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		return ViewImage(ctx, scope, in.Path)
	case "write_file":
		var in WriteFileInput
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		write, err := WriteFile(ctx, scope.Root, in)
		if err != nil {
			return "", err
		}
		return observeWrite(scope, write)
	case "patch_file":
		var in PatchFileInput
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		write, err := PatchFile(ctx, scope.Root, in)
		if err != nil {
			return "", err
		}
		return observeWrite(scope, write)
	case "run_test":
		var in struct {
			Toolchain      string `json:"toolchain"`
			Target         string `json:"target"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := Decode(raw, &in); err != nil {
			return "", err
		}
		test, err := RunTest(ctx, scope.Root, RunTestInput{Toolchain: in.Toolchain, Target: in.Target, TimeoutSeconds: in.TimeoutSeconds})
		if err != nil {
			return "", err
		}
		source, err := scope.Capture(w.Source{Kind: "test", Title: test.Command, Path: test.Target, SHA256: test.WorkspaceSHA256, Content: test.Output, CapturedAt: w.Now(), Truncated: test.Truncated, ExitCode: test.ExitCode, TimedOut: test.TimedOut})
		if err != nil {
			return "", err
		}
		if !test.Passed {
			reason := "test exited with code " + strconv.Itoa(test.ExitCode)
			if test.TimedOut {
				reason = "test exceeded its bounded timeout and was killed"
			}
			return "", fmt.Errorf("test_failed: %s; command=%s; source_id=%s; output:\n%s", reason, test.Command, source.ID, utf8Prefix(test.Output, 4096))
		}
		result = map[string]any{"passed": true, "toolchain": test.Toolchain, "target": test.Target, "command": test.Command, "exit_code": 0, "duration_ms": test.DurationMS, "truncated": test.Truncated, "workspace_sha256": test.WorkspaceSHA256, "workspace_changed": test.WorkspaceChanged, "evidence_reason": test.EvidenceReason, "source_id": source.ID, "output_preview": utf8Prefix(test.Output, 4096)}
		return marshalObservation(result)
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

// observeWrite turns an accepted write into the shared observation and, only
// for a real change, one captured diff source.
func observeWrite(scope Scope, write WriteResult) (string, error) {
	observation := map[string]any{"path": write.Path, "changed": write.Changed, "sha256": write.SHA256, "previous_sha256": write.PreviousSHA256, "bytes": write.Bytes, "source_id": "", "diff_truncated": write.DiffTruncated}
	if !write.Changed {
		reason := write.Reason
		if reason == "" {
			reason = "content already matches; nothing was overwritten"
		}
		observation["reason"] = reason
		return marshalObservation(observation)
	}
	source, err := scope.Capture(w.Source{Kind: "diff", Title: write.Path, Path: write.Path, SHA256: write.SHA256, Content: write.Diff, CapturedAt: w.Now(), Truncated: write.DiffTruncated})
	if err != nil {
		return "", err
	}
	observation["source_id"] = source.ID
	observation["diff_preview"] = utf8Prefix(write.Diff, 4096)
	return marshalObservation(observation)
}

func marshalObservation(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(encoded) > 65536 {
		return "", errors.New("tool observation exceeds 64 KiB")
	}
	return string(encoded), nil
}

// boundedText returns at most maxBytes of a valid UTF-8 prefix.
func utf8Prefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…"
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

// rebindApprovedTarget narrows the scope to exactly the user-approved target and
// returns true when it applied. It only accepts a normalized target equal to an
// approved file, or one inside an approved directory; anything else is left to
// the normal workspace confinement and will fail there.
func rebindApprovedTarget(name string, raw *json.RawMessage, scope *Scope) bool {
	switch name {
	case "read_file", "list_files", "search_content", "write_file", "patch_file", "view_image":
	default:
		return false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(*raw, &args) != nil {
		return false
	}
	var path string
	if err := json.Unmarshal(args["path"], &path); err != nil || path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		base := scope.Root
		if base == "" {
			base = scope.WorkDir
		}
		if base == "" {
			return false
		}
		path = filepath.Join(base, path)
	}
	resolved, err := resolveApprovedPath(path)
	if err != nil {
		return false
	}
	for _, approved := range scope.ApprovedRoots {
		root := approved
		local := ""
		if resolved == root {
			// An exact file approval: operate on its parent with its base name.
			if info, err := os.Lstat(resolved); err == nil && info.IsDir() {
				local = "."
			} else {
				root, local = filepath.Dir(resolved), filepath.Base(resolved)
			}
		} else if rel, err := filepath.Rel(root, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			local = rel
		} else {
			continue
		}
		if DeniedPath(local) {
			return false
		}
		scope.Root, scope.WorkDir = root, ""
		args["path"], _ = json.Marshal(local)
		encoded, err := json.Marshal(args)
		if err != nil {
			return false
		}
		*raw = encoded
		return true
	}
	return false
}

// resolveApprovedPath resolves a path to its real location, following symlinks on
// the deepest existing ancestor so a not-yet-created child cannot hide an escape.
func resolveApprovedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, nil
	}
	remaining := []string{}
	current := filepath.Clean(absolute)
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("path cannot be resolved")
		}
		remaining = append([]string{filepath.Base(current)}, remaining...)
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(append([]string{resolved}, remaining...)...), nil
		}
		current = parent
	}
}
