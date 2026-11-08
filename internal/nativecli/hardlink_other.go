//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd

package nativecli

import "os"

func hardlinkCountPlatform(os.FileInfo) int { return 1 }
