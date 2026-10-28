//go:build darwin || linux

package nativecli

import (
	"fmt"
	"os"
	"syscall"
)

func inodeIdentity(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	}
	return ""
}
