package workflow

import (
	"slices"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// recentHistory selects whole completed rounds, preferring the newest within both budgets.
func recentHistory(messages []w.Message, excludeRunID string, maxMessages, maxBytes int) []w.Message {
	selected := []w.Message{}
	bytes := 0
	for end := len(messages); end > 0; {
		start := end - 1
		for start > 0 && messages[start-1].RunID == messages[end-1].RunID {
			start--
		}
		round := messages[start:end]
		end = start
		if round[0].RunID == excludeRunID {
			continue
		}
		complete, hasReply, roundBytes := true, false, 0
		for _, message := range round {
			complete = complete && message.Status == "completed"
			hasReply = hasReply || message.Role == "assistant"
			roundBytes += len(message.Content)
		}
		if !complete || !hasReply {
			continue
		}
		if len(selected)+len(round) > maxMessages || bytes+roundBytes > maxBytes {
			break
		}
		bytes += roundBytes
		selected = append(slices.Clone(round), selected...)
	}
	return selected
}

// Handoff records exact committed inputs shared with a dependent step.
type Handoff struct {
	FromSteps    []string     `json:"from_steps"`
	ToStep       string       `json:"to_step"`
	ToAgentID    string       `json:"to_agent_id"`
	Instruction  string       `json:"instruction"`
	OriginalTask string       `json:"original_task"`
	Messages     []w.Message  `json:"messages"`
	Artifacts    []w.Artifact `json:"artifacts"`
	SourceIDs    []string     `json:"source_ids"`
}

func prepareHandoff(step w.Step, task string, messages []w.Message, artifacts []w.Artifact, sources []w.Source) Handoff {
	h := Handoff{FromSteps: slices.Clone(step.DependsOn), ToStep: step.ID, ToAgentID: step.AgentID, Instruction: step.Instruction, OriginalTask: task, Messages: []w.Message{}, Artifacts: []w.Artifact{}, SourceIDs: []string{}}
	for _, message := range messages {
		if message.RunID == step.RunID && message.Status == "completed" && slices.Contains(step.DependsOn, message.StepID) {
			h.Messages = append(h.Messages, message)
		}
	}
	for _, artifact := range artifacts {
		if artifact.RunID == step.RunID && slices.Contains(step.DependsOn, artifact.StepID) {
			h.Artifacts = append(h.Artifacts, artifact)
		}
	}
	for _, source := range sources {
		if !slices.Contains(h.SourceIDs, source.ID) {
			h.SourceIDs = append(h.SourceIDs, source.ID)
		}
	}
	return h
}
