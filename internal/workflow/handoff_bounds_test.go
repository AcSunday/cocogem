package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestHandoffBoundsStructuredArtifactAndKeepsArtifactJSONRetrieval covers a
// deliverable whose body is short but whose structured tail is far beyond the
// 100000-byte host input budget. The handoff must stay bounded and explicit,
// while the dependent step can still page the complete artifact_json.
func TestHandoffBoundsStructuredArtifactAndKeepsArtifactJSONRetrieval(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{
		{Agent: 0, Role: "A", Instruction: "publish"},
		{Agent: 1, Role: "B", Instruction: "review", DependsOn: []int{0}},
		{Agent: 2, Role: "C", Instruction: "extra"},
	})
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := w.Source{ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "web", URL: "https://example.org/counter", Content: "exact counter evidence", CaptureStepID: steps[0].ID, ContentDigest: contentDigest("exact counter evidence"), CapturedAt: w.Now()}
	tail := "counter-evidence-tail-не-omitted"
	findings := make([]w.Finding, 0, 50)
	for i := 0; i < 50; i++ {
		detail := fmt.Sprintf("finding-%02d ", i) + strings.Repeat("证", 1200)
		if i == 49 {
			detail = fmt.Sprintf("finding-%02d ", i) + tail + strings.Repeat("据", 1200)
		}
		findings = append(findings, w.Finding{Title: fmt.Sprintf("Condition %02d", i), Detail: detail, Status: "supported", SourceIDs: []string{source.ID}})
	}
	makeArtifact := func(stepID string) w.Artifact {
		return w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: stepID, Type: "research_report", Title: "Structured report", Body: "short body", Findings: findings, SourceIDs: []string{source.ID}, Limitations: []string{strings.Repeat("limitation ", 500)}, Feedback: "unreviewed", CreatedAt: w.Now()}
	}
	dependent := makeArtifact(steps[0].ID)
	referenced := makeArtifact(steps[2].ID)
	steps[1].ContextRefs = []string{fmt.Sprintf("%s@%d", referenced.ID, referenced.Version)}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveSource(source); err != nil {
			return err
		}
		for _, artifact := range []w.Artifact{dependent, referenced} {
			if err := tx.SaveArtifact(artifact); err != nil {
				return err
			}
		}
		return tx.SaveStep(steps[1])
	}); err != nil {
		t.Fatal(err)
	}
	var handoff Handoff
	var scope *readScope
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		metadata, err := tx.ArtifactsMetadata(run.ThreadID)
		if err != nil {
			return err
		}
		previews := []w.Artifact{}
		for _, artifact := range metadata {
			if artifact.StepID != steps[0].ID {
				continue
			}
			preview, err := tx.ArtifactPreview(artifact.ID, artifact.Version, 6000)
			if err != nil {
				return err
			}
			previews = append(previews, preview)
		}
		handoff = prepareHandoff(steps[1], run.Input.Message, nil, previews, []w.Source{source})
		if err := addReferencedInputs(tx, steps[1], run.ThreadID, &handoff); err != nil {
			return err
		}
		if err := sealHandoff(tx, &handoff); err != nil {
			return err
		}
		scope, err = buildReadScope(tx, run, steps[1], nil, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 64<<10 {
		t.Fatalf("structured handoff is not bounded: %d bytes", len(encoded))
	}
	if !strings.Contains(string(encoded), "structured_previews") || !strings.Contains(string(encoded), "field=artifact_json") {
		t.Fatalf("handoff did not name the structured omission and retrieval: %s", encoded)
	}
	if strings.Contains(string(encoded), tail) {
		t.Fatal("the complete structured tail was inlined into the handoff")
	}
	if len(handoff.ArtifactRefs) != 2 || handoff.Digests[fmt.Sprintf("artifact:%s@%d", dependent.ID, dependent.Version)] == "" || handoff.Digests[fmt.Sprintf("artifact:%s@%d", referenced.ID, referenced.Version)] == "" {
		t.Fatalf("exact artifact refs or digests were lost: refs=%v digests=%v", handoff.ArtifactRefs, handoff.Digests)
	}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, _ agent.Observe) error { return nil })}}
	cursor := ""
	var canonical strings.Builder
	for page := 0; page < 100; page++ {
		input := map[string]any{"kind": "artifact", "id": dependent.ID, "version": dependent.Version, "field": "artifact_json", "max_bytes": 8192}
		if cursor != "" {
			input["cursor"] = cursor
		}
		raw, _ := json.Marshal(input)
		result, err := e.readContext(t.Context(), run, steps[1], scope, raw)
		if err != nil {
			t.Fatalf("artifact_json page %d: %v", page, err)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(result), &value); err != nil {
			t.Fatal(err)
		}
		canonical.WriteString(stringValue(value["content"]))
		if more, _ := value["has_more"].(bool); !more {
			break
		}
		cursor, _ = value["next_cursor"].(string)
		if cursor == "" {
			t.Fatal("artifact_json has_more without a cursor")
		}
	}
	var complete w.Artifact
	if err := json.Unmarshal([]byte(canonical.String()), &complete); err != nil {
		t.Fatalf("complete artifact_json did not round-trip: %v", err)
	}
	if len(complete.Findings) != len(findings) || complete.Findings[len(findings)-1].Detail != findings[len(findings)-1].Detail {
		t.Fatal("the structured tail was not retrievable from artifact_json")
	}
	if !strings.Contains(complete.Findings[len(findings)-1].Detail, tail) {
		t.Fatal("retrieved artifact_json lost the counter-evidence tail")
	}
}
