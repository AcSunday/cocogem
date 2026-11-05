package project

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/AcSunday/cocogem/internal/contextplan"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestDerivedBodyInheritsStructuredRecordExposure(t *testing.T) {
	for _, kind := range []string{"memory", "project", "claim", "dataset"} {
		t.Run(kind, func(t *testing.T) {
			db, service, th := setup(t)
			sender := create(t, service, th)
			targetThread := w.Thread{ID: "structured-target-thread-" + kind, WorkspaceID: th.WorkspaceID, CoordinatorID: "owner", Revision: 1, UpdatedAt: w.Now()}
			if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(targetThread) }); err != nil {
				t.Fatal(err)
			}
			target, err := service.CreateTask(t.Context(), CreateTaskInput{
				WorkspaceID: th.WorkspaceID, ThreadID: targetThread.ID, OwnerMemberID: "owner",
				Goal: "independent structured recipient", SourceMessageID: "structured-target-message-" + kind, AcceptedBy: "user",
			})
			if err != nil {
				t.Fatal(err)
			}
			ref := fmt.Sprintf("%s:private-%s@1", kind, kind)
			var record any
			switch kind {
			case "memory":
				record = w.MemoryRecord{ID: "private-memory", WorkspaceID: th.WorkspaceID, Revision: 1, Scope: sender.Task.ID, Kind: "research", Content: "private memory", SourceRefs: []string{}, ClaimStatus: "accepted", AcceptedBy: "reviewer", Visibility: "private"}
			case "project":
				record = w.ProjectRecord{ID: "private-project", WorkspaceID: th.WorkspaceID, Revision: 1, Kind: "decision", Content: "private project decision", Status: "accepted", SourceRefs: []string{}, AcceptedBy: "user", Visibility: "private"}
			case "claim":
				record = w.Claim{ID: "private-claim", WorkspaceID: th.WorkspaceID, TaskID: sender.Task.ID, Revision: 1, Statement: "private claim", Status: "proposed", Visibility: "private"}
			case "dataset":
				record = w.DatasetManifest{ID: "private-dataset", WorkspaceID: th.WorkspaceID, TaskID: sender.Task.ID, Revision: 1, SourceRefs: []string{}, Visibility: "private"}
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			item := w.ContextItem{Ref: ref, Kind: kind, Digest: contextplan.Digest(string(encoded)), Visibility: "private", Mode: "inline", Required: true, Reason: "exact structured read"}
			manifest := w.ContextManifest{ID: "structured-manifest-" + kind, TaskID: sender.Task.ID, TaskRevision: 1, Items: []w.ContextItem{item}}
			run := w.Run{ID: "structured-run-" + kind, WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: sender.Task.ID, TaskRevision: 1, ContextRef: manifest.ID, Status: "succeeded", CreatedAt: w.Now()}
			step := w.Step{ID: "structured-step-" + kind, RunID: run.ID, AgentID: "owner", Status: "succeeded"}
			session := w.ExecutionSession{ID: "structured-session-" + kind, TaskID: sender.Task.ID, MemberID: "owner", Status: "fresh_only", ExposedRefs: []string{item.Ref + ":" + item.Digest}, CreatedAt: w.Now(), UpdatedAt: w.Now()}
			session.ExposureDigest = exposureRefsDigest(session.ExposedRefs)
			attempt := w.Attempt{ID: "structured-attempt-" + kind, RunID: run.ID, StepID: step.ID, Number: 1, Status: "succeeded", ExecutionSessionRef: session.ID}
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				if err := tx.InsertRun(run, "structured-run-key-"+kind, "structured-run-digest-"+kind); err != nil {
					return err
				}
				if err := tx.SaveStep(step); err != nil {
					return err
				}
				if err := tx.SaveContextManifest(manifest); err != nil {
					return err
				}
				if err := tx.SaveSession(session); err != nil {
					return err
				}
				if err := tx.SaveAttempt(attempt); err != nil {
					return err
				}
				switch value := record.(type) {
				case w.MemoryRecord:
					return tx.SaveMemory(value)
				case w.ProjectRecord:
					return tx.SaveProjectRecord(value)
				case w.Claim:
					return tx.SaveClaim(value)
				case w.DatasetManifest:
					return tx.SaveDataset(value)
				default:
					return fmt.Errorf("unsupported record kind %s", kind)
				}
			}); err != nil {
				t.Fatal(err)
			}
			_, err = service.SendTaskMessage(t.Context(), SendTaskMessageInput{
				SenderTaskID: sender.Task.ID, SenderAttemptID: attempt.ID, TargetTaskID: target.Task.ID,
				Kind: "result", Body: "clean-looking answer without structured references", RequestID: "structured-derived-" + kind,
			})
			if !IsCode(err, "disclosure_denied") {
				t.Fatalf("%s exposure was laundered into a cross-task answer: %v", kind, err)
			}
		})
	}
}
