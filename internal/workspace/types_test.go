package workspace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRunExecutionPolicyDistinguishesUnsetFromExplicitZero(t *testing.T) {
	b, err := json.Marshal(Run{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "execution_policy") {
		t.Fatalf("unset policy was serialized: %s", b)
	}
	run := Run{ExecutionPolicy: &ExecutionPolicy{StallAction: "warn"}}
	b, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var got Run
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExecutionPolicy == nil || got.ExecutionPolicy.RunTimeout != 0 || got.ExecutionPolicy.FirstResponse != 0 {
		t.Fatalf("explicit zero policy lost: %+v", got.ExecutionPolicy)
	}
	run.ExecutionPolicy.RunTimeout = 90 * time.Minute
	b, _ = json.Marshal(run)
	if !strings.Contains(string(b), `"run_timeout_ns":5400000000000`) {
		t.Fatalf("duration was not an integer nanosecond value: %s", b)
	}
}

func TestAttemptAndMessageRecoveryFieldsRoundTrip(t *testing.T) {
	attempt := Attempt{FailureKind: "context_overflow", InputPlanDigest: "plan", CheckpointDigest: "checkpoint", RecoveredFrom: "attempt-old", UsageKnown: true, InputTokens: 12, OutputTokens: 3}
	b, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	var got Attempt
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.FailureKind != attempt.FailureKind || got.InputPlanDigest != "plan" || got.CheckpointDigest != "checkpoint" || got.RecoveredFrom != "attempt-old" || !got.UsageKnown || got.InputTokens != 12 || got.OutputTokens != 3 {
		t.Fatalf("attempt recovery fields lost: %+v", got)
	}
	if (Message{}).Format() != ContentFormatInline {
		t.Fatal("legacy message format is not inline")
	}
	message := Message{ContentFormat: ContentFormatChunksV1, ContentBytes: 9000, ContentTruncated: true}
	if message.Format() != ContentFormatChunksV1 {
		t.Fatal("chunked message format lost")
	}
}

func TestAgentIdentityAndNames(t *testing.T) {
	a := Agent{ID: NewID(), DisplayName: "布偶猫", Handle: "ragdoll", Aliases: []string{"布布"}, Model: "test", Tools: []string{"read_file"}}
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
			if err := ValidateAgent(copy, nil, []string{"test"}, []string{"read_file"}); (err != nil) != tc.bad {
				t.Fatalf("error=%v", err)
			}
		})
	}
	peer := a
	peer.ID = NewID()
	if err := ValidateAgent(a, []Agent{peer}, []string{"test"}, []string{"read_file"}); err == nil {
		t.Fatal("collision accepted")
	}
	a.DisplayName = "橘猫"
	if len(a.ID) != 36 {
		t.Fatal("identity changed")
	}
}

func TestAgentAvatarIsBuiltInOnly(t *testing.T) {
	a := Agent{DisplayName: "成员", Handle: "member", Executor: "claude", Model: "default"}
	for _, value := range []string{"", "🐱", "🦊"} {
		a.Avatar = value
		if err := ValidateAgent(a, nil, nil, nil); err != nil {
			t.Fatalf("built-in avatar %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"https://example.test/a.png", "<svg/>", "data:image/png;base64,AA==", "custom"} {
		a.Avatar = value
		if err := ValidateAgent(a, nil, nil, nil); err == nil {
			t.Fatalf("non-built-in avatar accepted: %q", value)
		}
	}
}

func TestSettingsRejectUnknownRolesAndUnavailableMembers(t *testing.T) {
	agents := []Agent{{ID: "a", DisplayName: "一", Handle: "one", Model: "default"}, {ID: "b", DisplayName: "二", Handle: "two", Model: "default"}, {ID: "off", DisplayName: "停用", Handle: "off", Model: "default", Disabled: true}}
	valid := Settings{Revision: 1, DefaultCoordinatorID: "a", Assignments: map[string]map[string]string{
		"discussion":    {"coordinator": "b"},
		"research":      {"researcher": "a", "critic": "b", "coordinator": "a"},
		"code_analysis": {"analyst": "a", "reviewer": "b"},
	}}
	if err := ValidateSettings(valid, agents); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	if err := ValidateSettings(Settings{Revision: 1, Assignments: map[string]map[string]string{}}, agents); err != nil {
		t.Fatalf("empty reset rejected: %v", err)
	}
	for name, value := range map[string]Settings{
		"unknown workflow": {Assignments: map[string]map[string]string{"writing": {"coordinator": "a"}}},
		"unknown role":     {Assignments: map[string]map[string]string{"research": {"writer": "a"}}},
		"unknown member":   {DefaultCoordinatorID: "missing"},
		"disabled member":  {Assignments: map[string]map[string]string{"research": {"critic": "off"}}},
		"research duplicate": {Assignments: map[string]map[string]string{"research": {
			"researcher": "a", "critic": "a",
		}}},
		"analysis duplicate": {Assignments: map[string]map[string]string{"code_analysis": {
			"analyst": "a", "reviewer": "a",
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSettings(value, agents); err == nil {
				t.Fatal("invalid settings accepted")
			}
		})
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

func TestNativeExecutorUsesItsOwnModelSelection(t *testing.T) {
	for _, executor := range []string{"claude", "codex", "gemini"} {
		t.Run(executor, func(t *testing.T) {
			a := Agent{DisplayName: "研究员", Handle: "research", Executor: executor, Model: "default"}
			if err := ValidateAgent(a, nil, nil, nil); err != nil {
				t.Fatalf("default model: %v", err)
			}
			a.Model = "some-model-id"
			if err := ValidateAgent(a, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			a.Model = "bad\nmodel"
			if err := ValidateAgent(a, nil, nil, nil); err == nil {
				t.Fatal("multiline model accepted")
			}
		})
	}
}
