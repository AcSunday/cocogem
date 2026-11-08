package nativecli

import (
	_ "embed"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed gemini_sse.cjs
var geminiSSEShim []byte

// configureGeminiSSE leaves the installed CLI and its connection settings
// untouched. NODE_OPTIONS is restored before the CLI launches native tools.
func configureGeminiSSE(cmd *exec.Cmd, workdir string) error {
	path := filepath.Join(workdir, "gemini-sse.cjs")
	if err := os.WriteFile(path, geminiSSEShim, 0600); err != nil {
		return errors.New("write Gemini stream compatibility shim")
	}
	env := baseEnvironment(cmd.Env)
	var previous string
	present := false
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "NODE_OPTIONS" {
			previous, present = value, true
		}
	}
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path) + `"`
	options := strings.TrimSpace(previous)
	if options != "" {
		options += " "
	}
	options += "--require=" + quoted
	set := "0"
	if present {
		set = "1"
	}
	cmd.Env = overrideEnv(env, map[string]string{
		"NODE_OPTIONS":                         options,
		"COCOGEM_GEMINI_ORIGINAL_NODE_OPTIONS": previous,
		"COCOGEM_GEMINI_NODE_OPTIONS_PRESENT":  set,
	})
	return nil
}
