//go:build darwin

package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDarwinProfileIsDenyDefaultWithMinimalAllowlist(t *testing.T) {
	root := t.TempDir()
	temp := t.TempDir()
	goroot := t.TempDir()
	npmroot := t.TempDir()
	node := filepath.Join(goroot, "bin", "node")
	if err := os.MkdirAll(filepath.Dir(node), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, []byte("node"), 0700); err != nil {
		t.Fatal(err)
	}
	npm := filepath.Join(temp, "npm")
	if err := os.WriteFile(npm, []byte("npm"), 0700); err != nil {
		t.Fatal(err)
	}
	profile := darwinProfile(sandboxSpec{Root: root, Temp: temp, Toolchain: toolchainSpec{
		Command: []string{npm}, GoRoot: goroot, NodePath: node, NPMRoot: npmroot,
	}})
	for _, want := range []string{
		"(deny default)",
		"(deny network*)",
		"(deny appleevent-send)",
		"(allow file-read-metadata)",
		"(allow process-fork)",
		"(allow sysctl-read)",
		"(allow process-exec (subpath ",
		`(literal "/dev/null")`,
		`(deny file-read-data (literal "/bin/launchctl") (literal "/usr/bin/osascript"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile is missing %q:\n%s", want, profile)
		}
	}
	if strings.Contains(profile, "(allow default)") || strings.Contains(profile, "(allow process-exec)\n") {
		t.Fatalf("profile opened an unrestricted operation:\n%s", profile)
	}
	for _, unwanted := range []string{"/Library", "/private/etc", "/private/var/db", "/opt/homebrew"} {
		if strings.Contains(profile, unwanted) {
			t.Fatalf("profile allows an overly broad path %q:\n%s", unwanted, profile)
		}
	}
	if got := strings.Count(profile, "global-name"); got != 2 {
		t.Fatalf("mach-lookup allowlist has %d names, want 2:\n%s", got, profile)
	}
	for _, path := range []string{root, temp, goroot, npmroot, node, npm, "/usr", "/bin", "/sbin", "/System", "/dev"} {
		if !strings.Contains(profile, sbplString(path)) {
			t.Fatalf("profile is missing the exact path %q:\n%s", path, profile)
		}
	}
}

func TestDarwinSensitiveRegexMatchesSharedPolicy(t *testing.T) {
	// Regex metacharacters in the root prove the root stays escaped while only
	// the sensitive components are case-folded.
	root := "/work/pro.ject+(x)[y]"
	pattern := regexp.MustCompile(sensitiveRegex(root))
	for _, path := range []string{
		".env", ".env.local", ".ENV", ".EnV.local",
		".git/config", ".GiT/config",
		"sub/.git/config", "sub/.GiT/config",
		"secrets/token", "SeCreTs/token",
		"keys/server.key", "keys/server.KEY",
		"id_rsa", "ID_RSA",
		".git-credentials", ".Git-Credentials",
		".ssh/id_ed25519", ".SSH/id_ed25519",
		".cocogem/x", ".CoCoGeM/x",
		"app.sqlite", "app.SQLITE",
	} {
		if !SensitivePath(path) {
			t.Fatalf("shared policy does not treat %s as sensitive", path)
		}
		if !pattern.MatchString(root + "/" + path) {
			t.Fatalf("sensitive path %s is not denied by the sandbox regex", path)
		}
	}
	for _, path := range []string{"calc.go", "src/main.go", "SRC/Main.GO", "node_modules/pkg/index.js", "dist/app.js", "build/out"} {
		if SensitivePath(path) {
			t.Fatalf("%s is unexpectedly sensitive", path)
		}
		if pattern.MatchString(root + "/" + path) {
			t.Fatalf("project-testable path %s is denied by the sandbox regex", path)
		}
	}
	if pattern.MatchString("/work/proXject+(x)[y]/.env") {
		t.Fatal("sandbox regex root is not escaped")
	}
}
