package nativecli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestAdapterProbeKeepsOSIsolationBoundariesUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	script := "#!/bin/sh\ncase \"$1\" in\n--version) printf 'codex-cli 0.164.0\\n' ;;\n*) printf '%s\\n' '--ignore-user-config --ignore-rules --ephemeral --sandbox --json' ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	info, err := check(t.Context(), "codex", func(_ string, args ...string) *exec.Cmd {
		return exec.Command(path, args...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Supported {
		t.Fatalf("fixture adapter was not accepted: %+v", info)
	}
	for _, capability := range []string{
		"os=" + runtime.GOOS,
		"file_isolation=unknown", "secret_environment_isolation=unknown",
		"network_isolation=unknown", "descendant_lifecycle=unknown",
		"detached_descendant_lifecycle=unknown", "background_job_budget_accounting=unknown",
		"native_effect_audit=partial", "git_metadata_isolation=unknown", "session_resume_ack=unknown",
	} {
		if !slices.Contains(info.Capabilities, capability) {
			t.Fatalf("missing honest capability state %q: %+v", capability, info.Capabilities)
		}
	}
}
