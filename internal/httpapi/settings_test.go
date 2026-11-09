package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// ptr returns a pointer to a value for optional field-merge settings input.
func ptr[T any](value T) *T { return &value }

// assignments wraps a role map for the optional settings field.
func assignments(value map[string]map[string]string) *map[string]map[string]string {
	return &value
}

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
		{DefaultCoordinatorID: ptr("not-a-uuid")},
		{DefaultCoordinatorID: ptr("00000000-0000-4000-8000-000000000000")},
		{Assignments: assignments(map[string]map[string]string{"writing": {"coordinator": one[0].ID}})},
		{Assignments: assignments(map[string]map[string]string{"research": {"writer": one[0].ID}})},
		{Assignments: assignments(map[string]map[string]string{"research": {"researcher": one[0].ID, "critic": one[0].ID}})},
		{Assignments: assignments(map[string]map[string]string{"code_analysis": {"analyst": one[0].ID, "reviewer": one[0].ID}})},
		{Assignments: assignments(map[string]map[string]string{"engineering": {"developer": one[0].ID, "reviewer": one[0].ID}})},
		{Assignments: assignments(map[string]map[string]string{"engineering": {"writer": one[0].ID}})},
	}
	for _, input := range bad {
		call("PATCH", "/api/v1/settings", input, nil, 1, 400)
	}
	call("PATCH", "/api/v1/settings", SettingsInput{
		DefaultCoordinatorID: ptr(one[2].ID),
		Assignments: assignments(map[string]map[string]string{
			"discussion":    {"coordinator": one[1].ID},
			"research":      {"researcher": one[0].ID, "critic": one[1].ID, "coordinator": one[2].ID},
			"code_analysis": {"analyst": one[0].ID, "reviewer": one[1].ID},
			"engineering":   {"analyst": one[0].ID, "developer": one[1].ID, "reviewer": one[2].ID},
		}),
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
	if len(view.Steps) != 5 || view.Run.CoordinatorID != one[2].ID {
		t.Fatalf("unexpected run plan: %+v", view)
	}
	var threadView ThreadView
	call("GET", "/api/v1/threads/"+automatic.ID, nil, &threadView, 0, 200)
	if threadView.Thread.CoordinatorID != one[2].ID {
		t.Fatalf("thread coordinator changed: %+v", threadView.Thread)
	}

	// Changing the defaults later must not rewrite committed thread or run snapshots.
	call("PATCH", "/api/v1/settings", SettingsInput{DefaultCoordinatorID: ptr(one[0].ID)}, &settings, 2, 200)
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
		DefaultCoordinatorID: ptr(outsider.ID),
		Assignments:          assignments(map[string]map[string]string{"research": {"coordinator": outsider.ID}}),
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

	// Legacy tool arrays no longer intersect: editing only the workspace grant
	// cannot silently strip a profile capability, and the global default may be
	// used for the role it is qualified for.
	call("PATCH", "/api/v1/workspaces/"+second.ID+"/members/"+two[1].ID, w.WorkspaceMember{Enabled: true, Tools: []string{"read_file"}}, nil, 1, 200)
	call("PATCH", "/api/v1/settings", SettingsInput{
		Assignments: assignments(map[string]map[string]string{
			"research":   {"coordinator": two[1].ID},
			"discussion": {"coordinator": two[1].ID},
		}),
	}, &settings, 4, 200)
	input = w.RunInput{Message: "研究权限交集", Workflow: "research", Assignments: map[string]string{"researcher": two[0].ID, "critic": one[1].ID}}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" || preview.Routing.Assignments["coordinator"] != two[1].ID {
		t.Fatalf("legacy workspace tool array still narrowed a profile capability: %+v", preview)
	}
	// A disabled member can never be staffed, whatever its tool array says.
	call("PATCH", "/api/v1/workspaces/"+second.ID+"/members/"+two[1].ID, w.WorkspaceMember{Enabled: false, Tools: []string{"read_file"}}, nil, 2, 200)
	input = w.RunInput{Message: "研究停用回退", Workflow: "research", Assignments: map[string]string{"researcher": two[0].ID, "critic": two[2].ID}}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" || preview.Routing.Assignments["coordinator"] == two[1].ID {
		t.Fatalf("disabled member was staffed by a default: %+v", preview)
	}
	call("PATCH", "/api/v1/workspaces/"+second.ID+"/members/"+two[1].ID, w.WorkspaceMember{Enabled: true, Tools: []string{"read_file"}}, nil, 3, 200)
	// A discussion coordinator that is no longer enabled falls back to a
	// publisher in the same conversation instead of producing an impossible plan.
	input = w.RunInput{Message: "讨论发布权限回退", Workflow: "discussion", Targets: []string{two[0].ID, two[1].ID}}
	call("POST", "/api/v1/threads/"+fallback.ID+"/routing-preview", input, &preview, 0, 200)
	if preview.ProposalID == "" || preview.Routing.Assignments["coordinator"] == "" {
		t.Fatalf("discussion coordinator was not filled: %+v", preview)
	}

	// An explicit override is authoritative; an unusable pick is an error, not
	// a silent reassignment. Legacy tool arrays no longer make an otherwise
	// enabled member unusable, so the invalid case is a member that is not in
	// this conversation/workspace at all.
	input = w.RunInput{Message: "显式无效协调者", Workflow: "research", Assignments: map[string]string{"researcher": two[0].ID, "critic": two[1].ID, "coordinator": "missing-member"}}
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
	if len(view.Steps) != 5 {
		t.Fatalf("research plan = %d steps, want 5", len(view.Steps))
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
	if len(view.Steps) != 4 {
		t.Fatalf("code_analysis plan = %d steps, want 4", len(view.Steps))
	}

	// Two members are enough for code analysis: the analyst and reviewer must be
	// distinct, and a member whose legacy tool array omits publish_artifact is
	// still eligible because the runtime catalog provides it.
	pair := members(rooted.ID)
	authorProfile, err := loadAgent(call, pair[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	authorProfile.Tools = []string{"read_file", "write_file", "run_test"}
	call("PATCH", "/api/v1/agents/"+authorProfile.ID, authorProfile, nil, authorProfile.Revision, 200)
	var paired w.Thread
	call("POST", "/api/v1/workspaces/"+rooted.ID+"/threads", w.Thread{Title: "双成员代码分析", AgentIDs: []string{pair[0].ID, pair[1].ID}}, &paired, 0, 200)
	input = w.RunInput{Message: "双成员代码分析默认", Workflow: "code_analysis"}
	call("POST", "/api/v1/threads/"+paired.ID+"/routing-preview", input, &preview, 0, 200)
	analyst, reviewer := preview.Routing.Assignments["analyst"], preview.Routing.Assignments["reviewer"]
	if analyst == "" || reviewer == "" || analyst == reviewer {
		t.Fatalf("two-member defaults = %+v", preview.Routing.Assignments)
	}
	if analyst != pair[0].ID && analyst != pair[1].ID || reviewer != pair[0].ID && reviewer != pair[1].ID {
		t.Fatalf("two-member defaults used a member outside the conversation: %+v", preview.Routing.Assignments)
	}
	input.ProposalID = preview.ProposalID
	var pairedRun w.Run
	submit(paired.ID, "settings-paired-analysis", input, &pairedRun)
	call("GET", "/api/v1/runs/"+pairedRun.ID, nil, &view, 0, 200)
	if len(view.Steps) != 4 {
		t.Fatalf("two-member code_analysis plan = %d steps, want 4", len(view.Steps))
	}
	// An explicit analyst still needs a different reviewer in the same roster.
	input = w.RunInput{Message: "显式唯一发布者分析", Workflow: "code_analysis", Assignments: map[string]string{"analyst": pair[0].ID, "reviewer": pair[0].ID}}
	call("POST", "/api/v1/threads/"+paired.ID+"/routing-preview", input, nil, 0, 400)

	// A configured critic/reviewer keeps the preference but never doubles as
	// the other exclusive role.
	call("PATCH", "/api/v1/settings", SettingsInput{
		DefaultCoordinatorID: ptr(two[2].ID),
		Assignments: assignments(map[string]map[string]string{
			"research":      {"critic": two[0].ID},
			"code_analysis": {"reviewer": two[0].ID},
		}),
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

	// Avatars accept only verified image uploads; URLs and SVG are rejected.
	edited := one[0]
	const avatar = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAAEElEQVR4nGLSb5wLCAAA//8COAFQWwmV3gAAAABJRU5ErkJggg=="
	edited.Avatar = avatar
	call("PATCH", "/api/v1/agents/"+edited.ID, edited, &edited, edited.Revision, 200)
	if edited.Avatar != avatar {
		t.Fatalf("avatar was not saved: %+v", edited)
	}
	for _, avatar := range []string{"https://example.test/a.svg", "<svg/>", "data:image/svg+xml;base64,AA=="} {
		edited.Avatar = avatar
		call("PATCH", "/api/v1/agents/"+edited.ID, edited, nil, edited.Revision, 400)
	}
}

// TestEngineeringRoleDefaultsUseEngineerAndReviewer covers role staffing under
// the retired member tool configuration: the runtime catalog supplies publish and
// write, so staffing is decided by roster and role exclusivity.
func TestEngineeringRoleDefaultsUseEngineerAndReviewer(t *testing.T) {
	builder := w.Agent{ID: "builder", DisplayName: "执行", Tools: []string{"read_file", "write_file", "run_test", "publish_artifact"}}
	writer := w.Agent{ID: "writer", DisplayName: "写手", Tools: []string{"read_file", "write_file", "run_test"}}
	reviewer := w.Agent{ID: "reviewer", DisplayName: "复核", Tools: []string{"read_file", "run_test", "publish_artifact"}}
	reader := w.Agent{ID: "reader", DisplayName: "只读", Tools: []string{"read_file"}}
	agents := []w.Agent{builder, writer, reviewer, reader}
	thread := w.Thread{AgentIDs: []string{builder.ID, writer.ID, reviewer.ID, reader.ID}, CoordinatorID: reviewer.ID}
	prepare := func(in w.RunInput, settings w.Settings) (w.RunInput, error) {
		in.Workflow = "engineering"
		return applySettingsDefaults(in, thread, agents, settings)
	}

	// The conversation coordinator is reserved for coordination, never consumed
	// as the engineer when another eligible member exists.
	got, err := prepare(w.RunInput{}, w.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	// Every member carries the runtime catalog, so staffing is by roster order and
	// role exclusivity; a legacy array without publish_artifact no longer excludes
	// the writer from the reviewer candidate list.
	if got.Assignments["engineer"] != builder.ID || got.Assignments["reviewer"] != writer.ID {
		t.Fatalf("engineering defaults = %+v", got.Assignments)
	}
	if got.Assignments["engineer"] == got.Assignments["reviewer"] {
		t.Fatalf("engineering reused one member for both roles: %+v", got.Assignments)
	}
	if _, ok := got.Assignments["analyst"]; ok {
		t.Fatalf("engineering defaults included a legacy role: %+v", got.Assignments)
	}
	// A configured engineer and reviewer are consumed when eligible.
	got, err = prepare(w.RunInput{}, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"engineer": writer.ID, "reviewer": reviewer.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["engineer"] != writer.ID || got.Assignments["reviewer"] != reviewer.ID {
		t.Fatalf("configured engineering roles = %+v", got.Assignments)
	}
	// A read-only member may engineer; only the reviewer needs publication.
	got, err = prepare(w.RunInput{}, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"engineer": reader.ID, "reviewer": reviewer.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["engineer"] != reader.ID || got.Assignments["reviewer"] != reviewer.ID {
		t.Fatalf("read-only engineer default = %+v", got.Assignments)
	}
	// A legacy role or a member outside the conversation is rejected.
	if _, err := prepare(w.RunInput{Assignments: map[string]string{"analyst": reader.ID}}, w.Settings{}); err == nil || !strings.Contains(err.Error(), "不支持角色") {
		t.Fatalf("explicit analyst err = %v", err)
	}
	if _, err := prepare(w.RunInput{Assignments: map[string]string{"engineer": "outside"}}, w.Settings{}); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("explicit outside engineer err = %v", err)
	}
	// The same member may not engineer and review its own work.
	self, err := prepare(w.RunInput{Assignments: map[string]string{"engineer": builder.ID, "reviewer": builder.ID}}, w.Settings{})
	if err == nil {
		_, err = workflow.Plan("preview", self, agents)
	}
	if err == nil {
		t.Fatal("engineer and reviewer shared a member")
	}

	// An explicit engineer is honored and the reviewer is a different eligible
	// member; no legacy tool array decides either.
	publisher := w.Agent{ID: "publisher", DisplayName: "发布者", Tools: []string{"publish_artifact"}}
	pairAgents := []w.Agent{publisher, writer, reader}
	pair := w.Thread{AgentIDs: []string{publisher.ID, writer.ID, reader.ID}, CoordinatorID: publisher.ID}
	reserved, err := applySettingsDefaults(w.RunInput{Workflow: "engineering"}, pair, pairAgents, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"engineer": publisher.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Assignments["engineer"] != publisher.ID || reserved.Assignments["reviewer"] == publisher.ID || reserved.Assignments["reviewer"] == "" {
		t.Fatalf("explicit engineer reservation = %+v", reserved.Assignments)
	}
	// A journal entry for an unsupported engineering role is rejected.
	if err := w.ValidateSettings(w.Settings{Assignments: map[string]map[string]string{"engineering": {"writer": publisher.ID}}}, agents); err == nil {
		t.Fatal("settings accepted an unsupported engineering role")
	}
}

// TestEngineeringReviewerDefaultsDoNotRequireRunTest proves the single
// engineering workflow only requires the reviewer to publish. A read-only
// reviewer without run_test remains valid; write/test capability belongs to the
// engineer's existing grants.
func TestEngineeringReviewerDefaultsDoNotRequireRunTest(t *testing.T) {
	engineer := w.Agent{ID: "dev", DisplayName: "执行", Tools: []string{"read_file", "write_file", "run_test"}}
	publisher := w.Agent{ID: "publisher", DisplayName: "仅发布", Tools: []string{"read_file", "publish_artifact"}}
	reviewer := w.Agent{ID: "reviewer", DisplayName: "复核", Tools: []string{"read_file", "run_test", "publish_artifact"}}
	agents := []w.Agent{engineer, publisher, reviewer}
	thread := w.Thread{AgentIDs: []string{engineer.ID, publisher.ID, reviewer.ID}, CoordinatorID: publisher.ID}
	prepare := func(in w.RunInput, settings w.Settings) (w.RunInput, error) {
		in.Workflow = "engineering"
		return applySettingsDefaults(in, thread, agents, settings)
	}

	got, err := prepare(w.RunInput{}, w.Settings{Assignments: map[string]map[string]string{
		"engineering": {"reviewer": publisher.ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["reviewer"] != publisher.ID || got.Assignments["engineer"] != engineer.ID {
		t.Fatalf("engineering defaults = %+v", got.Assignments)
	}
	got, err = prepare(w.RunInput{Assignments: map[string]string{"engineer": engineer.ID, "reviewer": publisher.ID}}, w.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["engineer"] != engineer.ID || got.Assignments["reviewer"] != publisher.ID {
		t.Fatalf("explicit engineering assignments changed: %+v", got.Assignments)
	}
}

// TestCodeAnalysisDefaultsKeepRolesDistinct proves analyst and reviewer are two
// different enabled members from the runtime catalog, and an explicit analyst is
// authoritative even when its legacy tool array lists nothing.
func TestCodeAnalysisDefaultsKeepRolesDistinct(t *testing.T) {
	publisher := w.Agent{ID: "publisher", DisplayName: "发布者", Tools: []string{"publish_artifact"}}
	reader := w.Agent{ID: "reader", DisplayName: "读者"}
	agents := []w.Agent{publisher, reader}
	thread := w.Thread{AgentIDs: []string{publisher.ID, reader.ID}, CoordinatorID: reader.ID}
	checkDistinct := func(name string, in w.RunInput, settings w.Settings) {
		t.Helper()
		got, err := applySettingsDefaults(in, thread, agents, settings)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		analyst, reviewer := got.Assignments["analyst"], got.Assignments["reviewer"]
		if analyst == "" || reviewer == "" || analyst == reviewer {
			t.Fatalf("%s: assignments = %+v", name, got.Assignments)
		}
	}
	checkDistinct("no defaults", w.RunInput{Workflow: "code_analysis"}, w.Settings{})
	checkDistinct("global analyst", w.RunInput{Workflow: "code_analysis"}, w.Settings{
		Assignments: map[string]map[string]string{"code_analysis": {"analyst": publisher.ID}},
	})
	// An explicit analyst is authoritative; the reviewer role moves to the other
	// member because one member cannot hold both exclusive roles.
	got, err := applySettingsDefaults(w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": publisher.ID}}, thread, agents, w.Settings{})
	if err != nil {
		t.Fatalf("explicit analyst was not honored: %v", err)
	}
	if got.Assignments["analyst"] != publisher.ID || got.Assignments["reviewer"] != reader.ID {
		t.Fatalf("explicit analyst assignments = %+v", got.Assignments)
	}
	checkDistinct("explicit analyst", w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": reader.ID}}, w.Settings{})

	// A configured exclusive partner keeps its member and the other role moves.
	research := w.Thread{AgentIDs: []string{publisher.ID, reader.ID}, CoordinatorID: publisher.ID}
	got, err = applySettingsDefaults(w.RunInput{Workflow: "research"}, research, agents, w.Settings{
		Assignments: map[string]map[string]string{"research": {"critic": reader.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["critic"] != reader.ID || got.Assignments["researcher"] != publisher.ID {
		t.Fatalf("configured exclusive role was not preserved: %+v", got.Assignments)
	}
}

// TestRoutingProposalCatalogIsAvatarFreeButPermissionSensitive reproduces the
// web case where a member already has a profile avatar when routing is
// previewed. Avatars are presentation-only and must not stale the submission,
// while real grant and disabled-state changes still must.
func TestRoutingProposalCatalogIsAvatarFreeButPermissionSensitive(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine := &workflow.Engine{Store: db, Model: testutil.Model{}, Web: testutil.Web{}}
	api := newDeliveryAPI(t, dir, db, engine)

	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "头像路由回归"}, nil, 200, &ws)
	var listed Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &listed)
	if len(listed.Items) < 2 {
		t.Fatalf("seeded members = %d", len(listed.Items))
	}
	fox, other := listed.Items[0], listed.Items[1]
	const avatar = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAAEElEQVR4nGLSb5wLCAAA//8COAFQWwmV3gAAAABJRU5ErkJggg=="
	fox.Avatar = avatar
	api.call("PATCH", "/api/v1/agents/"+fox.ID, fox, map[string]string{"If-Match": strconv.Itoa(fox.Revision)}, 200, &fox)
	if fox.Avatar != avatar {
		t.Fatalf("avatar was not saved: %+v", fox)
	}
	newThread := func(title string) w.Thread {
		t.Helper()
		var th w.Thread
		api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: title, AgentIDs: []string{fox.ID, other.ID}, Workflow: "discussion"}, nil, 200, &th)
		return th
	}
	preview := func(threadID string) w.RunInput {
		t.Helper()
		in := w.RunInput{Message: "头像只影响展示，不影响本次讨论", Workflow: "discussion", Targets: []string{fox.ID, other.ID}}
		var proposal RoutingPreview
		api.call("POST", "/api/v1/threads/"+threadID+"/routing-preview", in, nil, 200, &proposal)
		if proposal.ProposalID == "" {
			t.Fatalf("preview did not return a proposal: %+v", proposal)
		}
		in.ProposalID = proposal.ProposalID
		return in
	}
	stale := func(threadID string, in w.RunInput, key string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		api.call("POST", "/api/v1/threads/"+threadID+"/runs", in, map[string]string{"Idempotency-Key": key}, 409, &body)
		if body.Error.Code != "routing_proposal_stale" {
			t.Fatalf("changed permission was not rejected as a stale proposal: %+v", body)
		}
	}

	// The actual UI reproduction: a nonempty avatar before preview is accepted.
	avatarThread := newThread("头像讨论")
	input := preview(avatarThread.ID)
	headers := map[string]string{"Idempotency-Key": "avatar-preview-submit"}
	var accepted, replay w.Run
	api.call("POST", "/api/v1/threads/"+avatarThread.ID+"/runs", input, headers, 202, &accepted)
	api.call("POST", "/api/v1/threads/"+avatarThread.ID+"/runs", input, headers, 202, &replay)
	if accepted.ID == "" || replay.ID != accepted.ID {
		t.Fatalf("idempotent repeat changed the accepted run: %s / %s", accepted.ID, replay.ID)
	}

	// A workspace tool grant change still invalidates the proposal.
	toolThread := newThread("权限变更")
	toolInput := preview(toolThread.ID)
	api.call("PATCH", "/api/v1/workspaces/"+ws.ID+"/members/"+other.ID, w.WorkspaceMember{Enabled: true, Tools: []string{"read_file"}}, map[string]string{"If-Match": "1"}, 200, nil)
	stale(toolThread.ID, toolInput, "avatar-tool-change")

	// A global disable still invalidates the proposal.
	disabledThread := newThread("成员停用")
	disabledInput := preview(disabledThread.ID)
	fox.Disabled = true
	api.call("PATCH", "/api/v1/agents/"+fox.ID, fox, map[string]string{"If-Match": strconv.Itoa(fox.Revision)}, 200, &fox)
	stale(disabledThread.ID, disabledInput, "avatar-disabled-member")
}

// TestWorkerRolesPreferNonCoordinatorMembers keeps the conversation
// coordinator available for the coordinating role instead of consuming it as
// the first researcher or analyst when other eligible members exist. A
// two-member roster still fills every role by backtracking.
func TestWorkerRolesPreferNonCoordinatorMembers(t *testing.T) {
	coordinator := w.Agent{ID: "coord", DisplayName: "协调者", Tools: []string{"read_file", "publish_artifact"}}
	researcher := w.Agent{ID: "researcher", DisplayName: "研究成员", Tools: []string{"read_file", "publish_artifact"}}
	critic := w.Agent{ID: "critic", DisplayName: "质疑成员", Tools: []string{"read_file", "publish_artifact"}}
	agents := []w.Agent{coordinator, researcher, critic}
	thread := w.Thread{AgentIDs: []string{"coord", "researcher", "critic"}, CoordinatorID: "coord"}
	got, err := applySettingsDefaults(w.RunInput{Workflow: "research"}, thread, agents, w.Settings{
		Assignments: map[string]map[string]string{"research": {"coordinator": "coord"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["researcher"] != "researcher" || got.Assignments["critic"] != "critic" || got.Assignments["coordinator"] != "coord" {
		t.Fatalf("worker roles consumed the coordinator: %+v", got.Assignments)
	}

	pair := w.Thread{AgentIDs: []string{"coord", "researcher"}, CoordinatorID: "coord"}
	got, err = applySettingsDefaults(w.RunInput{Workflow: "research"}, pair, []w.Agent{coordinator, researcher}, w.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignments["researcher"] != "researcher" || got.Assignments["critic"] != "coord" {
		t.Fatalf("two-member fallback did not use the coordinator as the worker of last resort: %+v", got.Assignments)
	}
}

// loadAgent reads one global profile so a test can change its audit tool array.
func loadAgent(call func(method, path string, in, out any, revision, want int), id string) (w.Agent, error) {
	var list struct{ Items []w.Agent }
	call("GET", "/api/v1/agents", nil, &list, 0, 200)
	for _, agent := range list.Items {
		if agent.ID == id {
			return agent, nil
		}
	}
	return w.Agent{}, fmt.Errorf("agent %s not found", id)
}
