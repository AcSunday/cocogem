package workflow

import (
	"context"
	w "github.com/AcSunday/cocogem/internal/workspace"
	"testing"
)

func TestNamesAndIntentRouting(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "布偶猫", Handle: "ragdoll", Aliases: []string{"布布"}}, {ID: "b", DisplayName: "缅因猫", Handle: "maine"}}
	thread := w.Thread{CoordinatorID: "b"}
	for _, tc := range []struct{ name, msg, decision, target string }{
		{"assignment", "布布帮我研究一下", "assign", "a"},
		{"discussion", "我觉得布偶猫做研究挺好", "discuss", "b"},
		{"conditional", "如果让布偶猫研究，会有什么问题", "discuss", "b"},
		{"negation", "不要让布偶猫处理这个", "discuss", "b"},
		{"quote", "```布偶猫帮我研究```", "discuss", "b"},
		{"quoted mention", "引用：“ @ragdoll 帮我研究”", "discuss", "b"},
		{"negated mention", "不要让 @ragdoll 做这个", "clarify", ""},
		{"explicit", "@ragdoll 研究", "assign", "a"},
		{"unknown", "@missing 做一下", "clarify", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ResolveRouting(context.Background(), nil, true, w.RunInput{Message: tc.msg}, thread, agents)
			if err != nil || r.Decision != tc.decision {
				t.Fatal(r, err)
			}
			if tc.target != "" && (len(r.Targets) != 1 || r.Targets[0] != tc.target) {
				t.Fatal(r)
			}
		})
	}
	agents[1].DisplayName = "布偶猫"
	r, _ := ResolveRouting(context.Background(), nil, true, w.RunInput{Message: "布偶猫帮我研究"}, thread, agents)
	if r.Decision != "clarify" {
		t.Fatal("ambiguity guessed", r)
	}
}
