package nativecli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

// These cases prove the kernel refuses the effect even when the host adapter
// auto-approves the command (no outside path was extracted from the text). The
// sandbox, not the classifier, is the boundary.
func TestInstalledClaudeAutonomousKernelBoundary(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(area, "secret.txt")
	if err := os.WriteFile(secret, []byte("KERNEL_SECRET_FIXTURE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	// A local service that a sandboxed command must not reach directly.
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "LOCAL_SERVICE_BODY")
	}))
	defer service.Close()
	serviceURL := service.URL

	cases := []struct {
		name    string
		command string
		check   func() error
	}{
		{
			name:    "parent traversal is blocked",
			command: "cd .. && cat secret.txt > " + shellQuote(filepath.Join(root, "leak1.txt")) + " 2>/dev/null; cd - >/dev/null; echo done",
			check: func() error {
				return absent(filepath.Join(root, "leak1.txt"))
			},
		},
		{
			name:    "symlink to outside is blocked",
			command: `python3 -c "print(open('linked.txt').read())"`,
			check: func() error {
				return nil
			},
		},
		{
			name:    "embedded absolute read is blocked",
			command: `python3 -c "print(open(chr(47)+'etc'+chr(47)+'passwd').read())"`,
			check: func() error {
				return nil
			},
		},
		{
			name:    "child process inherits the boundary",
			command: "sh -c " + shellQuote("echo child > "+shellQuote(filepath.Join(area, "child.txt"))),
			check: func() error {
				return absent(filepath.Join(area, "child.txt"))
			},
		},
		{
			name:    "direct local service is blocked",
			command: "curl -sS --max-time 3 " + shellQuote(serviceURL) + " > " + shellQuote(filepath.Join(root, "service.txt")) + " 2>/dev/null; echo done",
			check: func() error {
				data, err := os.ReadFile(filepath.Join(root, "service.txt"))
				if err == nil && strings.Contains(string(data), "LOCAL_SERVICE_BODY") {
					return fmt.Errorf("sandboxed command reached a local service directly")
				}
				return nil
			},
		},
		{
			name:    "external socket is blocked",
			command: "curl -sS --max-time 3 http://203.0.113.1:9/ > /dev/null 2>&1; echo done",
			check: func() error {
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secret := &atomic.Bool{}
			err := runClaudeAutonomous(t, claudeAutonomousFixture{
				root: root, tool: "Bash", input: map[string]any{"command": tc.command}, secret: secret,
				authorize: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					// Auto-allow the in-project command; the OS boundary must still
					// refuse any external effect the text classifier did not name.
					return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
				},
			})
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			if err := tc.check(); err != nil {
				t.Fatal(err)
			}
			if secret.Load() {
				t.Fatal("outside secret content reached the model stream")
			}
		})
	}
}

func absent(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s was created outside the approved boundary", path)
	}
	return nil
}

func TestInstalledClaudeAutonomousPartialEffectIsHonest(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf(`python3 -c "import os
try:
    open(os.path.join('..','outside.txt'),'w').write('ESCAPE')
except Exception:
    pass
open('%s','w').write('INSIDE')"`, filepath.Join(root, "inside.txt"))
	secret := &atomic.Bool{}
	err := runClaudeAutonomous(t, claudeAutonomousFixture{
		root: root, tool: "Bash", input: map[string]any{"command": command}, secret: secret,
		authorize: func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(area, "outside.txt")); !os.IsNotExist(err) {
		t.Fatal("an external write escaped before the later in-project effect")
	}
	if data, err := os.ReadFile(filepath.Join(root, "inside.txt")); err != nil || string(data) != "INSIDE" {
		t.Fatalf("the later in-project effect did not run honestly: %q %v", data, err)
	}
	if secret.Load() {
		t.Fatal("outside content leaked into the model stream")
	}
}
