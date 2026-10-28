package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func sha(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestWriteFileCreateOverwriteAndIdempotentRetry(t *testing.T) {
	root := t.TempDir()
	created, err := WriteFile(context.Background(), root, WriteFileInput{Path: "calc.go", Content: "package calc\n\nfunc Add(a, b int) int { return a + b }\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Changed || created.SHA256 != sha("package calc\n\nfunc Add(a, b int) int { return a + b }\n") || created.PreviousSHA256 != "" {
		t.Fatalf("create result = %+v", created)
	}
	if !strings.Contains(created.Diff, "+func Add(a, b int) int { return a + b }") || !strings.Contains(created.Diff, "--- /dev/null") {
		t.Fatalf("new-file diff missing changed line: %s", created.Diff)
	}
	// Retrying the same content is idempotent even without a stale hash.
	retry, err := WriteFile(context.Background(), root, WriteFileInput{Path: "calc.go", Content: "package calc\n\nfunc Add(a, b int) int { return a + b }\n"})
	if err != nil || retry.Changed || retry.SHA256 != created.SHA256 || retry.Diff != "" {
		t.Fatalf("idempotent retry = %+v, %v", retry, err)
	}
	if _, err := WriteFile(context.Background(), root, WriteFileInput{Path: "calc.go", Content: "different\n"}); err == nil {
		t.Fatal("overwrite without expected_sha256 was accepted")
	}
	if _, err := WriteFile(context.Background(), root, WriteFileInput{Path: "calc.go", Content: "different\n", ExpectedSHA256: sha("wrong")}); err == nil {
		t.Fatal("stale expected_sha256 was accepted")
	}
	updated, err := WriteFile(context.Background(), root, WriteFileInput{Path: "calc.go", Content: "package calc\n\nfunc Add(a, b int) int { return a - b }\n", ExpectedSHA256: created.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Changed || updated.PreviousSHA256 != created.SHA256 || !strings.Contains(updated.Diff, "-func Add(a, b int) int { return a + b }") || !strings.Contains(updated.Diff, "+func Add(a, b int) int { return a - b }") {
		t.Fatalf("overwrite evidence = %+v", updated)
	}
	content, err := os.ReadFile(filepath.Join(root, "calc.go"))
	if err != nil || string(content) != "package calc\n\nfunc Add(a, b int) int { return a - b }\n" {
		t.Fatalf("written content = %q, %v", content, err)
	}
	if temp, err := filepath.Glob(filepath.Join(root, ".cocogem-write-*")); err != nil || len(temp) != 0 {
		t.Fatalf("temporary files leaked: %v %v", temp, err)
	}
	if current, err := CurrentSHA256(root, "calc.go"); err != nil || current != updated.SHA256 {
		t.Fatalf("CurrentSHA256 = %q, %v", current, err)
	}
}

func TestWriteFileRejectsUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "linked-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(outside, "secret.txt"), filepath.Join(root, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	expect := func(name string, in WriteFileInput) {
		t.Helper()
		if _, err := WriteFile(context.Background(), root, in); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	expect("parent traversal", WriteFileInput{Path: "../escape.txt", Content: "x"})
	expect("absolute path", WriteFileInput{Path: filepath.Join(root, "absolute.txt"), Content: "x"})
	expect("symlink parent", WriteFileInput{Path: "linked-dir/new.txt", Content: "x"})
	expect("symlink target", WriteFileInput{Path: "linked-file", Content: "x", ExpectedSHA256: sha("outside")})
	expect("hardlink target", WriteFileInput{Path: "hardlink.txt", Content: "x", ExpectedSHA256: sha("outside")})
	expect("sensitive path", WriteFileInput{Path: ".env", Content: "x"})
	expect("git metadata", WriteFileInput{Path: ".git/config", Content: "x"})
	expect("directory target", WriteFileInput{Path: "dir", Content: "x"})
	expect("missing parent", WriteFileInput{Path: "missing/new.txt", Content: "x"})
	expect("non-UTF8", WriteFileInput{Path: "binary.txt", Content: string([]byte{0xff, 0xfe})})
	expect("oversized", WriteFileInput{Path: "large.txt", Content: strings.Repeat("a", MaxWriteBytes+1)})
	expect("expected on missing file", WriteFileInput{Path: "new.txt", Content: "x", ExpectedSHA256: sha("x")})
}

func TestWriteFileHonorsCancellationBeforeWrite(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WriteFile(ctx, root, WriteFileInput{Path: "file.txt", Content: "x"}); err == nil {
		t.Fatal("canceled write was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "file.txt")); !os.IsNotExist(err) {
		t.Fatal("canceled write created a file")
	}
}

func TestWriteFileRejectsUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission bits")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0700)
	if _, err := WriteFile(context.Background(), root, WriteFileInput{Path: "locked/file.txt", Content: "x"}); err == nil {
		t.Fatal("write into a read-only directory was accepted")
	}
}

func TestSensitivePathIsNarrowerThanReadDeniedPath(t *testing.T) {
	for _, path := range []string{".env", ".env.local", "sub/.git/config", "keys/server.key", "id_rsa", ".cocogem/x", "app.sqlite"} {
		if !SensitivePath(path) {
			t.Fatalf("%s should be sensitive", path)
		}
	}
	for _, path := range []string{"node_modules/pkg/index.js", "dist/app.js", "calc.go"} {
		if SensitivePath(path) {
			t.Fatalf("%s should remain testable", path)
		}
	}
}

// Every component hidden from project tools as a secret must also be hidden
// from the OS test sandbox; only build/dependency directories may differ.
func TestDeniedAndSensitivePathPoliciesStayConsistent(t *testing.T) {
	for _, path := range []string{".env", ".env.local", "sub/.git/config", "secrets/x", "keys/server.key", "id_rsa", "app.sqlite", ".git-credentials", ".cocogem/x", ".ssh/id_ed25519"} {
		if !SensitivePath(path) || !DeniedPath(path) {
			t.Fatalf("secret %s must be both sensitive and denied", path)
		}
	}
	for _, path := range []string{"node_modules/pkg/index.js", "dist/app.js", "build/out"} {
		if SensitivePath(path) || !DeniedPath(path) {
			t.Fatalf("%s should be tool-denied but test-visible", path)
		}
	}
	for _, path := range []string{"calc.go", "src/main.go"} {
		if SensitivePath(path) || DeniedPath(path) {
			t.Fatalf("%s should be allowed", path)
		}
	}
}

func TestWriteFileConcurrentCreateCommitsExactlyOnce(t *testing.T) {
	root := t.TempDir()
	type outcome struct {
		result WriteResult
		err    error
	}
	results := make(chan outcome, 2)
	var group sync.WaitGroup
	for _, content := range []string{"first\n", "second\n"} {
		group.Add(1)
		go func(content string) {
			defer group.Done()
			result, err := WriteFile(context.Background(), root, WriteFileInput{Path: "race.txt", Content: content})
			results <- outcome{result, err}
		}(content)
	}
	group.Wait()
	close(results)
	winners := []outcome{}
	for item := range results {
		if item.err == nil {
			winners = append(winners, item)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("concurrent creates produced %d successes: %+v", len(winners), winners)
	}
	content, err := os.ReadFile(filepath.Join(root, "race.txt"))
	if err != nil || sha(string(content)) != winners[0].result.SHA256 {
		t.Fatalf("final content = %q, %v; winner = %+v", content, err, winners[0].result)
	}
	if temp, err := filepath.Glob(filepath.Join(root, ".cocogem-write-*")); err != nil || len(temp) != 0 {
		t.Fatalf("temporary files leaked: %v %v", temp, err)
	}
}

func TestPatchFileEditsLargeFileAndCapturesAccurateDiff(t *testing.T) {
	root := t.TempDir()
	padding := strings.Repeat("// padding line for a file larger than the write budget\n", 900)
	oldLine := "func Add(a, b int) int { return a - b }\n"
	newLine := "func Add(a, b int) int { return a + b }\n"
	original := "package calc\n\n" + padding + oldLine + "// tail\n"
	if len(original) <= MaxWriteBytes {
		t.Fatalf("fixture is only %d bytes; it must exceed the write_file budget", len(original))
	}
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	captured := []w.Source{}
	scope := Scope{Root: root, Capture: func(source w.Source) (w.Source, error) {
		source.ID = "captured"
		captured = append(captured, source)
		return source, nil
	}}
	read, err := Execute(context.Background(), "read_file", json.RawMessage(`{"path":"calc.go","start_line":1,"end_line":1}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	var readView struct {
		Content string `json:"content"`
		SHA256  string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(read), &readView); err != nil {
		t.Fatal(err)
	}
	if readView.SHA256 != sha(original) || readView.Content == original {
		t.Fatalf("read_file did not return the complete-file hash for the excerpt: %+v", readView)
	}
	// The public complete-file write budget is unchanged; only patch_file may
	// edit this existing file.
	if _, err := WriteFile(context.Background(), root, WriteFileInput{Path: "calc.go", Content: newLine, ExpectedSHA256: readView.SHA256}); err == nil {
		t.Fatal("write_file replaced a file over its 32 KiB budget")
	}
	raw, err := json.Marshal(map[string]any{"path": "calc.go", "expected_sha256": readView.SHA256, "old_text": oldLine, "new_text": newLine})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Execute(context.Background(), "patch_file", raw, scope)
	if err != nil {
		t.Fatal(err)
	}
	var writeView struct {
		Changed        bool   `json:"changed"`
		SHA256         string `json:"sha256"`
		PreviousSHA256 string `json:"previous_sha256"`
		SourceID       string `json:"source_id"`
		DiffPreview    string `json:"diff_preview"`
	}
	if err := json.Unmarshal([]byte(out), &writeView); err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(original, oldLine, newLine, 1)
	if !writeView.Changed || writeView.SHA256 != sha(expected) || writeView.PreviousSHA256 != readView.SHA256 || writeView.SourceID == "" {
		t.Fatalf("patch observation = %+v", writeView)
	}
	if !strings.Contains(writeView.DiffPreview, "-"+strings.TrimSuffix(oldLine, "\n")) || !strings.Contains(writeView.DiffPreview, "+"+strings.TrimSuffix(newLine, "\n")) {
		t.Fatalf("patch diff preview is not the actual replacement: %s", writeView.DiffPreview)
	}
	content, err := os.ReadFile(filepath.Join(root, "calc.go"))
	if err != nil || string(content) != expected {
		t.Fatalf("patched content = %q, %v", content, err)
	}
	if len(captured) != 2 {
		t.Fatalf("captured sources = %d, want read plus diff", len(captured))
	}
	diff := captured[1]
	if diff.Kind != "diff" || diff.Path != "calc.go" || diff.SHA256 != sha(expected) || !strings.Contains(diff.Content, "-"+strings.TrimSuffix(oldLine, "\n")) || !strings.Contains(diff.Content, "+"+strings.TrimSuffix(newLine, "\n")) {
		t.Fatalf("captured diff is not the actual change: %+v", diff)
	}
	if current, err := CurrentSHA256(root, "calc.go"); err != nil || current != sha(expected) {
		t.Fatalf("large-file evidence hash = %q, %v", current, err)
	}
}

func TestPatchFileRejectsBadEditsAndRepeatedRetryDoesNotReapply(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	in := PatchFileInput{Path: "file.txt", ExpectedSHA256: sha(original), OldText: "beta", NewText: "BETA"}
	first, err := PatchFile(context.Background(), root, in)
	if err != nil || !first.Changed || first.SHA256 != sha("alpha\nBETA\ngamma\n") {
		t.Fatalf("first patch = %+v, %v", first, err)
	}
	// The same call again must not apply the fragment a second time; the proof
	// is the reconstructed expected content, not an assumption from a missing
	// old_text alone.
	retry, err := PatchFile(context.Background(), root, in)
	if err != nil {
		t.Fatalf("identical retry was not recognized: %v", err)
	}
	if retry.Changed || retry.Diff != "" || retry.SHA256 != first.SHA256 || retry.PreviousSHA256 != first.SHA256 || retry.Reason == "" {
		t.Fatalf("identical retry = %+v", retry)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("retry changed the file: %q, %v", content, err)
	}
	// The same absent old_text with an unrelated expected hash must stay an
	// error instead of being guessed as already applied.
	if _, err := PatchFile(context.Background(), root, PatchFileInput{Path: "file.txt", ExpectedSHA256: sha("wrong"), OldText: "beta", NewText: "BETA"}); err == nil {
		t.Fatal("stale hash with absent old_text was guessed as applied")
	}
	if err := os.WriteFile(path, []byte("same\nsame\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PatchFile(context.Background(), root, PatchFileInput{Path: "file.txt", ExpectedSHA256: sha("same\nsame\n"), OldText: "same", NewText: "other"}); err == nil {
		t.Fatal("multiple old_text matches were accepted")
	}
	if _, err := PatchFile(context.Background(), root, PatchFileInput{Path: "file.txt", ExpectedSHA256: sha("same\nsame\n"), OldText: "missing", NewText: "other"}); err == nil {
		t.Fatal("missing old_text was accepted")
	}
	if _, err := PatchFile(context.Background(), root, PatchFileInput{Path: "file.txt", ExpectedSHA256: sha("same\nsame\n"), OldText: "", NewText: "other"}); err == nil {
		t.Fatal("empty old_text was accepted")
	}
	if _, err := PatchFile(context.Background(), root, PatchFileInput{Path: "file.txt", ExpectedSHA256: sha("same\nsame\n"), OldText: strings.Repeat("a", MaxWriteBytes), NewText: "b"}); err == nil {
		t.Fatal("oversized fragment pair was accepted")
	}
	big := strings.Repeat("a", maxPatchFileBytes+1)
	if err := os.WriteFile(path, []byte(big), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PatchFile(context.Background(), root, PatchFileInput{Path: "file.txt", ExpectedSHA256: sha(big), OldText: "a", NewText: "b"}); err == nil {
		t.Fatal("file over the 1 MiB patch budget was accepted")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != big {
		t.Fatalf("rejected patches changed the file: %q, %v", content, err)
	}
}

func TestPatchFileSharesUnsafeTargetPolicy(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "linked-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(root, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	expect := func(name string, in PatchFileInput) {
		t.Helper()
		in.ExpectedSHA256 = sha("secret\n")
		if in.OldText == "" {
			in.OldText = "secret"
		}
		if in.NewText == "" {
			in.NewText = "visible"
		}
		if _, err := PatchFile(context.Background(), root, in); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	expect("symlink target", PatchFileInput{Path: "linked-file"})
	expect("symlink parent", PatchFileInput{Path: "linked-dir/new.txt"})
	expect("hardlink target", PatchFileInput{Path: "hardlink.txt"})
	expect("sensitive path", PatchFileInput{Path: ".env"})
	expect("directory target", PatchFileInput{Path: "dir"})
	expect("absolute path", PatchFileInput{Path: secret})
	expect("missing parent", PatchFileInput{Path: "missing/new.txt"})
	expect("missing file", PatchFileInput{Path: "new.txt"})
}
