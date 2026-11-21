package httpapi

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestSubmissionKeepsHistoricalFailureWithCoordinator covers the deterministic
// replacement for the retired routing preview: a failure record that merely
// names a member in prose does not dispatch work to that member.
func TestSubmissionKeepsHistoricalFailureWithCoordinator(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine := &workflow.Engine{Store: db, Model: testutil.Model{}, Web: testutil.Web{}}
	api := newDeliveryAPI(t, dir, db, engine)
	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "历史故障"}, nil, 200, &ws)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	if len(members.Items) < 2 {
		t.Fatal("expected two fixture members")
	}
	failed, coordinator := members.Items[0], members.Items[1]
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "历史故障", AgentIDs: []string{failed.ID, coordinator.ID}, CoordinatorID: coordinator.ID}, nil, 200, &thread)
	input := w.RunInput{Message: "帮我检查上次失败。\n失败成员是" + failed.DisplayName + "，executor=fixture，model=fixture/model。\n不要修改其他目录。"}
	var run w.Run
	api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "historical-failure-fixture"}, 202, &run)
	if !slices.Equal(run.Input.Targets, []string{coordinator.ID}) || run.Input.Message != input.Message {
		t.Fatalf("accepted message lost its text or coordinator: %+v", run.Input)
	}
}

// TestSubmissionPrioritizesUserRecipients covers explicit mentions and
// Composer-selected members winning over the conversation coordinator.
func TestSubmissionPrioritizesUserRecipients(t *testing.T) {
	for _, tc := range []struct {
		name string
		text func(orange w.Agent) string
	}{
		{name: "explicit mention", text: func(orange w.Agent) string {
			return "@" + orange.Handle + " 新文档对比原文档有什么变化？"
		}},
		{name: "natural language name", text: func(orange w.Agent) string { return "橘猫帮我对比新文档和原文档的变化" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			api := newDeliveryAPI(t, dir, db, nil)
			var ws w.Workspace
			api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "指定成员"}, nil, 200, &ws)
			var members Page[w.Agent]
			api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
			if len(members.Items) < 3 {
				t.Fatal("expected initialized members")
			}
			orange := members.Items[slices.IndexFunc(members.Items, func(a w.Agent) bool { return a.Handle == "orange" })]
			ids := []string{}
			for _, a := range members.Items {
				ids = append(ids, a.ID)
			}
			var thread w.Thread
			api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "文档比较", AgentIDs: ids, CoordinatorID: members.Items[0].ID}, nil, 200, &thread)
			input := w.RunInput{Message: tc.text(orange)}
			if tc.name == "natural language name" {
				// Only an explicit @mention or a Composer selection dispatches;
				// a prose name keeps the coordinator and the human can reassign.
				input.Targets = []string{orange.ID}
			}
			headers := map[string]string{"Idempotency-Key": "explicit-recipient-0001"}
			var run w.Run
			raw := api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, headers, 202, &run)
			validateResponse(t, "/api/v1/threads/{id}/runs", "post", 202, raw)
			want := orange.ID
			if tc.name == "natural language name" && len(input.Targets) > 0 {
				want = input.Targets[0]
			}
			if !slices.Equal(run.Input.Targets, []string{want}) {
				t.Fatalf("persisted recipients=%+v want %s", run.Input.Targets, want)
			}
			var replay w.Run
			api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, headers, 202, &replay)
			if replay.ID != run.ID {
				t.Fatal("submission retry dispatched twice")
			}
			var view RunView
			api.call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
			if len(view.Steps) != 1 || view.Steps[0].AgentID != want || view.Steps[0].Role != "responder" {
				t.Fatalf("steps=%+v", view.Steps)
			}
		})
	}
}

// TestSubmissionDoesNotDispatchNegatedMembers keeps the deterministic negation
// guard on the real submit path.
func TestSubmissionDoesNotDispatchNegatedMembers(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := newDeliveryAPI(t, dir, db, nil)
	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "否定默认成员"}, nil, 200, &ws)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	ids := []string{}
	for _, member := range members.Items {
		ids = append(ids, member.ID)
	}
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "保留否定指令", AgentIDs: ids, CoordinatorID: ids[0]}, nil, 200, &thread)
	input := w.RunInput{Message: "不要让@" + members.Items[1].Handle + "参与这个问题"}
	var failure struct {
		Error struct{ Code, Message string }
	}
	api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "negated-member-0001"}, 409, &failure)
	if failure.Error.Code != "routing_clarification" {
		t.Fatalf("negated mention was dispatched: %+v", failure)
	}
}
