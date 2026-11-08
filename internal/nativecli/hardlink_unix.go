//go:build darwin || linux || freebsd || netbsd || openbsd

package nativecli

import (
	"os"
	"syscall"
)

func hardlinkCountPlatform(info os.FileInfo) int {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Nlink)
	}
	return 1
}
