package workflow

import (
	"errors"
	"fmt"
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
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
	ContextRefs  []string                     `json:"context_refs,omitempty"`
	FromSteps    []string                     `json:"from_steps"`
	ToStep       string                       `json:"to_step"`
	ToAgentID    string                       `json:"to_agent_id"`
	Instruction  string                       `json:"instruction"`
	OriginalTask string                       `json:"original_task"`
	Messages     []w.Message                  `json:"messages"`
	Artifacts    []w.Artifact                 `json:"artifacts"`
	SourceIDs    []string                     `json:"source_ids"`
	MessageRefs  []string                     `json:"message_refs,omitempty"`
	ArtifactRefs []w.ArtifactRef              `json:"artifact_refs,omitempty"`
	Digests      map[string]string            `json:"digests,omitempty"`
	Structured   map[string]structuredPreview `json:"structured_previews,omitempty"`
}

// A handoff never inlines a complete structured deliverable: findings and
// limitations are previewed, source pointers are kept while they fit, and the
// marker names the exact artifact_json retrieval for the omitted tail.
const (
	handoffArtifactBudget = 4096
	handoffFindingSources = 8
)

type structuredPreview struct {
	PreviewOmitted   bool   `json:"preview_omitted"`
	FindingsTotal    int    `json:"findings_total"`
	LimitationsTotal int    `json:"limitations_total"`
	SourceIDsTotal   int    `json:"source_ids_total"`
	ContentSHA256    string `json:"content_sha256"`
	Retrieve         string `json:"retrieve"`
}

func boundHandoffArtifact(artifact w.Artifact) (w.Artifact, *structuredPreview) {
	preview := artifact
	preview.Body = shortTaskResult(artifact.Body)
	preview.Findings, preview.Limitations, preview.SourceIDs = nil, nil, nil
	budget := handoffArtifactBudget
	truncated := false
	for _, finding := range artifact.Findings {
		item := finding
		if len(item.SourceIDs) > handoffFindingSources {
			item.SourceIDs = slices.Clone(item.SourceIDs[:handoffFindingSources])
			truncated = true
		}
		overhead := len(item.Title) + len(item.Status) + len(item.Severity) + 64
		for _, id := range item.SourceIDs {
			overhead += len(id) + 3
		}
		room := budget - overhead
		if room <= 0 {
			truncated = true
			break
		}
		cut := false
		if len(item.Detail) > room {
			item.Detail = trimBytes(item.Detail, room) + "…"
			truncated, cut = true, true
		}
		budget -= overhead + len(item.Detail)
		preview.Findings = append(preview.Findings, item)
		if cut {
			break
		}
	}
	for _, limitation := range artifact.Limitations {
		room := budget - 3
		if room <= 0 {
			truncated = true
			break
		}
		item := limitation
		cut := false
		if len(item) > room {
			item = trimBytes(item, room) + "…"
			truncated, cut = true, true
		}
		budget -= len(item) + 3
		preview.Limitations = append(preview.Limitations, item)
		if cut {
			break
		}
	}
	for _, id := range artifact.SourceIDs {
		size := len(id) + 3
		if size > budget {
			truncated = true
			break
		}
		budget -= size
		preview.SourceIDs = append(preview.SourceIDs, id)
	}
	if !truncated {
		return preview, nil
	}
	marker := structuredPreview{
		PreviewOmitted:   true,
		FindingsTotal:    len(artifact.Findings),
		LimitationsTotal: len(artifact.Limitations),
		SourceIDsTotal:   len(artifact.SourceIDs),
		ContentSHA256:    artifactDigest(artifact),
		Retrieve:         fmt.Sprintf("read_context kind=artifact id=%s version=%d field=artifact_json; follow next_cursor for the complete findings, source IDs and limitations", artifact.ID, artifact.Version),
	}
	return preview, &marker
}

func addHandoffArtifact(handoff *Handoff, artifact w.Artifact) {
	preview, marker := boundHandoffArtifact(artifact)
	handoff.Artifacts = append(handoff.Artifacts, preview)
	if marker == nil {
		return
	}
	if handoff.Structured == nil {
		handoff.Structured = map[string]structuredPreview{}
	}
	handoff.Structured[fmt.Sprintf("%s@%d", artifact.ID, artifact.Version)] = *marker
}

func prepareHandoff(step w.Step, task string, messages []w.Message, artifacts []w.Artifact, sources []w.Source) Handoff {
	h := Handoff{FromSteps: slices.Clone(step.DependsOn), ToStep: step.ID, ToAgentID: step.AgentID, Instruction: step.Instruction, OriginalTask: task, Messages: []w.Message{}, Artifacts: []w.Artifact{}, SourceIDs: []string{}}
	for _, message := range messages {
		if message.RunID == step.RunID && message.Status == "completed" && slices.Contains(step.DependsOn, message.StepID) {
			message.Content = shortTaskResult(message.Content)
			h.Messages = append(h.Messages, message)
		}
	}
	for _, artifact := range artifacts {
		if artifact.RunID == step.RunID && slices.Contains(step.DependsOn, artifact.StepID) {
			addHandoffArtifact(&h, artifact)
		}
	}
	for _, source := range sources {
		if !slices.Contains(h.SourceIDs, source.ID) {
			h.SourceIDs = append(h.SourceIDs, source.ID)
		}
	}
	return h
}

// addReferencedInputs preserves exact explicitly selected inputs beyond the recent history window.
func addReferencedInputs(tx *store.Tx, step w.Step, threadID string, handoff *Handoff) error {
	if len(step.ContextRefs) == 0 {
		return nil
	}
	messages, err := tx.MessagesMetadata(threadID)
	if err != nil {
		return err
	}
	artifacts, err := tx.ArtifactsMetadata(threadID)
	if err != nil {
		return err
	}
	sources, err := tx.SourceMetadata(threadID)
	if err != nil {
		return err
	}
	handoff.ContextRefs = slices.Clone(step.ContextRefs)
	for _, ref := range step.ContextRefs {
		found := false
		id, version := splitArtifactRef(ref)
		for _, m := range messages {
			if m.ID != ref || m.Status != "completed" {
				continue
			}
			preview, err := tx.MessagePreview(m.ID, 6000)
			if err != nil {
				return err
			}
			found = true
			if !slices.ContainsFunc(handoff.Messages, func(old w.Message) bool { return old.ID == preview.ID }) {
				handoff.Messages = append(handoff.Messages, preview)
			}
		}
		if version > 0 {
			for _, a := range artifacts {
				if a.ID != id || a.Version != version {
					continue
				}
				exact, err := tx.Artifact(a.ID, a.Version)
				if err != nil {
					return err
				}
				found = true
				if !slices.ContainsFunc(handoff.Artifacts, func(old w.Artifact) bool { return old.ID == exact.ID && old.Version == exact.Version }) {
					addHandoffArtifact(handoff, exact)
				}
			}
		}
		for _, source := range sources {
			if source.ID == ref {
				found = true
				if !slices.Contains(handoff.SourceIDs, source.ID) {
					handoff.SourceIDs = append(handoff.SourceIDs, source.ID)
				}
			}
		}
		if !found {
			return errors.New("referenced handoff input is no longer available")
		}
	}
	return nil
}
