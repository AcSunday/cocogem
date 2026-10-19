package workflow

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestRecentHistoryPrefersWholeLatestRounds(t *testing.T) {
	round := func(id, text string) []w.Message {
		return []w.Message{{ID: id + "-user", RunID: id, Role: "user", Content: text, Status: "completed"}, {ID: id + "-reply", RunID: id, Role: "assistant", Content: "answer", Status: "completed"}}
	}
	history := append(round("old", strings.Repeat("a", 16000)), round("new", "latest correction")...)
	history = append(history, round("current", "current question")...)
	for _, tc := range []struct {
		name      string
		count     int
		byteLimit int
		want      []string
	}{
		{"byte budget", 12, 16000, []string{"new-user", "new-reply"}},
		{"message budget", 3, 50000, []string{"new-user", "new-reply"}},
		{"no partial round", 1, 50000, []string{}},
		{"latest round too large", 12, 5, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := recentHistory(history, "current", tc.count, tc.byteLimit)
			ids := []string{}
			for _, message := range got {
				ids = append(ids, message.ID)
			}
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("history IDs=%v, want %v", ids, tc.want)
			}
		})
	}
	failed := round("failed", "failed question")
	failed[1].Status = "failed"
	history = append(round("good", "retained question"), failed...)
	if got := recentHistory(history, "current", 12, 16000); len(got) != 2 || got[0].RunID != "good" {
		t.Fatalf("included unfinished round: %+v", got)
	}
}

func TestHandoffRetainsExactMessagesArtifactsAndSources(t *testing.T) {
	step := w.Step{ID: "review", RunID: "run", AgentID: "critic", Instruction: "Challenge the findings", DependsOn: []string{"research"}}
	messages := []w.Message{
		{ID: "upstream-message", RunID: "run", StepID: "research", Status: "completed", Content: "See the structured report"},
		{ID: "incomplete", RunID: "run", StepID: "research", Status: "processing"},
		{ID: "sibling", RunID: "run", StepID: "independent", Status: "completed"},
		{ID: "other-run", RunID: "another", StepID: "research", Status: "completed"},
	}
	artifacts := []w.Artifact{
		{ID: "report", Version: 2, RunID: "run", StepID: "research", Body: "Exact captured body", Findings: []w.Finding{{Title: "Disputed claim", Status: "disputed", SourceIDs: []string{"source"}}}, Limitations: []string{"Limited evidence"}, SourceIDs: []string{"source"}},
		{ID: "other-report", Version: 1, RunID: "another", StepID: "research"},
	}
	handoff := prepareHandoff(step, "Compare alternatives", messages, artifacts, []w.Source{{ID: "source"}, {ID: "source"}})
	encoded, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	var got Handoff
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.ToAgentID != "critic" || got.OriginalTask != "Compare alternatives" || !slices.Equal(got.FromSteps, []string{"research"}) || len(got.Messages) != 1 || got.Messages[0].ID != "upstream-message" || len(got.Artifacts) != 1 || got.Artifacts[0].ID != "report" || got.Artifacts[0].Version != 2 {
		t.Fatalf("lost exact handoff identity: %+v", got)
	}
	if got.Artifacts[0].Body != "Exact captured body" || got.Artifacts[0].Findings[0].Status != "disputed" || got.Artifacts[0].Limitations[0] != "Limited evidence" || !slices.Equal(got.SourceIDs, []string{"source"}) {
		t.Fatalf("lost structured evidence: %+v", got)
	}
}
