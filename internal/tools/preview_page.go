package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PreviewInput is the deliberately narrow page probe contract. It accepts no
// URL and no port: the host derives the sole origin itself, so a model can never
// ask the browser for an arbitrary address.
type PreviewInput struct {
	// Mode is "static" (the host serves the bound project read-only) or
	// "command" (the host runs a bounded preview command on a random port).
	Mode string `json:"mode"`
	// Entry is a project-relative HTML entry for mode=static, or the exact
	// preview argv elements for mode=command.
	Entry string   `json:"entry"`
	Argv  []string `json:"argv"`
	// EntryRoot is the host-bound project root. It is filled by the host tool
	// dispatcher and never by model input (the field is ignored on decode).
	EntryRoot string `json:"-"`
	// TrustedCommand marks full_access mode where the command preview runs
	// without the OS sandbox. Filled only by the host dispatcher.
	TrustedCommand bool `json:"-"`
	// Viewport is a bounded width/height for the screenshot.
	Width  int `json:"width"`
	Height int `json:"height"`
	// TimeoutSeconds bounds the page render (navigation, readiness and capture).
	// A cold browser start gets its own bounded allowance on top of it so a slow
	// machine cannot spend the render budget before a page target exists.
	TimeoutSeconds int `json:"timeout_seconds"`
}

// PreviewResult is the factual observation of one rendered page. The screenshot
// is returned as bytes for the caller to capture as an image source; the code
// root is never written.
type PreviewResult struct {
	Origin        string
	Title         string
	ConsoleErrors []string
	PageErrors    []string
	AXTree        string
	ScreenshotPNG []byte
	// EngineUnavailable is non-empty when no browser engine exists; the caller
	// must report that as a capability limit rather than as a rendered page.
	EngineUnavailable string
	DurationMS        int64
}

// PreviewLimits keeps one probe bounded in time, memory and output.
const (
	previewWidthDefault  = 1280
	previewHeightDefault = 800
	previewMaxDimension  = 2560
	previewTimeout       = 45 * time.Second
	// previewStartupAllowance bounds a cold browser start on top of the render
	// budget, so a slow runner cannot spend the caller's timeout before a page
	// target exists.
	previewStartupAllowance = 120 * time.Second
	previewMaxScreenshot    = 16 << 20
)

// browserEngine resolves a Chromium-family binary without accepting a model
// supplied path. It prefers the bundled Playwright/Chromium locations, then the
// system Chrome. An unavailable engine is an honest capability gap.
func browserEngine() (string, string) {
	candidates := []string{
		os.Getenv("COCOGEM_CHROME"),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, "Library", "Caches", "ms-playwright", "chromium-*", "chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium"),
		)
	}
	for _, candidate := range candidates {
		if candidate == "" || strings.Contains(candidate, "*") {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return candidate, ""
		}
	}
	// A Playwright cache uses a versioned directory; inspect the glob explicitly
	// on both macOS and Linux.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		patterns := []string{
			filepath.Join(home, "Library", "Caches", "ms-playwright", "chromium-*", "chrome-mac*", "Chromium.app", "Contents", "MacOS", "Chromium"),
			filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux*", "chrome"),
		}
		for _, pattern := range patterns {
			if entries, err := filepath.Glob(pattern); err == nil {
				for _, entry := range entries {
					if info, statErr := os.Stat(entry); statErr == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
						return entry, ""
					}
				}
			}
		}
	}
	return "", "browser_engine_unavailable: no Chromium-family browser is installed; the page was not rendered"
}

// ServeStaticProject starts an in-memory read-only static server for entry's
// directory on a random loopback port. It never exposes the rest of the project
// or a directory listing.
func ServeStaticProject(ctx context.Context, root, entry string) (origin string, closeFn func(), err error) {
	if root == "" {
		return "", nil, errors.New("no source directory is bound")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, errors.New("source directory is unavailable")
	}
	cleanEntry := strings.TrimSpace(entry)
	if cleanEntry == "" {
		cleanEntry = "index.html"
	}
	if len(cleanEntry) > 512 || !filepath.IsLocal(cleanEntry) {
		return "", nil, errors.New("preview entry must be a project-relative file")
	}
	if DeniedPath(cleanEntry) {
		return "", nil, errors.New("preview entry is a sensitive path")
	}
	// Open through os.OpenRoot(resolved) so no symlink component in cleanEntry
	// can ever escape the bound project directory.
	projectRoot, err := os.OpenRoot(resolved)
	if err != nil {
		return "", nil, errors.New("source directory is unavailable")
	}
	entryFile, err := projectRoot.Open(filepath.FromSlash(cleanEntry))
	if err != nil {
		projectRoot.Close()
		return "", nil, errors.New("preview entry file does not exist")
	}
	info, err := entryFile.Stat()
	entryFile.Close()
	if err != nil || !info.Mode().IsRegular() {
		projectRoot.Close()
		return "", nil, errors.New("preview entry file does not exist")
	}
	relDir := filepath.Dir(filepath.FromSlash(cleanEntry))
	var serveRoot *os.Root
	if relDir == "." || relDir == "" {
		serveRoot = projectRoot
	} else {
		serveRoot, err = projectRoot.OpenRoot(relDir)
		projectRoot.Close()
		if err != nil {
			return "", nil, errors.New("preview entry directory is unavailable")
		}
	}
	server := &http.Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		// Only files under the entry's own directory inside the bound project are
		// reachable, and every open is confined to serveRoot without following
		// symlinks outside it.
		requestPath := strings.TrimPrefix(r.URL.Path, "/")
		if requestPath == "" {
			requestPath = filepath.Base(cleanEntry)
		}
		if !filepath.IsLocal(requestPath) || DeniedPath(requestPath) {
			http.NotFound(rw, r)
			return
		}
		file, err := serveRoot.Open(filepath.FromSlash(requestPath))
		if err != nil {
			http.NotFound(rw, r)
			return
		}
		defer file.Close()
		fileInfo, err := file.Stat()
		if err != nil || !fileInfo.Mode().IsRegular() {
			http.NotFound(rw, r)
			return
		}
		http.ServeContent(rw, r, filepath.FromSlash(requestPath), fileInfo.ModTime(), file)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		serveRoot.Close()
		return "", nil, errors.New("preview listener could not start")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	server.Handler = mux
	go func() {
		_ = server.Serve(listener)
	}()
	closeFn = func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = listener.Close()
		_ = serveRoot.Close()
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), closeFn, nil
}

// PreviewOrigin is the only origin the CDP interceptor admits. Any other
// address, including another loopback port or a public URL, is aborted.
func PreviewOrigin(origin string) string {
	return strings.TrimSuffix(origin, "/")
}

// previewAllowedURL reports whether one request may be served by the probe:
// the exact host origin plus its own data/blob assets.
func previewAllowedURL(origin, target string) bool {
	if target == origin || strings.HasPrefix(target, origin+"/") {
		return true
	}
	return strings.HasPrefix(target, "data:") ||
		strings.HasPrefix(target, "blob:") ||
		strings.HasPrefix(target, "about:blank")
}

// ParsePreviewScreenshot decodes the browser's base64 screenshot into exact
// bytes and enforces a size bound.
func ParsePreviewScreenshot(encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("the browser screenshot was not valid base64")
	}
	if len(data) == 0 || len(data) > previewMaxScreenshot {
		return nil, errors.New("the browser screenshot was empty or exceeded the bounded size")
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG")) {
		return nil, errors.New("the browser screenshot was not a PNG image")
	}
	return data, nil
}

// previewAXTree renders a compact AXTree summary from the raw protocol JSON.
// Supports both real cdproto/accessibility.Node payloads (where role/name/value
// are objects with a `value` field and children are `childIds`) and flat test
// fixtures with plain string fields.
func previewAXTree(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return ""
	}
	extract := func(m map[string]any, key string) string {
		v, ok := m[key]
		if !ok || v == nil {
			return ""
		}
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
		if obj, ok := v.(map[string]any); ok {
			if inner, ok := obj["value"]; ok && inner != nil {
				return strings.TrimSpace(fmt.Sprint(inner))
			}
		}
		return ""
	}
	root := items[0]
	role := extract(root, "role")
	name := extract(root, "name")
	value := extract(root, "value")
	childCount := 0
	if ids, ok := root["childIds"].([]any); ok {
		childCount = len(ids)
	} else if kids, ok := root["children"].([]any); ok {
		childCount = len(kids)
	}
	parts := []string{}
	if role != "" || name != "" {
		if role != "" && name != "" {
			parts = append(parts, role+": "+name)
		} else if role != "" {
			parts = append(parts, role)
		} else {
			parts = append(parts, name)
		}
	}
	if len(items) > 1 {
		parts = append(parts, fmt.Sprintf("%d nodes", len(items)))
	}
	if childCount > 0 {
		parts = append(parts, fmt.Sprintf("%d child nodes", childCount))
	}
	if value != "" {
		parts = append(parts, "value="+boundedField(value, 80))
	}
	return strings.Join(parts, " | ")
}

// stopPreviewCommand terminates an owned preview process group. A preview script
// can spawn a dev server, so the whole group is signaled and then verified dead.
func stopPreviewCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	terminateTestGroup(cmd)
	_ = cmd.Wait()
	_ = cleanupTestGroup(cmd)
}

// boundedField trims one metadata field without cutting a UTF-8 code point.
func boundedField(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}
