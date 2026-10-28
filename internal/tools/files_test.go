package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFilesEnforceScopeAndCapture(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("one\ntarget\nthree"), 0600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600)
	os.WriteFile(filepath.Join(root, "binary"), []byte{0, 1}, 0600)
	os.Symlink("hello.txt", filepath.Join(root, "link"))
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadFile(context.Background(), root, "hello.txt", 2, 2)
	if err != nil || s.Content != "2: target\n" || len(s.SHA256) != 64 {
		t.Fatal(s, err)
	}
	big := "first\n" + strings.Repeat("line\n", 10000)
	if err := os.WriteFile(filepath.Join(root, "big-text"), []byte(big), 0600); err != nil {
		t.Fatal(err)
	}
	excerpt, err := ReadFile(context.Background(), root, "big-text", 1, 1)
	if err != nil || excerpt.SHA256 != sha(big) || excerpt.Content == big {
		t.Fatalf("excerpt sha256 must cover the complete file: %q %v", excerpt.SHA256, err)
	}
	for _, p := range []string{"../outside", "/etc/passwd", ".env", "link", "binary", "pipe"} {
		t.Run(p, func(t *testing.T) {
			if _, err := ReadFile(context.Background(), root, p, 0, 0); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	hits, _, err := SearchContent(context.Background(), root, ".", "target")
	if err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
	hits, _, err = SearchContent(context.Background(), root, ".", "secret")
	if err != nil || len(hits) != 0 {
		t.Fatal("secret searched", hits, err)
	}
	os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("a", 1024*1024+1)), 0600)
	if _, err := ReadFile(context.Background(), root, "large", 0, 0); err == nil {
		t.Fatal("oversized file read")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadFile(ctx, root, "hello.txt", 0, 0); err == nil {
		t.Fatal("cancel ignored")
	}
}
