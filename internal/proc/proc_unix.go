//go:build darwin || linux

// Package proc verifies ownership of local child processes across restarts.
package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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

// Inspect reads the kernel's process start identity and process group.
func Inspect(pid int) (Identity, error) {
	if pid <= 0 {
		return Identity{}, ErrNotRunning
	}
	command := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "pgid=", "-o", "stat=")
	command.Env = append(os.Environ(), "LC_ALL=C")
	out, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return Identity{}, ErrNotRunning
		}
		return Identity{}, fmt.Errorf("inspect process %d: %w", pid, err)
	}
	fields := strings.Fields(string(out))
	// lstart is five tokens: Mon Jan  2 15:04:05 2006.
	if len(fields) < 7 {
		return Identity{}, ErrNotRunning
	}
	if strings.HasPrefix(fields[6], "Z") {
		return Identity{}, ErrNotRunning
	}
	pgid, err := strconv.Atoi(fields[5])
	if err != nil {
		return Identity{}, ErrNotRunning
	}
	return Identity{PID: pid, PGID: pgid, Start: strings.Join(fields[:5], " ")}, nil
}

// Self returns the current server's process identity.
func Self() (Identity, error) {
	return Inspect(os.Getpid())
}

// Alive reports whether this exact process instance still exists.
func (id Identity) Alive() bool {
	if id.PID <= 0 {
		return false
	}
	current, err := Inspect(id.PID)
	if err != nil {
		return false
	}
	return current.Start == id.Start
}

// GroupMembers lists live, non-zombie processes in one owned process group.
func GroupMembers(pgid int) ([]int, error) {
	if pgid <= 0 {
		return nil, nil
	}
	command := exec.Command("ps", "-A", "-o", "pid=,pgid=,stat=")
	command.Env = append(os.Environ(), "LC_ALL=C")
	out, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect process group %d: %w", pgid, err)
	}
	members := []int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		group, err := strconv.Atoi(fields[1])
		if err != nil || group != pgid || strings.HasPrefix(fields[2], "Z") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil && pid > 0 {
			members = append(members, pid)
		}
	}
	return members, nil
}

// GroupAlive reports whether the recorded group still has live members.
func GroupAlive(pgid int) (bool, error) {
	members, err := GroupMembers(pgid)
	return len(members) > 0, err
}

// Terminate verifies identity before signaling, then waits for the whole recorded group.
func Terminate(id Identity, grace time.Duration) error {
	if id.PID <= 0 && id.PGID <= 0 {
		return nil
	}
	grouped := id.PGID > 0 && id.PGID == id.PID
	// A live leader with a different start time is a different process; never signal it.
	if current, err := Inspect(id.PID); err == nil && id.Start != "" && current.Start != id.Start {
		return fmt.Errorf("process %d identity no longer matches", id.PID)
	}
	clean := func() (bool, error) {
		if grouped {
			members, err := GroupMembers(id.PGID)
			return len(members) == 0, err
		}
		return !id.Alive(), nil
	}
	if done, err := clean(); err != nil {
		return err
	} else if done {
		return nil
	}
	signal := func(sig syscall.Signal) {
		if grouped {
			_ = syscall.Kill(-id.PGID, sig)
			return
		}
		_ = syscall.Kill(id.PID, sig)
	}
	signal(syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if done, err := clean(); err != nil {
			return err
		} else if done {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	signal(syscall.SIGKILL)
	for i := 0; i < 40; i++ {
		if done, err := clean(); err != nil {
			return err
		} else if done {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if grouped {
		members, _ := GroupMembers(id.PGID)
		return fmt.Errorf("process group %d still has live members %v", id.PGID, members)
	}
	return fmt.Errorf("process %d did not stop", id.PID)
}
