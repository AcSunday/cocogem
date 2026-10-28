package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestEngineeringDevelopRealWriteTestReviewFlow drives the real four-step
// development plan against a temporary Go project. The developer and reviewer
// tools are the production host tools; the test child is the actual OS
// sandbox, so the diff and exit-code evidence in the final report is real.
func TestEngineeringDevelopRealWriteTestReviewFlow(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real sandbox path is verified on macOS; Linux run_test is blocked")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is unavailable")
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := "package calc\n\nfunc Add(a, b int) int { return a - b }\n"
	fixed := "package calc\n\nfunc Add(a, b int) int { return a + b }\n"
	for name, content := range map[string]string{
		"go.mod":       "module example.test/calc\n\ngo 1.20\n",
		"calc.go":      original,
		"calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatalf(\"got %d\", Add(2, 3))\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := seedEngineeringRun(t, s, root, original)
	runner := &engineeringRunner{t: t, root: root, fixed: fixed}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	result := awaitEngineeringRun(t, s, run.ID)
	cancel()
	e.Wait()
	if result.Status != "succeeded" {
		t.Fatalf("run status=%s error=%s failure=%s", result.Status, result.Error, result.FailureKind)
	}
	content, err := os.ReadFile(filepath.Join(root, "calc.go"))
	if err != nil || string(content) != fixed {
		t.Fatalf("final file = %q, %v", content, err)
	}
	var sources []w.Source
	var artifacts []w.Artifact
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		sources, err = tx.Sources(run.ThreadID)
		if err != nil {
			return err
		}
		artifacts, err = tx.Artifacts(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	diffs, tests := 0, 0
	for _, source := range sources {
		switch source.Kind {
		case "diff":
			diffs++
			if source.SHA256 == "" || !strings.Contains(source.Content, "+func Add(a, b int) int { return a + b }") {
				t.Fatalf("diff evidence is not the actual change: %+v", source)
			}
		case "test":
			tests++
			if source.ExitCode != 0 || source.TimedOut {
				t.Fatalf("test evidence is not passing: %+v", source)
			}
		}
	}
	if diffs != 1 || tests != 4 {
		t.Fatalf("evidence counts: diffs=%d tests=%d", diffs, tests)
	}
	report := 0
	for _, artifact := range artifacts {
		if artifact.Type != "engineering_report" {
			continue
		}
		report++
		if !slices.Contains(artifact.SourceIDs, findSourceID(sources, "diff")) {
			t.Fatalf("final report does not cite the write diff: %+v", artifact)
		}
	}
	if report != 1 {
		t.Fatalf("engineering reports = %d, artifacts = %+v", report, artifacts)
	}
	if runner.finalArtifact == "" {
		t.Fatal("final reviewer did not publish an engineering report")
	}
}

func seedEngineeringRun(t *testing.T, s *store.Store, root, original string) w.Run {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "engineering", Root: root, Revision: 1}
	memberTools := []string{"read_file", "write_file", "run_test", "read_source", "list_sources", "read_context", "publish_artifact"}
	agents := []w.Agent{
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "开发者", Handle: "developer", Executor: "claude", Model: "default", Tools: memberTools, Revision: 1},
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "复核者", Handle: "reviewer", Executor: "claude", Model: "default", Tools: []string{"read_file", "run_test", "read_source", "list_sources", "read_context", "publish_artifact"}, Revision: 1},
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: []string{agents[0].ID, agents[1].ID}, CoordinatorID: agents[1].ID, Revision: 1, UpdatedAt: w.Now()}
	in := w.RunInput{Message: "修复 Add，使测试通过。", Workflow: "engineering", EngineeringAction: w.EngineeringDevelop, Assignments: map[string]string{"developer": agents[0].ID, "reviewer": agents[1].ID}, ResearchScope: "given_sources"}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "queued", CreatedAt: w.Now(), Agents: agents, CoordinatorID: th.CoordinatorID, Input: in}
	steps, err := Plan(run.ID, in, agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, member := range agents {
			if err := tx.SaveAgent(member); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "hash"); err != nil {
			return err
		}
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

type engineeringRunner struct {
	t             *testing.T
	root          string
	fixed         string
	finalArtifact string
	reviewerDiff  string
	reviewerTest  string
}

func (r *engineeringRunner) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	r.t.Helper()
	system := request.System
	call := func(name string, input map[string]any) (string, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return "", err
		}
		return execute(ctx, name, raw)
	}
	emit := func() error { return observe("text", map[string]any{"text": "step completed"}) }
	listSources := func() ([]map[string]any, error) {
		out, err := call("list_sources", map[string]any{})
		if err != nil {
			return nil, err
		}
		var listed struct {
			Sources []map[string]any `json:"sources"`
		}
		if err := json.Unmarshal([]byte(out), &listed); err != nil {
			return nil, err
		}
		return listed.Sources, nil
	}
	evidenceIDs := func(sources []map[string]any) (diffs, tests []string) {
		for _, item := range sources {
			id, _ := item["source_id"].(string)
			switch item["kind"] {
			case "diff":
				diffs = append(diffs, id)
			case "test":
				if exit, ok := item["exit_code"].(float64); ok && exit == 0 {
					tests = append(tests, id)
				}
			}
		}
		return diffs, tests
	}
	switch {
	case strings.Contains(system, "type=engineering_report"):
		read, err := call("read_file", map[string]any{"path": "calc.go"})
		if err != nil {
			return err
		}
		if !strings.Contains(read, "a + b") {
			return errors.New("final reviewer did not see the fixed file")
		}
		if _, err := call("run_test", map[string]any{"toolchain": "go", "target": ".", "timeout_seconds": 120}); err != nil {
			return err
		}
		listed, err := listSources()
		if err != nil {
			return err
		}
		diffs, tests := evidenceIDs(listed)
		if len(diffs) == 0 || len(tests) == 0 {
			return fmt.Errorf("final reviewer saw incomplete evidence: diffs=%v tests=%v", diffs, tests)
		}
		payload, _ := json.Marshal(map[string]any{"type": "engineering_report", "title": "工程交付", "body": "修复 Add 并通过实际测试。", "findings": []any{}, "source_ids": append(diffs, tests...), "limitations": []string{"仅 go test"}})
		if _, err := execute(ctx, "publish_artifact", payload); err != nil {
			return err
		}
		r.finalArtifact = "engineering_report"
		return emit()
	case strings.Contains(system, "The reviewer's exact step message"):
		// The developer must consume the reviewer's exact plain reply and its
		// source references from the frozen handoff, not an artifact.
		prompt := request.Messages[0].Content[0].Text
		if !strings.Contains(prompt, "Reviewer plain reply: no defect") || !strings.Contains(prompt, r.reviewerDiff) || !strings.Contains(prompt, r.reviewerTest) {
			return errors.New("developer did not receive the reviewer reply and source references")
		}
		if _, err := call("run_test", map[string]any{"toolchain": "go", "target": ".", "timeout_seconds": 120}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "Developer retest passed; reviewer message and references were consumed."})
	case strings.Contains(system, "role=developer"):
		content, err := os.ReadFile(filepath.Join(r.root, "calc.go"))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		out, err := call("write_file", map[string]any{"path": "calc.go", "content": r.fixed, "expected_sha256": hex.EncodeToString(sum[:])})
		if err != nil {
			return err
		}
		var written struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal([]byte(out), &written); err != nil || written.SourceID == "" {
			return fmt.Errorf("write_file evidence missing: %s %v", out, err)
		}
		out, err = call("run_test", map[string]any{"toolchain": "go", "target": ".", "timeout_seconds": 120})
		if err != nil {
			return err
		}
		var tested struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal([]byte(out), &tested); err != nil || tested.SourceID == "" {
			return fmt.Errorf("run_test evidence missing: %s %v", out, err)
		}
		// Intermediate steps must not be offered publish_artifact.
		if slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "publish_artifact" }) {
			return errors.New("developer was offered publish_artifact")
		}
		return observe("text", map[string]any{"text": "Developer implemented the fix and ran the test. Diff source " + written.SourceID + ", test source " + tested.SourceID + "."})
	case strings.Contains(system, "role=reviewer"):
		if slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "write_file" }) {
			return errors.New("reviewer was offered write_file")
		}
		if slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "publish_artifact" }) {
			return errors.New("intermediate reviewer was offered publish_artifact")
		}
		if _, err := execute(ctx, "write_file", json.RawMessage(`{"path":"calc.go","content":"x"}`)); err == nil {
			return errors.New("reviewer write_file was not rejected")
		}
		if _, err := call("read_file", map[string]any{"path": "calc.go"}); err != nil {
			return err
		}
		if _, err := call("run_test", map[string]any{"toolchain": "go", "target": ".", "timeout_seconds": 120}); err != nil {
			return err
		}
		listed, err := listSources()
		if err != nil {
			return err
		}
		diffs, tests := evidenceIDs(listed)
		if len(diffs) == 0 || len(tests) == 0 {
			return fmt.Errorf("reviewer saw incomplete evidence: diffs=%v tests=%v", diffs, tests)
		}
		r.reviewerDiff, r.reviewerTest = diffs[0], tests[0]
		return observe("text", map[string]any{"text": "Reviewer plain reply: no defect. Diff source " + diffs[0] + ", test source " + tests[0] + "."})
	}
	return fmt.Errorf("unexpected engineering step: %s", system)
}

func awaitEngineeringRun(t *testing.T, s *store.Store, id string) w.Run {
	t.Helper()
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	for {
		wake := s.Changed()
		var result w.Run
		if err := s.Read(context.Background(), func(tx *store.Tx) error {
			var err error
			result, err = tx.Run(id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if w.Terminal(result.Status) {
			return result
		}
		select {
		case <-deadline.C:
			t.Fatal("engineering run did not settle")
		case <-wake:
		}
	}
}

func findSourceID(sources []w.Source, kind string) string {
	for _, source := range sources {
		if source.Kind == kind {
			return source.ID
		}
	}
	return ""
}
