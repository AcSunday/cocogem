package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// ThreadFileView is the bounded file, line-range, image and diff payload opened
// from a member message or tool activity row.
type ThreadFileView struct {
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	DisplayPath    string `json:"display_path"`
	RootKind       string `json:"root_kind"`
	MediaType      string `json:"media_type,omitempty"`
	RawURL         string `json:"raw_url,omitempty"`
	Content        string `json:"content"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	TotalLines     int    `json:"total_lines"`
	HighlightStart int    `json:"highlight_start,omitempty"`
	HighlightEnd   int    `json:"highlight_end,omitempty"`
	Truncated      bool   `json:"truncated"`
	Diff           string `json:"diff,omitempty"`
	DiffSourceID   string `json:"diff_source_id,omitempty"`
	SourceID       string `json:"source_id,omitempty"`
}

const (
	maxThreadFileReadBytes = 2 << 20
	maxThreadFileBodyBytes = 512 << 10
	maxThreadFileLines     = 2000
	windowThreadFileLines  = 1200
	maxThreadImageBytes    = 16 << 20
)

var (
	lineHashSuffix  = regexp.MustCompile(`#L(\d+)(?:-L?(\d+))?$`)
	lineColonSuffix = regexp.MustCompile(`:(\d+)(?:-(\d+))?(?::\d+)?$`)
)

func parsePathAndLines(raw string, startHint, endHint int) (string, int, int) {
	cleaned := strings.TrimSpace(raw)
	if strings.HasPrefix(cleaned, "file://") {
		if parsed, err := url.Parse(cleaned); err == nil && parsed.Scheme == "file" && parsed.Path != "" {
			cleaned = parsed.Path
			if parsed.Fragment != "" {
				cleaned += "#" + parsed.Fragment
			}
		} else {
			cleaned = strings.TrimPrefix(cleaned, "file://")
		}
	}
	start, end := startHint, endHint
	if match := lineHashSuffix.FindStringSubmatchIndex(cleaned); match != nil {
		if start == 0 {
			start, _ = strconv.Atoi(cleaned[match[2]:match[3]])
		}
		if end == 0 && match[4] >= 0 {
			end, _ = strconv.Atoi(cleaned[match[4]:match[5]])
		}
		cleaned = cleaned[:match[0]]
	} else if match := lineColonSuffix.FindStringSubmatchIndex(cleaned); match != nil {
		if start == 0 {
			start, _ = strconv.Atoi(cleaned[match[2]:match[3]])
		}
		if end == 0 && match[4] >= 0 {
			end, _ = strconv.Atoi(cleaned[match[4]:match[5]])
		}
		cleaned = cleaned[:match[0]]
	}
	if start > 0 && end < start {
		end = start
	}
	return cleaned, start, end
}

func isBenignEnvExample(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == ".env.example" || base == ".env.sample" || base == ".env.template"
}

func threadFileSensitive(path string) bool {
	if isBenignEnvExample(path) {
		for _, part := range strings.Split(filepath.ToSlash(path), "/") {
			if strings.EqualFold(part, ".env.example") || strings.EqualFold(part, ".env.sample") || strings.EqualFold(part, ".env.template") {
				continue
			}
			if tools.SensitivePath(part) {
				return true
			}
		}
		return false
	}
	return tools.SensitivePath(path)
}

func isPreviewableImageMedia(mediaType string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	switch base {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml":
		return true
	}
	return false
}

type resolvedThreadTarget struct {
	absPath     string
	relPath     string
	displayPath string
	rootKind    string
	rootDir     string
	candidate   w.WorkspaceExecution
	wsRoot      string
	sources     []w.Source
}

func (s *Server) resolveThreadFileTarget(ctx context.Context, threadID, rawPath string) (resolvedThreadTarget, error) {
	if strings.TrimSpace(rawPath) == "" {
		return resolvedThreadTarget{}, &RequestError{400, "invalid_request", "文件路径不能为空"}
	}
	var th w.Thread
	var ws w.Workspace
	var settings w.Settings
	var executions []w.WorkspaceExecution
	var runs []w.Run
	var sources []w.Source
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		th, err = tx.Thread(threadID)
		if err != nil {
			return err
		}
		ws, err = tx.Workspace(th.WorkspaceID)
		if err != nil {
			return err
		}
		settings, _ = tx.Settings()
		executions, _ = tx.ThreadExecutions(threadID)
		runs, _ = tx.Runs(threadID)
		sources, _ = tx.Sources(threadID)
		return nil
	})
	if err != nil {
		return resolvedThreadTarget{}, err
	}
	if threadFileSensitive(rawPath) {
		return resolvedThreadTarget{}, &RequestError{403, "sensitive_path", "处于安全策略保护下的凭证文件不可通过文件查看器打开"}
	}
	var liveCandidate w.WorkspaceExecution
	type searchRoot struct {
		dir  string
		kind string
	}
	roots := []searchRoot{}
	for i := len(executions) - 1; i >= 0; i-- {
		ex := executions[i]
		if ex.Kind == "verify_dir" || ex.Path == "" {
			continue
		}
		if ex.Status == "active" || ex.Status == "completed" || ex.Status == "uncertain" {
			if info, statErr := os.Stat(ex.Path); statErr == nil && info.IsDir() {
				if liveCandidate.ID == "" {
					liveCandidate = ex
				}
				roots = append(roots, searchRoot{dir: ex.Path, kind: "candidate"})
			}
		}
	}
	if ws.Root != "" {
		roots = append(roots, searchRoot{dir: ws.Root, kind: "workspace"})
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if dir := runs[i].TaskDirectory; dir != "" {
			roots = append(roots,
				searchRoot{dir: dir, kind: "task"},
				searchRoot{dir: filepath.Join(dir, "outputs"), kind: "task"},
				searchRoot{dir: filepath.Join(dir, "inputs"), kind: "task"},
			)
			break
		}
	}
	checkRegular := func(candidateAbs, rootDir, rootKind string) (resolvedThreadTarget, bool) {
		realPath, err := filepath.EvalSymlinks(candidateAbs)
		if err != nil {
			return resolvedThreadTarget{}, false
		}
		if threadFileSensitive(realPath) {
			return resolvedThreadTarget{}, false
		}
		info, err := os.Stat(realPath)
		if err != nil || !info.Mode().IsRegular() {
			return resolvedThreadTarget{}, false
		}
		rel := filepath.ToSlash(candidateAbs)
		display := rel
		if rootDir != "" {
			realRoot, rootErr := filepath.EvalSymlinks(rootDir)
			if rootErr != nil {
				return resolvedThreadTarget{}, false
			}
			if !w.ScopeContains(realRoot, realPath) {
				return resolvedThreadTarget{}, false
			}
			if r, relErr := filepath.Rel(realRoot, realPath); relErr == nil {
				rel = filepath.ToSlash(r)
				display = rel
			}
		}
		return resolvedThreadTarget{
			absPath:     realPath,
			relPath:     rel,
			displayPath: display,
			rootKind:    rootKind,
			rootDir:     rootDir,
			candidate:   liveCandidate,
			wsRoot:      ws.Root,
			sources:     sources,
		}, true
	}

	cleaned := filepath.Clean(rawPath)
	if !filepath.IsAbs(cleaned) {
		for _, root := range roots {
			if !filepath.IsLocal(cleaned) {
				continue
			}
			if target, ok := checkRegular(filepath.Join(root.dir, cleaned), root.dir, root.kind); ok {
				return target, nil
			}
		}
	} else {
		// 1. If the absolute path sits inside the bound workspace root and an
		// active candidate worktree exists, prefer the candidate's copy of that
		// relative path first (fall back to workspace if deleted in candidate).
		if ws.Root != "" {
			if realWS, wsErr := filepath.EvalSymlinks(ws.Root); wsErr == nil {
				if realTarget, tErr := filepath.EvalSymlinks(cleaned); tErr == nil && w.ScopeContains(realWS, realTarget) {
					if rel, relErr := filepath.Rel(realWS, realTarget); relErr == nil && filepath.IsLocal(rel) {
						for _, root := range roots {
							if target, ok := checkRegular(filepath.Join(root.dir, rel), root.dir, root.kind); ok {
								return target, nil
							}
						}
					}
				}
			}
		}
		// 2. Direct match inside any thread/workspace/allowed root.
		for _, root := range roots {
			if target, ok := checkRegular(cleaned, root.dir, root.kind); ok {
				return target, nil
			}
		}
		for _, allowedRoot := range s.config.Roots {
			if target, ok := checkRegular(cleaned, allowedRoot, "workspace"); ok {
				return target, nil
			}
		}
		// 2. Historical worktree/verify_dir path or workspace-prefixed suffix: match
		// trailing segments inside current candidate or workspace root.
		slash := filepath.ToSlash(cleaned)
		if idx := strings.Index(slash, "/worktree/"); idx >= 0 {
			rel := slash[idx+len("/worktree/"):]
			for _, root := range roots {
				if filepath.IsLocal(rel) {
					if target, ok := checkRegular(filepath.Join(root.dir, rel), root.dir, root.kind); ok {
						return target, nil
					}
				}
			}
		}
		// 3. Full access or captured source path allows opening host files directly.
		capturedMatch := slices.ContainsFunc(sources, func(src w.Source) bool {
			return src.Path == rawPath || src.Path == cleaned || src.Path == slash
		})
		if settings.ExecutionMode == w.ExecutionFullAccess || capturedMatch {
			if target, ok := checkRegular(cleaned, "", "host"); ok {
				return target, nil
			}
		}
	}

	return resolvedThreadTarget{
		relPath:     filepath.ToSlash(cleaned),
		displayPath: filepath.ToSlash(cleaned),
		candidate:   liveCandidate,
		wsRoot:      ws.Root,
		sources:     sources,
	}, nil
}

func extractFileDiff(fullDiff, relPath string) string {
	if fullDiff == "" || relPath == "" {
		return ""
	}
	target := filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	headerA := "diff --git a/" + target + " "
	headerB := " b/" + target
	lines := strings.Split(fullDiff, "\n")
	var out []string
	capturing := false
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			if capturing {
				break
			}
			if strings.HasPrefix(line, headerA) || strings.HasSuffix(line, headerB) {
				capturing = true
			}
		}
		if capturing {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func (s *Server) lookupFileDiff(ctx context.Context, target resolvedThreadTarget) (string, string) {
	rel := strings.TrimPrefix(target.relPath, "./")
	if target.candidate.ID != "" && s.engine != nil && rel != "" && filepath.IsLocal(rel) {
		if fullDiff, err := s.engine.CandidateDiff(ctx, target.candidate.ThreadID, target.candidate.ID); err == nil {
			if section := extractFileDiff(fullDiff, rel); section != "" {
				return section, ""
			}
		}
	}
	gitRoot := target.wsRoot
	if target.rootKind == "candidate" && target.rootDir != "" {
		gitRoot = target.rootDir
	}
	if gitRoot != "" && rel != "" && filepath.IsLocal(rel) && !tools.DeniedPath(rel) {
		for _, rev := range []string{"HEAD", ""} {
			raw, err := tools.GitInspect(ctx, gitRoot, tools.GitInspectInput{
				Subcommand: "diff",
				Path:       rel,
				Revision:   rev,
			})
			if err != nil {
				continue
			}
			var decoded struct {
				Output string `json:"output"`
			}
			if decodeJSONString(raw, &decoded) && strings.TrimSpace(decoded.Output) != "" {
				return strings.TrimSpace(decoded.Output), ""
			}
		}
	}
	for i := len(target.sources) - 1; i >= 0; i-- {
		src := target.sources[i]
		if src.Kind != "diff" {
			continue
		}
		normSrc := strings.TrimPrefix(filepath.ToSlash(src.Path), "./")
		if normSrc == rel || src.Path == target.absPath || src.Title == rel {
			if strings.TrimSpace(src.Content) != "" {
				return src.Content, src.ID
			}
		}
	}
	return "", ""
}

func (s *Server) threadFile(rw http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("id")
	startHint, _ := strconv.Atoi(r.URL.Query().Get("start_line"))
	endHint, _ := strconv.Atoi(r.URL.Query().Get("end_line"))
	cleanPath, startLine, endLine := parsePathAndLines(r.URL.Query().Get("path"), startHint, endHint)
	target, err := s.resolveThreadFileTarget(r.Context(), threadID, cleanPath)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	if target.absPath != "" {
		f, openErr := os.Open(target.absPath)
		if openErr != nil {
			respond(rw, r, nil, openErr)
			return
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxThreadFileReadBytes+1))
		_ = f.Close()
		if readErr != nil {
			respond(rw, r, nil, readErr)
			return
		}
		media := tools.FileMediaType(target.absPath, data)
		if isPreviewableImageMedia(media) {
			diffText, diffSourceID := s.lookupFileDiff(r.Context(), target)
			respond(rw, r, ThreadFileView{
				Kind:         "image",
				Path:         target.absPath,
				DisplayPath:  target.displayPath,
				RootKind:     target.rootKind,
				MediaType:    media,
				RawURL:       "/api/v1/threads/" + url.PathEscape(threadID) + "/file/raw?path=" + url.QueryEscape(cleanPath),
				Diff:         diffText,
				DiffSourceID: diffSourceID,
			}, nil)
			return
		}
		fileTruncated := len(data) > maxThreadFileReadBytes
		if fileTruncated {
			data = data[:maxThreadFileReadBytes]
		}
		text := ""
		if parsed, ok, parseErr := tools.AdaptDocument(r.Context(), target.absPath, data); ok && parseErr == nil && parsed.Content != "" {
			text = parsed.Content
			if parsed.Truncated {
				fileTruncated = true
			}
		} else {
			if bytes.ContainsRune(data, 0) || !utf8.Valid(data) {
				respond(rw, r, nil, &RequestError{415, "binary_file", "该文件为二进制格式，暂不支持在线文本预览"})
				return
			}
			text = string(data)
		}
		diffText, diffSourceID := s.lookupFileDiff(r.Context(), target)
		view := buildTextFileView(target, text, startLine, endLine, fileTruncated)
		view.MediaType = media
		view.Diff = diffText
		view.DiffSourceID = diffSourceID
		respond(rw, r, view, nil)
		return
	}

	// Fallback: check captured sources (e.g. deleted files, diff sources, native_file captures).
	norm := strings.TrimPrefix(filepath.ToSlash(cleanPath), "./")
	var matchedFile, matchedDiff *w.Source
	for i := len(target.sources) - 1; i >= 0; i-- {
		src := target.sources[i]
		srcPath := strings.TrimPrefix(filepath.ToSlash(src.Path), "./")
		if srcPath != norm && src.Path != cleanPath && src.Title != cleanPath && src.ID != cleanPath {
			continue
		}
		if src.Kind == "image" || isPreviewableImageMedia(src.MediaType) {
			respond(rw, r, ThreadFileView{
				Kind:        "image",
				Path:        src.Path,
				DisplayPath: src.Title,
				RootKind:    "source",
				MediaType:   src.MediaType,
				RawURL:      "/api/v1/sources/" + url.PathEscape(src.ID) + "/original?inline=1",
				SourceID:    src.ID,
			}, nil)
			return
		}
		if src.Kind == "diff" && matchedDiff == nil {
			copySrc := src
			matchedDiff = &copySrc
		} else if (src.Kind == "file" || src.Kind == "native_file") && matchedFile == nil {
			copySrc := src
			matchedFile = &copySrc
		}
	}
	if matchedFile != nil {
		diffText, diffSourceID := s.lookupFileDiff(r.Context(), target)
		if diffText == "" && matchedDiff != nil {
			diffText, diffSourceID = matchedDiff.Content, matchedDiff.ID
		}
		respond(rw, r, ThreadFileView{
			Kind:           "text",
			Path:           matchedFile.Path,
			DisplayPath:    firstNonEmptyString(matchedFile.Path, matchedFile.Title),
			RootKind:       "source",
			Content:        matchedFile.Content,
			StartLine:      max(1, matchedFile.StartLine),
			EndLine:        matchedFile.EndLine,
			TotalLines:     max(matchedFile.EndLine, len(strings.Split(matchedFile.Content, "\n"))),
			HighlightStart: startLine,
			HighlightEnd:   endLine,
			Truncated:      matchedFile.Truncated,
			Diff:           diffText,
			DiffSourceID:   diffSourceID,
			SourceID:       matchedFile.ID,
		}, nil)
		return
	}
	if matchedDiff != nil {
		respond(rw, r, ThreadFileView{
			Kind:         "diff",
			Path:         matchedDiff.Path,
			DisplayPath:  firstNonEmptyString(matchedDiff.Path, matchedDiff.Title),
			RootKind:     "source",
			Diff:         matchedDiff.Content,
			DiffSourceID: matchedDiff.ID,
			SourceID:     matchedDiff.ID,
		}, nil)
		return
	}
	respond(rw, r, nil, &RequestError{404, "file_not_found", "未找到该文件，可能已被移动或位于未绑定目录"})
}

func buildTextFileView(target resolvedThreadTarget, text string, highlightStart, highlightEnd int, fileTruncated bool) ThreadFileView {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	totalLines := len(lines)
	windowStart := 1
	windowEnd := totalLines
	truncated := fileTruncated

	if totalLines > maxThreadFileLines {
		anchor := highlightStart
		if anchor < 1 || anchor > totalLines {
			anchor = 1
		}
		windowStart = max(1, anchor-windowThreadFileLines/3)
		windowEnd = min(totalLines, windowStart+windowThreadFileLines-1)
		if windowEnd-windowStart+1 < windowThreadFileLines {
			windowStart = max(1, windowEnd-windowThreadFileLines+1)
		}
		truncated = true
	}

	var out strings.Builder
	actualEnd := windowStart - 1
	for i := windowStart - 1; i < windowEnd; i++ {
		line := lines[i]
		if out.Len()+len(line)+1 > maxThreadFileBodyBytes && i > windowStart-1 {
			truncated = true
			break
		}
		if i > windowStart-1 {
			out.WriteByte('\n')
		}
		out.WriteString(line)
		actualEnd = i + 1
	}
	return ThreadFileView{
		Kind:           "text",
		Path:           target.absPath,
		DisplayPath:    target.displayPath,
		RootKind:       target.rootKind,
		Content:        out.String(),
		StartLine:      windowStart,
		EndLine:        actualEnd,
		TotalLines:     totalLines,
		HighlightStart: highlightStart,
		HighlightEnd:   highlightEnd,
		Truncated:      truncated,
	}
}

func (s *Server) threadFileRaw(rw http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("id")
	cleanPath, _, _ := parsePathAndLines(r.URL.Query().Get("path"), 0, 0)
	target, err := s.resolveThreadFileTarget(r.Context(), threadID, cleanPath)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	if target.absPath == "" {
		respond(rw, r, nil, &RequestError{404, "file_not_found", "未找到该图片文件"})
		return
	}
	f, err := os.Open(target.absPath)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxThreadImageBytes+1))
	_ = f.Close()
	if readErr != nil {
		respond(rw, r, nil, readErr)
		return
	}
	if len(data) > maxThreadImageBytes {
		respond(rw, r, nil, &RequestError{413, "file_too_large", "图片文件超过 16 MiB 预览上限"})
		return
	}
	media := tools.FileMediaType(target.absPath, data)
	if !isPreviewableImageMedia(media) {
		respond(rw, r, nil, &RequestError{415, "unsupported_media", "仅支持预览常见图片格式"})
		return
	}
	rw.Header().Set("Content-Type", media)
	rw.Header().Set("Content-Length", strconv.Itoa(len(data)))
	rw.Header().Set("Content-Disposition", "inline")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Cache-Control", "private, no-store")
	if strings.HasPrefix(strings.ToLower(media), "image/svg+xml") {
		rw.Header().Set("Content-Security-Policy", "sandbox")
	}
	rw.WriteHeader(200)
	_, _ = rw.Write(data)
}

func decodeJSONString(raw string, out any) bool {
	return json.Unmarshal([]byte(raw), out) == nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
