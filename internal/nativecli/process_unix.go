//go:build darwin || linux

package nativecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Lock the directory itself so a crashed host releases the session without a
// stale lockfile. Waiters can be canceled before they launch a native process.
func lockSessionDirectory(ctx context.Context, dir string) (*os.File, error) {
	file, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	for ctx.Err() == nil {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
	file.Close()
	return nil, ctx.Err()
}

func configureProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	return nil
}

func terminateProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
