package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNormalizeScopeResolvesSymlinksAndMissingSegments proves the scope guard is
// a real-path comparison, not a string prefix:
//   - a relative target resolves against the root, never the process cwd;
//   - a symlink inside the root that points outside resolves to the outside
//     target and is therefore not "inside" the root;
//   - a not-yet-existing child under that symlink keeps the resolved outside
//     prefix instead of a lexical inside prefix.
func TestNormalizeScopeResolvesSymlinksAndMissingSegments(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("in"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	resolvedRoot := NormalizeScope(root, root)
	if resolvedRoot == "" {
		t.Fatal("root scope was not resolved")
	}
	t.Run("relative target resolves against the root", func(t *testing.T) {
		got := NormalizeScope(root, "inside.txt")
		if !ScopeContains(resolvedRoot, got) {
			t.Fatalf("relative in-root target = %q, root = %q", got, resolvedRoot)
		}
	})
	t.Run("relative escape is outside", func(t *testing.T) {
		got := NormalizeScope(root, "../"+filepath.Base(outside))
		if ScopeContains(resolvedRoot, got) {
			t.Fatalf("relative escaping target %q was treated as inside %q", got, resolvedRoot)
		}
	})
	t.Run("symlink to outside is outside", func(t *testing.T) {
		got := NormalizeScope(root, link)
		if ScopeContains(resolvedRoot, got) {
			t.Fatalf("symlink target %q was treated as inside %q", got, resolvedRoot)
		}
	})
	t.Run("missing child under an escaping symlink is outside", func(t *testing.T) {
		got := NormalizeScope(root, filepath.Join(link, "newdir", "file.txt"))
		if ScopeContains(resolvedRoot, got) {
			t.Fatalf("missing child under an escaping symlink %q was treated as inside %q", got, resolvedRoot)
		}
		if !ScopeContains(NormalizeScope(outside, outside), got) {
			t.Fatalf("missing child did not resolve under the real outside dir: %q", got)
		}
	})
	t.Run("absolute inside path is inside", func(t *testing.T) {
		got := NormalizeScope(root, filepath.Join(root, "inside.txt"))
		if !ScopeContains(resolvedRoot, got) {
			t.Fatalf("absolute inside target %q was treated as outside %q", got, resolvedRoot)
		}
	})
	t.Run("sibling prefix is not inside", func(t *testing.T) {
		sibling := root + "-sibling"
		if err := os.MkdirAll(sibling, 0700); err != nil {
			t.Fatal(err)
		}
		if ScopeContains(resolvedRoot, NormalizeScope(root, sibling)) {
			t.Fatalf("sibling path prefix was accepted as inside %q", resolvedRoot)
		}
	})
}

// TestRequestableActionsCoverDeniableClasses pins the request offer: every class
// a mode can deny must be requestable, otherwise an outside effect fails with no
// way for the member to ask.
func TestRequestableActionsCoverDeniableClasses(t *testing.T) {
	for _, mode := range []string{ExecutionControlled, ExecutionProjectAutonomous} {
		for _, action := range []string{"read", "write", "run_test", "execute", "network", "publish_artifact"} {
			if !containsAction(RequestableActions(mode), action) {
				t.Fatalf("%s cannot request %q: %v", mode, action, RequestableActions(mode))
			}
		}
	}
	if got := RequestableActions(ExecutionFullAccess); len(got) != 0 {
		t.Fatalf("full access should ask nothing: %v", got)
	}
}

func containsAction(actions []string, want string) bool {
	for _, action := range actions {
		if action == want {
			return true
		}
	}
	return false
}
