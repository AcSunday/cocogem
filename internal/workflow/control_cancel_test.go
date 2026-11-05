package workflow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
)

func TestCancelTaskStopsOwnedNativeExecution(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedPublicationRun(t, s, "free", "responder")
	created, err := project.New(s).CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, OwnerMemberID: run.Agents[0].ID,
		Goal: "停止在途原生执行", SourceMessageID: "control-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run.TaskID, run.TaskRevision, run.BudgetAccountID = created.Task.ID, 1, created.Budget.ID
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
		t.Fatal(err)
	}
	started, stopped := make(chan struct{}), make(chan struct{})
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, _ agent.Observe) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		got, _ := loadRun(t, s, run.ID)
		_, admissionErr := e.admitRun(t.Context(), got)
		t.Fatalf("run did not start: %+v, admission=%v", got, admissionErr)
	}
	requestCtx, requestCancel := context.WithCancel(t.Context())
	requestCancel()
	if err := e.CancelTask(requestCtx, created.Task.ID); err != nil {
		t.Fatalf("committed task control lost cancellation: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("task native process kept running")
	}
	if got := awaitRun(t, s, run.ID); got.Status != "canceled" {
		t.Fatalf("task run settled as %s: %s", got.Status, got.Error)
	}
}

func TestCancelWorkspaceStopsExecutionBeforePolicyChange(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedPublicationRun(t, s, "free", "responder")
	started, stopped := make(chan struct{}), make(chan struct{})
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, _ agent.Observe) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not start")
	}
	if err := e.CancelWorkspace(t.Context(), run.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("policy change left the old native process running")
	}
	if got := awaitRun(t, s, run.ID); got.Status != "canceled" {
		t.Fatalf("policy-change run settled as %s: %s", got.Status, got.Error)
	}
}
