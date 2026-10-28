//go:build !darwin && !linux

package proc

import (
	"errors"
	"os"
	"time"
)

// ErrNotRunning means the recorded process is gone or no longer matches.
var ErrNotRunning = errors.New("process is not running")

// Identity is a PID plus the facts needed to rule out PID reuse.
type Identity struct {
	PID        int    `json:"pid"`
	PGID       int    `json:"pgid,omitempty"`
	Start      string `json:"start,omitempty"`
	Executable string `json:"executable,omitempty"`
}

func Inspect(pid int) (Identity, error) {
	return Identity{}, errors.New("process ownership checks support macOS and Linux only")
}
func Self() (Identity, error)   { return Inspect(os.Getpid()) }
func (id Identity) Alive() bool { return false }
func GroupMembers(int) ([]int, error) {
	return nil, errors.New("process ownership checks support macOS and Linux only")
}
func GroupAlive(int) (bool, error) {
	return false, errors.New("process ownership checks support macOS and Linux only")
}
func Terminate(Identity, time.Duration) error {
	return errors.New("process ownership checks support macOS and Linux only")
}
