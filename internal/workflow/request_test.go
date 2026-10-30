package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func seedPublicationRun(t *testing.T, s *store.Store, workflow, role string) w.Run {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "publication", Revision: 1}
	member := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "发布者", Handle: "publisher", Model: "default", Executor: "claude", Tools: tools.Names, Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{member}, Input: w.RunInput{Message: "验证发布契约", Workflow: workflow, ResearchScope: "given_sources", SourceURLs: testutil.Sources}}
	step := w.Step{ID: w.NewID(), RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: role, Instruction: "发布结果", Status: "pending"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "contract"); err != nil {
			return err
		}
		return tx.SaveStep(step)
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

type optionalSearchWeb struct {
	testutil.Web
	enabled bool
}

func (web optionalSearchWeb) SearchEnabled() bool { return web.enabled }

func TestResearchScopeControlsDiscoveryAndMessageLinks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scope   string
		message string
		search  bool
		native  []string
		wantOK  bool
		wantErr string
	}{
		{"auto without links", "auto", "研究协作方法并独立核对", true, []string{"WebSearch", "WebFetch"}, true, ""},
		{"auto with message link and no search", "auto", "研究 " + testutil.Sources[0], false, []string{"WebFetch"}, true, ""},
		{"legacy given sources", "given_sources", "研究 " + testutil.Sources[0], true, nil, false, "given-source research scope"},
		{"legacy empty scope", "", "研究 " + testutil.Sources[0], true, nil, false, "given-source research scope"},
		{"explicit discovery requires search", "web_discovery", "研究协作方法", false, nil, false, "online search is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedPublicationRun(t, s, "free", "responder")
			run.Input.ResearchScope, run.Input.Message, run.Input.SourceURLs = tc.scope, tc.message, nil
			if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
				t.Fatal(err)
			}
			runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
				if !slices.Equal(request.NativeTools, tc.native) {
					return fmt.Errorf("native tools = %v, want %v", request.NativeTools, tc.native)
				}
				if request.WorkspaceRoot != "" {
					return errors.New("empty workspace unexpectedly received a native root")
				}
				if tc.scope == "auto" && len(request.RequiredTools) != 0 {
					return fmt.Errorf("auto unexpectedly requires tools: %v", request.RequiredTools)
				}
				if tc.scope == "auto" && !strings.Contains(request.System, "Reviewers must independently read evidence") {
					return errors.New("independent research guidance missing")
				}
				hasSearch := slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "web_search" })
				if hasSearch != tc.search {
					return fmt.Errorf("search exposure = %v, want %v", hasSearch, tc.search)
				}
				if !tc.search && !strings.Contains(request.System, "Online search is unavailable") {
					return errors.New("search limitation missing")
				}
				if tc.scope == "auto" && tc.search {
					if _, err := execute(ctx, "web_search", json.RawMessage(`{"query":"协作方法"}`)); err != nil {
						return err
					}
				}
				_, err := execute(ctx, "fetch_url", json.RawMessage(`{"url":"`+testutil.Sources[0]+`"}`))
				if err != nil {
					return err
				}
				return observe("text", map[string]any{"text": "research result"})
			})
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: optionalSearchWeb{enabled: tc.search}}
			cancel, wait := startEngine(t, &e)
			defer wait()
			defer cancel()
			got := awaitRun(t, s, run.ID)
			if (got.Status == "succeeded") != tc.wantOK {
				t.Fatalf("run status = %s, error = %s", got.Status, got.Error)
			}
			if !tc.wantOK {
				if !strings.Contains(got.Error, tc.wantErr) {
					t.Fatalf("run error = %q, want %q", got.Error, tc.wantErr)
				}
				return
			}
			if err := s.Read(t.Context(), func(tx *store.Tx) error {
				sources, err := tx.Sources(run.ThreadID)
				if err != nil {
					return err
				}
				if !slices.ContainsFunc(sources, func(source w.Source) bool { return source.Kind == "web" && source.URL == testutil.Sources[0] }) {
					return errors.New("read original was not captured")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func artifactWithStatus(kind, status string) (w.Artifact, []w.Source) {
	sources := []w.Source{{ID: "captured", Kind: "file"}}
	return w.Artifact{
		Type: kind, Title: "title", Body: "body", SourceIDs: []string{"captured"},
		Findings: []w.Finding{{Title: "finding", Detail: "detail", Status: status, Severity: "high", SourceIDs: []string{"captured"}}},
	}, sources
}

func TestPublishSchemaMatchesValidatorStatuses(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cases := []struct {
		name        string
		workflow    string
		role        string
		publishType string
		statuses    []string
	}{
		{"note", "free", "responder", "note", []string{"supported", "confirmed", "disputed", "insufficient_evidence"}},
		{"research report", "research", "coordinator", "research_report", []string{"supported", "disputed", "insufficient_evidence"}},
		{"code analysis", "code_analysis", "reviewer", "code_analysis", []string{"confirmed", "disputed", "insufficient_evidence"}},
		{"final code analysis", "code_analysis", "final_reviewer", "code_analysis", []string{"confirmed", "disputed", "insufficient_evidence"}},
	}
	runs := make([]w.Run, len(cases))
	for i, tc := range cases {
		runs[i] = seedPublicationRun(t, s, tc.workflow, tc.role)
	}
	var mu sync.Mutex
	visibleStatuses := map[string][]string{}
	visibleTypes := map[string][]string{}
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		match := rolePattern.FindStringSubmatch(request.System)
		if match == nil {
			return errors.New("request has no step role")
		}
		role := match[1]
		for _, tool := range request.Tools {
			if tool.Name != "publish_artifact" {
				continue
			}
			properties := tool.Schema["properties"].(map[string]any)
			findingProperties := properties["findings"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			mu.Lock()
			visibleTypes[role] = properties["type"].(map[string]any)["enum"].([]string)
			visibleStatuses[role] = findingProperties["status"].(map[string]any)["enum"].([]string)
			mu.Unlock()
		}
		mu.Lock()
		types := visibleTypes[role]
		mu.Unlock()
		if len(types) != 1 {
			return errors.New("request does not expose exactly one publish type")
		}
		payload, _ := json.Marshal(map[string]any{"type": types[0], "title": "契约检查", "body": "正文", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
		_, err := execute(ctx, "publish_artifact", payload)
		return err
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	for i, run := range runs {
		if got := awaitRun(t, s, run.ID); got.Status != "succeeded" {
			t.Fatalf("%s run did not publish: %+v", cases[i].name, got)
		}
	}
	allStatuses := []string{"supported", "confirmed", "disputed", "insufficient_evidence"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			types := visibleTypes[tc.role]
			statuses := visibleStatuses[tc.role]
			mu.Unlock()
			if !slices.Equal(types, []string{tc.publishType}) {
				t.Fatalf("visible publish type = %v, want %s", types, tc.publishType)
			}
			if !slices.Equal(statuses, tc.statuses) {
				t.Fatalf("visible statuses for %s = %v, want %v", tc.publishType, statuses, tc.statuses)
			}
			for _, status := range tc.statuses {
				artifact, sources := artifactWithStatus(tc.publishType, status)
				if err := ValidateArtifact(artifact, tc.publishType, sources); err != nil {
					t.Fatalf("schema allows %q but validator rejects: %v", status, err)
				}
			}
			for _, status := range allStatuses {
				if slices.Contains(tc.statuses, status) {
					continue
				}
				artifact, sources := artifactWithStatus(tc.publishType, status)
				if err := ValidateArtifact(artifact, tc.publishType, sources); err == nil {
					t.Fatalf("schema hides %q but validator accepts it", status)
				}
			}
		})
	}
}

func TestResearchReportStatusErrorGuidesCorrectionAndPublishesOneArtifact(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedPublicationRun(t, s, "research", "coordinator")
	var rejection string
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		result, err := execute(ctx, "fetch_url", json.RawMessage(`{"url":"`+testutil.Sources[0]+`"}`))
		if err != nil {
			return err
		}
		var captured struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal([]byte(result), &captured); err != nil {
			return err
		}
		if captured.SourceID == "" {
			return errors.New("fetch_url did not capture a source")
		}
		publish := func(status string) (string, error) {
			payload, _ := json.Marshal(map[string]any{
				"type": "research_report", "title": "研究报告", "body": "正文", "limitations": []string{},
				"source_ids": []string{captured.SourceID},
				"findings":   []any{map[string]any{"title": "结论", "detail": "证据", "status": status, "source_ids": []string{captured.SourceID}}},
			})
			return execute(ctx, "publish_artifact", payload)
		}
		if _, err := publish("confirmed"); err == nil {
			return errors.New("confirmed research finding was accepted")
		} else {
			rejection = err.Error()
		}
		_, err = publish("supported")
		return err
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("corrected publication did not succeed: %+v (rejection: %s)", got, rejection)
	}
	if !strings.Contains(rejection, "invalid research finding status") ||
		!strings.Contains(rejection, `"confirmed"`) ||
		!strings.Contains(rejection, "allowed statuses: supported, disputed, insufficient_evidence") {
		t.Fatalf("rejection did not explain the legal statuses: %q", rejection)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		artifacts, err := tx.Artifacts(run.ThreadID)
		if err != nil {
			return err
		}
		if len(artifacts) != 1 {
			return fmt.Errorf("published %d artifacts, want 1", len(artifacts))
		}
		artifact := artifacts[0]
		if artifact.Type != "research_report" || len(artifact.Findings) != 1 || artifact.Findings[0].Status != "supported" {
			return fmt.Errorf("unexpected artifact: %+v", artifact)
		}
		sources, err := tx.Sources(run.ThreadID)
		if err != nil {
			return err
		}
		if len(sources) != 1 || !slices.Contains(artifact.SourceIDs, sources[0].ID) {
			return fmt.Errorf("artifact does not cite the captured source: %+v", sources)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeFirstGuidanceNamesActualToolsAndMarksOnlyTheirFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		executor   string
		scope      string
		root       bool
		wantNative []string
		marked     map[string]string
		unmarked   []string
		nativeWeb  bool
	}{
		{
			name: "claude auto", executor: "claude", scope: "auto", root: true,
			wantNative: []string{"Read", "Glob", "Grep", "WebSearch", "WebFetch"},
			marked:     map[string]string{"read_file": "Read", "list_files": "Glob", "search_content": "Grep", "web_search": "WebSearch", "fetch_url": "WebFetch"},
			unmarked:   []string{"read_source", "publish_artifact"},
			nativeWeb:  true,
		},
		{
			name: "claude given sources", executor: "claude", scope: "given_sources", root: true,
			wantNative: []string{"Read", "Glob", "Grep"},
			marked:     map[string]string{"read_file": "Read", "list_files": "Glob", "search_content": "Grep"},
			unmarked:   []string{"web_search", "fetch_url", "read_source", "publish_artifact"},
		},
		{
			name: "codex auto", executor: "codex", scope: "auto", root: true,
			wantNative: []string{"web_search"},
			marked:     map[string]string{"web_search": "web_search", "fetch_url": "web_search"},
			unmarked:   []string{"read_file", "list_files", "search_content", "read_source", "publish_artifact"},
			nativeWeb:  true,
		},
		{
			name: "no native capabilities", executor: "claude", scope: "given_sources",
			unmarked: []string{"web_search", "fetch_url", "read_source", "publish_artifact"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedPublicationRun(t, s, "free", "responder")
			run.Input.ResearchScope = tc.scope
			run.Agents[0].Executor = tc.executor
			if tc.root {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				run.Workspace.Root = root
			}
			if err := s.Write(t.Context(), func(tx *store.Tx) error {
				member, err := tx.Agent(run.Agents[0].ID)
				if err != nil {
					return err
				}
				member.Executor, member.Revision = tc.executor, member.Revision+1
				if err := tx.SaveAgent(member); err != nil {
					return err
				}
				workspace, err := tx.Workspace(run.WorkspaceID)
				if err != nil {
					return err
				}
				workspace.Root = run.Workspace.Root
				if err := tx.SaveWorkspace(workspace); err != nil {
					return err
				}
				return tx.SaveRun(run)
			}); err != nil {
				t.Fatal(err)
			}
			var got model.Request
			runner := nativeRunnerFunc(func(_ context.Context, request model.Request, _ agent.Execute, observe agent.Observe) error {
				got = request
				return observe("text", map[string]any{"text": "native guidance reply"})
			})
			e := Engine{Store: s, Runners: map[string]agent.Runner{tc.executor: runner}, Web: testutil.Web{}}
			cancel, wait := startEngine(t, &e)
			defer wait()
			defer cancel()
			if settled := awaitRun(t, s, run.ID); settled.Status != "succeeded" {
				t.Fatalf("run status=%s error=%s", settled.Status, settled.Error)
			}
			if !slices.Equal(got.NativeTools, tc.wantNative) {
				t.Fatalf("native tools = %v, want %v", got.NativeTools, tc.wantNative)
			}
			descriptions := map[string]string{}
			for _, tool := range got.Tools {
				descriptions[tool.Name] = tool.Description
			}
			if len(tc.wantNative) == 0 {
				if strings.Contains(got.System, "Native CLI tools are enabled") || strings.Contains(got.System, "native web tools first") {
					t.Fatalf("no-native request still claims native tools: %s", got.System)
				}
			} else {
				if !strings.Contains(got.System, "Native CLI tools are enabled only for this member's existing grants: "+strings.Join(tc.wantNative, ", ")) {
					t.Fatalf("system prompt does not name the actual native tools: %s", got.System)
				}
				if !strings.Contains(got.System, "MCP fallback mapping:") {
					t.Fatalf("system prompt does not map MCP fallbacks: %s", got.System)
				}
			}
			if tc.nativeWeb != strings.Contains(got.System, "native web tools first") {
				t.Fatalf("native web guidance = %v, want %v: %s", strings.Contains(got.System, "native web tools first"), tc.nativeWeb, got.System)
			}
			if tc.scope == "given_sources" {
				contextText := got.Messages[0].Content[0].Text
				if !strings.Contains(contextText, "For given_sources") || strings.Contains(got.System, "WebFetch") {
					t.Fatalf("given_sources guidance changed: %s", got.System)
				}
			}
			for name, native := range tc.marked {
				if !strings.Contains(descriptions[name], "Fallback only") || !strings.Contains(descriptions[name], native) {
					t.Errorf("%s description is not fallback-only for %s: %s", name, native, descriptions[name])
				}
			}
			for _, name := range tc.unmarked {
				if description, ok := descriptions[name]; ok && strings.Contains(description, "Fallback only") {
					t.Errorf("%s is unexpectedly fallback-only: %s", name, description)
				}
			}
			if tc.name == "codex auto" {
				if !strings.Contains(got.System, "web_search/fetch_url<-web_search") {
					t.Fatalf("codex combined web mapping missing: %s", got.System)
				}
			}
			if tc.name == "claude auto" {
				for _, mapping := range []string{"read_file<-Read", "list_files<-Glob", "search_content<-Grep", "web_search<-WebSearch", "fetch_url<-WebFetch"} {
					if !strings.Contains(got.System, mapping) {
						t.Fatalf("native mapping %q missing: %s", mapping, got.System)
					}
				}
			}
		})
	}
}
