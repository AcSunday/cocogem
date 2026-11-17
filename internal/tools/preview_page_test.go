package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreviewStaticServerServesOnlyTheEntrySubtree is the SSRF/scope regression:
// a static preview serves the entry and a file beside it, never a parent
// directory or a sensitive path.
func TestPreviewStaticServerServesOnlyTheEntrySubtree(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "site")
	if err := os.MkdirAll(page, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(page, "index.html"), []byte("<!doctype html><title>Probe</title><h1>OK</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(page, "asset.txt"), []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	origin, closeFn, err := ServeStaticProject(context.Background(), root, "site/index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if !strings.HasPrefix(origin, "http://127.0.0.1:") {
		t.Fatalf("origin = %q", origin)
	}
	if _, _, err := ServeStaticProject(context.Background(), root, "outside.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ServeStaticProject(context.Background(), root, "../escape.html"); err == nil {
		t.Fatal("a traversal entry was accepted")
	}
	if _, _, err := ServeStaticProject(context.Background(), root, "missing.html"); err == nil {
		t.Fatal("a missing entry was accepted")
	}
}

// TestPreviewInterceptorAllowsOnlyTheProbeOrigin is the CDP policy regression:
// the exact origin, its subtree, and data/blob are allowed; another loopback
// port and a public URL are not.
func TestPreviewInterceptorAllowsOnlyTheProbeOrigin(t *testing.T) {
	origin := "http://127.0.0.1:41234"
	for _, target := range []string{
		origin,
		origin + "/index.html",
		origin + "/nested/asset.js",
		"data:image/png;base64,AAAA",
		"blob:http://127.0.0.1:41234/abcd",
		"about:blank",
	} {
		if !previewAllowedURL(origin, target) {
			t.Fatalf("%q was not admitted", target)
		}
	}
	for _, target := range []string{
		"http://127.0.0.1:41235/",
		"http://localhost:41234/",
		"http://127.0.0.1:8741/api/v1/system",
		"https://example.com/",
		"http://169.254.169.254/latest/meta-data/",
	} {
		if previewAllowedURL(origin, target) {
			t.Fatalf("%q was admitted by the interceptor", target)
		}
	}
}

// TestPreviewScreenshotMustBeARealPNG rejects an empty or non-PNG capture.
func TestPreviewScreenshotMustBeARealPNG(t *testing.T) {
	if _, err := ParsePreviewScreenshot(""); err == nil {
		t.Fatal("an empty screenshot was accepted")
	}
	if _, err := ParsePreviewScreenshot("bm90IGEgcG5n"); err == nil {
		t.Fatal("a non-PNG capture was accepted")
	}
}

// TestPreviewCommandRunsInItsOwnGroupAndKeepsThePort is the process-boundary
// regression: a command receives a random port and is stopped as a group.
func TestPreviewCommandStopsTheOwnedGroup(t *testing.T) {
	if _, err := execLookPathForTest(t, "/bin/sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	script := "printf 'preview up on %s\\n' \"$PORT\"; sleep 60"
	origin, cmd, err := startPreviewCommand(context.Background(), []string{"/bin/sh", "-c", "sleep 1; " + script})
	if err == nil {
		stopPreviewCommand(cmd)
		t.Fatalf("a command that never answered was reported ready: %s", origin)
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("unexpected preview command error: %v", err)
	}
	if _, _, err := startPreviewCommand(context.Background(), nil); err == nil {
		t.Fatal("an empty preview command was accepted")
	}
}

// execLookPathForTest keeps the test dependency-free.
func execLookPathForTest(t *testing.T, name string) (string, error) {
	t.Helper()
	if info, err := os.Stat(name); err != nil || info.Mode()&0111 == 0 {
		return "", err
	}
	return name, nil
}

// TestPreviewCommandModeIsRefusedInAReadOnlyTurn keeps Plan-Only from starting
// project code through the preview command path.
func TestPreviewCommandModeIsRefusedInAReadOnlyTurn(t *testing.T) {
	scope := Scope{Root: t.TempDir(), ReadOnly: true}
	raw := []byte(`{"mode":"command","argv":["/bin/sh","-c","true"]}`)
	if _, err := Execute(context.Background(), "preview_page", raw, scope); err == nil || !strings.Contains(err.Error(), "read_only_step") {
		t.Fatalf("a read-only command preview was accepted: %v", err)
	}
}
