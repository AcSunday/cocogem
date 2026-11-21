package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInspectRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	run("init", "--quiet")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "--", "a.txt")
	run("-c", "user.name=Cocogem Test", "-c", "user.email=cocogem@example.invalid", "commit", "--quiet", "-m", "one")
	return root
}

// TestGitInspectIsReadOnlyAndHardened is the A13 regression: status/diff/log
// return real repository facts without executing a project external diff driver
// or a repository hook.
func TestGitInspectIsReadOnlyAndHardened(t *testing.T) {
	root := gitInspectRepo(t)
	// A hostile diff driver and hook must never run. The host prefix disables
	// them; if they did run, this marker would appear.
	marker := filepath.Join(root, "hook-ran")
	hook := filepath.Join(root, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := GitInspect(context.Background(), root, GitInspectInput{Subcommand: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "a.txt") {
		t.Fatalf("status did not report the modified file: %s", status)
	}
	diff, err := GitInspect(context.Background(), root, GitInspectInput{Subcommand: "diff", Path: "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+two") {
		t.Fatalf("diff did not contain the actual change: %s", diff)
	}
	log, err := GitInspect(context.Background(), root, GitInspectInput{Subcommand: "log", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "one") {
		t.Fatalf("log did not contain the commit: %s", log)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository hook ran during a read-only inspection")
	}
}

// TestGitInspectRejectsTraversalAndArbitraryArguments keeps the argv host-owned.
func TestGitInspectRejectsTraversalAndArbitraryArguments(t *testing.T) {
	root := gitInspectRepo(t)
	for _, in := range []GitInspectInput{
		{Subcommand: "diff", Path: "../outside"},
		{Subcommand: "diff", Path: "--exec=sh"},
		{Subcommand: "log", Revision: "--all"},
		{Subcommand: "push"},
		{Subcommand: "log", Limit: 9000},
	} {
		if _, err := GitInspect(context.Background(), root, in); err == nil {
			t.Fatalf("unsafe git_inspect input was accepted: %+v", in)
		}
	}
	if _, err := GitInspect(context.Background(), t.TempDir(), GitInspectInput{Subcommand: "status"}); err == nil {
		t.Fatal("a non-repository directory was accepted")
	}
}

// TestGitInspectObservationIsBoundedJSON keeps the tool output machine-readable.
func TestGitInspectObservationIsBoundedJSON(t *testing.T) {
	root := gitInspectRepo(t)
	output, err := GitInspect(context.Background(), root, GitInspectInput{Subcommand: "show", Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Subcommand string `json:"subcommand"`
		Output     string `json:"output"`
		Truncated  bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		t.Fatalf("observation is not JSON: %v", err)
	}
	if value.Subcommand != "show" || !strings.Contains(value.Output, "one") {
		t.Fatalf("show observation = %+v", value)
	}
}
