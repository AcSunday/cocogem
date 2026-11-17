package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRunTestFilterAndKindAreValidated is the A13 enum regression: an unknown
// toolchain, kind or an unsafe filter is rejected before any process starts.
func TestRunTestFilterAndKindAreValidated(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module probe\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, in := range []RunTestInput{
		{Toolchain: "python"},
		{Toolchain: "go", Kind: "smoke"},
		{Toolchain: "go", Filter: "TestSomething; rm -rf /"},
		{Toolchain: "go", Filter: strings.Repeat("x", 300)},
	} {
		if _, err := RunTest(context.Background(), root, in); err == nil {
			t.Fatalf("unsafe run_test input was accepted: %+v", in)
		}
	}
}

// TestAutoDetectsTheProjectToolchain proves auto reads a real project manifest
// instead of requiring the model to guess.
func TestAutoDetectsTheProjectToolchain(t *testing.T) {
	for _, test := range []struct {
		file, want string
	}{
		{"go.mod", ToolchainGo},
		{"package.json", ToolchainNPM},
		{"pnpm-lock.yaml", ToolchainPNPM},
		{"yarn.lock", ToolchainYarn},
		{"pyproject.toml", ToolchainPytest},
		{"Cargo.toml", ToolchainCargo},
	} {
		t.Run(test.file, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, test.file), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := detectToolchain(root, ".")
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("%s detected %q, want %q", test.file, got, test.want)
			}
		})
	}
	if _, err := detectToolchain(t.TempDir(), "."); err == nil {
		t.Fatal("auto accepted a directory without a manifest")
	}
}

// TestGoFilterProducesARealSingleTestCommand is the TDD red/green regression:
// filter becomes `-run <name>` and the command actually selects that test.
func TestGoFilterProducesARealSingleTestCommand(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is unavailable")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the verified sandbox is macOS-only")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module probe\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe_test.go"), []byte(`package probe

import "testing"

func TestAlpha(t *testing.T) { t.Log("alpha ran") }
func TestBeta(t *testing.T) { t.Log("beta ran") }
`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := RunTest(context.Background(), root, RunTestInput{Toolchain: ToolchainGo, Kind: TestKindTest, Filter: "TestAlpha", TimeoutSeconds: 120})
	if err != nil {
		t.Fatalf("filtered go test failed to start: %v", err)
	}
	if !result.Passed {
		t.Fatalf("filtered go test did not pass: %+v\n%s", result, result.Output)
	}
	if result.Filter != "TestAlpha" || !strings.Contains(result.Command, "-run") {
		t.Fatalf("filter was not applied to the command: %+v", result)
	}
	if strings.Contains(result.Output, "beta ran") {
		t.Fatalf("the filter ran an unrelated test:\n%s", result.Output)
	}
}

// TestKindIsSupportingEvidenceOnly keeps a typecheck/lint/build run from being
// presented as proof of runtime behaviour.
func TestKindIsSupportingEvidenceOnly(t *testing.T) {
	if kindEvidenceNote(TestKindBuild) == "" || kindEvidenceNote(TestKindLint) == "" {
		t.Fatal("a build/lint run did not state its evidence limit")
	}
	if kindEvidenceNote(TestKindTest) != "" {
		t.Fatal("a real test was marked as supporting evidence only")
	}
	for _, in := range []RunTestInput{
		{Toolchain: ToolchainGo, Kind: TestKindLint},
		{Toolchain: ToolchainPytest, Kind: TestKindLint},
	} {
		if _, err := toolchainCommand(in.Kind, in.Toolchain, ""); err != nil && in.Toolchain == ToolchainPytest {
			if !strings.Contains(err.Error(), "not a pytest capability") {
				t.Fatalf("pytest lint error = %v", err)
			}
		}
	}
}
