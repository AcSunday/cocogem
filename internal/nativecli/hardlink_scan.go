package nativecli

import (
	"os"
	"path/filepath"
	"sort"
)

// hardlinkScan finds files inside root whose inode has more links than the
// project itself accounts for. Such a file can alias an outside inode that a
// path-based sandbox cannot distinguish, so the adapter must deny access to the
// exact paths rather than rely on a path prefix.
type hardlinkScan struct {
	dangerous []string
	// unknown means the bounded scan could not prove there are no external
	// links. Callers must fail closed instead of assuming safety.
	unknown bool
}

const hardlinkScanLimit = 50000

type hardlinkGroup struct {
	links int
	paths []string
}

func scanProjectHardlinks(root string) hardlinkScan {
	if root == "" {
		return hardlinkScan{unknown: true}
	}
	groups := map[string]*hardlinkGroup{}
	visited := 0
	truncated := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		visited++
		if visited > hardlinkScanLimit {
			truncated = true
			return filepath.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		key, links, ok := hardlinkIdentity(info)
		if !ok || links <= 1 {
			return nil
		}
		group := groups[key]
		if group == nil {
			group = &hardlinkGroup{links: links}
			groups[key] = group
		}
		group.paths = append(group.paths, path)
		return nil
	})
	if truncated {
		return hardlinkScan{unknown: true}
	}
	result := hardlinkScan{}
	for _, group := range groups {
		// Every existing link must be inside the project. A larger inode link
		// count means at least one link lives outside and cannot be isolated by
		// an inside path rule.
		if group.links <= len(group.paths) {
			continue
		}
		result.dangerous = append(result.dangerous, group.paths...)
	}
	sort.Strings(result.dangerous)
	if len(result.dangerous) > 2000 {
		return hardlinkScan{unknown: true}
	}
	return result
}
