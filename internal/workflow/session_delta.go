package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// sessionDeltaPayload is the exact incremental input of one resumed turn. It
// never repeats the static system prefix and never re-sends history the native
// session already acknowledged, so a long conversation stays linear.
type sessionDeltaPayload struct {
	Protocol string `json:"protocol"`
	// ContextEpoch advances on every resumed turn of one session.
	ContextEpoch int `json:"context_epoch"`
	// Step is the instruction of the step about to run.
	Step string `json:"current_step_instruction"`
	// UserInput is the newest user message, if this turn has one.
	UserInput string `json:"current_user_input,omitempty"`
	// NewMessages are committed messages the native session has not seen.
	NewMessages []deltaMessage `json:"new_messages,omitempty"`
	// SupersededConstraints are earlier constraint refs that no longer govern.
	SupersededConstraints []string `json:"superseded_constraint_refs,omitempty"`
	// NewOrChangedWork are context-plan items added or changed since the last
	// acknowledged turn.
	NewOrChangedWork []deltaWork `json:"new_or_changed_work_items,omitempty"`
	// Candidate is the current working tree summary for continuity.
	Candidate string `json:"candidate_summary,omitempty"`
}

type deltaMessage struct {
	ID      string `json:"message_id"`
	AgentID string `json:"agent_id,omitempty"`
	StepID  string `json:"step_id,omitempty"`
	Status  string `json:"status"`
	Content string `json:"content"`
}

type deltaWork struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"`
	Digest  string `json:"digest"`
	Content string `json:"content,omitempty"`
}

// sessionDeltaRequest builds the pure-delta input for one resumed native session.
//
// It works for a plain conversation and for a durable task: the scope is the
// task when present and the conversation otherwise, and the acknowledged-ref
// ledger is what decides which messages are new.
func (e *Engine) sessionDeltaRequest(ctx context.Context, run w.Run, step w.Step, attempt w.Attempt) ([]model.Message, int, error) {
	var payload sessionDeltaPayload
	payload.Protocol = "session_delta/v1"
	epoch := 0
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		session, err := tx.Session(attempt.ExecutionSessionRef)
		if err != nil {
			return err
		}
		epoch = session.ContextEpoch
		payload.ContextEpoch = epoch
		payload.Step = step.Instruction
		payload.UserInput = run.Input.Message
		seen := map[string]bool{}
		for _, ref := range session.AcknowledgedExposedRefs {
			seen[ref] = true
		}
		if err := collectDeltaMessages(tx, run, step, seen, &payload); err != nil {
			return err
		}
		// Only mark a previously acknowledged constraint ref as superseded when
		// the current turn carries a DIFFERENT digest for the same constraint key.
		currentByPrefix := map[string]string{}
		for _, ref := range session.ExposedRefs {
			if prefix, digest, ok := constraintRefParts(ref); ok {
				currentByPrefix[prefix] = digest
			}
		}
		for _, ref := range session.AcknowledgedExposedRefs {
			prefix, oldDigest, ok := constraintRefParts(ref)
			if !ok {
				continue
			}
			newDigest, stillPresent := currentByPrefix[prefix]
			if (!stillPresent || newDigest != oldDigest) && len(payload.SupersededConstraints) < 100 {
				payload.SupersededConstraints = append(payload.SupersededConstraints, ref)
			}
		}
		if err := collectDeltaWork(tx, run, seen, &payload); err != nil {
			return err
		}
		payload.Candidate = candidateSummary(run)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if payload.UserInput == "" && len(payload.NewMessages) == 0 && len(payload.NewOrChangedWork) == 0 {
		// Nothing new reached this member: keep the delta minimal but explicit so
		// the model knows the boundary is real, not a truncation.
		payload.NewMessages = nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	text := "Apply this managed session delta at the current execution boundary. Your earlier native history is context, not new input: do not restate it. Superseded constraints no longer govern. A runner acknowledgement confirms receipt only, not model compliance.\n" + string(encoded)
	return []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: text}}}}, epoch, nil
}

// collectDeltaMessages adds only committed messages this native session has not
// acknowledged. Its own prior replies, canceled attempts and error placeholders
// are not re-sent.
func collectDeltaMessages(tx *store.Tx, run w.Run, step w.Step, seen map[string]bool, payload *sessionDeltaPayload) error {
	all, err := tx.Messages(run.ThreadID)
	if err != nil {
		return err
	}
	budget := 24000
	for _, message := range all {
		if message.Status != "completed" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if message.StepID == step.ID || message.RunID == run.ID && message.Role == "user" {
			// The current step's own output and this turn's user message are sent
			// explicitly below, never as history.
			continue
		}
		key := "message:" + message.ID
		if seen[key] {
			continue
		}
		content := shortTaskResult(message.Content)
		if len(content) > budget {
			continue
		}
		budget -= len(content)
		payload.NewMessages = append(payload.NewMessages, deltaMessage{ID: message.ID, AgentID: message.AgentID, StepID: message.StepID, Status: message.Status, Content: content})
	}
	return nil
}

// collectDeltaWork adds context-plan items not yet acknowledged for this scope.
func collectDeltaWork(tx *store.Tx, run w.Run, seen map[string]bool, payload *sessionDeltaPayload) error {
	if run.ContextRef == "" {
		return nil
	}
	manifest, err := tx.ContextManifest(run.ContextRef)
	if err != nil {
		return err
	}
	if run.TaskID != "" {
		task, err := tx.Task(run.TaskID)
		if err != nil {
			return err
		}
		if manifest.TaskID != task.ID || task.CurrentRevision != run.TaskRevision {
			return w.ErrConflict
		}
	}
	appended, err := tx.ContextScopeEntries(run.ContextRef)
	if err != nil {
		return err
	}
	task := w.Task{}
	if run.TaskID != "" {
		if task, err = tx.Task(run.TaskID); err != nil {
			return err
		}
	}
	// Both the manifest's own items and the appended scope entries can be new
	// or changed; the acknowledged-ref ledger decides which are truly new.
	items := append(append([]w.ContextItem{}, manifest.Items...), appended...)
	for _, item := range items {
		key := item.Ref + ":" + item.Digest
		if seen[key] {
			continue
		}
		entry := deltaWork{Ref: item.Ref, Kind: item.Kind, Digest: item.Digest}
		if run.TaskID != "" {
			if content, err := project.ResolveContextItem(tx, manifest, task, item); err == nil {
				entry.Content = shortTaskResult(content)
			}
		}
		payload.NewOrChangedWork = append(payload.NewOrChangedWork, entry)
	}
	return nil
}

// candidateSummary names the working tree a resumed member must keep building
// on, so a fresh native session can continue instead of restarting.
func candidateSummary(run w.Run) string {
	root := run.ProjectRoot()
	if root == "" {
		return ""
	}
	return root
}

func constraintRefParts(ref string) (prefix, digest string, ok bool) {
	base, rest, found := strings.Cut(ref, ":")
	if !found {
		return "", "", false
	}
	switch base {
	case "task", "work", "project", "memory":
		lastColon := strings.LastIndex(rest, ":")
		if lastColon <= 0 {
			return base + ":" + rest, "", true
		}
		return base + ":" + rest[:lastColon], rest[lastColon+1:], true
	}
	return "", "", false
}

// ErrSessionSealed means the bound native session can no longer be resumed; the
// caller must start a fresh session and inject a continuity capsule.
var ErrSessionSealed = errors.New("workflow: the bound native session is sealed or retired")

var _ = store.ErrNotFound
