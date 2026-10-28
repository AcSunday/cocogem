package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestCheckpointDigestDeduplicatesContentAndIgnoresSiblings(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "A", Instruction: "one"}, {Agent: 1, Role: "B", Instruction: "two"}})
	stepA, stepB := runStepIDs(t, s, run.ID)
	first := w.Source{ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "web", URL: "https://example.org/a", Content: "same capture", CaptureStepID: stepA, ContentDigest: contentDigest("same capture"), CapturedAt: w.Now()}
	duplicate := first
	duplicate.ID = w.NewID()
	sibling := w.Source{ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "web", URL: "https://example.org/b", Content: "sibling content", CaptureStepID: stepB, ContentDigest: contentDigest("sibling content"), CapturedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		for _, source := range []w.Source{first, duplicate, sibling} {
			if err := tx.SaveSource(source); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var baseline, afterDuplicate, afterSibling string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		baseline, err = checkpointDigest(tx, run.ID, run.ThreadID, stepA, []string{first.ID}, nil)
		if err != nil {
			return err
		}
		afterDuplicate, err = checkpointDigest(tx, run.ID, run.ThreadID, stepA, []string{first.ID}, nil)
		if err != nil {
			return err
		}
		afterSibling, err = checkpointDigest(tx, run.ID, run.ThreadID, stepA, []string{first.ID, sibling.ID}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if baseline != afterDuplicate {
		t.Fatal("a repeated capture with a new UUID changed the checkpoint digest")
	}
	if baseline != afterSibling {
		t.Fatal("a sibling step's capture changed this step's checkpoint digest")
	}
	// Draft findings are deliverable content even when the body is unchanged.
	body := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: stepA, Type: "note", Title: "Draft", Body: "same body", SourceIDs: []string{}, Limitations: []string{}}
	withFinding := body
	withFinding.Findings = []w.Finding{{Title: "Finding", Detail: "evidence changed", Status: "supported", SourceIDs: []string{}}}
	var bodyDigest, findingDigest string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		bodyDigest, err = checkpointDigest(tx, run.ID, run.ThreadID, stepA, []string{first.ID}, &body)
		if err != nil {
			return err
		}
		findingDigest, err = checkpointDigest(tx, run.ID, run.ThreadID, stepA, []string{first.ID}, &withFinding)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if bodyDigest == findingDigest {
		t.Fatal("draft findings were excluded from the checkpoint digest")
	}
}

func TestReadContextMessageCursorIsStrictAndBounded(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "read"}})
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: w.NewID(), AgentID: run.Agents[1].ID, Role: "assistant", Status: "completed", ContentFormat: w.ContentFormatChunksV1, Content: "abcdef", ContentBytes: 10, CreatedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		if err := tx.AppendMessageChunk(message.ID, 1, "abcdef"); err != nil {
			return err
		}
		return tx.AppendMessageChunk(message.ID, 2, "ghij")
	}); err != nil {
		t.Fatal(err)
	}
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	var version int
	var format string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		digest, version, format, err = tx.MessageDigest(message.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := &readScope{messages: map[string]scopedMessage{message.ID: {digest: digest, version: version, format: format}}, artifacts: map[string][]scopedArtifact{}, sources: map[string]scopedSource{}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error { return nil })}}
	read := func(cursor string) error {
		raw, _ := json.Marshal(map[string]any{"kind": "message", "id": message.ID, "cursor": cursor})
		_, err := e.readContext(t.Context(), run, steps[0], scope, raw)
		return err
	}
	cursor := func(value messageCursor) string {
		raw, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	valid := cursor(messageCursor{ID: message.ID, Format: format, Version: version, Digest: digest, Seq: 2, Offset: 0})
	if err := read(valid); err != nil {
		t.Fatalf("a valid cursor was rejected: %v", err)
	}
	for name, token := range map[string]string{
		"oversized":     strings.Repeat("A", 513),
		"bad base64":    "!!!",
		"unknown field": base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x","format":"chunks_v1","version":10,"digest":"d","seq":1,"offset":0,"extra":1}`)),
		"trailing json": base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x","format":"chunks_v1","version":10,"digest":"d","seq":1,"offset":0}{}`)),
		"missing seq":   cursor(messageCursor{ID: message.ID, Format: format, Version: version, Digest: digest, Seq: 0, Offset: 0}),
		"outside seq":   cursor(messageCursor{ID: message.ID, Format: format, Version: version, Digest: digest, Seq: 99, Offset: 0}),
		"huge offset":   cursor(messageCursor{ID: message.ID, Format: format, Version: version, Digest: digest, Seq: 2, Offset: 500}),
		"wrong digest":  cursor(messageCursor{ID: message.ID, Format: format, Version: version, Digest: "other", Seq: 1, Offset: 0}),
	} {
		if err := read(token); err == nil {
			t.Fatalf("%s cursor was accepted", name)
		}
	}
}

func runStepIDs(t *testing.T, s *store.Store, runID string) (string, string) {
	t.Helper()
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected two steps, got %d", len(steps))
	}
	return steps[0].ID, steps[1].ID
}

func TestArtifactJSONPagesTheCompleteStructuredDeliverable(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "publish"}})
	findings := []w.Finding{}
	for i := 0; i < 6; i++ {
		findings = append(findings, w.Finding{Title: "Finding", Detail: strings.Repeat("证据", 5000) + string(rune('A'+i)), Status: "supported", SourceIDs: []string{}})
	}
	artifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: w.NewID(), Type: "note", Title: "Structured", Body: strings.Repeat("body text ", 3000), Findings: findings, SourceIDs: []string{}, Limitations: []string{"one limitation"}, Feedback: "unreviewed", CreatedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveArtifact(artifact) }); err != nil {
		t.Fatal(err)
	}
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := &readScope{messages: map[string]scopedMessage{}, sources: map[string]scopedSource{}, artifacts: map[string][]scopedArtifact{artifact.ID: {{version: 1, digest: artifactDigest(artifact)}}}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error { return nil })}}
	read := func(input map[string]any) (map[string]any, error) {
		raw, _ := json.Marshal(input)
		encoded, err := e.readContext(t.Context(), run, steps[0], scope, raw)
		if err != nil {
			return nil, err
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(encoded), &value); err != nil {
			return nil, err
		}
		return value, nil
	}
	body, err := read(map[string]any{"kind": "artifact", "id": artifact.ID, "version": 1, "max_bytes": 8192})
	if err != nil {
		t.Fatal(err)
	}
	if omitted, _ := body["structured_omitted"].(bool); !omitted {
		t.Fatalf("an oversized structured payload was not marked omitted: %v", body["structured_omitted"])
	}
	bodyCursor, _ := body["next_cursor"].(string)
	if bodyCursor == "" {
		t.Fatal("body page did not return a cursor")
	}
	cursor := ""
	var canonical strings.Builder
	for page := 0; page < 100; page++ {
		input := map[string]any{"kind": "artifact", "id": artifact.ID, "version": 1, "field": "artifact_json", "max_bytes": 8192}
		if cursor != "" {
			input["cursor"] = cursor
		}
		value, err := read(input)
		if err != nil {
			t.Fatalf("artifact_json page %d: %v", page, err)
		}
		canonical.WriteString(stringValue(value["content"]))
		if more, _ := value["has_more"].(bool); !more {
			if value["content_sha256"] != artifactDigest(artifact) {
				t.Fatalf("artifact_json page lost the version digest: %v", value["content_sha256"])
			}
			break
		}
		cursor, _ = value["next_cursor"].(string)
		if cursor == "" {
			t.Fatal("artifact_json has_more without a cursor")
		}
	}
	var decoded w.Artifact
	if err := json.Unmarshal([]byte(canonical.String()), &decoded); err != nil {
		t.Fatalf("canonical artifact JSON did not round-trip: %v", err)
	}
	if len(decoded.Findings) != len(findings) || decoded.Findings[len(findings)-1].Detail != findings[len(findings)-1].Detail {
		t.Fatalf("structured findings were lost: %d", len(decoded.Findings))
	}
	if decoded.Body != artifact.Body || len(decoded.Limitations) != 1 {
		t.Fatalf("artifact payload fields were lost: %+v", decoded)
	}
	if _, err := read(map[string]any{"kind": "artifact", "id": artifact.ID, "version": 1, "field": "artifact_json", "cursor": bodyCursor}); err == nil {
		t.Fatal("a body cursor was accepted for artifact_json")
	}
	if _, err := read(map[string]any{"kind": "message", "id": artifact.ID, "field": "artifact_json"}); err == nil {
		t.Fatal("artifact_json was accepted for a message")
	}
}

func TestCheckpointDigestMapsSourceUUIDsToCanonicalEvidence(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "capture"}})
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := w.Source{ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "web", URL: "https://example.org/evidence", Content: "same evidence", CaptureStepID: steps[0].ID, ContentDigest: contentDigest("same evidence"), CapturedAt: w.Now()}
	second := first
	second.ID = w.NewID()
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		for _, source := range []w.Source{first, second} {
			if err := tx.SaveSource(source); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	draft := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, Type: "research_report", Title: "Same content", Body: "short body", SourceIDs: []string{first.ID}, Findings: []w.Finding{{Title: "Condition", Detail: "still holds", Status: "supported", SourceIDs: []string{first.ID}}}, Limitations: []string{"sample size"}}
	clone := draft
	clone.ID, clone.Version, clone.Revision, clone.CreatedAt = w.NewID(), 2, 3, w.Now()
	clone.SourceIDs = []string{second.ID}
	clone.Findings[0].SourceIDs = []string{second.ID}
	changed := draft
	changed.Findings = []w.Finding{{Title: "Condition", Detail: "counter-evidence changed", Status: "disputed", SourceIDs: []string{first.ID}}}
	var same, changedDigest string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		if same, err = checkpointDigest(tx, run.ID, run.ThreadID, steps[0].ID, []string{first.ID}, &clone); err != nil {
			return err
		}
		changedDigest, err = checkpointDigest(tx, run.ID, run.ThreadID, steps[0].ID, []string{first.ID}, &changed)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	original, err := checkpointDigestInTest(t, s, run, steps[0].ID, first.ID, draft)
	if err != nil {
		t.Fatal(err)
	}
	if original != same {
		t.Fatal("a same-content source under a new UUID changed the checkpoint digest")
	}
	if original == changedDigest {
		t.Fatal("a real finding change did not advance the checkpoint digest")
	}
}

func checkpointDigestInTest(t *testing.T, s *store.Store, run w.Run, stepID, sourceID string, draft w.Artifact) (string, error) {
	t.Helper()
	var digest string
	err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		digest, err = checkpointDigest(tx, run.ID, run.ThreadID, stepID, []string{sourceID}, &draft)
		return err
	})
	return digest, err
}
