package workspace

import "testing"

func TestAgentIdentityAndNames(t *testing.T) {
	a := Agent{ID: NewID(), DisplayName: "布偶猫", Handle: "ragdoll", Aliases: []string{"布布"}, Model: "demo", Tools: []string{"read_file"}}
	for _, tc := range []struct {
		name string
		edit func(*Agent)
		bad  bool
	}{
		{"valid", func(*Agent) {}, false},
		{"invalid handle", func(a *Agent) { a.Handle = "--bad" }, true},
		{"duplicate alias", func(a *Agent) { a.Aliases = []string{"布布", "布布"} }, true},
		{"unknown model", func(a *Agent) { a.Model = "unknown" }, true},
		{"unknown tool", func(a *Agent) { a.Tools = []string{"shell"} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := a
			tc.edit(&copy)
			if err := ValidateAgent(copy, nil, []string{"demo"}, []string{"read_file"}); (err != nil) != tc.bad {
				t.Fatalf("error=%v", err)
			}
		})
	}
	peer := a
	peer.ID = NewID()
	if err := ValidateAgent(a, []Agent{peer}, []string{"demo"}, []string{"read_file"}); err == nil {
		t.Fatal("collision accepted")
	}
	a.DisplayName = "橘猫"
	if len(a.ID) != 36 {
		t.Fatal("identity changed")
	}
}

func TestTerminal(t *testing.T) {
	for _, s := range []string{"succeeded", "failed", "canceled", "interrupted"} {
		if !Terminal(s) {
			t.Fatal(s)
		}
	}
	if Terminal("canceling") {
		t.Fatal("unconfirmed cancellation is terminal")
	}
}
