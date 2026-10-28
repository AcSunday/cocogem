package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestSettingsDriveNewThreadsAndRunsWithoutGrantingAccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(Config{Host: "local.test", WebDir: dir, AuthFile: filepath.Join(dir, "auth.json"), Models: []string{"default"}, Executors: []ExecutorInfo{
		{ID: "claude", Installed: true, Supported: true},
		{ID: "codex", Installed: true, Supported: true},
		{ID: "gemini", Installed: true, Supported: true},
	}}, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	var cookie *http.Cookie
	csrf := ""
	call := func(method, path string, in, out any, revision, want int) {
		t.Helper()
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if revision > 0 {
			req.Header.Set("If-Match", strconv.Itoa(revision))
		}
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		if rw.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, rw.Code, want, rw.Body.String())
		}
		if path == "/api/v1/settings" {
			validateResponse(t, path, strings.ToLower(method), rw.Code, rw.Body.Bytes())
		}
		if path == "/auth/setup" {
			cookie = rw.Result().Cookies()[0]
			var session SessionReply
			if err := json.Unmarshal(rw.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			csrf = session.CSRFToken
		}
		if out != nil {
			if err := json.Unmarshal(rw.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	call("POST", "/auth/setup", PasswordInput{Password: "local-settings-test"}, nil, 0, 200)
	var first, second w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "one"}, &first, 0, 200)
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "two"}, &second, 0, 200)
	members := func(id string) []w.Agent {
		t.Helper()
		var list struct{ Items []w.Agent }
		call("GET", "/api/v1/workspaces/"+id+"/agents", nil, &list, 0, 200)
		return list.Items
	}
	one, two := members(first.ID), members(second.ID)
	if len(one) != 3 || len(two) != 3 {
		t.Fatalf("unexpected seeded members: %+v %+v", one, two)
	}
	var settings w.Settings
	call("GET", "/api/v1/settings", nil, &settings, 0, 200)
	if settings.Revision != 1 || settings.DefaultCoordinatorID != "" || len(settings.Assignments) != 0 {
		t.Fatalf("unexpected initial settings: %+v", settings)
	}

	bad := []SettingsInput{
		{DefaultCoordinatorID: "not-a-uuid"},
		{DefaultCoordinatorID: "00000000-0000-4000-8000-000000000000"},
		{Assignments: map[string]map[string]string{"writing": {"coordinator": one[0].ID}}},
		{Assignments: map[string]map[string]string{"research": {"writer": one[0].ID}}},
		{Assignments: map[string]map[string]string{"research": {"researcher": one[0].ID, "critic": one[0].ID}}},
		{Assignments: map[string]map[string]string{"code_analysis": {"analyst": one[0].ID, "reviewer": one[0].ID}}},
		{Assignments: map[string]map[string]string{"engineering": {"developer": one[0].ID, "reviewer": one[0].ID}}},
		{Assignments: map[string]map[string]string{"engineering": {"writer": one[0].ID}}},
	}
	for _, input := range bad {
		call("PATCH", "/api/v1/settings", input, nil, 1, 400)
	}
	call("PATCH", "/api/v1/settings", SettingsInput{
		DefaultCoordinatorID: one[2].ID,
		Assignments: map[string]map[string]string{
			"discussion":    {"coordinator": one[1].ID},
			"research":      {"researcher": one[0].ID, "critic": one[1].ID, "coordinator": one[2].ID},
			"code_analysis": {"analyst": one[0].ID, "reviewer": one[1].ID},
			"engineering":   {"analyst": one[0].ID, "developer": one[1].ID, "reviewer": one[2].ID},
		},
	}, &settings, 1, 200)
	if settings.Revision != 2 || settings.DefaultCoordinatorID != one[2].ID || settings.Assignments["research"]["critic"] != one[1].ID {
		t.Fatalf("settings were not saved: %+v", settings)
	}
	call("PATCH", "/api/v1/settings", SettingsInput{}, nil, 1, 412)

	// A new conversation without an explicit coordinator uses the global default.
	var automatic w.Thread
	call("POST", "/api/v1/workspaces/"+second.ID+"/threads", map[string]string{"title": "默认协调者"}, &automatic, 0, 200)
	if automatic.CoordinatorID != one[2].ID || len(automatic.AgentIDs) != 3 {
		t.Fatalf("default coordinator was not applied: %+v", automatic)
	}
	var explicit w.Thread
	call("POST", "/api/v1/workspaces/"+second.ID+"/threads", w.Thread{Title: "显式协调者", AgentIDs: []string{two[0].ID, two[1].ID}, CoordinatorID: two[1].ID}, &explicit, 0, 200)
	if explicit.CoordinatorID != two[1].ID || len(explicit.AgentIDs) != 2 {
		t.Fatalf("explicit coordinator was overwritten: %+v", explicit)
	}

	// Preview fills the omitted research coordinator; submit freezes that choice.
	input := w.RunInput{Message: "研究默认分工", Workflow: "research", Assignments: map[string]string{"researcher": two[0].ID, "critic": two[1].ID}}
	var preview RoutingPreview
	call("POST", "/api/v1/threads/"+automatic.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" || preview.Routing.Assignments["coordinator"] != one[2].ID {
		t.Fatalf("preview did not apply the default assignment: %+v", preview)
	}
	input.ProposalID = preview.ProposalID
	var run w.Run
	submit := func(threadID, key string, in w.RunInput, out any) {
		t.Helper()
		b, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", "http://local.test/api/v1/threads/"+threadID+"/runs", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.AddCookie(cookie)
		req.Header.Set("X-CSRF-Token", csrf)
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		if rw.Code != 202 {
			t.Fatalf("submit got %d: %s", rw.Code, rw.Body.String())
		}
		if err := json.Unmarshal(rw.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	submit(automatic.ID, "settings-default-run", input, &run)
	if run.Routing.Assignments["researcher"] != two[0].ID || run.Routing.Assignments["critic"] != two[1].ID || run.Routing.Assignments["coordinator"] != one[2].ID {
		t.Fatalf("run did not receive the resolved assignments: %+v", run.Routing)
	}
	var view RunView
	call("GET", "/api/v1/runs/"+run.ID, nil, &view, 0, 200)
	if len(view.Steps) != 3 || view.Run.CoordinatorID != one[2].ID {
		t.Fatalf("unexpected run plan: %+v", view)
	}
	var threadView ThreadView
	call("GET", "/api/v1/threads/"+automatic.ID, nil, &threadView, 0, 200)
	if threadView.Thread.CoordinatorID != one[2].ID {
		t.Fatalf("thread coordinator changed: %+v", threadView.Thread)
	}

	// Changing the defaults later must not rewrite committed thread or run snapshots.
	call("PATCH", "/api/v1/settings", SettingsInput{DefaultCoordinatorID: one[0].ID}, &settings, 2, 200)
	call("GET", "/api/v1/runs/"+run.ID, nil, &view, 0, 200)
	if view.Run.Routing.Assignments["coordinator"] != one[2].ID || view.Run.CoordinatorID != one[2].ID {
		t.Fatalf("settings changed a historical run: %+v", view.Run)
	}
	call("GET", "/api/v1/threads/"+automatic.ID, nil, &threadView, 0, 200)
	if threadView.Thread.CoordinatorID != one[2].ID {
		t.Fatalf("settings changed an existing thread: %+v", threadView.Thread)
	}

	// A global member outside this workspace is not pulled in; visible fallback wins.
	var outsider w.Agent
	call("POST", "/api/v1/agents", w.Agent{DisplayName: "空间外成员", Handle: "outsider", Aliases: []string{}, Model: "default", Executor: "claude", Tools: []string{"read_file", "publish_artifact"}}, &outsider, 0, 200)
	call("PATCH", "/api/v1/settings", SettingsInput{
		DefaultCoordinatorID: outsider.ID,
		Assignments:          map[string]map[string]string{"research": {"coordinator": outsider.ID}},
	}, &settings, 3, 200)
	var fallback w.Thread
	call("POST", "/api/v1/workspaces/"+second.ID+"/threads", map[string]string{"title": "空间外回退"}, &fallback, 0, 200)
	// The global default is outside this workspace, so the documented roster
	// fallback applies: the third selected member, or the first enabled one.
	if len(fallback.AgentIDs) != 3 {
		t.Fatalf("unexpected fallback roster: %+v", fallback)
	}
	third := fallback.AgentIDs[min(2, len(fallback.AgentIDs)-1)]
	if fallback.CoordinatorID != third {
		t.Fatalf("fallback coordinator = %s, want third selected member %s (roster %v)", fallback.CoordinatorID, third, fallback.AgentIDs)
	}
	if fallback.CoordinatorID == outsider.ID {
		t.Fatalf("cross-workspace default was used: %+v", fallback)
	}
	if !slices.ContainsFunc(members(second.ID), func(agent w.Agent) bool {
		return agent.ID == fallback.CoordinatorID && !agent.Disabled
	}) {
		t.Fatalf("fallback coordinator is not an enabled member of this workspace: %+v", fallback)
	}
	for _, agent := range members(second.ID) {
		if agent.ID == outsider.ID {
			t.Fatal("global default granted workspace membership")
		}
	}
	input = w.RunInput{Message: "研究空间外回退", Workflow: "research", Assignments: map[string]string{"researcher": two[0].ID, "critic": two[1].ID}}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" || preview.Routing.Assignments["coordinator"] != two[2].ID {
		t.Fatalf("preview did not fall back to a workspace member: %+v", preview)
	}

	// Permission intersection: a default cannot supply a role it is not granted.
	call("PATCH", "/api/v1/workspaces/"+second.ID+"/members/"+two[1].ID, w.WorkspaceMember{Enabled: true, Tools: []string{"read_file"}}, nil, 1, 200)
	call("PATCH", "/api/v1/settings", SettingsInput{
		Assignments: map[string]map[string]string{
			"research":   {"coordinator": two[1].ID},
			"discussion": {"coordinator": two[1].ID},
		},
	}, &settings, 4, 200)
	input = w.RunInput{Message: "研究权限交集", Workflow: "research", Assignments: map[string]string{"researcher": two[0].ID, "critic": one[1].ID}}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" || preview.Routing.Assignments["coordinator"] == two[1].ID {
		t.Fatalf("default ignored publish_artifact intersection: %+v", preview)
	}
	// A discussion coordinator that lost the grant falls back to a publisher in
	// the same conversation instead of producing an impossible plan.
	input = w.RunInput{Message: "讨论发布权限回退", Workflow: "discussion", Targets: []string{two[0].ID, two[1].ID}}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" || preview.Routing.Assignments["coordinator"] == two[1].ID || preview.Routing.Assignments["coordinator"] == "" {
		t.Fatalf("discussion coordinator did not fall back to a publisher: %+v", preview)
	}

	// An explicit override is authoritative; an unusable pick is an error, not
	// a silent reassignment.
	input = w.RunInput{Message: "显式无效协调者", Workflow: "research", Assignments: map[string]string{"researcher": two[0].ID, "critic": two[1].ID, "coordinator": two[1].ID}}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, nil, 0, 400)

	// No assignments at all: every role is filled from the conversation and the
	// run can still be submitted.
	input = w.RunInput{Message: "无分工研究默认", Workflow: "research"}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" {
		t.Fatalf("no-assignment research preview failed: %+v", preview)
	}
	researchDefaults := preview.Routing.Assignments
	if researchDefaults["researcher"] == "" || researchDefaults["critic"] == "" || researchDefaults["coordinator"] == "" || researchDefaults["researcher"] == researchDefaults["critic"] {
		t.Fatalf("research defaults were not filled safely: %+v", researchDefaults)
	}
	input.ProposalID = preview.ProposalID
	var researchRun w.Run
	submit(fallback.ID, "settings-no-assignment-research", input, &researchRun)
	if researchRun.Routing.Assignments["researcher"] == "" || researchRun.Routing.Assignments["critic"] == "" || researchRun.Routing.Assignments["coordinator"] == "" {
		t.Fatalf("submitted research run lost its defaults: %+v", researchRun.Routing)
	}
	call("GET", "/api/v1/runs/"+researchRun.ID, nil, &view, 0, 200)
	if len(view.Steps) != 3 {
		t.Fatalf("research plan = %d steps, want 3", len(view.Steps))
	}

	// The same holds for code analysis in a workspace bound to a source root.
	var rooted w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "代码空间", Root: dir}, &rooted, 0, 200)
	var analysisThread w.Thread
	call("POST", "/api/v1/workspaces/"+rooted.ID+"/threads", map[string]string{"title": "无分工代码分析"}, &analysisThread, 0, 200)
	input = w.RunInput{Message: "无分工代码分析默认", Workflow: "code_analysis"}
	call("POST", "/api/v1/threads/"+analysisThread.ID+"/routing-preview", input, &preview, 0, 200)
	codeDefaults := preview.Routing.Assignments
	if preview.ProposalID == "" || codeDefaults["analyst"] == "" || codeDefaults["reviewer"] == "" || codeDefaults["analyst"] == codeDefaults["reviewer"] {
		t.Fatalf("code_analysis defaults were not filled safely: %+v", preview)
	}
	input.ProposalID = preview.ProposalID
	var analysisRun w.Run
	submit(analysisThread.ID, "settings-no-assignment-analysis", input, &analysisRun)
	call("GET", "/api/v1/runs/"+analysisRun.ID, nil, &view, 0, 200)
	if len(view.Steps) != 2 {
		t.Fatalf("code_analysis plan = %d steps, want 2", len(view.Steps))
	}

	// Two members with only one publisher can still analyze: the publisher
	// reviews and the other member analyzes.
	pair := members(rooted.ID)
	call("PATCH", "/api/v1/workspaces/"+rooted.ID+"/members/"+pair[1].ID, w.WorkspaceMember{Enabled: true, Tools: []string{"read_file"}}, nil, 1, 200)
	var paired w.Thread
	call("POST", "/api/v1/workspaces/"+rooted.ID+"/threads", w.Thread{Title: "双成员代码分析", AgentIDs: []string{pair[0].ID, pair[1].ID}}, &paired, 0, 200)
	input = w.RunInput{Message: "双成员代码分析默认", Workflow: "code_analysis"}
	call("POST", "/api/v1/threads/"+paired.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.Routing.Assignments["analyst"] != pair[1].ID || preview.Routing.Assignments["reviewer"] != pair[0].ID {
		t.Fatalf("two-member defaults = %+v, want analyst %s and reviewer %s", preview.Routing.Assignments, pair[1].ID, pair[0].ID)
	}
	input.ProposalID = preview.ProposalID
	var pairedRun w.Run
	submit(paired.ID, "settings-paired-analysis", input, &pairedRun)
	call("GET", "/api/v1/runs/"+pairedRun.ID, nil, &view, 0, 200)
	if len(view.Steps) != 2 {
		t.Fatalf("two-member code_analysis plan = %d steps, want 2", len(view.Steps))
	}
	// An explicit analyst that is the only publisher is not silently changed.
	input = w.RunInput{Message: "显式唯一发布者分析", Workflow: "code_analysis", Assignments: map[string]string{"analyst": pair[0].ID}}
	call("POST", "/api/v1/threads/"+paired.ID+"/routing-preview", input, nil, 0, 400)

	// A configured critic/reviewer keeps the preference but never doubles as
	// the other exclusive role.
	call("PATCH", "/api/v1/settings", SettingsInput{
		DefaultCoordinatorID: two[2].ID,
		Assignments: map[string]map[string]string{
			"research":      {"critic": two[0].ID},
			"code_analysis": {"reviewer": two[0].ID},
		},
	}, &settings, 5, 200)
	input = w.RunInput{Message: "仅质疑默认", Workflow: "research"}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.Routing.Assignments["critic"] != two[0].ID || preview.Routing.Assignments["researcher"] == "" || preview.Routing.Assignments["researcher"] == two[0].ID {
		t.Fatalf("critic default self-reviewed: %+v", preview.Routing.Assignments)
	}
	input = w.RunInput{Message: "仅复核默认", Workflow: "code_analysis"}
	call("POST", "/api/v1/threads/"+analysisThread.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.Routing.Assignments["reviewer"] != two[0].ID || preview.Routing.Assignments["analyst"] == "" || preview.Routing.Assignments["analyst"] == two[0].ID {
		t.Fatalf("reviewer default self-reviewed: %+v", preview.Routing.Assignments)
	}

	// An explicit conversation roster is never expanded by defaults, including
	// the global default coordinator.
	var narrow w.Thread
	call("POST", "/api/v1/workspaces/"+second.ID+"/threads", w.Thread{Title: "显式会话名单", AgentIDs: []string{two[0].ID, two[1].ID}}, &narrow, 0, 200)
	if !slices.Equal(narrow.AgentIDs, []string{two[0].ID, two[1].ID}) || narrow.CoordinatorID != two[1].ID {
		t.Fatalf("thread defaults changed an explicit roster: %+v", narrow)
	}
	input = w.RunInput{Message: "显式会话不扩员", Workflow: "research"}
	call("POST", "/api/v1/threads/"+narrow.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" {
		t.Fatalf("narrow roster preview failed: %+v", preview)
	}
	for role, id := range preview.Routing.Assignments {
		if !slices.Contains(narrow.AgentIDs, id) {
			t.Fatalf("default %s expanded outside the explicit roster: %s", role, id)
		}
	}
	input.ProposalID = preview.ProposalID
	var narrowRun w.Run
	submit(narrow.ID, "settings-explicit-roster", input, &narrowRun)
	call("GET", "/api/v1/threads/"+narrow.ID, nil, &threadView, 0, 200)
	if !slices.Equal(threadView.Thread.AgentIDs, []string{two[0].ID, two[1].ID}) {
		t.Fatalf("submit expanded the explicit roster: %+v", threadView.Thread)
	}

	// Avatars accept only the built-in choices; URLs and SVG are rejected.
	edited := one[0]
	edited.Avatar = "🐱"
	call("PATCH", "/api/v1/agents/"+edited.ID, edited, &edited, edited.Revision, 200)
	if edited.Avatar != "🐱" {
		t.Fatalf("avatar was not saved: %+v", edited)
	}
	for _, avatar := range []string{"https://example.test/a.svg", "<svg/>", "data:image/svg+xml;base64,AA=="} {
		edited.Avatar = avatar
		call("PATCH", "/api/v1/agents/"+edited.ID, edited, nil, edited.Revision, 400)
	}
}

// TestCodeAnalysisDefaultsKeepTheOnlyPublisherForTheReviewer covers a feasible
// two-member assignment: the member without publish_artifact analyzes and the
// only publisher reviews, instead of rejecting the run.
func TestEngineeringRoleDefaultsFollowTheSelectedAction(t *testing.T) {
	builder := w.Agent{ID: "builder", DisplayName: "开发", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	writer := w.Agent{ID: "writer", DisplayName: "写手", Tools: []string{"read_file", "write_file", "run_test"}}
	reviewer := w.Agent{ID: "reviewer", DisplayName: "复核", Tools: []string{"read_file", "run_test", "publish_artifact"}}
	reader := w.Agent{ID: "reader", DisplayName: "只读", Tools: []string{"read_file"}}
	agents := []w.Agent{builder, writer, reviewer, reader}
	thread := w.Thread{AgentIDs: []string{builder.ID, writer.ID, reviewer.ID, reader.ID}, CoordinatorID: reviewer.ID}
	develop := func(in w.RunInput, settings w.Settings) (w.RunInput, error) {
		in.Workflow = "engineering"
		in.EngineeringAction = w.EngineeringDevelop
		return applySettingsDefaults(in, thread, agents, settings)
	}

	// Omitted roles use only the action's subset and never self-review.
	got, err := develop(w.RunInput{}, w.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["developer"] != builder.ID || got.Assignments["reviewer"] != reviewer.ID {
		t.Fatalf("develop defaults = %+v", got.Assignments)
	}
	if _, ok := got.Assignments["analyst"]; ok {
		t.Fatalf("develop defaults included the analysis role: %+v", got.Assignments)
	}
	// A configured role UUID is consumed when it is actually eligible.
	got, err = develop(w.RunInput{}, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"developer": writer.ID, "reviewer": reviewer.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["developer"] != writer.ID || got.Assignments["reviewer"] != reviewer.ID {
		t.Fatalf("configured engineering roles = %+v", got.Assignments)
	}
	// A configured default that cannot perform the role is not used and does
	// not expand the conversation roster.
	got, err = develop(w.RunInput{}, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"developer": reader.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["developer"] != builder.ID || got.Assignments["reviewer"] != reviewer.ID {
		t.Fatalf("unavailable developer default = %+v", got.Assignments)
	}
	// An explicit role outside the action or outside the roster is an error.
	if _, err := develop(w.RunInput{Assignments: map[string]string{"analyst": reader.ID}}, w.Settings{}); err == nil || !strings.Contains(err.Error(), "不支持角色") {
		t.Fatalf("explicit analyst on develop err = %v", err)
	}
	if _, err := develop(w.RunInput{Assignments: map[string]string{"developer": "outside"}}, w.Settings{}); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("explicit outside developer err = %v", err)
	}
	// The same member may not develop and review its own work.
	self, err := develop(w.RunInput{Assignments: map[string]string{"developer": builder.ID, "reviewer": builder.ID}}, w.Settings{})
	if err == nil {
		_, err = workflow.Plan("preview", self, agents)
	}
	if err == nil {
		t.Fatal("developer and reviewer shared a member")
	}

	// Analysis ignores the inapplicable developer default and keeps the
	// publisher for the reviewer.
	analyze, err := applySettingsDefaults(w.RunInput{Workflow: "engineering", EngineeringAction: w.EngineeringAnalyze}, thread, agents, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"developer": writer.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if analyze.Assignments["analyst"] != builder.ID || analyze.Assignments["reviewer"] != reviewer.ID {
		t.Fatalf("analysis defaults = %+v", analyze.Assignments)
	}
	if _, ok := analyze.Assignments["developer"]; ok {
		t.Fatalf("analysis used the developer role: %+v", analyze.Assignments)
	}

	// The only publisher is reserved for the reviewer even when it is also the
	// first eligible developer; the other writer takes development.
	publisher := w.Agent{ID: "publisher", DisplayName: "唯一发布者", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	pairAgents := []w.Agent{publisher, writer, reader}
	pair := w.Thread{AgentIDs: []string{publisher.ID, writer.ID, reader.ID}, CoordinatorID: publisher.ID}
	reserved, err := applySettingsDefaults(w.RunInput{Workflow: "engineering", EngineeringAction: w.EngineeringDevelop}, pair, pairAgents, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"developer": publisher.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Assignments["developer"] != writer.ID || reserved.Assignments["reviewer"] != publisher.ID {
		t.Fatalf("publisher reservation = %+v", reserved.Assignments)
	}
	// A journal entry for an unsupported engineering role is rejected.
	if err := w.ValidateSettings(w.Settings{Assignments: map[string]map[string]string{"engineering": {"writer": publisher.ID}}}, agents); err == nil {
		t.Fatal("settings accepted an unsupported engineering role")
	}
}

// TestEngineeringDevelopReviewerDefaultsRequireRunTest proves the production
// default resolver applies the develop action's full reviewer grant set: a
// publisher without run_test cannot review development, while analysis keeps
// its read-only reviewer requirement.
func TestEngineeringDevelopReviewerDefaultsRequireRunTest(t *testing.T) {
	developer := w.Agent{ID: "dev", DisplayName: "开发", Tools: []string{"read_file", "write_file", "run_test"}}
	publisher := w.Agent{ID: "publisher", DisplayName: "仅发布", Tools: []string{"read_file", "publish_artifact"}}
	reviewer := w.Agent{ID: "reviewer", DisplayName: "复核", Tools: []string{"read_file", "run_test", "publish_artifact"}}
	agents := []w.Agent{developer, publisher, reviewer}
	thread := w.Thread{AgentIDs: []string{developer.ID, publisher.ID, reviewer.ID}, CoordinatorID: publisher.ID}
	develop := func(in w.RunInput, settings w.Settings) (w.RunInput, error) {
		in.Workflow = "engineering"
		in.EngineeringAction = w.EngineeringDevelop
		return applySettingsDefaults(in, thread, agents, settings)
	}

	// A global reviewer preference without run_test is unusable and falls back
	// to an eligible reviewer inside the same roster.
	got, err := develop(w.RunInput{}, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"reviewer": publisher.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["reviewer"] != reviewer.ID || got.Assignments["developer"] != developer.ID {
		t.Fatalf("develop defaults accepted an undergranted reviewer: %+v", got.Assignments)
	}
	// An explicit undergranted reviewer is authoritative and refused.
	_, err = develop(w.RunInput{Assignments: map[string]string{"reviewer": publisher.ID}}, w.Settings{})
	if err == nil || !strings.Contains(err.Error(), "run_test") {
		t.Fatalf("explicit develop reviewer without run_test err = %v", err)
	}
	// An explicit valid reviewer stays unchanged.
	got, err = develop(w.RunInput{Assignments: map[string]string{"developer": developer.ID, "reviewer": reviewer.ID}}, w.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["developer"] != developer.ID || got.Assignments["reviewer"] != reviewer.ID {
		t.Fatalf("explicit develop assignments changed: %+v", got.Assignments)
	}
	// Analysis keeps the publish-only reviewer requirement: no run_test needed.
	analyze, err := applySettingsDefaults(w.RunInput{Workflow: "engineering", EngineeringAction: w.EngineeringAnalyze, Assignments: map[string]string{"analyst": developer.ID, "reviewer": publisher.ID}}, thread, agents, w.Settings{})
	if err != nil {
		t.Fatalf("analysis reviewer without run_test was refused: %v", err)
	}
	if analyze.Assignments["reviewer"] != publisher.ID {
		t.Fatalf("analysis assignments = %+v", analyze.Assignments)
	}
}

func TestCodeAnalysisDefaultsKeepTheOnlyPublisherForTheReviewer(t *testing.T) {
	publisher := w.Agent{ID: "publisher", DisplayName: "发布者", Tools: []string{"publish_artifact"}}
	reader := w.Agent{ID: "reader", DisplayName: "读者"}
	agents := []w.Agent{publisher, reader}
	thread := w.Thread{AgentIDs: []string{publisher.ID, reader.ID}, CoordinatorID: reader.ID}
	check := func(name string, in w.RunInput, settings w.Settings, wantAnalyst, wantReviewer string) {
		t.Helper()
		got, err := applySettingsDefaults(in, thread, agents, settings)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Assignments["analyst"] != wantAnalyst || got.Assignments["reviewer"] != wantReviewer {
			t.Fatalf("%s: assignments = %+v, want analyst %s reviewer %s", name, got.Assignments, wantAnalyst, wantReviewer)
		}
	}
	check("no defaults", w.RunInput{Workflow: "code_analysis"}, w.Settings{}, reader.ID, publisher.ID)
	// A global analyst is soft: it yields the only publisher to the reviewer
	// role instead of blocking the plan.
	check("global analyst", w.RunInput{Workflow: "code_analysis"}, w.Settings{
		Assignments: map[string]map[string]string{"code_analysis": {"analyst": publisher.ID}},
	}, reader.ID, publisher.ID)
	// An explicit analyst is authoritative. Occupying the only publisher leaves
	// no other reviewer; report that instead of silently reassigning the user.
	_, err := applySettingsDefaults(w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": publisher.ID}}, thread, agents, w.Settings{})
	if err == nil || !strings.Contains(err.Error(), "另一位") {
		t.Fatalf("explicit unique publisher analyst error = %v, want a missing reviewer error", err)
	}
	// An explicit non-publisher analyst stays unchanged.
	check("explicit analyst", w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": reader.ID}}, w.Settings{}, reader.ID, publisher.ID)

	// A configured exclusive partner keeps its member and the other role moves.
	research := w.Thread{AgentIDs: []string{publisher.ID, reader.ID}, CoordinatorID: publisher.ID}
	got, err := applySettingsDefaults(w.RunInput{Workflow: "research"}, research, agents, w.Settings{
		Assignments: map[string]map[string]string{"research": {"critic": reader.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["critic"] != reader.ID || got.Assignments["researcher"] != publisher.ID {
		t.Fatalf("configured exclusive role was not preserved: %+v", got.Assignments)
	}
}
