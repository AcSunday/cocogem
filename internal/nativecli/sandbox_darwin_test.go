//go:build darwin

package nativecli

import (
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func runWrapped(t *testing.T, sandbox *nativeSandbox, profile, root, command string) (string, error) {
	t.Helper()
	wrapped := sandbox.wrap(profile, root, command)
	out, err := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", wrapped).CombinedOutput()
	return string(out), err
}

func TestNativeSandboxRealBoundary(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE_SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	sandbox, err := newNativeSandbox(filepath.Join(area, "private"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	profile, err := sandbox.profile("boundary", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("inside write", func(t *testing.T) {
		if _, err := runWrapped(t, sandbox, profile, root, "printf INSIDE > inside.txt"); err != nil {
			t.Fatalf("inside write failed: %v", err)
		}
		if data, err := os.ReadFile(filepath.Join(root, "inside.txt")); err != nil || string(data) != "INSIDE" {
			t.Fatalf("inside marker = %q, %v", data, err)
		}
	})
	t.Run("outside read", func(t *testing.T) {
		out, _ := runWrapped(t, sandbox, profile, root, "cat "+shellQuote(outside)+" > leak.txt")
		if strings.Contains(out, "OUTSIDE_SECRET") {
			t.Fatal("outside secret was read")
		}
		if data, _ := os.ReadFile(filepath.Join(root, "leak.txt")); strings.Contains(string(data), "OUTSIDE_SECRET") {
			t.Fatal("outside secret reached a project file")
		}
	})
	t.Run("outside write", func(t *testing.T) {
		target := filepath.Join(area, "escaped.txt")
		if _, err := runWrapped(t, sandbox, profile, root, "printf ESCAPE > "+shellQuote(target)); err == nil {
			t.Fatal("outside write unexpectedly succeeded")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("outside marker exists")
		}
	})
	t.Run("read approval is not a write approval", func(t *testing.T) {
		readProfile, err := sandbox.profile("read-only", []string{outside}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := runWrapped(t, sandbox, readProfile, root, "cat "+shellQuote(outside)); err != nil || !strings.Contains(out, "OUTSIDE_SECRET") {
			t.Fatalf("approved read failed: %v %q", err, out)
		}
		if _, err := runWrapped(t, sandbox, readProfile, root, "printf MUTATED > "+shellQuote(outside)); err == nil {
			t.Fatal("a read approval granted write access")
		}
		if data, _ := os.ReadFile(outside); string(data) != "OUTSIDE_SECRET" {
			t.Fatalf("outside file was mutated: %q", data)
		}
	})
	var calls atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "LOCAL")
	}))
	defer service.Close()
	t.Run("direct loopback POST", func(t *testing.T) {
		command := "/usr/bin/curl --noproxy '*' -sS --max-time 3 -X POST " + shellQuote(service.URL)
		_, _ = runWrapped(t, sandbox, profile, root, command)
		if calls.Load() != 0 {
			t.Fatal("sandboxed command reached a direct loopback service")
		}
	})
}

func TestNativeSandboxScrubsCredentialEnvironment(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	sandbox, err := newNativeSandbox(filepath.Join(area, "private"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	profile, err := sandbox.profile("scrub", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCOGEM_MCP_TOKEN", "SECRET_MCP_TOKEN")
	t.Setenv("ANTHROPIC_API_KEY", "SECRET_ANTHROPIC_KEY")
	out, _ := runWrapped(t, sandbox, profile, root, "printf '%s|%s' \"$COCOGEM_MCP_TOKEN\" \"$ANTHROPIC_API_KEY\"")
	if strings.Contains(out, "SECRET_MCP_TOKEN") || strings.Contains(out, "SECRET_ANTHROPIC_KEY") {
		t.Fatalf("credential environment leaked into the sandbox: %q", out)
	}
}

func TestSandboxProfileScopesReadAndWriteSeparately(t *testing.T) {
	area := t.TempDir()
	sandbox, err := newNativeSandbox(filepath.Join(area, "private"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(area, "root")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("X"), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := sandbox.profile("read", []string{outside}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "(version 1)\n(deny default)") {
		t.Fatal("the profile is not deny-by-default")
	}
	writeSection := ""
	if _, after, ok := strings.Cut(text, "(allow file-write*"); ok {
		writeSection = after
	}
	if strings.Contains(writeSection, outside) {
		t.Fatal("a read-only approval widened the write rules")
	}
	if !strings.Contains(text, "(subpath \""+outside+"\")") {
		t.Fatal("the approved read scope is missing")
	}
	if strings.Contains(text, "localhost:*") {
		t.Fatal("the profile allows every loopback port")
	}
	if strings.Contains(text, ".cargo\"") || strings.Contains(text, ".cargo/credentials") {
		t.Fatal("the profile exposes the user's cargo credentials directory")
	}
}

func TestNativeSandboxProfileIsMinimal(t *testing.T) {
	area := t.TempDir()
	sandbox, err := newNativeSandbox(filepath.Join(area, "private"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	sandbox.proxyAddr = "127.0.0.1:45678"
	profile, err := sandbox.profile("minimal", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "(version 1)\n(deny default)") {
		t.Fatal("profile is not deny-by-default")
	}
	if !strings.Contains(text, `(remote ip "localhost:45678")`) {
		t.Fatal("the exact egress proxy port is missing")
	}
	for _, forbidden := range []string{"localhost:*", "/Users/sunday/.cargo\"", "/private/etc/passwd", "/private/etc\""} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("profile exposes %q", forbidden)
		}
	}
}

func TestNativeSandboxMITMBlocksDependencyPost(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	var gets, posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		} else {
			gets.Add(1)
		}
		_, _ = w.Write([]byte("UPSTREAM"))
	}))
	defer server.Close()
	proxy, err := newEgressProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	proxy.allowTest = true
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	proxy.upstreamRoots = roots
	sandbox, err := newNativeSandbox(filepath.Join(area, "private"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	sandbox.proxyURL = proxy.url()
	sandbox.proxyAddr = proxy.addr()
	if err := sandbox.setCA(proxy.caPEM); err != nil {
		t.Fatal(err)
	}
	profile, err := sandbox.profile("mitm", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyArgs := "--noproxy '' --proxy " + shellQuote(proxy.url()) + " "
	readOut, readErr := runWrapped(t, sandbox, profile, root, "/usr/bin/curl "+proxyArgs+"-sS --max-time 10 "+shellQuote(server.URL))
	if readErr != nil || !strings.Contains(readOut, "UPSTREAM") {
		t.Fatalf("dependency read failed: %v %q", readErr, readOut)
	}
	writeOut, _ := runWrapped(t, sandbox, profile, root, "/usr/bin/curl "+proxyArgs+"-sS --max-time 10 -X POST "+shellQuote(server.URL))
	if !strings.Contains(writeOut, "external write") {
		t.Fatalf("dependency POST was not refused: %q", writeOut)
	}
	if posts.Load() != 0 || gets.Load() != 1 {
		t.Fatalf("upstream received posts=%d gets=%d", posts.Load(), gets.Load())
	}
}

// Opt-in real public-egress check: a dependency read must work through the
// MITM method policy, while an external write must not reach the upstream.
func TestNativeSandboxRealPublicEgress(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_NATIVE_EGRESS") != "1" {
		t.Skip("set COCOGEM_CHECK_NATIVE_EGRESS=1 to exercise real public egress")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	proxy, err := newEgressProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	sandbox, err := newNativeSandbox(filepath.Join(area, "private"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	sandbox.proxyURL = proxy.url()
	sandbox.proxyAddr = proxy.addr()
	if err := sandbox.setCA(proxy.caPEM); err != nil {
		t.Fatal(err)
	}
	profile, err := sandbox.profile("public", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	readOut, readErr := runWrapped(t, sandbox, profile, root, "/usr/bin/curl -sS --max-time 20 -o /dev/null -w '%{http_code}' https://registry.npmjs.org/")
	if readErr != nil || !strings.Contains(readOut, "200") {
		t.Fatalf("public dependency read failed: %v %q", readErr, readOut)
	}
	headOut, headErr := runWrapped(t, sandbox, profile, root, "/usr/bin/curl -sS -I --max-time 20 -o /dev/null -w '%{http_code}' https://registry.npmjs.org/")
	if headErr != nil || !strings.Contains(headOut, "200") {
		t.Fatalf("public dependency HEAD failed: %v %q", headErr, headOut)
	}
}

// Opt-in real dependency check: a normal package download must work through
// the method-filtering egress proxy inside the OS sandbox.
func TestNativeSandboxDependencyInstall(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_NATIVE_DEPENDENCIES") != "1" {
		t.Skip("set COCOGEM_CHECK_NATIVE_DEPENDENCIES=1 to exercise a real package download")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm is not installed")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	proxy, err := newEgressProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	sandbox, err := newNativeSandbox(filepath.Join(area, "private"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	sandbox.proxyURL = proxy.url()
	sandbox.proxyAddr = proxy.addr()
	if err := sandbox.setCA(proxy.caPEM); err != nil {
		t.Fatal(err)
	}
	profile, err := sandbox.profile("npm", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, runErr := runWrapped(t, sandbox, profile, root, shellQuote(npm)+" pack left-pad --pack-destination . --no-audit --no-fund 2>&1")
	if runErr != nil {
		t.Fatalf("dependency download failed: %v %s", runErr, out)
	}
	entries, _ := os.ReadDir(root)
	found := false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tgz") {
			found = true
		}
	}
	if !found {
		t.Fatalf("dependency download produced no package: %s", out)
	}
}

func TestNativeSandboxDeniesHardlinkedInode(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "root")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "pkg", "linked.txt")
	if err := os.Link(outside, link); err != nil {
		t.Fatal(err)
	}
	scan := scanProjectHardlinks(root)
	sandbox, err := newNativeSandbox(filepath.Join(area, "private"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	sandbox.setHardlinkDenies(scan.dangerous, scan.unknown)
	profile, err := sandbox.profile("hardlink", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"cat pkg/linked.txt", "printf MUTATED > pkg/linked.txt"} {
		_, _ = runWrapped(t, sandbox, profile, root, command)
	}
	if data, _ := os.ReadFile(outside); string(data) != "ORIGINAL" {
		t.Fatalf("hardlinked outside file changed: %q", data)
	}
}
