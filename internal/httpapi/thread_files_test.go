package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestThreadFileViewerLinesDiffCandidateImageAndSensitivePaths(t *testing.T) {
	baseRoot := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(baseRoot); err == nil {
		baseRoot = resolved
	}
	wsRoot := filepath.Join(baseRoot, "repo")
	candRoot := filepath.Join(baseRoot, "candidate-worktree")
	if err := os.MkdirAll(filepath.Join(wsRoot, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(candRoot, "src"), 0755); err != nil {
		t.Fatal(err)
	}

	// Initialize git in wsRoot so git diff fallback also works naturally.
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(wsRoot, "init")
	runGit(wsRoot, "config", "user.email", "test@example.com")
	runGit(wsRoot, "config", "user.name", "Test User")

	origGo := "package src\n\nfunc Hello() string {\n\treturn \"v1\"\n}\n"
	if err := os.WriteFile(filepath.Join(wsRoot, "src", "app.go"), []byte(origGo), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsRoot, ".env.example"), []byte("PORT=8080\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsRoot, ".env"), []byte("SECRET_KEY=topsecret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(wsRoot, "add", "src/app.go", ".env.example")
	runGit(wsRoot, "commit", "-m", "init")

	// Modify src/app.go in workspace so git diff shows the change.
	dirtyGo := "package src\n\nfunc Hello() string {\n\treturn \"v2-ws\"\n}\n"
	if err := os.WriteFile(filepath.Join(wsRoot, "src", "app.go"), []byte(dirtyGo), 0644); err != nil {
		t.Fatal(err)
	}

	// Candidate worktree has an even newer uncommitted revision.
	candGo := "package src\n\nfunc Hello() string {\n\t// line 4\n\treturn \"v3-cand\"\n}\n"
	if err := os.WriteFile(filepath.Join(candRoot, "src", "app.go"), []byte(candGo), 0644); err != nil {
		t.Fatal(err)
	}

	// Tiny valid 1x1 PNG in workspace plus captured image source.
	pngBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsRoot, "evidence.png"), pngBytes, 0644); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(filepath.Join(baseRoot, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	engine := &workflow.Engine{Store: db, AllowedRoots: []string{baseRoot}, AllowedModels: []string{"default"}}
	api := newDeliveryAPI(t, baseRoot, db, engine)

	now := w.Now()
	ws := w.Workspace{ID: "ws-files", Name: "FilesRepo", Root: wsRoot, Revision: 1, CreatedAt: now}
	member := w.Agent{ID: "coder", WorkspaceID: ws.ID, DisplayName: "Coder", Handle: "coder", Model: "default", Revision: 1}
	th := w.Thread{ID: "th-files", WorkspaceID: ws.ID, Title: "Inspect files", CoordinatorID: member.ID, AgentIDs: []string{member.ID}, Revision: 1, UpdatedAt: now}
	imgSrc := w.Source{
		ID: "src-img-1", WorkspaceID: ws.ID, ThreadID: th.ID, RunID: "run-1",
		Kind: "image", Title: "page_preview.png", Path: "captures/page_preview.png",
		MediaType: "image/png", OriginalContent: base64.StdEncoding.EncodeToString(pngBytes),
	}
	diffSrc := w.Source{
		ID: "src-diff-1", WorkspaceID: ws.ID, ThreadID: th.ID, RunID: "run-1",
		Kind: "diff", Title: "src/app.go", Path: "src/app.go",
		Content: "--- a/src/app.go\n+++ b/src/app.go\n@@ -3,3 +3,4 @@\n+\t// line 4\n",
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		if err := tx.InsertRun(w.Run{
			ID: "run-1", WorkspaceID: ws.ID, ThreadID: th.ID, Status: "succeeded", CreatedAt: now,
		}, "run-1-key", "run-1-digest"); err != nil {
			return err
		}
		if err := tx.SaveSource(imgSrc); err != nil {
			return err
		}
		return tx.SaveSource(diffSrc)
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Line range parsing (:3-4 and #L3-L4) + git diff fallback in workspace.
	var view ThreadFileView
	api.call("GET", "/api/v1/threads/th-files/file?path=src/app.go:3-4", nil, nil, http.StatusOK, &view)
	if view.Kind != "text" || view.RootKind != "workspace" {
		t.Fatalf("unexpected workspace file view: %+v", view)
	}
	if view.HighlightStart != 3 || view.HighlightEnd != 4 {
		t.Fatalf("expected highlight 3-4, got %d-%d", view.HighlightStart, view.HighlightEnd)
	}
	if !strings.Contains(view.Content, `return "v2-ws"`) {
		t.Fatalf("expected workspace content, got %q", view.Content)
	}
	if !strings.Contains(view.Diff, "v2-ws") && !strings.Contains(view.Diff, "// line 4") {
		t.Fatalf("expected git or source diff, got %q", view.Diff)
	}

	// 2. When an active candidate worktree exists, prefer candidate content.
	cand := w.WorkspaceExecution{
		ID: "cand-1", WorkspaceID: ws.ID, ThreadID: th.ID, Kind: "copy",
		Path: candRoot, CanonicalRoot: wsRoot, Status: "active", LeaseEpoch: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveExecution(cand)
	}); err != nil {
		t.Fatal(err)
	}
	fileURLPath := "file://" + filepath.Join(wsRoot, "src", "app.go") + "#L4-L5"
	var candView ThreadFileView
	api.call("GET", "/api/v1/threads/th-files/file?path="+url.QueryEscape(fileURLPath), nil, nil, http.StatusOK, &candView)
	if candView.RootKind != "candidate" || !strings.Contains(candView.Content, `v3-cand`) {
		t.Fatalf("expected candidate content preferred over workspace, got %+v", candView)
	}
	if candView.HighlightStart != 4 || candView.HighlightEnd != 5 {
		t.Fatalf("expected #L4-L5 highlight, got %d-%d", candView.HighlightStart, candView.HighlightEnd)
	}

	// 3. Sensitive .env is rejected (403), while .env.example is allowed (200).
	rawErr, status := api.callStatus("GET", "/api/v1/threads/th-files/file?path=.env", nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for .env, got %d (%s)", status, rawErr)
	}
	var envExampleView ThreadFileView
	api.call("GET", "/api/v1/threads/th-files/file?path=.env.example", nil, nil, http.StatusOK, &envExampleView)
	if !strings.Contains(envExampleView.Content, "PORT=8080") {
		t.Fatalf("expected .env.example readable, got %+v", envExampleView)
	}

	// 4. Local image metadata & raw image bytes + inline captured source image.
	var imgView ThreadFileView
	api.call("GET", "/api/v1/threads/th-files/file?path=evidence.png", nil, nil, http.StatusOK, &imgView)
	if imgView.Kind != "image" || imgView.MediaType != "image/png" || !strings.Contains(imgView.RawURL, "/api/v1/threads/th-files/file/raw") {
		t.Fatalf("unexpected image file view: %+v", imgView)
	}
	req, _ := http.NewRequest("GET", api.server.URL+imgView.RawURL, nil)
	resp, err := api.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("unexpected raw image response: status=%d ct=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// 5. Captured source image inline serving (?inline=1 -> Content-Disposition: inline; image/png).
	srcReq, _ := http.NewRequest("GET", api.server.URL+"/api/v1/sources/src-img-1/original?inline=1", nil)
	srcResp, err := api.client.Do(srcReq)
	if err != nil {
		t.Fatal(err)
	}
	defer srcResp.Body.Close()
	if srcResp.StatusCode != http.StatusOK || srcResp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(srcResp.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("unexpected source inline image headers: status=%d ct=%q disp=%q",
			srcResp.StatusCode, srcResp.Header.Get("Content-Type"), srcResp.Header.Get("Content-Disposition"))
	}
	var srcBody bytes.Buffer
	_, _ = srcBody.ReadFrom(srcResp.Body)
	if !bytes.Equal(srcBody.Bytes(), pngBytes) {
		t.Fatalf("source original image bytes mismatch")
	}

	// 6. Validate response against OpenAPI contract!
	rawJSON, _ := json.Marshal(view)
	validateResponse(t, "/api/v1/threads/{id}/file", "get", http.StatusOK, rawJSON)

	// Silence unused import check if project isn't needed directly here.
	_ = project.SettingsPatch{}
}
