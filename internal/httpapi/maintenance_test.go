package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/workflow"
)

func TestMaintenanceWaitCoversInFlightCleanup(t *testing.T) {
	s, _, _, _ := pagingServer(t, workflow.DefaultLimits())
	entered := make(chan struct{})
	release := make(chan struct{})
	s.purgeExpired = func(ctx context.Context) (int, error) {
		close(entered)
		<-release
		if ctx.Err() == nil {
			return 0, errors.New("maintenance cancellation was not visible to the in-flight cleanup")
		}
		return 0, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	wait := s.StartMaintenance(ctx)
	<-entered
	cancel()
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
		t.Fatal("maintenance wait returned before the in-flight cleanup exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance wait did not return after cleanup exited")
	}
}
