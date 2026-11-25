//go:build !darwin && !linux

package nativecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func lockSessionDirectory(context.Context, string) (*os.File, error) {
	return nil, errors.New("native CLI supports macOS and Linux only")
}

func configureProcess(*exec.Cmd) error { return errors.New("native CLI supports macOS and Linux only") }
func terminateProcess(*exec.Cmd)       {}
