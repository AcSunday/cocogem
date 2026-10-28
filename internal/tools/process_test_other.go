//go:build !darwin && !linux

package tools

import (
	"errors"
	"os/exec"
)

func configureTestProcess(*exec.Cmd) error {
	return errors.New("this platform has no verified test process boundary")
}

func terminateTestGroup(*exec.Cmd) {}
