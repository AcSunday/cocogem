package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestArchiveContainsOnlyRelativeAssets(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cocogem-test")
	os.MkdirAll(filepath.Join(dir, "web"), 0755)
	os.WriteFile(filepath.Join(dir, "web", "index.html"), []byte("hello"), 0644)
	archive := filepath.Join(root, "release.tar.gz")
	if err := archiveTree(dir, archive); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	r := tar.NewReader(gz)
	header, err := r.Next()
	if err != nil || header.Name != "cocogem-test/web/index.html" || filepath.IsAbs(header.Name) {
		t.Fatal(header, err)
	}
	b, _ := io.ReadAll(r)
	if string(b) != "hello" {
		t.Fatal(string(b))
	}
}

func TestReleaseRefusesUncommittedSource(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if err := requireCleanCheckout(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requireCleanCheckout(dir); err == nil {
		t.Fatal("dirty release accepted")
	}
}
