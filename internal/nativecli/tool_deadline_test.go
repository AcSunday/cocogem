package nativecli

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestHostToolDeadlinePausesDuringPermissionWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events int
	b, err := newBridge(ctx, testRequest().Tools, func(callCtx context.Context, name string, input json.RawMessage) (string, error) {
		resume := PauseToolDeadline(callCtx)
		select {
		case <-time.After(120 * time.Millisecond):
		case <-callCtx.Done():
			return "", callCtx.Err()
		}
		resume()
		select {
		case <-time.After(10 * time.Millisecond):
			return "ok", nil
		case <-callCtx.Done():
			return "", callCtx.Err()
		}
	}, func(string, map[string]any) error { events++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	b.toolLimit = 40 * time.Millisecond
	result := b.call(json.RawMessage(`{"name":"read_file","arguments":{"path":"README.md"}}`))
	response, ok := result.(map[string]any)
	if !ok || response["isError"] == true {
		t.Fatalf("paused execution was canceled: %#v", result)
	}
	if events == 0 {
		t.Fatal("tool events were not persisted")
	}
}

func TestBridgeEventsAreNotBlockedByHostExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	b, err := newBridge(ctx, testRequest().Tools, func(context.Context, string, json.RawMessage) (string, error) {
		close(started)
		<-release
		return "ok", nil
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	go b.call(json.RawMessage(`{"name":"read_file","arguments":{"path":"README.md"}}`))
	<-started
	emitted := make(chan error, 1)
	go func() { emitted <- b.emit("native_activity", map[string]any{"kind": "permission_wait"}) }()
	select {
	case err := <-emitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("event emission was blocked by an in-flight host tool")
	}
	close(release)
}
