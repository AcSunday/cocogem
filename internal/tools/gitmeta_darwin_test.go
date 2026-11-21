package tools

import (
	"runtime"
	"strings"
	"testing"
)

// TestDarwinProfileDeniesGitHooksAndRefs checks the compiled profile sends the
// deny after the write allow, so it actually overrides it.
func TestDarwinProfileDeniesGitHooksAndRefs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the verified sandbox profile is macOS-only")
	}
	denies := []string{"/repo/.git/hooks", "/repo/.git/refs/heads"}
	profile := darwinProfile(sandboxSpec{Root: "/tmp/root", Temp: "/tmp/temp", DenyWrites: denies})
	writeAllow := strings.Index(profile, "(allow file-write*")
	if writeAllow < 0 {
		t.Fatal("no write allow in the profile")
	}
	for _, deny := range denies {
		rule := `(deny file-write* (literal "` + deny + `") (subpath "` + deny + `"))`
		if !strings.Contains(profile, rule) {
			t.Fatalf("missing deny rule %q", rule)
		}
		if strings.Index(profile, rule) < writeAllow {
			t.Fatalf("deny for %s precedes the write allow", deny)
		}
	}
}
