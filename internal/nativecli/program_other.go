//go:build !darwin && !linux

package nativecli

import "os"

func inodeIdentity(os.FileInfo) string { return "" }
