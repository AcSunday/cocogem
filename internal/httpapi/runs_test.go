package httpapi

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestRoutingPreviewAndSubmissionPrioritizeUserRecipients(t *testing.T) {
	for _, tc := range []struct {
		name, workflow string
		solo, named    bool
	}{
		{name: "research", workflow: "research"},
		{name: "research with no available default roles", workflow: "research", solo: true},
		{name: "natural language assignment", workflow: "research", solo: true, named: true},
		{name: "discussion", workflow: "discussion"},
		{name: "engineering", workflow: "engineering"},
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
			ids := []string{orange.ID}
			if !tc.solo {
				for _, a := range members.Items {
					if a.ID != orange.ID {
						ids = append(ids, a.ID)
					}
				}
			}
			var thread w.Thread
			api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "文档比较", AgentIDs: ids, CoordinatorID: orange.ID, Workflow: tc.workflow}, nil, 200, &thread)
			text := "@orange 新文档对比原文档有什么变化？"
			if tc.named {
				text = "橘猫帮我对比新文档和原文档的变化"
			}
			input := w.RunInput{Message: text, Workflow: tc.workflow}
			var preview RoutingPreview
			raw := api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
			validateResponse(t, "/api/v1/threads/{id}/routing-preview", "post", 200, raw)
			if preview.ProposalID == "" || preview.Routing.Decision != "assign" || preview.Routing.Workflow != "free" || !slices.Equal(preview.Routing.Targets, []string{orange.ID}) || len(preview.Routing.Assignments) != 0 {
				t.Fatalf("preview=%+v", preview)
			}
			input.ProposalID = preview.ProposalID
			headers := map[string]string{"Idempotency-Key": "explicit-recipient-0001"}
			var run w.Run
			raw = api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, headers, 202, &run)
			validateResponse(t, "/api/v1/threads/{id}/runs", "post", 202, raw)
			if run.Input.Workflow != "free" || len(run.Input.Assignments) != 0 || !slices.Equal(run.Input.Targets, []string{orange.ID}) || run.WriteBranch {
				t.Fatalf("persisted input=%+v write_branch=%v", run.Input, run.WriteBranch)
			}
			var replay w.Run
			api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, headers, 202, &replay)
			if replay.ID != run.ID {
				t.Fatal("submission retry dispatched twice")
			}
			var view RunView
			api.call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
			if len(view.Steps) != 1 || view.Steps[0].AgentID != orange.ID || view.Steps[0].Role != "responder" {
				t.Fatalf("steps=%+v", view.Steps)
			}
			var unchanged w.Thread
			if err := db.Read(t.Context(), func(tx *store.Tx) error {
				var err error
				unchanged, err = tx.Thread(thread.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if unchanged.Workflow != tc.workflow {
				t.Fatalf("one-off routing rewrote the conversation default: %+v", unchanged)
			}
		})
	}
}

func TestRoutingDoesNotDispatchNegatedDefaultMembers(t *testing.T) {
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
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "保留否定指令", AgentIDs: ids, CoordinatorID: ids[0], Workflow: "research"}, nil, 200, &thread)
	input := w.RunInput{Message: "不要让" + members.Items[0].DisplayName + "参与这个问题", Workflow: "research"}
	var preview RoutingPreview
	api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
	if preview.ProposalID != "" || preview.Routing.Decision != "clarify" || len(preview.Routing.Targets)+len(preview.Routing.Assignments) != 0 {
		t.Fatalf("dispatched a negated default member: %+v", preview)
	}
}
