//go:build darwin

package nativecli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// nativeSandbox is the per-attempt OS execution boundary for CLI-owned tools on
// macOS. It is used for project_autonomous only: file and command effects that
// leave the bound project must be refused by the kernel before they happen.
// This is real enforcement, not a prompt or a cwd string comparison.
type nativeSandbox struct {
	base         string
	home         string
	tmp          string
	cache        string
	profiles     string
	writeRoots   []string
	readRoots    []string
	proxyURL     string
	proxyAddr    string
	caPath       string
	denyPaths    []string
	denyUnknown  bool
	allowAddrs   []string
	allowInbound bool
}

func newNativeSandbox(base string) (*nativeSandbox, error) {
	s := &nativeSandbox{base: base, profiles: filepath.Join(base, "sandbox")}
	for _, dir := range []struct {
		name string
		path *string
	}{
		{"home", &s.home}, {"tmp", &s.tmp}, {"cache", &s.cache}, {"profiles", &s.profiles},
	} {
		path := filepath.Join(base, dir.name)
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, errors.New("create private native sandbox directory")
		}
		if err := os.Chmod(path, 0700); err != nil {
			return nil, errors.New("secure private native sandbox directory")
		}
		*dir.path = path
	}
	return s, nil
}

// setHardlinkDenies records exact files that must stay unreadable/unwritable
// because their inode also has a link outside the project.
func (s *nativeSandbox) setAllowedAddrs(addrs []string) {
	if s == nil {
		return
	}
	s.allowAddrs = append([]string{}, addrs...)
}

func (s *nativeSandbox) setHardlinkDenies(paths []string, unknown bool) {
	if s == nil {
		return
	}
	s.denyPaths = append([]string{}, paths...)
	s.denyUnknown = unknown
}

// setCA installs the private egress CA for well-behaved TLS clients. A client
// that ignores these variables cannot complete a handshake and fails closed.
func (s *nativeSandbox) setCA(pemBytes []byte) error {
	if s == nil {
		return errors.New("native sandbox is unavailable")
	}
	path := filepath.Join(s.base, "egress-ca.pem")
	if err := os.WriteFile(path, pemBytes, 0600); err != nil {
		return errors.New("write native egress CA")
	}
	s.caPath = path
	return nil
}

// addRoots records the only locations the sandboxed command may write. The
// project and task directory are real paths; aliases are kept for the lexical
// path the CLI may use.
func (s *nativeSandbox) addRoots(roots ...string) {
	for _, root := range roots {
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		s.writeRoots = appendUnique(s.writeRoots, absolute)
		if resolved := resolvePhysicalPath(absolute); resolved != absolute {
			s.writeRoots = appendUnique(s.writeRoots, resolved)
		}
	}
}

// env returns the private runtime identity for a sandboxed command. The model
// never receives the user's HOME, cache or git credentials.
func (s *nativeSandbox) env() map[string]string {
	env := map[string]string{
		"HOME":                    s.home,
		"TMPDIR":                  s.tmp + string(filepath.Separator),
		"XDG_CACHE_HOME":          s.cache,
		"XDG_CONFIG_HOME":         filepath.Join(s.home, ".config"),
		"GOCACHE":                 filepath.Join(s.cache, "go-build"),
		"GOMODCACHE":              filepath.Join(s.cache, "go-mod"),
		"GOPATH":                  filepath.Join(s.home, "go"),
		"npm_config_cache":        filepath.Join(s.cache, "npm"),
		"PIP_CACHE_DIR":           filepath.Join(s.cache, "pip"),
		"GIT_CONFIG_GLOBAL":       "/dev/null",
		"GIT_CONFIG_SYSTEM":       "/dev/null",
		"GIT_TERMINAL_PROMPT":     "0",
		"PYTHONDONTWRITEBYTECODE": "1",
	}
	if s.proxyURL != "" {
		for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "ALL_PROXY", "all_proxy"} {
			env[key] = s.proxyURL
		}
		env["NO_PROXY"] = "127.0.0.1,localhost,::1"
		env["no_proxy"] = "127.0.0.1,localhost,::1"
		env["COCOGEM_EGRESS_PROXY"] = s.proxyURL
	}
	if s.caPath != "" {
		for _, key := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "GIT_SSL_CAINFO", "PIP_CERT", "npm_config_cafile", "DENO_CERT", "AWS_CA_BUNDLE"} {
			env[key] = s.caPath
		}
	}
	return env
}

// profile writes a deny-by-default seatbelt profile for one command. approved
// holds host-authorized resource scopes that this single call may reach.
func (s *nativeSandbox) profile(name string, approvedRead, approvedWrite []string) (string, error) {
	readRoots := append([]string{}, systemReadRoots()...)
	readRoots = append(readRoots, s.readRoots...)
	readRoots = append(readRoots, s.writeRoots...)
	readRoots = append(readRoots, s.base)
	writeRoots := append([]string{}, s.writeRoots...)
	writeRoots = append(writeRoots, s.base, s.home, s.tmp, s.cache, s.profiles)
	for _, scope := range approvedRead {
		if scope == "" || !filepath.IsAbs(scope) {
			continue
		}
		readRoots = appendUnique(readRoots, scope)
	}
	for _, scope := range approvedWrite {
		if scope == "" || !filepath.IsAbs(scope) {
			continue
		}
		readRoots = appendUnique(readRoots, scope)
		writeRoots = appendUnique(writeRoots, scope)
	}
	var rules strings.Builder
	rules.WriteString("(version 1)\n(deny default)\n")
	// System services needed to start any process. Execution still requires a
	// readable executable path below.
	rules.WriteString("(allow process*)\n(allow sysctl-read)\n(allow mach-lookup)\n(allow signal (target self))\n(allow ipc-posix*)\n")
	rules.WriteString("(allow file-read*\n  (literal \"/\")\n")
	for _, path := range ancestors(writeRoots) {
		fmt.Fprintf(&rules, "  (literal %s)\n", seatbeltQuote(path))
	}
	for _, path := range dedupePaths(readRoots) {
		fmt.Fprintf(&rules, "  (subpath %s)\n  (literal %s)\n", seatbeltQuote(path), seatbeltQuote(path))
		if resolved := resolvePhysicalPath(path); resolved != path {
			fmt.Fprintf(&rules, "  (subpath %s)\n  (literal %s)\n", seatbeltQuote(resolved), seatbeltQuote(resolved))
		}
	}
	// /dev is needed for /dev/null, /dev/fd and terminal handling.
	rules.WriteString("  (subpath \"/dev\")\n)\n")
	rules.WriteString("(allow file-write*\n")
	for _, path := range dedupePaths(writeRoots) {
		fmt.Fprintf(&rules, "  (subpath %s)\n  (literal %s)\n", seatbeltQuote(path), seatbeltQuote(path))
		if resolved := resolvePhysicalPath(path); resolved != path {
			fmt.Fprintf(&rules, "  (subpath %s)\n  (literal %s)\n", seatbeltQuote(resolved), seatbeltQuote(resolved))
		}
	}
	rules.WriteString("  (subpath \"/dev\")\n)\n")
	// Network is limited to loopback so a shell command can reach only the
	// host-owned egress proxy. Public retrieval and dependency installation use
	// that proxy; arbitrary external writes without a proxy allowlisted host are
	// refused by the kernel before the socket exists.
	if s.denyUnknown {
		// The scan could not prove absence of external hardlinks. Fail closed
		// for effects: the command may still read normal project files, but it
		// cannot write through any path the host could not classify.
		rules.WriteString("(deny file-write* (subpath \"/\"))\n")
	}
	for _, path := range s.denyPaths {
		if !filepath.IsAbs(path) {
			continue
		}
		for _, variant := range []string{path, resolvePhysicalPath(path)} {
			fmt.Fprintf(&rules, "(deny file-read* (literal %s) (subpath %s))\n", seatbeltQuote(variant), seatbeltQuote(variant))
			fmt.Fprintf(&rules, "(deny file-write* (literal %s) (subpath %s))\n", seatbeltQuote(variant), seatbeltQuote(variant))
		}
	}
	rules.WriteString("(deny network*)\n")
	if s.allowInbound {
		// A sandboxed local server (for example OpenCode's private HTTP
		// listener) may accept loopback connections, but no external inbound
		// socket is exposed.
		rules.WriteString("(allow network-inbound (local ip \"localhost:*\"))\n")
	}
	addrs := append([]string{}, s.allowAddrs...)
	if s.proxyAddr != "" {
		addrs = append(addrs, s.proxyAddr)
	}
	seenPorts := map[string]bool{}
	for _, address := range addrs {
		_, port, err := net.SplitHostPort(address)
		if err != nil || port == "" || seenPorts[port] {
			continue
		}
		seenPorts[port] = true
		fmt.Fprintf(&rules, "(allow network-outbound (remote ip \"localhost:%s\"))\n", port)
	}
	path := filepath.Join(s.profiles, safeToken(name)+".sb")
	if err := os.WriteFile(path, []byte(rules.String()), 0600); err != nil {
		return "", errors.New("write native sandbox profile")
	}
	return path, nil
}

// wrap returns a shell command that executes the original command inside the
// per-call sandbox. The profile must already include every approved scope.
func (s *nativeSandbox) wrap(profilePath, cwd, command string) string {
	var env []string
	for _, key := range sortedKeys(s.env()) {
		env = append(env, key+"="+shellQuote(s.env()[key]))
	}
	inner := "cd " + shellQuote(cwd) + " || exit 126; " + command
	scrub := ""
	for _, key := range []string{
		"COCOGEM_MCP_TOKEN", "OPENCODE_PASSWORD", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"GITHUB_TOKEN", "GH_TOKEN", "NPM_TOKEN", "SSH_AUTH_SOCK", "CLAUDE_CODE_OAUTH_TOKEN",
	} {
		scrub += "-u " + key + " "
	}
	return "env " + scrub + strings.Join(env, " ") + " /usr/bin/sandbox-exec -f " + shellQuote(profilePath) + " -- /bin/bash --noprofile --norc -c " + shellQuote(inner)
}

// canWrap reports whether the host can build a per-call profile for this command.
func (s *nativeSandbox) canWrap() bool {
	return s != nil && s.profiles != ""
}

func systemReadRoots() []string {
	roots := []string{
		"/System", "/Library", "/usr", "/bin", "/sbin", "/opt/homebrew", "/usr/local",
		"/private/var/db", "/private/var/select", "/private/var/run", "/dev",
		"/private/etc/ssl", "/private/etc/ssh", "/private/etc/pam.d", "/private/etc/security",
	}
	for _, file := range []string{"/private/etc/hosts", "/private/etc/resolv.conf", "/private/etc/localtime", "/private/etc/services", "/private/etc/protocols", "/private/etc/nsswitch.conf", "/private/etc/group"} {
		roots = append(roots, file)
	}
	for _, home := range toolchainRoots() {
		roots = append(roots, home)
	}
	return roots
}

// toolchainRoots lets commands run installed compilers and package managers
// without granting the model the rest of the user's home directory.
func toolchainRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	var roots []string
	for _, name := range []string{
		".nvm", ".pyenv", ".cargo/bin", ".rustup/toolchains", ".bun", ".volta/tools", ".asdf/installs",
		".local/bin", ".local/share/pnpm", ".local/share/uv", ".cache/uv",
		"go/pkg/mod", "go/bin",
	} {
		path := filepath.Join(home, name)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
}

func ancestors(paths []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		current := filepath.Clean(path)
		for {
			parent := filepath.Dir(current)
			if parent == current {
				if !seen[current] {
					seen[current] = true
					result = append(result, current)
				}
				break
			}
			current = parent
			if seen[current] {
				break
			}
			seen[current] = true
			result = append(result, current)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(result)))
	return result
}

func dedupePaths(paths []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	return result
}

func seatbeltQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sandboxExecutable verifies the platform helper exists before exposing any
// command tool. A missing helper is a capability gap, never a silent fallback.
func sandboxExecutable() error {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return errors.New("macOS sandbox-exec is unavailable")
	}
	return nil
}
