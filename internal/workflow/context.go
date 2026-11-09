package workflow

import (
	"errors"
	"fmt"
	"slices"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Shared-history budgets are measured in estimated tokens over stored previews.
// The defaults cover several full discussion rounds on modern CLI context
// windows; a member with an explicit context_size scales the window, while the
// server environment can raise the fallback for models with very large context.
const (
	defaultHistoryMessages = 128
	defaultHistoryTokens   = 64000
	// historyMessages and historyTokens remain the test-facing defaults.
	historyMessages = defaultHistoryMessages
	historyTokens   = defaultHistoryTokens
)

// HistoryLimits bounds how much committed conversation history is inlined
// into one attempt. Tokens is a conservative estimate, not a provider-exact
// tokenizer count.
type HistoryLimits struct {
	Messages int
	Tokens   int
}

// DefaultHistoryLimits is used when the server does not override the fallback
// and the member has no explicit context_size.
func DefaultHistoryLimits() HistoryLimits {
	return HistoryLimits{Messages: defaultHistoryMessages, Tokens: defaultHistoryTokens}
}

func (h HistoryLimits) normalized() HistoryLimits {
	if h.Messages <= 0 {
		h.Messages = defaultHistoryMessages
	}
	if h.Tokens <= 0 {
		h.Tokens = defaultHistoryTokens
	}
	return h
}

// previewBytes chooses a per-message byte ceiling from the token budget so the
// loader can retain enough text to estimate and trim by tokens. Four bytes per
// token leaves room for ASCII and CJK UTF-8; long bodies remain pageable.
func (h HistoryLimits) previewBytes() int {
	previewTokens := h.normalized().Tokens / 16
	if previewTokens < 2000 {
		previewTokens = 2000
	}
	if previewTokens > 32000 {
		previewTokens = 32000
	}
	previewBytes := previewTokens * 4
	if previewBytes < 6000 {
		previewBytes = 6000
	}
	if previewBytes > 131072 {
		previewBytes = 131072
	}
	return previewBytes
}

// historyLimitsFor applies an explicit member input token budget. Three fifths
// is reserved for conversation history; the rest stays available for the
// current task, role instructions, upstream handoff, source pointers and tool
// schemas. A member without context_size follows the server fallback.
func (e *Engine) historyLimitsFor(member w.Agent) HistoryLimits {
	limits := e.History.normalized()
	if member.ContextSize > 0 {
		limits.Tokens = member.ContextSize * 3 / 5
		if limits.Tokens < 1 {
			limits.Tokens = 1
		}
	}
	return limits
}

// historyPreviewBytes loads enough of each message for the largest explicit
// member window in this run. Selection later applies each member's own limits.
func (e *Engine) historyPreviewBytes(run w.Run) int {
	preview := e.History.normalized().previewBytes()
	for _, member := range run.Agents {
		if member.ContextSize <= 0 {
			continue
		}
		if candidate := e.historyLimitsFor(member).previewBytes(); candidate > preview {
			preview = candidate
		}
	}
	return preview
}

func messageTokens(message w.Message) int {
	return model.EstimateTokens(message.Content)
}

// trimToTokens keeps the largest UTF-8 prefix whose conservative estimate fits
// the token budget. It is used only for the over-budget preview path.
func trimToTokens(value string, maxTokens int) string {
	if value == "" || maxTokens <= 0 {
		return ""
	}
	if model.EstimateTokens(value) <= maxTokens {
		return value
	}
	best := ""
	low, high := 0, len(value)
	for low <= high {
		middle := (low + high) / 2
		candidate := trimBytes(value, middle)
		if candidate == "" {
			low = middle + 1
			continue
		}
		if model.EstimateTokens(candidate) <= maxTokens {
			best = candidate
			low = middle + 1
			continue
		}
		high = middle - 1
	}
	return best
}

// recentHistory selects whole completed rounds, preferring the newest within
// both budgets. The newest previous round may be incomplete because its run
// failed or was interrupted: its committed messages still carry useful context,
// while failed and in-flight bodies never enter the window. Older incomplete
// rounds are skipped so repeated failures cannot crowd out a successful round.
func recentHistory(messages []w.Message, excludeRunID string, maxMessages, maxTokens int) []w.Message {
	selected := []w.Message{}
	tokens := 0
	latest := true
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
		complete, hasReply := true, false
		committed := make([]w.Message, 0, len(round))
		roundTokens := 0
		for _, message := range round {
			if message.Status != "completed" {
				complete = false
				continue
			}
			committed = append(committed, message)
			hasReply = hasReply || message.Role == "assistant"
			roundTokens += messageTokens(message)
		}
		if !hasReply || (!complete && !latest) {
			continue
		}
		chosen := round
		if !complete {
			chosen = committed
		}
		if len(selected)+len(chosen) > maxMessages || tokens+roundTokens > maxTokens {
			if latest {
				selected = append(boundedRoundPreview(chosen, maxMessages, maxTokens), selected...)
			}
			break
		}
		latest = false
		tokens += roundTokens
		selected = append(slices.Clone(chosen), selected...)
	}
	return selected
}

// boundedRoundPreview keeps the newest completed round usable under the same
// history budgets. A count cut preserves the original task and final reply; a
// token cut gives every kept message a fair share, so the last reply is never
// the only survivor of a truncated preview.
func boundedRoundPreview(round []w.Message, maxMessages, maxTokens int) []w.Message {
	if len(round) == 0 || maxMessages <= 0 || maxTokens < 0 {
		return nil
	}
	selected := slices.Clone(round)
	if len(round) > maxMessages {
		if maxMessages == 1 {
			selected = slices.Clone(round[len(round)-1:])
		} else {
			selected = append(slices.Clone(round[:1]), slices.Clone(round[len(round)-maxMessages+1:])...)
		}
	}
	remaining := maxTokens
	for i := range selected {
		original := len(selected[i].Content)
		if selected[i].ContentBytes < original {
			selected[i].ContentBytes = original
		}
		share := remaining / (len(selected) - i)
		current := messageTokens(selected[i])
		if current > share {
			selected[i].Content = trimToTokens(selected[i].Content, share)
			selected[i].ContentTruncated = true
		}
		remaining -= messageTokens(selected[i])
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
