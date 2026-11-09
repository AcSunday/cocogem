//go:build darwin || linux || freebsd || netbsd || openbsd

package nativecli

import (
	"fmt"
	"os"
	"syscall"
)

func hardlinkIdentity(info os.FileInfo) (string, int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, false
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), int(stat.Nlink), true
}
