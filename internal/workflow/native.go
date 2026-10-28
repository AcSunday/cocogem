package workflow

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// nativeEventLimit bounds the persisted UI event; the full bounded native
// observation lives in the captured source and is read through read_source.
const nativeEventLimit = 64 << 10
const nativeWebLimitation = "原生联网工具返回的是 CLI 处理后的观察或摘要，可能不是原始网页全文。"

func rawJSON(value any) []byte {
	switch current := value.(type) {
	case json.RawMessage:
		return current
	case []byte:
		return current
	case string:
		return []byte(current)
	default:
		return nil
	}
}

func trimRunes(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

// nativeEventPayload keeps the persisted event bounded without discarding the
// actual observation that capture stores separately.
func nativeEventPayload(data map[string]any) map[string]any {
	out := make(map[string]any, len(data)+1)
	for key, value := range data {
		out[key] = value
	}
	if output, ok := out["output"].(string); ok {
		if trimmed, cut := trimRunes(output, nativeEventLimit); cut {
			out["output"] = trimmed
			out["output_truncated"] = true
		}
	}
	if file, ok := out["file"]; ok {
		// The CLI's file body belongs to the bounded source capture, never to
		// the event trailer; keep only the small range metadata here.
		if meta := boundedNativeFileMetadata(file); meta != nil {
			out["file"] = meta
		} else {
			delete(out, "file")
		}
	}
	return out
}

func boundedNativeFileMetadata(value any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var meta nativeReadFile
	if json.Unmarshal(encoded, &meta) != nil {
		return nil
	}
	out := map[string]any{
		"filePath":   boundedField(meta.FilePath, 300),
		"numLines":   meta.NumLines,
		"startLine":  meta.StartLine,
		"totalLines": meta.TotalLines,
	}
	if meta.Truncated {
		out["truncated"] = true
	}
	return out
}

// nativeReadFile is the CLI's own completeness metadata for a Read result.
// Content is a pointer to distinguish "no content field" from an empty file.
type nativeReadFile struct {
	FilePath   string  `json:"filePath"`
	Content    *string `json:"content"`
	NumLines   int     `json:"numLines"`
	StartLine  int     `json:"startLine"`
	TotalLines int     `json:"totalLines"`
	Truncated  bool    `json:"truncated"`
}

// nativeReadMetadata returns the CLI-reported file range, never a host
// re-read. Missing metadata means the host cannot claim the whole file.
func nativeReadMetadata(data map[string]any) (nativeReadFile, bool) {
	value, ok := data["file"]
	if !ok {
		return nativeReadFile{}, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nativeReadFile{}, false
	}
	var meta nativeReadFile
	if json.Unmarshal(encoded, &meta) != nil || meta.FilePath == "" {
		return nativeReadFile{}, false
	}
	return meta, true
}

// nativeSource converts only an actual successful native result into a source.
// Failed results and blank web/search observations never become evidence. A
// blank Read is kept only when the CLI's own file metadata proves the file
// exists (an empty file), which is not the same as a blank web page.
func nativeSource(run w.Run, data map[string]any) (w.Source, bool) {
	if stringValue(data["status"]) != "ok" {
		return w.Source{}, false
	}
	output := stringValue(data["output"])
	var input struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Query    string `json:"query"`
		Pattern  string `json:"pattern"`
		URL      string `json:"url"`
	}
	_ = json.Unmarshal(rawJSON(data["input"]), &input)
	name := stringValue(data["name"])
	fileMeta, fileKnown := nativeReadMetadata(data)
	if name == "Read" && fileKnown && fileMeta.Content != nil {
		// The CLI's file body is the actual returned text; the display copy
		// may have been line-truncated. A present content field wins even when
		// it is shorter or empty. Never re-read the file from the host.
		output = *fileMeta.Content
	}
	if strings.TrimSpace(output) == "" && !(name == "Read" && fileKnown) {
		return w.Source{}, false
	}
	source := w.Source{Content: output, CapturedAt: w.Now()}
	switch name {
	case "Read":
		source.Kind = "native_file"
		source.Path = nativePath(run.Workspace.Root, input.FilePath)
		if source.Path == "" && fileMeta.FilePath != "" {
			source.Path = nativePath(run.Workspace.Root, fileMeta.FilePath)
		}
		source.Title = source.Path
		if source.Title == "" {
			source.Title = "Read"
		}
	case "WebFetch":
		source.Kind = "native_web"
		source.URL = input.URL
		source.Title = "WebFetch observation"
		if host := urlHost(input.URL); host != "" {
			source.Title = host
		}
	case "WebSearch":
		source.Kind = "native_search"
		source.URL = input.URL
		label := input.Query
		if label == "" {
			label = input.URL
		}
		source.Title = "WebSearch: " + label
	case "Grep":
		source.Kind = "native_search"
		source.Path = nativePath(run.Workspace.Root, input.Path)
		source.Title = "Grep: " + input.Pattern
	case "Glob":
		source.Kind = "native_search"
		source.Path = nativePath(run.Workspace.Root, input.Path)
		source.Title = "Glob: " + input.Pattern
	default:
		return w.Source{}, false
	}
	source.Title = boundedField(strings.TrimSpace(source.Title), 300)
	source.Truncated, _ = data["truncated"].(bool)
	if name == "Read" {
		if !fileKnown {
			// The host only saw whatever the CLI returned; without the CLI's
			// own range metadata it must not claim the whole file was read.
			source.Truncated = true
		} else {
			source.StartLine = fileMeta.StartLine
			if fileMeta.NumLines > 0 {
				source.EndLine = fileMeta.StartLine + fileMeta.NumLines - 1
			}
			// A known-empty file is complete; any other read is a segment
			// unless it starts at line 1 and reaches totalLines.
			knownEmpty := fileMeta.NumLines == 0 && fileMeta.TotalLines == 0 && strings.TrimSpace(output) == ""
			if fileMeta.Truncated || (!knownEmpty &&
				(fileMeta.StartLine > 1 || fileMeta.NumLines <= 0 || fileMeta.TotalLines <= 0 || source.EndLine < fileMeta.TotalLines)) {
				source.Truncated = true
			}
		}
	}
	if len(source.Content) > 512<<10 {
		source.Content, _ = trimRunes(source.Content, 512<<10)
		source.Truncated = true
	}
	return source, true
}

func nativePath(root, path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	if root != "" {
		if rel, err := filepath.Rel(filepath.Clean(root), clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(clean)
}

func urlHost(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// ensureNativeObservationDisclosure adds one explicit limitation when a
// deliverable cites a native web observation, so an artifact cannot present it
// as raw page text.
func ensureNativeObservationDisclosure(artifact *w.Artifact, sources []w.Source) {
	if artifact == nil {
		return
	}
	byID := map[string]w.Source{}
	for _, source := range sources {
		byID[source.ID] = source
	}
	cited := slices.ContainsFunc(artifact.SourceIDs, func(id string) bool { return byID[id].Kind == "native_web" })
	if !cited {
		return
	}
	for _, limitation := range artifact.Limitations {
		if limitation == nativeWebLimitation {
			return
		}
	}
	artifact.Limitations = append(artifact.Limitations, nativeWebLimitation)
}
