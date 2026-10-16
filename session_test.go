package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const testSessionID = "12345678-1234-1234-1234-123456789abc"

func TestSessionRoundTrip(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	for _, cli := range []string{"claude", "codex", "gemini"} {
		if err := saveSession(dir, Result{CLI: cli, SessionID: testSessionID}); err != nil {
			t.Fatal(err)
		}
		id, err := loadSession(dir, cli)
		if err != nil || id != testSessionID {
			t.Fatalf("%s: session = %q, error = %v", cli, id, err)
		}
		info, err := os.Stat(filepath.Join(dir, cli+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("session file mode = %v, want 0600", info.Mode().Perm())
		}
	}
	if err := saveSession(dir, Result{CLI: "claude", SessionID: "invalid"}); err == nil {
		t.Fatal("invalid session should not overwrite saved data")
	}
	id, err := loadSession(dir, "claude")
	if err != nil || id != testSessionID {
		t.Fatalf("previous session was lost: ID = %q, error = %v", id, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 3 {
		t.Fatalf("unexpected files after save: count = %d, error = %v", len(files), err)
	}
}

func TestLoadSessionErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
	}{
		{name: "missing"},
		{name: "corrupt", data: "{"},
		{name: "wrong CLI", data: `{"cli":"codex","session_id":"` + testSessionID + `"}`},
		{name: "invalid ID", data: `{"cli":"claude","session_id":"--other-option"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tt.data != "" {
				if err := os.WriteFile(filepath.Join(dir, "claude.json"), []byte(tt.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadSession(dir, "claude"); err == nil {
				t.Fatal("invalid saved session should fail")
			}
		})
	}
	if _, err := sessionPath(t.TempDir(), "../../escape"); err == nil {
		t.Fatal("unknown CLI must not become a file path")
	}
}

func TestSaveSessionRenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "claude.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveSession(dir, Result{CLI: "claude", SessionID: testSessionID}); err == nil {
		t.Fatal("replacing a directory should fail")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "claude.json" {
		t.Fatalf("temporary session file leaked: files = %v, error = %v", files, err)
	}
}
