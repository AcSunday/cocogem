package workflow

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestNamesAndIntentRouting(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Aliases: []string{"布布"}}, {ID: "b", DisplayName: "缅因猫", Handle: "maine"}}
	thread := w.Thread{CoordinatorID: "b"}
	for _, tc := range []struct {
		name, msg, decision, target string
		route                       Routing
	}{
		{"assignment", "布布帮我研究一下", "assign", "a", Routing{Decision: "assign", Workflow: "free", Targets: []string{"a"}, Evidence: []w.RoutingEvidence{{AgentID: "a", Quote: "布布帮我研究一下"}}}},
		{"discussion", "我觉得布偶猫做研究挺好", "discuss", "b", Routing{Decision: "discuss"}},
		{"conditional", "如果让布偶猫研究，会有什么问题", "discuss", "b", Routing{Decision: "discuss"}},
		{"negation", "不要让布偶猫处理这个", "discuss", "b", Routing{Decision: "discuss"}},
		{"quote", "```布偶猫帮我研究```", "discuss", "b", Routing{}},
		{"quoted mention", "引用：“ @ragdoll 帮我研究”", "discuss", "b", Routing{}},
		{"negated mention", "不要让 @ragdoll 做这个", "clarify", "", Routing{}},
		{"explicit", "@ragdoll 研究", "assign", "a", Routing{}},
		{"unknown", "@missing 做一下", "clarify", "", Routing{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ResolveRouting(context.Background(), routingModel{tc.route}, w.RunInput{Message: tc.msg}, thread, agents)
			if err != nil || r.Decision != tc.decision {
				t.Fatal(r, err)
			}
			if tc.target != "" && (len(r.Targets) != 1 || r.Targets[0] != tc.target) {
				t.Fatal(r)
			}
		})
	}
	agents[1].DisplayName = "布偶猫"
	r, _ := ResolveRouting(context.Background(), nil, w.RunInput{Message: "布偶猫帮我研究"}, thread, agents)
	if r.Decision != "clarify" {
		t.Fatal("ambiguity guessed", r)
	}
}

type recordingRouter struct {
	routing Routing
	request model.Request
	calls   int
}

func (m *recordingRouter) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	m.request = request
	m.calls++
	return (routingModel{m.routing}).Complete(ctx, request, emit)
}

func TestWorkflowDoesNotBypassNamedInstructions(t *testing.T) {
	agents := []w.Agent{
		{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Model: "test", Executor: "codex", Tools: []string{"publish_artifact"}},
		{ID: "b", DisplayName: "缅因猫", Handle: "maine", Model: "test", Executor: "claude", Tools: []string{"publish_artifact"}},
	}
	thread := w.Thread{CoordinatorID: "b", AgentIDs: []string{"a", "b"}}
	assignments := map[string]string{"researcher": "a", "critic": "b", "coordinator": "b"}
	for _, tc := range []struct {
		name     string
		input    w.RunInput
		route    Routing
		decision string
		calls    int
	}{
		{"named single task conflicts with research", w.RunInput{Message: "布偶猫帮我调查模型", Workflow: "research", Assignments: assignments}, Routing{Decision: "assign", Workflow: "free", Targets: []string{"a"}, Evidence: []w.RoutingEvidence{{AgentID: "a", Quote: "布偶猫帮我调查模型"}}}, "clarify", 1},
		{"ordinary mention retains selected research", w.RunInput{Message: "我觉得布偶猫的观点值得讨论", Workflow: "research", Assignments: assignments}, Routing{Decision: "discuss"}, "discuss", 1},
		{"unnamed task retains selected research", w.RunInput{Message: "比较三种方法", Workflow: "research", Assignments: assignments}, Routing{}, "assign", 0},
		{"selected recipient remains authoritative", w.RunInput{Message: "布偶猫帮我调查模型", Targets: []string{"b"}}, Routing{}, "assign", 0},
		{"same research roles are accepted", w.RunInput{Message: "布偶猫查资料，缅因猫复核并汇总", Workflow: "research", Assignments: assignments}, Routing{Decision: "assign", Workflow: "research", Assignments: assignments, Evidence: []w.RoutingEvidence{{AgentID: "a", Quote: "布偶猫查资料"}, {AgentID: "b", Quote: "缅因猫复核并汇总"}}}, "assign", 1},
		{"different research roles require clarification", w.RunInput{Message: "布偶猫复核并汇总，缅因猫查资料", Workflow: "research", Assignments: assignments}, Routing{Decision: "assign", Workflow: "research", Assignments: map[string]string{"researcher": "b", "critic": "a", "coordinator": "a"}, Evidence: []w.RoutingEvidence{{AgentID: "a", Quote: "布偶猫复核并汇总"}, {AgentID: "b", Quote: "缅因猫查资料"}}}, "clarify", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &recordingRouter{routing: tc.route}
			got, err := ResolveRouting(context.Background(), m, tc.input, thread, agents)
			if err != nil || got.Decision != tc.decision || m.calls != tc.calls {
				t.Fatalf("route=%+v calls=%d err=%v", got, m.calls, err)
			}
			if tc.calls > 0 && m.request.Executor != "codex" {
				t.Fatalf("semantic routing ignored member executor: %s", m.request.Executor)
			}
			if tc.decision == "clarify" && len(got.Targets)+len(got.Assignments) != 0 {
				t.Fatalf("clarification retained dispatch: %+v", got)
			}
			if tc.name == "ordinary mention retains selected research" && (got.Workflow != "research" || got.Assignments["critic"] != "b" || !got.ModelCall) {
				t.Fatalf("lost selected workflow or routing usage: %+v", got)
			}
			if tc.name == "selected recipient remains authoritative" && !slices.Equal(got.Targets, []string{"b"}) {
				t.Fatalf("replaced explicit recipient: %+v", got)
			}
		})
	}
}

func TestNegationNeverFallsBackToExcludedCoordinator(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Aliases: []string{"布布"}, Model: "test"}, {ID: "b", DisplayName: "缅因猫", Handle: "maine", Model: "test"}}
	for _, tc := range []struct{ name, text string }{
		{"display name", "不要让布偶猫处理这个"},
		{"alias", "别让布布参与"},
		{"handle", "do not ask ragdoll to research"},
	} {
		for _, coordinator := range []string{"a", "b"} {
			t.Run(tc.name+"/"+coordinator, func(t *testing.T) {
				got, err := ResolveRouting(context.Background(), routingModel{Routing{Decision: "discuss"}}, w.RunInput{Message: tc.text}, w.Thread{CoordinatorID: coordinator}, agents)
				if err != nil {
					t.Fatal(err)
				}
				if coordinator == "a" && (got.Decision != "clarify" || len(got.Targets) != 0) {
					t.Fatalf("dispatched negated coordinator: %+v", got)
				}
				if coordinator == "b" && (got.Decision != "discuss" || !slices.Equal(got.Targets, []string{"b"})) {
					t.Fatalf("lost permitted default coordinator: %+v", got)
				}
			})
		}
	}
}

type routingModel struct{ routing Routing }

func (m routingModel) Complete(context.Context, model.Request, func(string) error) (model.Response, error) {
	b, _ := json.Marshal(m.routing)
	return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: "route", Name: "routing_decision", Input: b}}}, nil
}

func TestSemanticRoutingMustCiteActualUserInstruction(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Model: "test"}, {ID: "b", DisplayName: "缅因猫", Handle: "maine", Model: "test"}}
	input := w.RunInput{Message: "布偶猫帮我研究模型"}
	for _, tc := range []struct {
		name  string
		route Routing
		bad   bool
	}{
		{"exact", Routing{Decision: "assign", Workflow: "free", Targets: []string{"a"}, Evidence: []w.RoutingEvidence{{AgentID: "a", Quote: "布偶猫帮我研究模型"}}}, false},
		{"invented quote", Routing{Decision: "assign", Workflow: "free", Targets: []string{"a"}, Evidence: []w.RoutingEvidence{{AgentID: "a", Quote: "布偶猫帮我删除资料"}}}, true},
		{"unnamed recipient", Routing{Decision: "assign", Workflow: "free", Targets: []string{"b"}, Evidence: []w.RoutingEvidence{{AgentID: "b", Quote: input.Message}}}, true},
		{"missing evidence", Routing{Decision: "assign", Workflow: "free", Targets: []string{"a"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveRouting(context.Background(), routingModel{tc.route}, input, w.Thread{CoordinatorID: "b"}, agents)
			if (err != nil) != tc.bad {
				t.Fatal(err)
			}
		})
	}
}

func TestNamesUseLongestMatchAndNormalizedAmbiguity(t *testing.T) {
	agents := []w.Agent{{ID: "short", DisplayName: "布偶", Handle: "short"}, {ID: "long", DisplayName: "布偶猫", Handle: "long"}}
	got, ambiguous := namedCandidates("布偶猫帮我研究", agents)
	if ambiguous || len(got) != 1 || got[0].ID != "long" {
		t.Fatal(got, ambiguous)
	}
	agents = []w.Agent{{ID: "a", DisplayName: "Alice", Handle: "one"}, {ID: "b", DisplayName: "alice", Handle: "two"}}
	_, ambiguous = namedCandidates("Alice please investigate", agents)
	if !ambiguous {
		t.Fatal("case-insensitive name collision guessed")
	}
	got, _ = namedCandidates("malice is not a recipient", agents)
	if len(got) != 0 {
		t.Fatal("substring matched a name")
	}
}

func TestDiscussionDefaultsUseOnlyThreadMembers(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll"}, {ID: "b", DisplayName: "缅因猫", Handle: "maine"}, {ID: "c", DisplayName: "橘猫", Handle: "orange"}}
	thread := w.Thread{AgentIDs: []string{"a", "b"}, CoordinatorID: "b"}
	got, err := ResolveRouting(context.Background(), nil, w.RunInput{Message: "比较两个方案", Workflow: "discussion", Assignments: map[string]string{"coordinator": "b"}}, thread, agents)
	if err != nil || !sameIDs(got.Targets, thread.AgentIDs) {
		t.Fatalf("routing=%+v error=%v", got, err)
	}
}

func TestSemanticSuggestionCannotTurnNegationOrConditionsIntoAssignment(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Model: "test"}, {ID: "b", DisplayName: "缅因猫", Handle: "maine", Model: "test"}}
	for _, text := range []string{"不要让布偶猫处理这个", "如果让布偶猫来研究，会有什么问题？"} {
		t.Run(text, func(t *testing.T) {
			route := Routing{Decision: "assign", Workflow: "free", Targets: []string{"a"}, Evidence: []w.RoutingEvidence{{AgentID: "a", Quote: text}}}
			got, err := ResolveRouting(t.Context(), routingModel{route}, w.RunInput{Message: text}, w.Thread{CoordinatorID: "b"}, agents)
			if err != nil || got.Decision != "clarify" || len(got.Targets) != 0 {
				t.Fatalf("routing=%+v error=%v", got, err)
			}
		})
	}
}

func TestRoutingToolSchemaRestrictsTargetsAndRoleValuesToCandidateIDs(t *testing.T) {
	router := &recordingRouter{routing: Routing{Decision: "discuss"}}
	_, err := ResolveRouting(t.Context(), router, w.RunInput{Message: "我觉得布偶猫做研究挺好"}, w.Thread{CoordinatorID: "a"}, []w.Agent{{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Model: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	props := router.request.Tools[0].Schema["properties"].(map[string]any)
	target := props["targets"].(map[string]any)["items"].(map[string]any)
	if !slices.Equal(target["enum"].([]string), []string{"a"}) {
		t.Fatal("targets accept invented names or IDs")
	}
	assignment := props["assignments"].(map[string]any)
	if assignment["additionalProperties"] != false {
		t.Fatal("assignments accept task instruction fields")
	}
	for _, raw := range assignment["properties"].(map[string]any) {
		if !slices.Equal(raw.(map[string]any)["enum"].([]string), []string{"a"}) {
			t.Fatal("role accepts values outside member IDs")
		}
	}
}
