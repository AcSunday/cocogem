package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestLiveLongTaskSourceFixture proves the real-CLI fixture fits the product
// read budget and that its tail is only reachable after the first preview page.
func TestLiveLongTaskSourceFixture(t *testing.T) {
	data := liveSourceFile()
	if len(data) >= 1<<20 {
		t.Fatalf("fixture is %d bytes; tools.ReadFile rejects files over 1 MiB", len(data))
	}
	if lines := strings.Count(data, "\n") + 1; lines != liveSourceLines {
		t.Fatalf("fixture has %d lines, want %d", lines, liveSourceLines)
	}
	lastStart := liveSourceLines - liveWindowLines + 1
	if liveWindows != 65 || lastStart != 32001 {
		t.Fatalf("fixture windows=%d last_start=%d, want 65 and 32001", liveWindows, lastStart)
	}
	t.Logf("fixture bytes=%d lines=%d windows=%d last_start=%d", len(data), liveSourceLines, liveWindows, lastStart)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "evidence.txt"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Run("whole file within read budget", func(t *testing.T) {
		source, err := tools.ReadFile(ctx, dir, "evidence.txt", 1, 0)
		if err != nil {
			t.Fatalf("tools.ReadFile rejected the fixture: %v", err)
		}
		if source.EndLine != liveWindowLines || !source.Truncated {
			t.Fatalf("whole-file read = end=%d truncated=%v, want the first bounded window", source.EndLine, source.Truncated)
		}
	})

	t.Run("65 independent windows", func(t *testing.T) {
		seen := map[int]bool{}
		for start := 1; start <= liveSourceLines; start += liveWindowLines {
			window, err := tools.ReadFile(ctx, dir, "evidence.txt", start, start+liveWindowLines-1)
			if err != nil {
				t.Fatalf("window start_line=%d: %v", start, err)
			}
			if window.StartLine != start || window.EndLine != start+liveWindowLines-1 || window.Truncated {
				t.Fatalf("window start_line=%d = start=%d end=%d truncated=%v", start, window.StartLine, window.EndLine, window.Truncated)
			}
			if seen[window.StartLine] {
				t.Fatalf("duplicate window start_line=%d", window.StartLine)
			}
			seen[window.StartLine] = true
		}
		if len(seen) != liveWindows {
			t.Fatalf("read %d distinct windows, want %d", len(seen), liveWindows)
		}
	})

	t.Run("last window capture exceeds the first preview", func(t *testing.T) {
		capture, err := tools.ReadFile(ctx, dir, "evidence.txt", lastStart, liveSourceLines)
		if err != nil {
			t.Fatal(err)
		}
		if capture.StartLine != lastStart || capture.EndLine != liveSourceLines || capture.Truncated {
			t.Fatalf("last window = start=%d end=%d truncated=%v", capture.StartLine, capture.EndLine, capture.Truncated)
		}
		if len(capture.Content) <= w.MaxTextPageBytes || len(capture.Content) >= 32<<10 {
			t.Fatalf("last window capture is %d bytes; want >8 KiB and <32 KiB", len(capture.Content))
		}
		t.Logf("last window capture bytes=%d", len(capture.Content))
		for _, token := range liveEvidenceTokens {
			if !strings.Contains(capture.Content, token) {
				t.Fatalf("last window capture lost tail token %q", token)
			}
		}

		var captured w.Source
		observation, err := tools.Execute(ctx, "read_file", json.RawMessage(fmt.Sprintf(`{"path":"evidence.txt","start_line":%d,"end_line":%d}`, lastStart, liveSourceLines)), tools.Scope{Root: dir, Capture: func(source w.Source) (w.Source, error) {
			source.ID = "fixture-source"
			captured = source
			return source, nil
		}})
		if err != nil {
			t.Fatalf("tools.Execute read_file: %v", err)
		}
		var preview struct {
			SourceID         string `json:"source_id"`
			StartLine        int    `json:"start_line"`
			EndLine          int    `json:"end_line"`
			Content          string `json:"content"`
			HasMore          bool   `json:"has_more"`
			PreviewTruncated bool   `json:"preview_truncated"`
			SourceTruncated  bool   `json:"source_truncated"`
		}
		if err := json.Unmarshal([]byte(observation), &preview); err != nil {
			t.Fatalf("read_file observation is not JSON: %v: %s", err, observation)
		}
		if preview.SourceID == "" || preview.StartLine != lastStart || preview.EndLine != liveSourceLines || preview.SourceTruncated {
			t.Fatalf("unexpected preview metadata: %+v", preview)
		}
		if !preview.HasMore || !preview.PreviewTruncated || preview.Content == "" || len(preview.Content) > w.MaxTextPageBytes {
			t.Fatalf("preview was not bounded: has_more=%v truncated=%v bytes=%d", preview.HasMore, preview.PreviewTruncated, len(preview.Content))
		}
		t.Logf("first preview bytes=%d", len(preview.Content))
		for _, token := range []string{liveTailMarker, "2026-10-27", "3.25 USD/kg", "4.10 USD/kg"} {
			if strings.Contains(preview.Content, token) {
				t.Fatalf("first preview already contains tail evidence %q", token)
			}
		}
		if captured.Content != capture.Content || captured.EndLine != liveSourceLines || captured.Truncated {
			t.Fatalf("captured source differs from the exact last window")
		}
		for _, token := range liveEvidenceTokens {
			if !strings.Contains(captured.Content, token) {
				t.Fatalf("captured source lost tail token %q", token)
			}
		}

		ref := w.TextRef{Kind: "source", ID: preview.SourceID}
		first, err := w.PageText(ref, captured.Content, "", w.MaxTextPageBytes)
		if err != nil {
			t.Fatal(err)
		}
		if !first.HasMore || strings.Contains(first.Content, liveTailMarker) {
			t.Fatalf("first read_source page has_more=%v and already shows the tail", first.HasMore)
		}
		pages := 1
		last := first.Content
		for cursor := first.NextCursor; cursor != ""; {
			page, err := w.PageText(ref, captured.Content, cursor, w.MaxTextPageBytes)
			if err != nil {
				t.Fatalf("read_source page %d: %v", pages+1, err)
			}
			pages++
			last = page.Content
			cursor = page.NextCursor
			if pages > 16 {
				t.Fatal("read_source paging did not terminate")
			}
		}
		if pages < 2 || !strings.Contains(last, liveTailMarker) {
			t.Fatalf("read_source pages=%d lost the tail", pages)
		}
		t.Logf("read_source pages=%d", pages)
	})

	t.Run("evidence export is private and sanitized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "evidence.json")
		want := liveRunEvidence{CLI: "fixture", RunStatus: "succeeded"}
		if err := writeLiveEvidence(path, want); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("evidence file mode=%#o, want 0600", info.Mode().Perm())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got liveRunEvidence
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("evidence file is not JSON: %v", err)
		}
		if got.CLI != want.CLI || got.RunStatus != want.RunStatus {
			t.Fatalf("evidence file = %+v", got)
		}
	})

	t.Run("prompt keeps answers out", func(t *testing.T) {
		prompt := liveTaskPrompt()
		for _, leaked := range []string{"3.25", "4.10", "2026-10-27", liveTailMarker} {
			if strings.Contains(prompt, leaked) {
				t.Fatalf("task prompt leaks fixture answer %q", leaked)
			}
		}
		for _, want := range []string{"65 个窗口", "start_line=32001", "否定条件", "证据不足"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("task prompt is missing %q", want)
			}
		}
	})
}
