package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

// writeMu serializes this process's writes and evidence reads for the same
// server. It cannot coordinate another process editing the project; see the
// documented external-writer limit on WriteFile.
var writeMu sync.Mutex

// MaxWriteBytes bounds one complete-file write. It stays below the scoped
// MCP argument boundary and the model step output budget.
const MaxWriteBytes = 32 << 10

// maxPatchFileBytes bounds the existing and resulting file for patch_file and
// the whole-file hash used by diff evidence. The public write_file budget
// remains MaxWriteBytes; patch_file carries only a bounded fragment.
const maxPatchFileBytes = 1 << 20

// MaxDiffBytes bounds the captured unified diff. Longer diffs are marked
// truncated; the post-write SHA-256 still identifies the exact final content.
const MaxDiffBytes = 32 << 10

type WriteFileInput struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256"`
}

// PatchFileInput names one exact, unique substring replacement.
type PatchFileInput struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
	OldText        string `json:"old_text"`
	NewText        string `json:"new_text"`
}

// WriteResult is the actual filesystem evidence for one accepted write.
// Reason is set only when a retry is recognized as already applied.
type WriteResult struct {
	Path           string
	Changed        bool
	Bytes          int
	SHA256         string
	PreviousSHA256 string
	Diff           string
	DiffTruncated  bool
	Reason         string
}

// WriteFile performs one controlled, atomic UTF-8 file write below root. The
// caller must already have authorized this step's role and action; this layer
// enforces path confinement and expected-content custody. Writers inside this
// process are serialized; a concurrently writing external process is detected
// before replacement but cannot be excluded atomically (no CAS exists here).
func WriteFile(ctx context.Context, root string, in WriteFileInput) (WriteResult, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	return writeFileLocked(ctx, root, in, MaxWriteBytes)
}

// PatchFile replaces one exact, unique old_text occurrence in an existing file.
// It reuses WriteFile's validation, write lock, atomic commit and diff evidence;
// only the whole-file size limit is wider so ordinary large sources can be
// edited without sending their complete body through the MCP boundary.
func PatchFile(ctx context.Context, root string, in PatchFileInput) (WriteResult, error) {
	if in.OldText == "" {
		return WriteResult{}, errors.New("old_text must not be empty")
	}
	if len(in.OldText)+len(in.NewText) > MaxWriteBytes {
		return WriteResult{}, errors.New("old_text and new_text together exceed the 32 KiB patch budget")
	}
	if !utf8.ValidString(in.OldText) || !utf8.ValidString(in.NewText) || strings.ContainsRune(in.OldText, 0) || strings.ContainsRune(in.NewText, 0) {
		return WriteResult{}, errors.New("only UTF-8 text without NUL bytes may be patched")
	}
	expected := strings.ToLower(strings.TrimSpace(in.ExpectedSHA256))
	if !validSHA256(expected) {
		return WriteResult{}, errors.New("expected_sha256 must be the current complete-file SHA-256")
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	target, err := inspectWriteTarget(root, in.Path, maxPatchFileBytes)
	if err != nil {
		return WriteResult{}, err
	}
	if !target.exists {
		return WriteResult{}, errors.New("patch_file requires an existing file; use write_file to create a new path")
	}
	if target.sha != expected {
		// A repeated identical patch is recognized only when the current file
		// provably is the intended result: old_text is gone, the inverse
		// replacement reconstructs the expected content, and applying the patch
		// to that reconstruction yields the current bytes. Any weaker signal
		// stays a stale-hash error rather than guessing that a write happened.
		if patchAlreadyApplied(target.content, expected, in.OldText, in.NewText) {
			return WriteResult{Path: filepath.ToSlash(target.local), Bytes: len(target.content), SHA256: target.sha, PreviousSHA256: target.sha, Reason: "patch was already applied; the current file already matches the expected result"}, nil
		}
		return WriteResult{}, errors.New("expected_sha256 does not match the current file; re-read the file and retry")
	}
	count := strings.Count(string(target.content), in.OldText)
	if count == 0 {
		return WriteResult{}, errors.New("old_text was not found in the current file; re-read the file and retry")
	}
	if count > 1 {
		return WriteResult{}, fmt.Errorf("old_text matches %d times; provide a longer fragment that occurs exactly once", count)
	}
	content := strings.Replace(string(target.content), in.OldText, in.NewText, 1)
	if len(content) > maxPatchFileBytes {
		return WriteResult{}, errors.New("patched file would exceed the 1 MiB file budget")
	}
	return writeFileLocked(ctx, root, WriteFileInput{Path: in.Path, Content: content, ExpectedSHA256: expected}, maxPatchFileBytes)
}

// writeFileLocked runs the shared committed-write path under writeMu.
func writeFileLocked(ctx context.Context, root string, in WriteFileInput, maxBytes int) (WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	if root == "" {
		return WriteResult{}, errors.New("no source directory is bound")
	}
	local, err := validateWritePath(in.Path)
	if err != nil {
		return WriteResult{}, err
	}
	if len(in.Content) > maxBytes {
		return WriteResult{}, fmt.Errorf("content exceeds the %d KiB file budget", maxBytes>>10)
	}
	if !utf8.ValidString(in.Content) || strings.ContainsRune(in.Content, 0) {
		return WriteResult{}, errors.New("only UTF-8 text without NUL bytes may be written")
	}
	expected := strings.ToLower(strings.TrimSpace(in.ExpectedSHA256))
	if expected != "" && !validSHA256(expected) {
		return WriteResult{}, errors.New("expected_sha256 must be a 64-character hexadecimal SHA-256")
	}
	r, err := openWriteRoot(root, local)
	if err != nil {
		return WriteResult{}, err
	}
	defer r.Close()
	oldContent, mode, exists, err := readWriteTarget(r, local, maxBytes)
	if err != nil {
		return WriteResult{}, err
	}
	content := []byte(in.Content)
	sum := sha256.Sum256(content)
	desired := hex.EncodeToString(sum[:])
	result := WriteResult{Path: filepath.ToSlash(local), Bytes: len(content), SHA256: desired}
	oldSHA := ""
	if exists {
		oldSum := sha256.Sum256(oldContent)
		oldSHA = hex.EncodeToString(oldSum[:])
		result.PreviousSHA256 = oldSHA
		if oldSHA == desired {
			// A retry of the exact same content is idempotent and must not
			// rewrite the file or create a second diff.
			return result, nil
		}
		if expected == "" {
			return WriteResult{}, errors.New("the file already exists; expected_sha256 is required to prevent overwriting user changes")
		}
		if expected != oldSHA {
			return WriteResult{}, errors.New("expected_sha256 does not match the current file; re-read the file and retry")
		}
	} else if expected != "" {
		return WriteResult{}, errors.New("expected_sha256 was provided but the file does not exist")
	}
	result.Diff, result.DiffTruncated = unifiedDiff(oldContent, content, result.Path, MaxDiffBytes)
	temp, err := writeTempFile(r, local, content, mode)
	if err != nil {
		return WriteResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = r.Remove(temp)
		}
	}()
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	if exists {
		// Overwrite: re-read immediately before the atomic rename. The write
		// lock excludes same-process writers; an external process can still
		// replace the file inside this last window, so this is detection, not a
		// compare-and-swap guarantee.
		current, _, nowExists, err := readWriteTarget(r, local, maxBytes)
		if err != nil {
			return WriteResult{}, err
		}
		nowSHA := ""
		if nowExists {
			nowSum := sha256.Sum256(current)
			nowSHA = hex.EncodeToString(nowSum[:])
		}
		if !nowExists || nowSHA != oldSHA {
			if nowExists && nowSHA == desired {
				return result, nil
			}
			return WriteResult{}, errors.New("the file changed while writing; refusing to overwrite the newer content")
		}
		if err := r.Rename(temp, local); err != nil {
			return WriteResult{}, errors.New("atomic rename failed")
		}
		committed = true
	} else {
		// Create: link is an atomic no-clobber commit. If another writer created
		// the path after the absence check, the link fails instead of replacing
		// that file.
		if err := r.Link(temp, local); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return WriteResult{}, errors.New("could not commit the new file atomically")
			}
			created, _, nowExists, readErr := readWriteTarget(r, local, maxBytes)
			if readErr == nil && nowExists {
				nowSum := sha256.Sum256(created)
				if hex.EncodeToString(nowSum[:]) == desired {
					return result, nil
				}
			}
			return WriteResult{}, errors.New("the file was created while writing; refusing to replace it")
		}
		if err := r.Remove(temp); err != nil {
			return WriteResult{}, errors.New("could not finalize the new file")
		}
		committed = true
	}
	info, err := r.Lstat(local)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(content)) {
		return WriteResult{}, errors.New("written file could not be verified")
	}
	result.Changed = true
	return result, nil
}

// CurrentSHA256 returns the current hash of one allowed file for evidence
// revalidation. It applies the same symlink, hard-link and regular-file rules.
func CurrentSHA256(root, path string) (string, error) {
	if root == "" || path == "" || !filepath.IsLocal(path) || DeniedPath(path) {
		return "", errors.New("path is outside the allowed source scope")
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", errors.New("source directory is unavailable")
	}
	defer r.Close()
	if err := checkWriteParents(r, filepath.Clean(path)); err != nil {
		return "", err
	}
	content, _, exists, err := readWriteTarget(r, filepath.Clean(path), maxPatchFileBytes)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", errors.New("file no longer exists")
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

// WorkspaceDigest summarizes every delivered project file (relative path, mode,
// size and content hash) under the same denied-path policy as the file tools.
// run_test calls it immediately before and after the test and exposes the
// digest only when both calls complete and match; the engineering gate also
// compares it with the current tree. A test that rewrites delivered files is
// reported as changed and cannot be cited as if it verified the delivered
// content. Unreadable or oversized trees fail instead of producing a digest
// that would overstate evidence.
func WorkspaceDigest(root string) (string, error) {
	if root == "" {
		return "", errors.New("no source directory is bound")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("source directory is unavailable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("source directory is unavailable")
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	r, err := os.OpenRoot(resolved)
	if err != nil {
		return "", errors.New("source directory is unavailable")
	}
	defer r.Close()
	type entry struct {
		path string
		mode os.FileMode
		size int64
		sum  [sha256.Size]byte
	}
	entries := []entry{}
	files := 0
	bytes := int64(0)
	err = filepath.WalkDir(resolved, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == resolved {
			return nil
		}
		relative, relErr := filepath.Rel(resolved, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if DeniedPath(relative) {
			if item.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, infoErr := item.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		files++
		if files > maxDigestFiles {
			return errDigestTooLarge
		}
		bytes += info.Size()
		if bytes > maxDigestBytes {
			return errDigestTooLarge
		}
		f, openErr := r.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			return openErr
		}
		hasher := sha256.New()
		copied, copyErr := io.Copy(hasher, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if copied != info.Size() {
			return errors.New("project changed while its evidence digest was computed")
		}
		var sum [sha256.Size]byte
		copy(sum[:], hasher.Sum(nil))
		entries = append(entries, entry{path: relative, mode: info.Mode().Perm(), size: copied, sum: sum})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	manifest := sha256.New()
	for _, item := range entries {
		fmt.Fprintf(manifest, "%s\x00%o\x00%d\x00%x\n", item.path, item.mode, item.size, item.sum)
	}
	return hex.EncodeToString(manifest.Sum(nil)), nil
}

const (
	maxDigestFiles = 20000
	maxDigestBytes = 512 << 20
)

var errDigestTooLarge = errors.New("project tree exceeds the workspace digest limit")

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// validateWritePath is the single relative-path policy for every committed
// write; patch_file and write_file reach it through the same helpers.
func validateWritePath(path string) (string, error) {
	if path == "" || len(path) > 512 || !filepath.IsLocal(path) || filepath.Clean(path) == "." || strings.HasSuffix(path, "/") || DeniedPath(path) {
		return "", errors.New("path is outside the allowed write scope")
	}
	return filepath.Clean(path), nil
}

func openWriteRoot(root, local string) (*os.Root, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, errors.New("source directory is unavailable")
	}
	if err := checkWriteParents(r, local); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

type writeTarget struct {
	local   string
	content []byte
	exists  bool
	sha     string
}

// inspectWriteTarget applies the shared path and regular-file policy, then
// returns the current bytes and whole-file hash. It never writes.
func inspectWriteTarget(root, path string, maxBytes int) (writeTarget, error) {
	local, err := validateWritePath(path)
	if err != nil {
		return writeTarget{}, err
	}
	r, err := openWriteRoot(root, local)
	if err != nil {
		return writeTarget{}, err
	}
	defer r.Close()
	content, _, exists, err := readWriteTarget(r, local, maxBytes)
	if err != nil {
		return writeTarget{}, err
	}
	target := writeTarget{local: local, content: content, exists: exists}
	if exists {
		sum := sha256.Sum256(content)
		target.sha = hex.EncodeToString(sum[:])
	}
	return target, nil
}

// patchAlreadyApplied proves a retry from filesystem content alone: the old
// fragment is gone, the inverse replacement reproduces the expected hash, and
// the forward replacement reproduces the current bytes exactly.
func patchAlreadyApplied(current []byte, expected, oldText, newText string) bool {
	if oldText == "" || newText == "" {
		return false
	}
	text := string(current)
	if strings.Contains(text, oldText) || strings.Count(text, newText) != 1 {
		return false
	}
	prior := strings.Replace(text, newText, oldText, 1)
	sum := sha256.Sum256([]byte(prior))
	return hex.EncodeToString(sum[:]) == expected &&
		strings.Count(prior, oldText) == 1 &&
		strings.Replace(prior, oldText, newText, 1) == text
}

// checkWriteParents rejects symbolic links, non-directories and denied
// components for every directory leading to the target.
func checkWriteParents(r *os.Root, local string) error {
	dir := filepath.Dir(local)
	current := ""
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return errors.New("path is outside the allowed write scope")
		}
		current = filepath.Join(current, part)
		if DeniedPath(current) {
			return errors.New("path is outside the allowed write scope")
		}
		info, err := r.Lstat(current)
		if err != nil {
			return errors.New("parent directory is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not supported")
		}
		if !info.IsDir() {
			return errors.New("parent path is not a directory")
		}
	}
	return nil
}

// readWriteTarget opens the destination without following links and returns
// its exact bytes and mode. Missing files are a valid create precondition.
func readWriteTarget(r *os.Root, local string, maxBytes int) ([]byte, os.FileMode, bool, error) {
	if DeniedPath(local) {
		return nil, 0, false, errors.New("path is outside the allowed write scope")
	}
	info, err := r.Lstat(local)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, errors.New("target path is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, false, errors.New("symbolic links are not supported")
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, errors.New("only regular files may be written")
	}
	if linkCount(info) > 1 {
		return nil, 0, false, errors.New("hard-linked files are not writable")
	}
	f, err := r.OpenFile(local, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, false, errors.New("target file is unreadable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || linkCount(opened) > 1 {
		return nil, 0, false, errors.New("only regular, non-hard-linked files may be written")
	}
	if opened.Size() > int64(maxBytes) {
		return nil, 0, false, fmt.Errorf("existing file exceeds the %d KiB file budget", maxBytes>>10)
	}
	content, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil || len(content) > maxBytes {
		return nil, 0, false, errors.New("existing file could not be read within the file budget")
	}
	if !utf8.Valid(content) || strings.ContainsRune(string(content), 0) {
		return nil, 0, false, errors.New("existing file is not UTF-8 text")
	}
	return content, opened.Mode(), true, nil
}

func writeTempFile(r *os.Root, local string, content []byte, mode os.FileMode) (string, error) {
	dir := filepath.Dir(local)
	for attempt := 0; attempt < 5; attempt++ {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", errors.New("could not allocate a temporary write file")
		}
		temp := filepath.Join(dir, ".cocogem-write-"+hex.EncodeToString(random[:]))
		f, err := r.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", errors.New("could not create a temporary write file")
		}
		if mode != 0 {
			_ = f.Chmod(mode.Perm())
		} else {
			_ = f.Chmod(0644)
		}
		if _, err := f.Write(content); err != nil {
			f.Close()
			_ = r.Remove(temp)
			return "", errors.New("could not write the temporary file")
		}
		if err := f.Sync(); err != nil {
			f.Close()
			_ = r.Remove(temp)
			return "", errors.New("could not flush the temporary file")
		}
		if err := f.Close(); err != nil {
			_ = r.Remove(temp)
			return "", errors.New("could not close the temporary file")
		}
		return temp, nil
	}
	return "", errors.New("could not allocate a unique temporary write file")
}

// unifiedDiff emits one bounded unified-diff hunk from an LCS line diff. Very
// large inputs fall back to a whole-file rewrite hunk rather than exhausting
// memory; the content hashes remain exact.
func unifiedDiff(oldContent, newContent []byte, path string, maxBytes int) (string, bool) {
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)
	var ops []diffOp
	if len(oldLines) > 0 && len(newLines) > 0 && len(oldLines)*len(newLines) <= 1_000_000 {
		ops = lineDiff(oldLines, newLines)
	} else {
		for _, line := range oldLines {
			ops = append(ops, diffOp{kind: '-', text: line})
		}
		for _, line := range newLines {
			ops = append(ops, diffOp{kind: '+', text: line})
		}
	}
	first, last := -1, -1
	for i, op := range ops {
		if op.kind == ' ' {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 {
		return "", false
	}
	start, end := max(0, first-3), min(len(ops), last+4)
	oldStart, newStart := 1, 1
	oldCount, newCount := 0, 0
	for i, op := range ops {
		if i >= start && i < end {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
			continue
		}
		if i < start {
			if op.kind != '+' {
				oldStart++
			}
			if op.kind != '-' {
				newStart++
			}
		}
	}
	var out strings.Builder
	oldName := "a/" + path
	if len(oldLines) == 0 {
		oldName = "/dev/null"
	}
	fmt.Fprintf(&out, "--- %s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", oldName, path, oldStart, oldCount, newStart, newCount)
	for _, op := range ops[start:end] {
		out.WriteByte(op.kind)
		out.WriteString(op.text)
		if !strings.HasSuffix(op.text, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
	value := out.String()
	if len(value) > maxBytes {
		return utf8Prefix(value, maxBytes), true
	}
	return value, false
}

type diffOp struct {
	kind byte
	text string
}

func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	value := string(content)
	lines := strings.SplitAfter(value, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// lineDiff returns a shortest-edit-script line diff within the callers' size cap.
func lineDiff(a, b []string) []diffOp {
	rows := len(a) + 1
	cols := len(b) + 1
	table := make([][]int32, rows)
	for i := range table {
		table[i] = make([]int32, cols)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	ops := make([]diffOp, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{kind: ' ', text: a[i]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, diffOp{kind: '-', text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', text: b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{kind: '-', text: a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{kind: '+', text: b[j]})
	}
	return ops
}

// SensitivePath marks project content that even the test sandbox must not
// expose. It is intentionally narrower than DeniedPath: dependency and build
// directories remain usable by projects' own tests. Every SensitivePath
// component is also a DeniedPath component; the shared policy lists make that
// structural rather than duplicated.
func SensitivePath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if sensitiveComponent(part) {
			return true
		}
	}
	return false
}

// nlink helpers live in engineering_unix.go / engineering_other.go.
