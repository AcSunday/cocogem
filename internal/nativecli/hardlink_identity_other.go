//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd

package nativecli

import "os"

func hardlinkIdentity(os.FileInfo) (string, int, bool) { return "", 0, false }
