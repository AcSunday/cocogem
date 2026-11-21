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
	// depth bounds nested `sh -c` parsing so a script cannot recurse forever.
	depth int
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
	return parseShellCommandDepth(command, 0)
}

func parseShellCommandDepth(command string, depth int) (shellCommand, error) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(command), "")
	if err != nil {
		return shellCommand{}, err
	}
	parsed := shellCommand{depth: depth}
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
		case "curl", "wget", "http", "https", "nc", "ssh", "scp", "rsync", "sudo":
			parsed.Network = true
		}
	}
	return parsed, nil
}

// commandWrappers invoke another command after optional flags or assignments.
var commandWrappers = map[string]bool{
	"env": true, "nohup": true, "timeout": true, "nice": true, "command": true, "exec": true,
}

// shellInterpreters execute the command string passed to -c. Codex and other
// adapters wrap every native command as `/bin/zsh -lc "<command>"`, so the
// quoted script must be parsed as a command instead of being read as one long
// path argument.
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"mksh": true, "ash": true, "fish": true, "busybox": true,
}

// collectCall records argv[0] as an executable and every other path-like word as
// a target. A shell interpreter's -c script is recursively parsed as a command,
// so its redirects and path arguments are the real targets.
func (c *shellCommand) collectCall(call *syntax.CallExpr) {
	if len(call.Args) == 0 {
		return
	}
	args := call.Args
	// Unwrap leading wrappers such as `env FOO=1 bash -c ...` or `timeout 10 sh -c ...`.
	for len(args) > 1 {
		head, ok := literalWord(args[0])
		if !ok || !commandWrappers[filepath.Base(head)] {
			break
		}
		c.Executables = appendUnique(c.Executables, head)
		base := filepath.Base(head)
		next := 1
		for next < len(args) {
			tok, tokOK := literalWord(args[next])
			if !tokOK {
				break
			}
			if tok == "--" {
				next++
				break
			}
			if strings.HasPrefix(tok, "-") {
				next++
				continue
			}
			if base == "env" && strings.Contains(tok, "=") {
				next++
				continue
			}
			if base == "timeout" && next == 1 {
				// Skip duration argument (e.g. `timeout 10 bash -c ...`).
				next++
				continue
			}
			break
		}
		if next >= len(args) {
			break
		}
		args = args[next:]
	}
	name, nameOK := literalWord(args[0])
	if nameOK && name != "" {
		c.Executables = appendUnique(c.Executables, name)
	}
	// Detect `git push` / `git fetch` / `git pull` / `git clone` / `git remote`
	// network/external write operations so they are never silently auto-allowed.
	if nameOK && filepath.Base(name) == "git" {
		for _, word := range args[1:] {
			tok, ok := literalWord(word)
			if !ok || strings.HasPrefix(tok, "-") {
				continue
			}
			if tok == "push" || tok == "clone" {
				c.Network = true
			}
			break
		}
	}
	scriptIndex := -1
	if nameOK && shellInterpreters[filepath.Base(name)] {
		scriptIndex = shellScriptIndex(args[1:])
	}
	for index, word := range args {
		if index == 0 {
			continue
		}
		if index-1 == scriptIndex {
			// A -c script is a command line, never a path; parse it instead.
			// Its text keeps `//` and other clean-visible characters because it
			// is a program, not a filesystem path.
			if script, ok := literalText(word); ok {
				c.mergeScript(script)
			}
			continue
		}
		value, ok := literalWord(word)
		if !ok {
			continue
		}
		// Extract path targets attached to flags or key=value operands such as
		// `--file=/outside/a.tar`, `if=/etc/passwd`, `of=/tmp/x`, `-o/outside/f`.
		if attached := attachedPathValue(value); attached != "" {
			if expanded := expandTilde(attached); pathLikeWord(expanded) {
				c.Targets = appendUnique(c.Targets, expanded)
			}
			continue
		}
		expanded := expandTilde(value)
		if !pathLikeWord(expanded) {
			continue
		}
		c.Targets = appendUnique(c.Targets, expanded)
	}
}

func expandTilde(value string) string {
	if value == "~" || strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if value == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	return value
}

func attachedPathValue(value string) string {
	if idx := strings.Index(value, "="); idx > 0 && idx+1 < len(value) {
		rhs := value[idx+1:]
		if strings.HasPrefix(rhs, "/") || strings.HasPrefix(rhs, "~/") || strings.HasPrefix(rhs, "./") || strings.HasPrefix(rhs, "../") {
			return rhs
		}
	}
	if len(value) > 3 && value[0] == '-' && value[1] != '-' && (value[2] == '/' || strings.HasPrefix(value[2:], "~/")) {
		return value[2:]
	}
	return ""
}

// shellScriptIndex returns the argv index of a shell's -c script, or -1. A
// cluster such as -lc or -ec counts as -c, `-o option` consumes its argument,
// and `--` ends flag parsing.
func shellScriptIndex(args []*syntax.Word) int {
	for index := 0; index < len(args); index++ {
		value, ok := literalWord(args[index])
		if !ok {
			return -1
		}
		if value == "--" {
			return -1
		}
		if !strings.HasPrefix(value, "-") || value == "-" {
			return -1
		}
		if strings.HasPrefix(value, "--") {
			continue
		}
		letters := strings.TrimPrefix(value, "-")
		for position, letter := range letters {
			if letter == 'c' {
				if index+1 < len(args) {
					return index + 1
				}
				return -1
			}
			if letter == 'o' && position == len(letters)-1 {
				// -o takes the next word as its value; skip it.
				index++
				break
			}
		}
	}
	return -1
}

// mergeScript folds one nested command string into this command's view. The
// nesting depth is bounded so `sh -c "sh -c ..."` cannot recurse without end;
// an unparseable script keeps the conservative token fallback for its targets.
func (c *shellCommand) mergeScript(script string) {
	if c.depth >= 3 {
		return
	}
	nested, err := parseShellCommandDepth(script, c.depth+1)
	if err != nil {
		for _, token := range shellPathTokens(script) {
			if pathLikeWord(token) {
				c.Targets = appendUnique(c.Targets, token)
			}
		}
		return
	}
	for _, executable := range nested.Executables {
		c.Executables = appendUnique(c.Executables, executable)
	}
	for _, target := range nested.Targets {
		c.Targets = appendUnique(c.Targets, target)
	}
	c.Network = c.Network || nested.Network
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

// literalWord returns the literal text of a word. Pure program expressions such
// as `jq '.a // empty'` or `awk '/ERR/'` are filtered out, while quoted real
// paths (including paths with `//`, `/./`, `..`, or a trailing `/`) are cleaned
// and retained so they cannot bypass target extraction.
func literalWord(word *syntax.Word) (string, bool) {
	text, quoted, ok := literalParts(word)
	if !ok {
		return "", false
	}
	if quoted && looksLikeProgramExpression(text) {
		return "", false
	}
	return text, true
}

func looksLikeProgramExpression(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	// Bare `//` or jq/awk expressions containing whitespace + operators (e.g. `.a // empty`).
	if trimmed == "//" || strings.Contains(trimmed, " // ") || strings.Contains(trimmed, " | ") {
		return true
	}
	// Awk/sed regex delimiter `/PATTERN/` (single segment enclosed in slashes,
	// not a multi-segment absolute path like `/Users/alice/.ssh/`).
	if len(trimmed) >= 3 && trimmed[0] == '/' && trimmed[len(trimmed)-1] == '/' {
		inner := trimmed[1 : len(trimmed)-1]
		if !strings.Contains(inner, "/") {
			// A top-level directory like `/tmp/` or `/etc/` is a real path, not a regex.
			switch inner {
			case "tmp", "etc", "var", "usr", "bin", "sbin", "opt", "home", "Users", "Library", "System", "Volumes", "private":
				return false
			}
			return true
		}
	}
	return false
}

// literalText returns the exact literal text of a word, without the path-clean
// guard: it is used for command strings that are programs rather than paths.
func literalText(word *syntax.Word) (string, bool) {
	text, _, ok := literalParts(word)
	return text, ok
}

// literalParts returns the exact literal text of a word and whether any part was
// quoted. It never applies the path-clean guard.
func literalParts(word *syntax.Word) (string, bool, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false, false
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
					return "", false, false
				}
			}
		default:
			// Parameter expansion, command substitution and arithmetic are
			// runtime values; their exact path is not statically knowable.
			return "", false, false
		}
	}
	return out.String(), quoted, true
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
