// Package tools implements bounded host-owned operations on untrusted input.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// DeniedPath identifies known credentials and irrelevant or private directories.
func DeniedPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		p := strings.ToLower(part)
		if p == ".git" || p == ".cocogem" || p == "node_modules" || p == "dist" || p == "build" || p == "secrets" || p == ".ssh" || strings.HasPrefix(p, ".env") || strings.HasSuffix(p, ".pem") || strings.HasSuffix(p, ".key") || strings.HasPrefix(p, "id_rsa") || strings.HasSuffix(p, ".sqlite") || strings.HasPrefix(p, ".git-credentials") {
			return true
		}
	}
	return false
}

func openPath(root, path string) (*os.Root, string, error) {
	if root == "" {
		return nil, "", errors.New("no source directory is bound")
	}
	if path == "" {
		path = "."
	}
	if !filepath.IsLocal(path) || DeniedPath(path) {
		return nil, "", errors.New("path is outside the allowed source scope")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", errors.New("source directory is unavailable")
	}
	current := ""
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := r.Lstat(current)
		if err != nil {
			r.Close()
			return nil, "", errors.New("source path is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			r.Close()
			return nil, "", errors.New("symbolic links are not supported")
		}
	}
	return r, filepath.Clean(path), nil
}

// ReadFile captures exact bytes and line coordinates within an os.Root boundary.
func ReadFile(ctx context.Context, root, path string, start, end int) (w.Source, error) {
	if err := ctx.Err(); err != nil {
		return w.Source{}, err
	}
	r, local, err := openPath(root, path)
	if err != nil {
		return w.Source{}, err
	}
	defer r.Close()
	f, err := r.Open(local)
	if err != nil {
		return w.Source{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return w.Source{}, err
	}
	if !info.Mode().IsRegular() {
		return w.Source{}, errors.New("only regular text files are readable")
	}
	if info.Size() > 1024*1024 {
		return w.Source{}, errors.New("file exceeds 1 MiB read budget")
	}
	b, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil {
		return w.Source{}, err
	}
	if len(b) > 1024*1024 {
		return w.Source{}, errors.New("file exceeds read budget")
	}
	if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return w.Source{}, errors.New("binary files are not supported")
	}
	lines := strings.Split(string(b), "\n")
	if start == 0 {
		start = 1
	}
	if start < 1 || start > len(lines) || end < 0 || (end != 0 && end < start) {
		return w.Source{}, errors.New("invalid line range")
	}
	if end == 0 || end > len(lines) {
		end = len(lines)
	}
	truncated := false
	if end-start+1 > 500 {
		end = start + 499
		truncated = true
	}
	var out strings.Builder
	actualEnd := start - 1
	for i := start - 1; i < end; i++ {
		if err := ctx.Err(); err != nil {
			return w.Source{}, err
		}
		line := fmt.Sprintf("%d: %s\n", i+1, lines[i])
		if out.Len()+len(line) > 32768 {
			truncated = true
			break
		}
		out.WriteString(line)
		actualEnd = i + 1
	}
	sum := sha256.Sum256(b)
	return w.Source{Kind: "file", Title: filepath.ToSlash(local), Path: filepath.ToSlash(local), StartLine: start, EndLine: actualEnd, SHA256: hex.EncodeToString(sum[:]), Content: out.String(), CapturedAt: w.Now(), Truncated: truncated}, nil
}

// ListFiles lists one allowed directory without following symbolic links.
func ListFiles(ctx context.Context, root, path string) ([]map[string]string, bool, error) {
	r, local, err := openPath(root, path)
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	f, err := r.Open(local)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	entries, err := f.ReadDir(501)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	out := []map[string]string{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if e.Type()&os.ModeSymlink != 0 || DeniedPath(filepath.Join(local, e.Name())) {
			continue
		}
		kind := "file"
		if e.IsDir() {
			kind = "directory"
		}
		out = append(out, map[string]string{"name": e.Name(), "type": kind})
	}
	return out, len(entries) > 500, nil
}

// SearchContent performs literal text search with bounded scan size and results.
func SearchContent(ctx context.Context, root, path, query string) ([]map[string]any, bool, error) {
	if query == "" || len(query) > 2000 {
		return nil, false, errors.New("query must contain 1–2000 bytes")
	}
	r, local, err := openPath(root, path)
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	out := []map[string]any{}
	bytesRead := 0
	files := 0
	truncated := false
	err = fs.WalkDir(r.FS(), filepath.ToSlash(local), func(path string, e fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if DeniedPath(path) {
			if e.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if e.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if e.IsDir() {
			return nil
		}
		files++
		if files > 1000 || bytesRead >= 8*1024*1024 || len(out) >= 50 {
			truncated = true
			return fs.SkipAll
		}
		f, err := r.Open(path)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			f.Close()
			return nil
		}
		b, readErr := io.ReadAll(io.LimitReader(f, 1024*1024+1))
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		bytesRead += len(b)
		if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, query) {
				if len(line) > 1000 {
					line = line[:1000]
				}
				out = append(out, map[string]any{"path": path, "line": i + 1, "text": line})
				if len(out) >= 50 {
					truncated = true
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	return out, truncated, err
}
