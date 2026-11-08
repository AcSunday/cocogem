package nativecli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// gateHardlinkScan merges the hardlink scan of every root the sandbox actually
// writes. Scanning only the workspace root would leave a task/working directory
// (or any additional root) unchecked, so an inside file there could alias an
// outside inode and a path-based profile would not notice.
//
// A root that cannot be scanned makes the whole result unverified: callers must
// fail closed rather than treat an unscanned root as safe.
func gateHardlinkScan(sandbox *nativeSandbox, extraRoots ...string) hardlinkScan {
	roots := append([]string{}, extraRoots...)
	if sandbox != nil {
		roots = append(roots, sandbox.writeRoots...)
	}
	seen := map[string]bool{}
	result := hardlinkScan{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			result.unknown = true
			continue
		}
		absolute = filepath.Clean(absolute)
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		// A root that does not exist or is not a directory cannot be scanned,
		// so its links stay unproven. The shared scanner reports "no dangerous
		// links" for a missing root, which must not be read as verified.
		info, statErr := os.Stat(absolute)
		if statErr != nil || !info.IsDir() {
			result.unknown = true
			continue
		}
		scan := scanProjectHardlinks(absolute)
		if scan.unknown {
			result.unknown = true
			continue
		}
		for _, path := range scan.dangerous {
			result.dangerous = appendUnique(result.dangerous, path)
		}
	}
	sort.Strings(result.dangerous)
	return result
}

// applyGateHardlinkPolicy wires one scan result into the approver and the
// sandbox. An unverified scan closes native command/file effects for the
// attempt instead of assuming the unchecked links are safe.
func applyGateHardlinkPolicy(gate *bridge, approver *nativeApprover, sandbox *nativeSandbox, scan hardlinkScan) error {
	if sandbox != nil {
		sandbox.setHardlinkDenies(scan.dangerous, scan.unknown)
	}
	if approver != nil {
		approver.setDangerousPaths(scan.dangerous)
	}
	if gate == nil {
		return nil
	}
	if scan.unknown {
		return gate.notice("native_hardlink_unverified",
			"a project root could not be fully scanned for external hardlinks; native file and command effects are denied for this step")
	}
	if len(scan.dangerous) != 0 {
		return gate.notice("native_hardlink_isolated", fmt.Sprintf(
			"%d file(s) across the attempt roots link outside them; those exact paths are denied so an outside original cannot be modified",
			len(scan.dangerous)))
	}
	return nil
}
