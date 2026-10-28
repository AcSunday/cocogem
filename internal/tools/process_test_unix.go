//go:build darwin || linux

package tools

import (
	"os/exec"
	"syscall"
	"time"

	"github.com/AcSunday/cocogem/internal/proc"
)

func configureTestProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	return nil
}

// terminateTestGroup signals the owned group only while it still has live
// members. An empty group's PGID may already be recycled by the kernel, and a
// negative-PID signal would then hit an unrelated process group.
func terminateTestGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	if members, err := proc.GroupMembers(pgid); err == nil && len(members) == 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
