package workflow

import (
	"slices"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func routingAgents() []w.Agent {
	return []w.Agent{
		{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Aliases: []string{"布布"}},
		{ID: "b", DisplayName: "缅因猫", Handle: "maine"},
		{ID: "c", DisplayName: "橘猫", Handle: "orange"},
	}
}

// TestExplicitMentionsAddressExactlyThoseMembers is the A05 regression for
// deterministic dispatch: a named @member is the recipient, and several names
// fan out to several independent members without any model call.
func TestExplicitMentionsAddressExactlyThoseMembers(t *testing.T) {
	agents := routingAgents()
	thread := w.Thread{AgentIDs: []string{"a", "b", "c"}, CoordinatorID: "c"}
	cases := []struct {
		message string
		targets []string
	}{
		{"@ragdoll 看一下这个报错", []string{"a"}},
		{"@maine @orange 各自评估一下", []string{"b", "c"}},
		{"@布布 请确认", []string{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.message, func(t *testing.T) {
			route, err := ResolveRouting(w.RunInput{Message: tc.message}, thread, agents)
			if err != nil {
				t.Fatal(err)
			}
			if route.Decision != "assign" || !slices.Equal(route.Targets, tc.targets) {
				t.Fatalf("route = %+v, want assign %v", route, tc.targets)
			}
		})
	}
}

// TestUnaddressedMessageGoesToTheCoordinator proves the single kernel has no
// semantic pre-routing round trip: no mention means the coordinator, instantly.
func TestUnaddressedMessageGoesToTheCoordinator(t *testing.T) {
	agents := routingAgents()
	thread := w.Thread{AgentIDs: []string{"a", "b", "c"}, CoordinatorID: "c"}
	route, err := ResolveRouting(w.RunInput{Message: "帮我看看这段代码为什么慢"}, thread, agents)
	if err != nil {
		t.Fatal(err)
	}
	if route.Decision != "discuss" || !slices.Equal(route.Targets, []string{"c"}) {
		t.Fatalf("route = %+v", route)
	}
	// A member merely named in prose is not an assignment target.
	named, err := ResolveRouting(w.RunInput{Message: "缅因猫上次说的不对"}, thread, agents)
	if err != nil {
		t.Fatal(err)
	}
	if named.Decision != "discuss" || !slices.Equal(named.Targets, []string{"c"}) {
		t.Fatalf("a prose mention dispatched work: %+v", named)
	}
}

// TestSelectedRecipientsAreAuthoritative covers the Composer selection path.
func TestSelectedRecipientsAreAuthoritative(t *testing.T) {
	agents := routingAgents()
	thread := w.Thread{AgentIDs: []string{"a", "b", "c"}, CoordinatorID: "c"}
	route, err := ResolveRouting(w.RunInput{Message: "跑一下", Targets: []string{"b"}}, thread, agents)
	if err != nil {
		t.Fatal(err)
	}
	if route.Decision != "assign" || !slices.Equal(route.Targets, []string{"b"}) {
		t.Fatalf("route = %+v", route)
	}
	// Explicit mentions that contradict the selected members are rejected
	// instead of silently dispatching to an unintended member.
	if _, err := ResolveRouting(w.RunInput{Message: "@maine 跑一下", Targets: []string{"a"}}, thread, agents); err == nil {
		t.Fatal("conflicting mention and selection accepted")
	}
}

// TestRoutingRejectsUnusableRequests keeps the boundary checks deterministic.
func TestRoutingRejectsUnusableRequests(t *testing.T) {
	agents := routingAgents()
	for name, tc := range map[string]struct {
		in     w.RunInput
		thread w.Thread
	}{
		"unknown mention":     {w.RunInput{Message: "@nobody 看一下"}, w.Thread{CoordinatorID: "c"}},
		"unknown selection":   {w.RunInput{Message: "x", Targets: []string{"missing"}}, w.Thread{CoordinatorID: "c"}},
		"duplicate selection": {w.RunInput{Message: "x", Targets: []string{"a", "a"}}, w.Thread{CoordinatorID: "c"}},
		"no coordinator":      {w.RunInput{Message: "x"}, w.Thread{}},
		"too many":            {w.RunInput{Message: "x", Targets: []string{"a", "b", "c", "a"}}, w.Thread{CoordinatorID: "c"}},
	} {
		t.Run(name, func(t *testing.T) {
			route, err := ResolveRouting(tc.in, tc.thread, agents)
			if err == nil && route.Decision != "clarify" {
				t.Fatalf("unusable request was accepted: %+v", route)
			}
		})
	}
	// A negated named member needs clarification rather than a silent dispatch.
	route, err := ResolveRouting(w.RunInput{Message: "不要让 @maine 做这个"}, w.Thread{CoordinatorID: "c"}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if route.Decision != "clarify" {
		t.Fatalf("a negated mention must clarify: %+v", route)
	}
}

// TestQuotedMentionsDoNotDispatch keeps fenced/quote text from creating work.
func TestQuotedMentionsDoNotDispatch(t *testing.T) {
	agents := routingAgents()
	thread := w.Thread{AgentIDs: []string{"a", "b", "c"}, CoordinatorID: "c"}
	message := "这个报错日志说 `@maine is unavailable`，请帮我排查。"
	route, err := ResolveRouting(w.RunInput{Message: message}, thread, agents)
	if err != nil {
		t.Fatal(err)
	}
	if route.Decision != "discuss" || !slices.Equal(route.Targets, []string{"c"}) {
		t.Fatalf("quoted mention dispatched work: %+v", route)
	}
}
