package workflow

import (
	"encoding/json"
	"fmt"
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
		// A newest round that does not fit by count keeps its final reply.
		{"latest round over count", 1, 50000, []string{"new-reply"}},
		// A newest round that does not fit by bytes keeps real IDs in bounded previews.
		{"latest round too large", 12, 5, []string{"new-user", "new-reply"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := recentHistory(history, "current", tc.count, tc.byteLimit)
			ids := []string{}
			bytes := 0
			for _, message := range got {
				ids = append(ids, message.ID)
				bytes += len(message.Content)
			}
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("history IDs=%v, want %v", ids, tc.want)
			}
			if bytes > tc.byteLimit {
				t.Fatalf("history preview used %d bytes, budget %d", bytes, tc.byteLimit)
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

func TestLatestOverBudgetHistoryRoundKeepsFinalReplyAndRealIDs(t *testing.T) {
	history := []w.Message{{ID: "latest-user", RunID: "previous-research", Role: "user", Status: "completed", Content: "研究问题"}}
	for _, id := range []string{"research", "critique", "response", "recheck", "final"} {
		history = append(history, w.Message{ID: id, RunID: "previous-research", Role: "assistant", Status: "completed", Content: strings.Repeat("e", 4000)})
	}
	selected := recentHistory(history, "follow-up", 12, 16000)
	if len(selected) != len(history) {
		t.Fatalf("over-budget latest round was erased: selected=%d want=%d", len(selected), len(history))
	}
	bytes := 0
	for i, message := range selected {
		bytes += len(message.Content)
		if message.ID != history[i].ID || message.RunID != "previous-research" || message.Status != "completed" || message.Role != history[i].Role {
			t.Fatalf("bounded preview lost identity at %d: %+v", i, message)
		}
	}
	if bytes > 16000 {
		t.Fatalf("bounded preview used %d bytes, budget 16000", bytes)
	}
	if !selected[1].ContentTruncated || !selected[len(selected)-1].ContentTruncated {
		t.Fatal("cut previews are not marked truncated")
	}
	if selected[len(selected)-1].ID != "final" {
		t.Fatalf("final reply ID was lost: %+v", selected)
	}
	if selected[0].ContentBytes != len(history[0].Content) || selected[1].ContentBytes != 4000 {
		t.Fatalf("full content size was lost: first=%d second=%d", selected[0].ContentBytes, selected[1].ContentBytes)
	}
}

func TestLatestHistoryRoundCountCutKeepsOriginalTaskAndFinalReply(t *testing.T) {
	history := []w.Message{{ID: "task", RunID: "discussion", Role: "user", Status: "completed", Content: "original task"}}
	for i := 0; i < 14; i++ {
		history = append(history, w.Message{ID: fmt.Sprintf("reply-%02d", i), RunID: "discussion", Role: "assistant", Status: "completed", Content: "reply"})
	}
	selected := recentHistory(history, "follow-up", 12, 16000)
	if len(selected) != 12 {
		t.Fatalf("bounded round count = %d, want 12", len(selected))
	}
	if selected[0].ID != "task" || selected[len(selected)-1].ID != "reply-13" {
		t.Fatalf("original task or final reply was lost: first=%s last=%s", selected[0].ID, selected[len(selected)-1].ID)
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
