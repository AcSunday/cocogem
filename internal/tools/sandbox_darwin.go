//go:build darwin

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// prepareSandbox builds and validates the macOS seatbelt profile. The profile
// is deny-by-default: no network, no AppleEvents, no arbitrary mach lookup,
// no shell or system-daemon helpers, and read access only to the runtime system
// trees, the bound project, the private test directory, the exact Go toolchain
// and the exact Node/npm program trees.
func prepareSandbox(ctx context.Context, spec sandboxSpec) (sandboxPlan, error) {
	sandbox, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return sandboxPlan{}, fmt.Errorf("%w: macOS sandbox-exec is not available", ErrSandboxUnavailable)
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// `/usr/bin/true` is injected as the allowed command so the preflight uses
	// the exact production profile builder, including deny-default.
	preflight := exec.CommandContext(preflightCtx, sandbox, "-p", darwinProfile(sandboxSpec{Toolchain: toolchainSpec{Command: []string{"/usr/bin/true"}}}), "/usr/bin/true")
	preflight.Env = []string{"PATH=/usr/bin:/bin"}
	if out, err := preflight.CombinedOutput(); err != nil {
		return sandboxPlan{}, fmt.Errorf("%w: sandbox-exec deny-default preflight failed: %s", ErrSandboxUnavailable, utf8Prefix(string(out), 512))
	}
	profilePath := filepath.Join(spec.Temp, "profile.sb")
	if err := os.WriteFile(profilePath, []byte(darwinProfile(spec)), 0600); err != nil {
		return sandboxPlan{}, errors.New("could not write the sandbox profile")
	}
	return sandboxPlan{Prefix: []string{sandbox, "-f", profilePath}, Env: spec.Toolchain.Env}, nil
}

func darwinProfile(spec sandboxSpec) string {
	readPaths := []string{"/usr", "/bin", "/sbin", "/System", "/dev", spec.Root, spec.Temp, spec.Toolchain.GoRoot, spec.Toolchain.GoModCache, spec.Toolchain.NPMRoot, spec.Toolchain.NodePath}
	execPaths := []string{"/bin/sh", "/bin/bash", "/usr/bin/env", spec.Root, spec.Temp, spec.Toolchain.GoRoot, spec.Toolchain.NPMRoot, spec.Toolchain.NodePath}
	if len(spec.Toolchain.Command) > 0 {
		readPaths = append(readPaths, spec.Toolchain.Command[0])
		execPaths = append(execPaths, spec.Toolchain.Command[0])
	}
	var out strings.Builder
	out.WriteString("(version 1)\n(deny default)\n(deny network*)\n(deny appleevent-send)\n")
	out.WriteString("(allow file-read-metadata)\n")
	// The literal root directory is needed for path resolution; content access
	// stays limited to the exact subtrees below.
	out.WriteString("(allow file-read-data (literal \"/\")")
	for _, path := range uniqueExistingPaths(readPaths) {
		out.WriteString(" (subpath " + sbplString(path) + ")")
	}
	out.WriteString(")\n")
	out.WriteString("(allow file-write*")
	// Writing devices other than /dev/null is neither needed nor allowed.
	for _, path := range []string{spec.Root, spec.Temp} {
		if path != "" {
			out.WriteString(" (subpath " + sbplString(path) + ")")
		}
	}
	out.WriteString(" (literal \"/dev/null\"))\n")
	// Scoped process execution is the boundary that keeps test code away from
	// osascript, launchctl and other system tools. Shells stay available because
	// npm lifecycle scripts and Go test helpers legitimately spawn them.
	out.WriteString("(allow process-exec")
	for _, path := range uniqueExistingPaths(execPaths) {
		out.WriteString(" (subpath " + sbplString(path) + ")")
	}
	out.WriteString(")\n")
	out.WriteString("(allow process-fork)\n(allow sysctl-read)\n")
	// Seatbelt's mach-lookup rule does not stop launchd XPC queries from a
	// process that was allowed to exec launchctl (real probe evidence), so the
	// exec allowlist above is the enforced daemon boundary. Only the two names
	// the Go runtime and libinfo need are granted here.
	out.WriteString("(allow mach-lookup (global-name \"com.apple.system.logger\") (global-name \"com.apple.system.opendirectoryd.libinfo\"))\n")
	// Deny rules come last: they override the subtree allows. The two system
	// helpers stay unreadable as well as non-executable, so test code cannot copy
	// them into the project and exec the copy; Seatbelt does not fully filter
	// launchd XPC traffic, so this matters. Secrets inside the project are denied
	// case by case, including files created after profile build.
	out.WriteString("(deny file-read-data (literal \"/bin/launchctl\") (literal \"/usr/bin/osascript\"))\n")
	sensitive := sensitiveRegex(spec.Root)
	out.WriteString("(deny file-read-data (regex #\"" + sensitive + "\"))\n")
	out.WriteString("(deny file-write* (regex #\"" + sensitive + "\"))\n")
	return out.String()
}

func uniqueExistingPaths(paths []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func sbplString(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value) + `"`
}

// sensitiveRegex denies every component matched by SensitivePath at any depth
// below the project. It is generated from the same policy lists so the sandbox
// and the file tools cannot drift apart. SBPL has no inline case flag, so each
// policy letter is spelled as an explicit ASCII [xX] class; the root stays an
// escaped literal.
func sensitiveRegex(root string) string {
	alternatives := make([]string, 0, len(sensitiveNames)+len(sensitivePrefixes)+len(sensitiveSuffixes))
	for _, name := range sensitiveNames {
		alternatives = append(alternatives, asciiCaseFold(name))
	}
	for _, prefix := range sensitivePrefixes {
		alternatives = append(alternatives, asciiCaseFold(prefix)+"[^/]*")
	}
	for _, suffix := range sensitiveSuffixes {
		alternatives = append(alternatives, "[^/]+"+asciiCaseFold(suffix))
	}
	escaped := regexp.QuoteMeta(filepath.ToSlash(root))
	return "^" + escaped + "(/.*)?/(" + strings.Join(alternatives, "|") + ")(/.*)?$"
}

// asciiCaseFold renders every ASCII letter as a two-case regex class and
// escapes the remaining bytes as literals.
func asciiCaseFold(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z':
			out.WriteByte('[')
			out.WriteByte(c)
			out.WriteByte(c - 'a' + 'A')
			out.WriteByte(']')
		case c >= 'A' && c <= 'Z':
			out.WriteByte('[')
			out.WriteByte(c - 'A' + 'a')
			out.WriteByte(c)
			out.WriteByte(']')
		default:
			out.WriteString(regexp.QuoteMeta(value[i : i+1]))
		}
	}
	return out.String()
}
