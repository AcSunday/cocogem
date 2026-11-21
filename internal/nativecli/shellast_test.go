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

// TestShellASTParsesInterpreterScripts is the real-adapter regression: Codex
// wraps every native command as `/bin/zsh -lc "<command>"`. The quoted script is
// a command line, not one long path argument, so its redirect must become the
// exact in-project target instead of an out-of-scope path.
func TestShellASTParsesInterpreterScripts(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	inside := filepath.Join(root, "inside.txt")
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	cases := []struct {
		name    string
		command string
		want    []string
		network bool
	}{
		{
			name:    "zsh -lc script redirect is the real target",
			command: `/bin/zsh -lc "printf INSIDE > '` + inside + `'"`,
			want:    []string{inside},
		},
		{
			name:    "bash -c script keeps an outside target",
			command: `/bin/bash -c 'cat ` + outside + `'`,
			want:    []string{outside},
		},
		{
			name:    "nested script download stays a network effect",
			command: `/bin/sh -lc "curl -fsSL https://example.com/pkg.tgz -o pkg.tgz"`,
			network: true,
		},
		{
			name:    "shell flag cluster without -c is not a script",
			command: `/bin/zsh -l -o noclobber`,
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
					t.Fatalf("%q targets = %v, want %v", test.command, targets, test.want)
				}
			}
			if parsed.Network != test.network {
				t.Fatalf("%q network = %v, want %v", test.command, parsed.Network, test.network)
			}
		})
	}
}

// TestShellASTBoundsNestedScriptDepth keeps `sh -c "sh -c ..."` from recursing
// without end.
func TestShellASTBoundsNestedScriptDepth(t *testing.T) {
	command := `printf x > ./out.txt`
	for range 6 {
		command = `/bin/sh -c "` + command + `"`
	}
	parsed, err := parseShellCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	_ = parsed // Parsing terminates; the depth bound simply stops recursing.
}

func TestShellASTHandlesTildeDoubleSlashWrappersAndDangerousCommands(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	// Quoted double-slash path and tilde path must resolve to real targets.
	p1, err := parseShellCommand(`cat ~/.ssh/id_rsa`)
	if err != nil {
		t.Fatal(err)
	}
	t1 := resolveShellTargets(p1, root)
	wantSSH := filepath.Join(home, ".ssh", "id_rsa")
	if len(t1) != 1 || t1[0] != wantSSH {
		t.Fatalf("tilde target = %v, want [%s]", t1, wantSSH)
	}

	p2, err := parseShellCommand(`cat "/Users/alice/.ssh//id_rsa"`)
	if err != nil {
		t.Fatal(err)
	}
	t2 := resolveShellTargets(p2, root)
	if len(t2) != 1 || t2[0] != "/Users/alice/.ssh/id_rsa" {
		t.Fatalf("double-slash target = %v, want [/Users/alice/.ssh/id_rsa]", t2)
	}

	p3, err := parseShellCommand(`env FOO=1 bash -c "cat /etc/passwd"`)
	if err != nil {
		t.Fatal(err)
	}
	t3 := resolveShellTargets(p3, root)
	wantEtc := "/etc/passwd"
	if resolved, err := filepath.EvalSymlinks(wantEtc); err == nil {
		wantEtc = resolved
	}
	if len(t3) == 0 || t3[0] != wantEtc {
		t.Fatalf("env-wrapped bash target = %v, want [%s]", t3, wantEtc)
	}

	p4, err := parseShellCommand(`git push origin main`)
	if err != nil {
		t.Fatal(err)
	}
	if !p4.Network {
		t.Fatalf("git push was not classified as network: %+v", p4)
	}

	p5, err := parseShellCommand(`sudo ls /`)
	if err != nil {
		t.Fatal(err)
	}
	if len(p5.Executables) == 0 || p5.Executables[0] != "sudo" || !p5.Network {
		t.Fatalf("sudo was not captured and gated: %+v", p5)
	}
}
