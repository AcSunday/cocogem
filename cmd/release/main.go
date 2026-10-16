// Command release builds reproducible local archives without publishing them.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if err := release(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func release() error {
	var sums strings.Builder
	for _, target := range []struct{ os, arch string }{{"darwin", "arm64"}, {"linux", "amd64"}} {
		name := "cocogem-0.1.0-" + target.os + "-" + target.arch
		dir := filepath.Join("dist", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(dir, "cocogem-server"), "./cmd/cocogem-server")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		if err := copyTree("web/dist", filepath.Join(dir, "web")); err != nil {
			return err
		}
		for _, file := range []string{"LICENSE", "README.md", "SECURITY.md", "THIRD_PARTY_NOTICES.txt"} {
			b, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, file), b, 0644); err != nil {
				return err
			}
		}
		if err := copyTree("docs", filepath.Join(dir, "docs")); err != nil {
			return err
		}
		if err := copyTree("examples", filepath.Join(dir, "examples")); err != nil {
			return err
		}
		archive := filepath.Join("dist", name+".tar.gz")
		if err := archiveTree(dir, archive); err != nil {
			return err
		}
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(hash.Sum(nil)), filepath.Base(archive))
		fmt.Printf("Built %s (execution verified only on %s/%s)\n", archive, runtime.GOOS, runtime.GOARCH)
	}
	return os.WriteFile("dist/SHA256SUMS", []byte(sums.String()), 0644)
}
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, relative)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported release asset: %s", path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0644)
	})
}
func archiveTree(dir, destination string) error {
	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.Dir(dir), path)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: filepath.ToSlash(relative), Mode: int64(info.Mode().Perm()), Size: info.Size()}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	return errors.Join(err, tw.Close(), gz.Close(), f.Close())
}
