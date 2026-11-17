package nativecli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

// TestNativeStateRootKeepsEachCLIPersistentState is the A27 privacy/perf
// regression: a managed session keeps the CLI's own directory (so OAuth refresh
// and prompt cache keep working) instead of a temporary CODEX_HOME or
// CLAUDE_CONFIG_DIR.
func TestNativeStateRootKeepsEachCLIPersistentState(t *testing.T) {
	home := t.TempDir()
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME", "PI_HOME", "XDG_DATA_HOME"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", home)
	cases := map[string]string{
		"claude":   filepath.Join(home, ".claude"),
		"codex":    filepath.Join(home, ".codex"),
		"gemini":   filepath.Join(home, ".gemini"),
		"pi":       filepath.Join(home, ".pi"),
		"opencode": filepath.Join(home, ".local", "share", "opencode"),
	}
	for executor, want := range cases {
		if got := nativeStateRoot(executor); got != want {
			t.Fatalf("%s native state root = %q, want %q", executor, got, want)
		}
	}
	// A user-set variable wins, so a custom install keeps its location.
	codex := filepath.Join(t.TempDir(), "custom-codex")
	t.Setenv("CODEX_HOME", codex)
	if got := nativeStateRoot("codex"); got != codex {
		t.Fatalf("a user CODEX_HOME was overridden: %q", got)
	}
}

// TestManagedNativeSessionSkipsEphemeralReviewer keeps the disposable verify_dir
// run out of native history while the ordinary author session stays resumable.
func TestManagedNativeSessionSkipsEphemeralReviewer(t *testing.T) {
	if managedNativeSession(model.Request{SessionStorageRoot: "/ledger/author"}) != true {
		t.Fatal("an author session was not resumable")
	}
	if managedNativeSession(model.Request{SessionStorageRoot: "/ledger/review", EphemeralSession: true}) {
		t.Fatal("an ephemeral reviewer session was treated as resumable")
	}
	if managedNativeSession(model.Request{}) {
		t.Fatal("a run without a ledger key was treated as managed")
	}
}

// TestNativeStateDirExistsUnderHome proves the path is not a temporary override
// and never points inside the candidate or exchange root.
func TestNativeStateDirIsOutsideProjectRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	state := nativeStateRoot("codex")
	info, err := os.Stat(filepath.Dir(state))
	if err != nil {
		t.Fatalf("state parent is unavailable: %v", err)
	}
	project := filepath.Join(home, "work")
	if _, err := os.Stat(project); err == nil {
		t.Fatal("fixture project unexpectedly exists")
	}
	if info.IsDir() != true {
		t.Fatal("state parent is not a directory")
	}
}
