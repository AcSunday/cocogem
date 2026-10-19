//go:build !darwin && !linux

package nativecli

import (
	"errors"
	"os/exec"
)

func configureProcess(*exec.Cmd) error { return errors.New("native CLI supports macOS and Linux only") }
func terminateProcess(*exec.Cmd)       {}
