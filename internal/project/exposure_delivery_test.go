package project

import (
	"fmt"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestDerivedBodyInheritsReadMessageAndArtifactExposure(t *testing.T) {
	for _, kind := range []string{"message", "artifact", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			db, service, th := setup(t)
			sender := create(t, service, th)
			targetThread := w.Thread{ID: "read-target-" + kind, WorkspaceID: th.WorkspaceID, CoordinatorID: "owner", Revision: 1, UpdatedAt: w.Now()}
			if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(targetThread) }); err != nil {
				t.Fatal(err)
			}
			target, err := service.CreateTask(t.Context(), CreateTaskInput{
				WorkspaceID: th.WorkspaceID, ThreadID: targetThread.ID, OwnerMemberID: "owner",
				Goal: "independent recipient", SourceMessageID: "read-target-message", AcceptedBy: "user",
			})
			if err != nil {
				t.Fatal(err)
			}
			originalRun := w.Run{ID: "read-original-run-" + kind, WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: sender.Task.ID, TaskRevision: 1, Status: "succeeded", CreatedAt: w.Now()}
			originalSession := w.ExecutionSession{ID: "read-original-session-" + kind, TaskID: sender.Task.ID, MemberID: "owner", Status: "fresh_only", ExposedRefs: []string{"source_id:read-private-" + kind + ":digest"}, CreatedAt: w.Now(), UpdatedAt: w.Now()}
			originalSession.ExposureDigest = exposureRefsDigest(originalSession.ExposedRefs)
			originalAttempt := w.Attempt{ID: "read-original-attempt-" + kind, RunID: originalRun.ID, StepID: "read-original-step-" + kind, Number: 1, Status: "succeeded", ExecutionSessionRef: originalSession.ID}
			message := w.Message{ID: "read-private-message-" + kind, ThreadID: th.ID, RunID: originalRun.ID, StepID: originalAttempt.StepID, AttemptID: originalAttempt.ID, AgentID: "owner", Role: "assistant", Content: "private result", Status: "completed", CreatedAt: w.Now()}
			artifact := w.Artifact{ID: "read-private-artifact-" + kind, Version: 1, Revision: 1, ThreadID: th.ID, RunID: originalRun.ID, StepID: originalAttempt.StepID, Type: "note", Title: "apparently public", Body: "private result", Visibility: "project", SourceIDs: []string{}, Limitations: []string{}}
			currentRun := w.Run{ID: "read-current-run-" + kind, WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: sender.Task.ID, TaskRevision: 1, Status: "succeeded", CreatedAt: w.Now()}
			currentAttempt := w.Attempt{ID: "read-current-attempt-" + kind, RunID: currentRun.ID, StepID: "read-current-step-" + kind, Number: 1, Status: "succeeded", ExecutionSessionRef: "read-current-session-" + kind}
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				for _, run := range []w.Run{originalRun, currentRun} {
					if err := tx.InsertRun(run, run.ID, run.ID+"-digest"); err != nil {
						return err
					}
				}
				if err := tx.SaveSource(w.Source{ID: "read-private-" + kind, WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: originalRun.ID, Kind: "file", Content: "secret", Visibility: "private"}); err != nil {
					return err
				}
				if err := tx.SaveStep(w.Step{ID: originalAttempt.StepID, RunID: originalRun.ID, AgentID: "owner", Status: "succeeded"}); err != nil {
					return err
				}
				if err := tx.SaveSession(originalSession); err != nil {
					return err
				}
				if err := tx.SaveAttempt(originalAttempt); err != nil {
					return err
				}
				if err := tx.SaveMessage(message); err != nil {
					return err
				}
				if err := tx.SaveArtifact(artifact); err != nil {
					return err
				}
				return tx.SaveStep(w.Step{ID: currentAttempt.StepID, RunID: currentRun.ID, AgentID: "owner", Status: "succeeded"})
			}); err != nil {
				t.Fatal(err)
			}
			exposed := "unsupported-read-reference"
			if kind == "message" {
				if err := db.Read(t.Context(), func(tx *store.Tx) error {
					digest, _, _, err := tx.MessageDigest(message.ID)
					exposed = fmt.Sprintf("message:%s:%s", message.ID, digest)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			} else if kind == "artifact" {
				exposed = fmt.Sprintf("artifact:%s@1:artifact-digest", artifact.ID)
			}
			currentSession := w.ExecutionSession{ID: currentAttempt.ExecutionSessionRef, TaskID: sender.Task.ID, MemberID: "owner", Status: "fresh_only", ExposedRefs: []string{exposed}, CreatedAt: w.Now(), UpdatedAt: w.Now()}
			currentSession.ExposureDigest = exposureRefsDigest(currentSession.ExposedRefs)
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				if err := tx.SaveSession(currentSession); err != nil {
					return err
				}
				return tx.SaveAttempt(currentAttempt)
			}); err != nil {
				t.Fatal(err)
			}
			_, err = service.SendTaskMessage(t.Context(), SendTaskMessageInput{
				SenderTaskID: sender.Task.ID, SenderAttemptID: currentAttempt.ID, TargetTaskID: target.Task.ID,
				Kind: "result", Body: "clean-looking answer without citations", RequestID: "read-derived-" + kind,
			})
			if !IsCode(err, "disclosure_denied") {
				t.Fatalf("%s exposure was laundered into a cross-task answer: %v", kind, err)
			}
		})
	}
}
