package workflow

import (
	"context"
	"errors"
	"sync"
)

// checkpointState lets one step stop new dispatch and cancel in-flight
// siblings before the run commits its interrupted checkpoint.
type checkpointState struct {
	mu     sync.Mutex
	stepID string
	reason string
	stop   context.CancelFunc
}

func newCheckpointState(stop context.CancelFunc) *checkpointState {
	return &checkpointState{stop: stop}
}

func (c *checkpointState) request(stepID, reason string) {
	c.mu.Lock()
	if c.stepID == "" {
		c.stepID, c.reason = stepID, reason
	}
	c.mu.Unlock()
	if c.stop != nil {
		c.stop()
	}
}

func (c *checkpointState) triggered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stepID != ""
}

func (c *checkpointState) by(stepID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stepID != "" && c.stepID != stepID
}

// describe records why the checkpoint stopped sibling dispatch.
func (c *checkpointState) describe() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stepID == "" {
		return ""
	}
	return "step " + c.stepID + " requested a checkpoint: " + c.reason
}

func errorFrom(cause string) error {
	if cause == "" {
		return nil
	}
	return errors.New(cause)
}
