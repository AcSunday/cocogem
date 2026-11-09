package workflow

import (
	"encoding/json"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestTaskPromotionBindsCurrentMessageOnceAndPreservesHistory(t *testing.T) {
	e, db, run, _, root := seedMethodFixture(t, nil)
	run.TaskIntake = true
	run.Input.Message = "就按刚才的方案实现，完成后交付报告"
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: "current-user", ThreadID: run.ThreadID, RunID: run.ID, Role: "user", Content: run.Input.Message, Status: "completed"})
	}); err != nil {
		t.Fatal(err)
	}
	in := taskPromotionInput{Title: "实现既定方案", Goal: "实现此前接受的方案，保留已有约束并交付报告", EvidenceQuote: run.Input.Message}
	raw, _ := json.Marshal(in)
	first, err := e.promoteTask(t.Context(), run, root, raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.promoteTask(t.Context(), run, root, raw)
	if err != nil || first != second {
		t.Fatalf("replay: %s %s %v", first, second, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		th, err := tx.Thread(run.ThreadID)
		if err != nil {
			return err
		}
		tasks, err := tx.AllTasks()
		if err != nil {
			return err
		}
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		if len(tasks) != 1 || th.TaskID != tasks[0].ID || current.TaskID != th.TaskID || !current.BudgetStarted || current.TaskPromotionStepID != root.ID {
			t.Fatalf("promotion: %+v %+v %+v", tasks, th, current)
		}
		if len(steps) != 2 || steps[1].Kind != "continuation" || steps[1].FromStepID != root.ID {
			t.Fatalf("steps: %+v", steps)
		}
		revision, err := tx.TaskRevision(th.TaskID, 1)
		if err != nil {
			return err
		}
		if revision.SourceMessageID != "current-user" || revision.AcceptedBy != "user" {
			t.Fatalf("authority: %+v", revision)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	in.Goal = "a different objective"
	raw, _ = json.Marshal(in)
	if _, err := e.promoteTask(t.Context(), run, root, raw); err == nil {
		t.Fatal("conflicting replay changed objective")
	}
	service := project.New(db)
	board, err := service.TaskBoard(t.Context())
	if err != nil || len(board.Tasks) != 1 {
		t.Fatalf("board: %+v %v", board, err)
	}
}

func TestTaskPromotionRejectsInventedAndQuotedInstructions(t *testing.T) {
	for _, tc := range []struct{ name, message, quote string }{
		{"invented", "解释这段代码", "修复并测试"},
		{"quoted", "解释这句话：\"修复并测试\"", "修复并测试"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, run, _, step := seedMethodFixture(t, nil)
			run.TaskIntake = true
			run.Input.Message = tc.message
			raw, _ := json.Marshal(taskPromotionInput{Title: "任务", Goal: "修复并测试", EvidenceQuote: tc.quote})
			if _, err := e.promoteTask(t.Context(), run, step, raw); err == nil {
				t.Fatal("accepted non-user authority")
			}
		})
	}
}

func TestPromotedIntakeResumeCompletesReceiptWithoutExecutingAgain(t *testing.T) {
	e, db, run, _, root := seedMethodFixture(t, nil)
	run.TaskIntake = true
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: "resume-user", RunID: run.ID, ThreadID: run.ThreadID, Role: "user", Content: run.Input.Message, Status: "completed"})
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(taskPromotionInput{Title: "恢复任务", Goal: run.Input.Message, EvidenceQuote: run.Input.Message})
	if _, err := e.promoteTask(t.Context(), run, root, raw); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		var err error
		run, err = tx.Run(run.ID)
		if err != nil {
			return err
		}
		root.Status = "interrupted"
		return tx.SaveStep(root)
	}); err != nil {
		t.Fatal(err)
	}
	// A nil model deliberately proves no second native/model invocation occurs.
	step := e.executeStepWith(t.Context(), run, root, nil, nil, nil, nil)
	if step.Status != "succeeded" {
		t.Fatalf("receipt recovery: %+v", step)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		if len(steps) != 2 || steps[1].Status != "pending" {
			t.Fatalf("continuation changed: %+v", steps)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
