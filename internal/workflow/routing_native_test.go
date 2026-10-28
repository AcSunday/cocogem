package workflow

import (
	"context"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type recordingModel struct {
	request model.Request
}

func (m *recordingModel) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	m.request = request
	return testutil.Model{}.Complete(ctx, request, emit)
}

func TestRoutingRequestNeverCarriesNativeExecutionFields(t *testing.T) {
	recorder := &recordingModel{}
	agents := []w.Agent{{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Tools: []string{"read_file", "fetch_url"}}}
	thread := w.Thread{AgentIDs: []string{"a"}, CoordinatorID: "a"}
	if _, err := ResolveRouting(t.Context(), recorder, w.RunInput{Message: "布偶猫查资料"}, thread, agents); err != nil {
		t.Fatal(err)
	}
	if len(recorder.request.NativeTools) != 0 || recorder.request.WorkspaceRoot != "" || recorder.request.NativeProxy != "" {
		t.Fatalf("routing request leaked native execution fields: %+v", recorder.request)
	}
	for _, tool := range recorder.request.Tools {
		if tool.Name != "routing_decision" {
			t.Fatalf("routing exposed a task tool: %s", tool.Name)
		}
	}
}
