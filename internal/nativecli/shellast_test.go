package nativecli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShellASTIgnoresClassicFalsePositives is the A10 parser regression: a
// device redirect, a jq fallback operator and an awk regex are not path
// arguments, so they must never become approval scopes.
func TestShellASTIgnoresClassicFalsePositives(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "device redirect is not a target",
			command: `go test ./... 2>/dev/null`,
		},
		{
			name:    "jq fallback operator is a program literal",
			command: `jq '.a // empty' data.json`,
		},
		{
			name:    "awk regex is a program literal",
			command: `awk '/ERR/' build.log`,
		},
		{
			name:    "redirect destination inside the project is a target",
			command: `printf x > ./out.txt`,
			want:    []string{filepath.Join(root, "out.txt")},
		},
		{
			name:    "executable path is never a data target",
			command: `/opt/homebrew/bin/node --version`,
		},
		{
			name:    "absolute outside path is a real target",
			command: `cat ` + filepath.Join(filepath.Dir(root), "outside.txt"),
			want:    []string{filepath.Join(filepath.Dir(root), "outside.txt")},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseShellCommand(test.command)
			if err != nil {
				t.Fatalf("parse %q: %v", test.command, err)
			}
			targets := resolveShellTargets(parsed, root)
			if len(targets) != len(test.want) {
				t.Fatalf("%q targets = %v, want %v", test.command, targets, test.want)
			}
			for index, want := range test.want {
				if targets[index] != want {
					t.Fatalf("%q target[%d] = %q, want %q", test.command, index, targets[index], want)
				}
			}
			for _, target := range targets {
				if target == "/" || filepath.Base(target) == "//" || filepath.Base(target) == "..." {
					t.Fatalf("%q produced a false target: %v", test.command, targets)
				}
			}
		})
	}
}

// TestShellASTDoesNotTreatProgramQuotingAsPath keeps a quoted literal that is
// not a clean path out of the approval scope while a real quoted path stays.
func TestShellASTDoesNotTreatProgramQuotingAsPath(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	parsed, err := parseShellCommand(`jq '//' in.json && printf x > "` + filepath.Join(root, "a b.txt") + `"`)
	if err != nil {
		t.Fatal(err)
	}
	targets := resolveShellTargets(parsed, root)
	for _, target := range targets {
		if target == "/" || filepath.Base(target) == "//" {
			t.Fatalf("a program literal became a path target: %v", targets)
		}
	}
	found := false
	for _, target := range targets {
		if filepath.Base(target) == "a b.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a genuinely quoted path was dropped: %v", targets)
	}
}

// TestShellASTNetworkClassification marks a download command as a network
// effect so the host never treats its destination as a local file scope.
func TestShellASTNetworkClassification(t *testing.T) {
	parsed, err := parseShellCommand(`curl -fsSL https://example.com/pkg.tgz -o pkg.tgz`)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Network {
		t.Fatal("curl was not classified as a network command")
	}
	for _, executable := range parsed.Executables {
		if executable == "curl" {
			return
		}
	}
	t.Fatalf("executables = %v", parsed.Executables)
}

// TestShellASTFallbackKeepsScanning proves an unparseable command still gets a
// conservative token scan instead of running with no scope at all.
func TestShellASTFallbackKeepsScanning(t *testing.T) {
	if _, err := parseShellCommand("cat <<'EOF'"); err == nil {
		t.Fatal("an incomplete heredoc unexpectedly parsed")
	}
	home, _ := os.UserHomeDir()
	tokens := shellPathTokens("cat /tmp/secret.txt " + filepath.Join(home, ".ssh/id_ed25519"))
	if len(tokens) < 2 {
		t.Fatalf("fallback tokens = %v", tokens)
	}
}
