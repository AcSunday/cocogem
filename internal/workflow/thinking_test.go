package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestThinkingEventsPreserveChronologicalOrderAroundTools(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedRun(t, db)
	enableExecutor(t, db, &run, "claude")

	runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
		if err := observe("thinking", map[string]any{"block_id": "b1", "text": "Step 1: inspect "}); err != nil {
			return err
		}
		if err := observe("thinking", map[string]any{"block_id": "b1", "text": "the target."}); err != nil {
			return err
		}
		if err := observe("tool_started", map[string]any{
			"tool_call_id": "cli:t1", "name": "Read", "input": json.RawMessage(`{"file_path":"README.md"}`), "origin": "native",
		}); err != nil {
			return err
		}
		if err := observe("tool_finished", map[string]any{
			"tool_call_id": "cli:t1", "name": "Read", "status": "ok", "output": "readme body", "origin": "native",
		}); err != nil {
			return err
		}
		if err := observe("thinking", map[string]any{"block_id": "b2", "text": "Step 2: summarize findings."}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "Final verified answer.", "activity": true})
	}}
	engine := Engine{
		Store:   db,
		Runners: map[string]agent.Runner{"claude": runner},
		Web:     testutil.Web{},
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	result := awaitRun(t, db, run.ID)
	cancel()
	engine.Wait()

	if result.Status != "succeeded" {
		t.Fatalf("run status=%s error=%q", result.Status, result.Error)
	}
	var events []w.Event
	var messages []w.Message
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		if events, err = tx.Events(run.ID, 0); err != nil {
			return err
		}
		messages, err = tx.Messages(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var seqTypes []string
	var thinkingTexts []string
	for _, ev := range events {
		switch ev.Type {
		case "thinking_delta", "tool_started", "tool_finished", "message_delta":
			seqTypes = append(seqTypes, ev.Type)
			if ev.Type == "thinking_delta" {
				var payload struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(ev.Data, &payload)
				thinkingTexts = append(thinkingTexts, payload.Text)
			}
		}
	}
	wantOrder := []string{"thinking_delta", "tool_started", "tool_finished", "thinking_delta", "message_delta"}
	if strings.Join(seqTypes, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("event order = %v, want %v", seqTypes, wantOrder)
	}
	if len(thinkingTexts) != 2 || thinkingTexts[0] != "Step 1: inspect the target." || thinkingTexts[1] != "Step 2: summarize findings." {
		t.Fatalf("thinking texts = %q", thinkingTexts)
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			if strings.Contains(msg.Content, "Step 1:") || strings.Contains(msg.Content, "Step 2:") {
				t.Fatalf("thinking leaked into visible message content: %q", msg.Content)
			}
			if msg.Content != "Final verified answer." {
				t.Fatalf("assistant content = %q, want %q", msg.Content, "Final verified answer.")
			}
		}
	}
}

func TestThinkingWriterUTF8BoundaryAlwaysEmitsTruncatedMarker(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedRun(t, db)
	var stepID string
	attemptID := w.NewID()
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		stepID = steps[0].ID
		return tx.SaveAttempt(w.Attempt{ID: attemptID, RunID: run.ID, StepID: stepID, Number: 1, Status: "running"})
	}); err != nil {
		t.Fatal(err)
	}

	tw := newThinkingWriter(db, t.Context(), time.Hour, run.ID, stepID, run.Agents[0].ID, attemptID)
	// Fill up to exactly 1 byte shy of the 64 KiB cap and flush so pending is empty.
	prefix := strings.Repeat("a", maxThinkingBytesPerAttempt-1)
	if err := tw.Append("b1", prefix); err != nil {
		t.Fatal(err)
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	// A 3-byte Chinese rune does not fit in the remaining 1 byte: trimBytes returns "".
	// thinkingWriter must still emit a single truncated=true marker without corrupting UTF-8.
	if err := tw.Append("b1", "中文超出边界"); err != nil {
		t.Fatal(err)
	}
	// Additional appends after truncation must not emit duplicate truncated markers.
	if err := tw.Append("b1", "更多内容"); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	var events []w.Event
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		events, err = tx.Events(run.ID, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var totalBytes int
	var truncatedEvents int
	for _, ev := range events {
		if ev.Type != "thinking_delta" {
			continue
		}
		var payload struct {
			Text      string `json:"text"`
			Truncated bool   `json:"truncated"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(payload.Text) {
			t.Fatalf("invalid UTF-8 persisted in thinking_delta")
		}
		totalBytes += len(payload.Text)
		if payload.Truncated {
			truncatedEvents++
		}
	}
	if totalBytes != maxThinkingBytesPerAttempt-1 {
		t.Fatalf("total thinking bytes = %d, want %d", totalBytes, maxThinkingBytesPerAttempt-1)
	}
	if truncatedEvents != 1 {
		t.Fatalf("truncated event count = %d, want 1", truncatedEvents)
	}
}

func TestThinkingFlushesBeforeHostToolExecution(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedRun(t, db)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello host tool\n"), 0600); err != nil {
		t.Fatal(err)
	}
	enableExecutor(t, db, &run, "claude")
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		ws, err := tx.Workspace(run.WorkspaceID)
		if err != nil {
			return err
		}
		ws.Root = root
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Workspace = ws
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}

	runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
		// Emit thinking immediately followed (<1ms, before 150ms ticker) by a host tool call.
		if err := observe("thinking", map[string]any{"block_id": "b1", "text": "Need to read note.txt"}); err != nil {
			return err
		}
		if err := observe("tool_started", map[string]any{"tool_call_id": "host-1", "name": "read_file", "input": json.RawMessage(`{"path":"note.txt"}`)}); err != nil {
			return err
		}
		out, err := execute(ctx, "read_file", json.RawMessage(`{"path":"note.txt"}`))
		if err != nil {
			return err
		}
		if err := observe("tool_finished", map[string]any{"tool_call_id": "host-1", "name": "read_file", "status": "ok", "output": out}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "Read completed.", "activity": true})
	}}
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	result := awaitRun(t, db, run.ID)
	cancel()
	engine.Wait()
	if result.Status != "succeeded" {
		t.Fatalf("status=%s err=%q", result.Status, result.Error)
	}

	var thinkingSeq, toolStartSeq int64
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, ev := range events {
			if ev.Type == "thinking_delta" && thinkingSeq == 0 {
				thinkingSeq = ev.Seq
			}
			if ev.Type == "tool_started" && toolStartSeq == 0 {
				toolStartSeq = ev.Seq
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if thinkingSeq == 0 || toolStartSeq == 0 || thinkingSeq >= toolStartSeq {
		t.Fatalf("expected thinking_delta (%d) strictly before tool_started (%d)", thinkingSeq, toolStartSeq)
	}
}
