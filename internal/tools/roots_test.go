package tools

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestResolveSourceRoot(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "中文 项目")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "中文 项目-other")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "sources"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "README.md")
	if err := os.WriteFile(file, []byte("public"), 0600); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(base, "pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, input string
		allowed     []string
		want        string
		fail        bool
	}{
		{name: "unbound"},
		{name: "local settings without startup roots", input: directory, want: directory},
		{name: "absolute path canonicalized", input: directory + string(os.PathSeparator) + ".", want: directory},
		{name: "explicit directory", input: directory, allowed: []string{directory}, want: directory},
		{name: "subdirectory within limit", input: filepath.Join(directory, "sources"), allowed: []string{directory}, want: filepath.Join(directory, "sources")},
		{name: "relative path", input: "relative/project", fail: true},
		{name: "missing directory", input: filepath.Join(base, "missing"), fail: true},
		{name: "file", input: file, fail: true},
		{name: "named pipe", input: pipe, fail: true},
		{name: "outside explicit limit", input: base, allowed: []string{directory}, fail: true},
		{name: "prefix sibling outside limit", input: outside, allowed: []string{directory}, fail: true},
		{name: "invalid path", input: "/bad\x00path", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveSourceRoot(tc.input, tc.allowed)
			if (err != nil) != tc.fail || !tc.fail && got != tc.want {
				t.Fatalf("got=%q error=%v want=%q fail=%v", got, err, tc.want, tc.fail)
			}
		})
	}
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSourceRoot(alias, []string{directory}); err == nil {
		t.Fatal("symlink escaped explicit directory boundary")
	}
	if got, err := ResolveSourceRoot(alias, nil); err != nil || got != outside {
		t.Fatalf("explicit alias selection not canonicalized: %s %v", got, err)
	}
	for _, name := range []string{".ssh", ".cocogem", "secrets"} {
		private := filepath.Join(base, name)
		if err := os.Mkdir(private, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveSourceRoot(private, nil); err == nil {
			t.Fatalf("private directory %q accepted", name)
		}
	}
	t.Run("unreadable directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions")
		}
		unreadable := filepath.Join(base, "unreadable")
		if err := os.Mkdir(unreadable, 0700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(unreadable, 0700) })
		if err := os.Chmod(unreadable, 0000); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveSourceRoot(unreadable, nil); err == nil {
			t.Fatal("unreadable directory accepted")
		}
	})
}

func TestResolveSourceRootExpandsHome(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "public"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveSourceRoot("~/public", nil)
	if err != nil || got != filepath.Join(home, "public") {
		t.Fatalf("home input=%q err=%v", got, err)
	}
}

func TestValidateSourceRootRejectsChangedBinding(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "root")
	other := filepath.Join(base, "other")
	for _, p := range []string{root, other} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateSourceRoot(root, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, root); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSourceRoot(root, nil); err == nil {
		t.Fatal("saved path silently followed a replacement symlink")
	}
}
