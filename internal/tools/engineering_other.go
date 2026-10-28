//go:build !darwin && !linux

package tools

import "os"

func linkCount(os.FileInfo) uint64 { return 1 }
