//go:build !darwin

package nativecli

import "errors"

// nativeSandbox is unavailable on this platform. project_autonomous native
// effects therefore stay closed until a real OS boundary exists; the adapter
// reports the gap instead of falling back to trusted execution. The type keeps
// the same fields/methods as the darwin implementation so shared adapters and
// tests compile, but every constructor/operation fails closed.
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

func newNativeSandbox(string) (*nativeSandbox, error) {
	return nil, errors.New("native execution isolation is only implemented and verified on macOS")
}

func (s *nativeSandbox) addRoots(...string) {}

func (s *nativeSandbox) env() map[string]string { return nil }

func (s *nativeSandbox) profile(string, []string, []string) (string, error) {
	return "", errors.New("native execution isolation is unavailable")
}

func (s *nativeSandbox) wrap(string, string, string) string { return "" }

func (s *nativeSandbox) canWrap() bool { return false }

func (s *nativeSandbox) setCA([]byte) error {
	return errors.New("native execution isolation is unavailable")
}

func (s *nativeSandbox) setAllowedAddrs([]string) {}

func (s *nativeSandbox) setHardlinkDenies([]string, bool) {}

func sandboxExecutable() error {
	return errors.New("OS-level native isolation is only implemented and verified on macOS")
}
