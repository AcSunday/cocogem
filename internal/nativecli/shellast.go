package nativecli

import (
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// shellCommand is the structural view of one shell command line. It is used only
// to decide which exact scope to show and to ask the host for; the OS sandbox
// remains the authority for what actually executes.
//
// Parsing the POSIX shell grammar instead of splitting on whitespace removes the
// classic false positives: `2>/dev/null` is a device redirect, `jq '.a // empty'`
// and `awk '/ERR/'` are program literals, and the executable's own path is a
// command rather than a data target.
type shellCommand struct {
	// Executables are command names (argv[0] of every simple command). A path
	// here is an interpreter or tool, not an effect target.
	Executables []string
	// Targets are path-like data arguments and redirect destinations.
	Targets []string
	// Network reports a public fetch/download command, never a local effect.
	Network bool
}

// allowedDevices are data sinks a normal command may freely redirect to. They
// are kernel devices, not user files, so they never need an approval.
func allowedDevice(path string) bool {
	switch path {
	case "/dev/null", "/dev/zero", "/dev/urandom", "/dev/random", "/dev/stdin", "/dev/stdout", "/dev/stderr":
		return true
	}
	return strings.HasPrefix(path, "/dev/fd/")
}

// parseShellCommand parses one command string. A parse failure is not fatal: the
// caller falls back to a conservative token scan so an unparseable command still
// requests its plausible targets instead of running unbounded.
func parseShellCommand(command string) (shellCommand, error) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(command), "")
	if err != nil {
		return shellCommand{}, err
	}
	parsed := shellCommand{}
	syntax.Walk(f, func(node syntax.Node) bool {
		switch value := node.(type) {
		case *syntax.CallExpr:
			parsed.collectCall(value)
		case *syntax.Redirect:
			parsed.collectRedirect(value)
		case *syntax.Word:
			// A bare word outside a call/redirect (for example a here-string body)
			// is not a path argument.
			_ = value
		}
		return true
	})
	// Network commands are classified by the executable name so a curl/wget
	// destination is handled by the external-effect path, not as a local file.
	for _, executable := range parsed.Executables {
		switch filepath.Base(executable) {
		case "curl", "wget", "http", "https", "nc", "ssh", "scp", "rsync":
			parsed.Network = true
		}
	}
	return parsed, nil
}

// collectCall records argv[0] as an executable and every other path-like word as
// a target.
func (c *shellCommand) collectCall(call *syntax.CallExpr) {
	for index, word := range call.Args {
		value, ok := literalWord(word)
		if !ok {
			continue
		}
		if index == 0 {
			if value != "" {
				c.Executables = appendUnique(c.Executables, value)
			}
			continue
		}
		if !pathLikeWord(value) {
			continue
		}
		c.Targets = appendUnique(c.Targets, value)
	}
}

func (c *shellCommand) collectRedirect(redirect *syntax.Redirect) {
	if redirect.Word == nil {
		// A here-document body is data, not a path.
		return
	}
	value, ok := literalWord(redirect.Word)
	if !ok || value == "" {
		return
	}
	if redirect.Op == syntax.RdrIn && allowedDevice(value) {
		return
	}
	if allowedDevice(value) || strings.HasPrefix(value, "/dev/fd/") {
		return
	}
	c.Targets = appendUnique(c.Targets, value)
}

// literalWord returns the literal text of a word. A word with a quoted literal
// whose cleaned path differs from the literal is a program expression
// (`jq '//'`, `awk '/ERR/'`), never a filesystem path.
func literalWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false
	}
	var out strings.Builder
	quoted := false
	for _, part := range word.Parts {
		switch value := part.(type) {
		case *syntax.Lit:
			// Shell metacharacters inside an unquoted literal are grammar,
			// already handled by the parser; the bytes are the literal text.
			out.WriteString(value.Value)
		case *syntax.SglQuoted:
			quoted = true
			out.WriteString(value.Value)
		case *syntax.DblQuoted:
			quoted = true
			for _, inner := range value.Parts {
				if lit, ok := inner.(*syntax.Lit); ok {
					out.WriteString(lit.Value)
				} else {
					return "", false
				}
			}
		default:
			// Parameter expansion, command substitution and arithmetic are
			// runtime values; their exact path is not statically knowable.
			return "", false
		}
	}
	text := out.String()
	if quoted {
		// A literal like `//` or `/ERR/` cleans to something else, so it is a
		// program expression. A quoted path such as `/tmp/a b.txt` cleans to
		// itself and stays a real target.
		if filepath.Clean(text) != text {
			return "", false
		}
	}
	return text, true
}

// pathLikeWord reports whether a word plausibly names a filesystem path. Only
// these are surfaced as approval scopes; plain flags and program arguments are
// left to the OS boundary.
func pathLikeWord(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.Contains(value, "=") {
		return false
	}
	if strings.Contains(value, "://") || strings.HasPrefix(value, "git@") {
		return false
	}
	if strings.ContainsAny(value, "*?[]${}") || strings.Contains(value, "...") {
		// A glob or the Go package pattern `./...` is not one concrete path;
		// the command's working directory stays the approval scope.
		return false
	}
	if strings.HasPrefix(value, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			value = filepath.Join(home, strings.TrimPrefix(value, "~"))
		}
	}
	return filepath.IsAbs(value) ||
		strings.HasPrefix(value, "./") ||
		strings.HasPrefix(value, "../") ||
		strings.ContainsRune(value, filepath.Separator)
}

// resolveShellTargets maps the structural targets to real paths relative to the
// command's working directory. Non-path words are filtered by pathLikeWord.
func resolveShellTargets(parsed shellCommand, cwd string) []string {
	out := []string{}
	for _, target := range parsed.Targets {
		resolved := resolveTarget(target, cwd)
		if resolved == "" {
			continue
		}
		out = appendUnique(out, resolved)
	}
	return out
}

// CommandTargets exposes the structural path targets of one shell command to the
// host permission layer. It returns the resolved real paths relative to cwd; an
// unparseable command falls back to the conservative token scan.
func CommandTargets(command, cwd string) []string {
	parsed, err := parseShellCommand(command)
	if err != nil {
		out := []string{}
		for _, token := range shellPathTokens(command) {
			if resolved := resolveTarget(token, cwd); resolved != "" {
				out = appendUnique(out, resolved)
			}
		}
		return out
	}
	return resolveShellTargets(parsed, cwd)
}

// CommandIsNetwork reports whether the command is a public download/fetch tool.
func CommandIsNetwork(command string) bool {
	parsed, err := parseShellCommand(command)
	return err == nil && parsed.Network
}

// ArgvTargets returns the resolved path-like arguments of a direct argv
// execution (no shell grammar). Flags, assignments and program arguments are not
// targets, so an ordinary tool invocation keeps its working directory scope.
func ArgvTargets(argv []string, cwd string) []string {
	out := []string{}
	for index, argument := range argv {
		if index == 0 || !pathLikeWord(argument) {
			continue
		}
		if resolved := resolveTarget(argument, cwd); resolved != "" {
			out = appendUnique(out, resolved)
		}
	}
	return out
}
