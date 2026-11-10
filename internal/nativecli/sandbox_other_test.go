//go:build !darwin

package nativecli

import (
	"path/filepath"
	"testing"
)

func TestNativeSandboxUnavailableFailsClosed(t *testing.T) {
	sandbox, err := newNativeSandbox(filepath.Join(t.TempDir(), "private"))
	if err == nil || sandbox != nil {
		t.Fatalf("unsupported native sandbox = %v, %v; want no sandbox and an error", sandbox, err)
	}
	if err := sandboxExecutable(); err == nil {
		t.Fatal("unsupported native isolation was reported as executable")
	}
}
