package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func callJSON(t *testing.T, ctx context.Context, execute agent.Execute, name string, input any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(input)
	result, err := execute(ctx, name, raw)
	if err != nil {
		return nil, err
	}
	if len(result) > 65536 {
		t.Errorf("%s observation exceeded 64 KiB: %d", name, len(result))
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(result), &value); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON: %w", name, err)
	}
	return value, nil
}

func readAllPages(t *testing.T, ctx context.Context, execute agent.Execute, input map[string]any, field string) string {
	t.Helper()
	cursor := ""
	var built strings.Builder
	for page := 0; page < 1000; page++ {
		request := map[string]any{"max_bytes": 8192}
		for key, value := range input {
			request[key] = value
		}
		if cursor != "" {
			request["cursor"] = cursor
		}
		value, err := callJSON(t, ctx, execute, field, request)
		if err != nil {
			t.Fatalf("%s page %d: %v", field, page, err)
		}
		content, _ := value["content"].(string)
		built.WriteString(content)
		if more, _ := value["has_more"].(bool); !more {
			return built.String()
		}
		cursor, _ = value["next_cursor"].(string)
		if cursor == "" {
			t.Fatalf("%s has_more without next_cursor", field)
		}
	}
	t.Fatal("paging did not terminate")
	return ""
}

func TestReadContextFrozenScopePagingAndRevocation(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	ambiguous := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: w.NewID(), Type: "code_analysis", Title: "Versioned", Body: "ambiguous version one", Feedback: "unreviewed"}
	second := ambiguous
	second.Version, second.Body = 2, "ambiguous version two"
	otherThread := w.Thread{ID: w.NewID(), WorkspaceID: run.WorkspaceID, Revision: 1, UpdatedAt: w.Now()}
	otherArtifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: otherThread.ID, RunID: run.ID, StepID: w.NewID(), Type: "note", Title: "Other", Body: "other thread body", Feedback: "unreviewed"}
	siblingHalf := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: w.NewID(), AgentID: run.Agents[1].ID, Role: "assistant", Status: "processing", Content: "half reply from a sibling", CreatedAt: w.Now()}
	run.Input.ArtifactRefs = []w.ArtifactRef{{ID: ambiguous.ID, Version: 1}, {ID: ambiguous.ID, Version: 2}}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		if err := tx.SaveArtifact(ambiguous); err != nil {
			return err
		}
		if err := tx.SaveArtifact(second); err != nil {
			return err
		}
		if err := tx.SaveThread(otherThread); err != nil {
			return err
		}
		if err := tx.SaveArtifact(otherArtifact); err != nil {
			return err
		}
		return tx.SaveMessage(siblingHalf)
	}); err != nil {
		t.Fatal(err)
	}
	longText := strings.Repeat("证据", 4000)
	var researcherMessage string
	var researcherArtifact string
	runner := &testRunner{run: func(ctx context.Context, request model.Request, call int, execute agent.Execute, observe agent.Observe) error {
		switch {
		case strings.Contains(request.System, "role=researcher"):
			if err := observe("text", map[string]any{"text": longText}); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Researcher note", "body": "researcher artifact body", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		case strings.Contains(request.System, "role=critic"):
			if !containsString(request.RequiredTools, "read_context") {
				return errors.New("read_context was not required for referenced inputs")
			}
			if strings.Contains(request.System, "at most") && strings.Contains(request.System, "tool calls") {
				return errors.New("host prompt still advertises a cumulative tool-call budget")
			}
			text := request.Messages[0].Content[0].Text
			_, raw, ok := strings.Cut(text, "Exact upstream handoff (messages, artifact versions, findings and source IDs; bodies are bounded previews, use read_context with the exact id/version for more):\n")
			if !ok {
				return errors.New("handoff missing from critic context")
			}
			var handoff Handoff
			if err := json.NewDecoder(strings.NewReader(raw)).Decode(&handoff); err != nil {
				return err
			}
			if len(handoff.Messages) == 0 || len(handoff.ArtifactRefs) == 0 {
				return errors.New("handoff lacks exact refs")
			}
			researcherMessage = handoff.Messages[0].ID
			for _, ref := range handoff.ArtifactRefs {
				if ref.ID != ambiguous.ID {
					researcherArtifact = ref.ID
				}
			}
			if researcherArtifact == "" {
				return fmt.Errorf("upstream researcher artifact missing from refs=%+v", handoff.ArtifactRefs)
			}
			full := readAllPages(t, ctx, execute, map[string]any{"kind": "message", "id": researcherMessage}, "read_context")
			if full != longText {
				return fmt.Errorf("message paging lost bytes: got=%d want=%d", len(full), len(longText))
			}
			firstPage, err := callJSON(t, ctx, execute, "read_context", map[string]any{"kind": "message", "id": researcherMessage, "max_bytes": 100})
			if err != nil {
				return err
			}
			messageCursor, _ := firstPage["next_cursor"].(string)
			if messageCursor == "" {
				return errors.New("small message page did not return a cursor")
			}
			if _, err := execute(ctx, "read_context", json.RawMessage(fmt.Sprintf(`{"kind":"artifact","id":%q,"version":1,"cursor":%q}`, ambiguous.ID, messageCursor))); err == nil {
				return errors.New("message cursor was accepted for an artifact")
			}
			if _, err := execute(ctx, "read_context", json.RawMessage(fmt.Sprintf(`{"kind":"artifact","id":%q}`, ambiguous.ID))); err == nil {
				return errors.New("ambiguous artifact version was silently resolved")
			}
			versioned, err := callJSON(t, ctx, execute, "read_context", map[string]any{"kind": "artifact", "id": ambiguous.ID, "version": 2})
			if err != nil || versioned["content"] != "ambiguous version two" {
				return fmt.Errorf("explicit artifact version failed: %v %+v", err, versioned)
			}
			upstream, err := callJSON(t, ctx, execute, "read_context", map[string]any{"kind": "artifact", "id": researcherArtifact, "version": 1})
			if err != nil || upstream["content"] != "researcher artifact body" {
				return fmt.Errorf("upstream artifact read failed: %v %+v", err, upstream)
			}
			if _, err := execute(ctx, "read_context", json.RawMessage(fmt.Sprintf(`{"kind":"message","id":%q}`, siblingHalf.ID))); err == nil {
				return errors.New("in-flight sibling output was readable")
			}
			if _, err := execute(ctx, "read_context", json.RawMessage(fmt.Sprintf(`{"kind":"artifact","id":%q,"version":1}`, otherArtifact.ID))); err == nil {
				return errors.New("cross-thread artifact was readable")
			}
			if _, err := execute(ctx, "read_context", json.RawMessage(fmt.Sprintf(`{"kind":"message","id":%q,"max_bytes":9000}`, researcherMessage))); err == nil {
				return errors.New("oversized read_context max_bytes was accepted")
			}
			if err := s.Write(ctx, func(tx *store.Tx) error {
				member, err := tx.WorkspaceAgent(run.WorkspaceID, run.Agents[1].ID)
				if err != nil {
					return err
				}
				member.Tools = []string{"publish_artifact"}
				member.Revision++
				return tx.SaveAgent(member)
			}); err != nil {
				return err
			}
			if _, err := execute(ctx, "read_context", json.RawMessage(fmt.Sprintf(`{"kind":"message","id":%q}`, researcherMessage))); err == nil {
				return errors.New("revoked member could still read context")
			}
			if err := s.Write(ctx, func(tx *store.Tx) error {
				member, err := tx.WorkspaceAgent(run.WorkspaceID, run.Agents[1].ID)
				if err != nil {
					return err
				}
				member.Tools = tools.Names
				member.Revision++
				return tx.SaveAgent(member)
			}); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "critique completed after paged reads"})
		case strings.Contains(request.System, "role=coordinator"):
			if err := observe("text", map[string]any{"text": "coordinator summary"}); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Report", "body": "Report body", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			_, err := execute(ctx, "publish_artifact", payload)
			return err
		}
		return nil
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("critic could not page frozen inputs: %+v", got)
	}
	if researcherMessage == "" || researcherArtifact == "" {
		t.Fatal("critic never received exact refs")
	}
}

func TestReadSourceAndReadFilePagesAndManyCalls(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "read"}})
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var fileBody strings.Builder
	for i := 1; i <= 900; i++ {
		fmt.Fprintf(&fileBody, "line-%d: %s\n", i, strings.Repeat("资料", 5))
	}
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte(fileBody.String()), 0600); err != nil {
		t.Fatal(err)
	}
	run.Workspace.Root = root
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(run.Workspace); err != nil {
			return err
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, execute agent.Execute, observe agent.Observe) error {
		read, err := callJSON(t, ctx, execute, "read_file", map[string]any{"path": "evidence.txt"})
		if err != nil {
			return err
		}
		preview, _ := read["content"].(string)
		if len(preview) > 8192 || !utf8Valid(preview) {
			return fmt.Errorf("read_file preview is unbounded: %d", len(preview))
		}
		sourceID, _ := read["source_id"].(string)
		if sourceID == "" || read["source_truncated"] == nil || read["has_more"] == nil {
			return errors.New("read_file result lacks structured capture metadata")
		}
		var captured w.Source
		if err := s.Read(ctx, func(tx *store.Tx) error {
			var readErr error
			captured, readErr = tx.Source(sourceID)
			return readErr
		}); err != nil {
			return err
		}
		if !strings.HasPrefix(captured.Content, preview) {
			return errors.New("preview is not a prefix of the exact capture")
		}
		full := readAllPages(t, ctx, execute, map[string]any{"source_id": sourceID}, "read_source")
		if full != captured.Content {
			return fmt.Errorf("read_source did not reproduce the capture: got=%d want=%d", len(full), len(captured.Content))
		}
		if _, err := execute(ctx, "read_source", json.RawMessage(fmt.Sprintf(`{"source_id":%q,"max_bytes":9000}`, sourceID))); err == nil {
			return errors.New("oversized read_source max_bytes was accepted")
		}
		for i := 0; i < 120; i++ {
			if _, err := callJSON(t, ctx, execute, "read_source", map[string]any{"source_id": sourceID, "max_bytes": 8192}); err != nil {
				return fmt.Errorf("read_source call %d failed: %w", i, err)
			}
		}
		payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Many reads", "body": "published after more than one hundred tool calls", "findings": []any{}, "source_ids": []string{sourceID}, "limitations": []string{}})
		if _, err := execute(ctx, "publish_artifact", payload); err != nil {
			return fmt.Errorf("publication after many calls failed: %w", err)
		}
		return observe("text", map[string]any{"text": "completed after paged reads"})
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("bounded reads or >100 tool calls did not complete: %+v", got)
	}
	var artifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		artifacts, readErr = tx.Artifacts(run.ThreadID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Title != "Many reads" {
		t.Fatalf("artifact after many calls missing: %+v", artifacts)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func utf8Valid(value string) bool {
	for _, r := range value {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestReadContextSupportsLegacyInlineReferences(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "read legacy"}})
	legacy := strings.Repeat("旧格式正文", 4000)
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: w.NewID(), AgentID: run.Agents[1].ID, Role: "assistant", Status: "completed", Content: legacy, CreatedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].ContextRefs = []string{message.ID}
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		if !containsString(request.RequiredTools, "read_context") {
			return errors.New("legacy reference did not require read_context")
		}
		full := readAllPages(t, ctx, execute, map[string]any{"kind": "message", "id": message.ID}, "read_context")
		if full != legacy {
			return fmt.Errorf("legacy inline paging lost bytes: got=%d want=%d", len(full), len(legacy))
		}
		return observe("text", map[string]any{"text": "legacy read complete"})
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("legacy inline reference could not be read: %+v", got)
	}
}
