package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `用法：go run . [claude|codex|gemini] [--resume latest|会话ID] "你的问题"`

type options struct {
	CLI    string
	Prompt string
	Resume string
}

func main() {
	opt, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := run(opt, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseArgs(args []string) (options, error) {
	opt := options{CLI: "claude"}
	if len(args) > 1 && !strings.HasPrefix(args[0], "-") {
		opt.CLI, args = args[0], args[1:]
	}
	if opt.CLI != "claude" && opt.CLI != "codex" && opt.CLI != "gemini" {
		return options{}, fmt.Errorf("%s\n不支持的 CLI：%s", usage, opt.CLI)
	}
	if len(args) == 0 || strings.TrimSpace(args[len(args)-1]) == "" {
		return options{}, fmt.Errorf("%s", usage)
	}
	opt.Prompt = args[len(args)-1]
	lastOption := strings.SplitN(opt.Prompt, "=", 2)[0]
	if (len(args) < 2 || args[len(args)-2] != "--") && (lastOption == "--resume" || lastOption == "-resume") {
		return options{}, fmt.Errorf("%s", usage)
	}
	flags := flag.NewFlagSet("cocogem", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opt.Resume, "resume", "", "latest 或会话 ID")
	if err := flags.Parse(args[:len(args)-1]); err != nil {
		return options{}, fmt.Errorf("%s: %w", usage, err)
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("%s", usage)
	}
	var emptyResume bool
	flags.Visit(func(f *flag.Flag) { emptyResume = f.Name == "resume" && opt.Resume == "" })
	if emptyResume {
		return options{}, fmt.Errorf("resume must be latest or a session UUID")
	}
	if opt.Resume != "" && opt.Resume != "latest" {
		if err := validateSessionID(opt.Resume); err != nil {
			return options{}, err
		}
	}
	return opt, nil
}

func run(opt options, output, diagnostics io.Writer) error {
	sessionID := opt.Resume
	if sessionID == "latest" {
		var err error
		sessionID, err = loadSession(".cocogem", opt.CLI)
		if err != nil {
			return err
		}
	}
	result, err := Invoke(opt.CLI, Request{Prompt: opt.Prompt, SessionID: sessionID}, output)
	if result.SessionID != "" {
		fmt.Fprintf(diagnostics, "Session (%s): %s\n", result.CLI, result.SessionID)
	}
	if err != nil {
		return err
	}
	return saveSession(".cocogem", result)
}
