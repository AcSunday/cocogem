// Command cocogem streams responses from an installed Claude, Codex or Gemini CLI.
package main

import (
	"os"

	"github.com/AcSunday/cocogem/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
