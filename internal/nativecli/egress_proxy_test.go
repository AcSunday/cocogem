package nativecli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func proxyAuth(proxy *egressProxy) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("cocogem:"+proxy.token))
}

func TestEgressProxyRejectsLoopbackTunnel(t *testing.T) {
	proxy, err := newEgressProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	for _, host := range []string{"127.0.0.1:1", "localhost:1", "[::1]:1"} {
		request := httptest.NewRequest(http.MethodConnect, "http://"+host, nil)
		request.Host = host
		request.Header.Set("Proxy-Authorization", proxyAuth(proxy))
		recorder := httptest.NewRecorder()
		proxy.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("CONNECT %s = %d, want 403", host, recorder.Code)
		}
	}
	if blocked := proxy.drainBlocked(); len(blocked) == 0 {
		t.Fatal("a refused local tunnel was not recorded")
	}
}

func TestEgressProxyRejectsMutatingPlainHTTP(t *testing.T) {
	proxy, err := newEgressProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	request := httptest.NewRequest(http.MethodPost, "http://example.com/upload", strings.NewReader("x"))
	request.Header.Set("Proxy-Authorization", proxyAuth(proxy))
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("proxy POST = %d, want 403", recorder.Code)
	}
}

func TestEgressProxyRejectsPrivateResolvedAddresses(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "::1", "fe80::1"} {
		if ip := net.ParseIP(raw); !unsafeEgressIP(ip) {
			t.Fatalf("%s was treated as public egress", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if ip := net.ParseIP(raw); unsafeEgressIP(ip) {
			t.Fatalf("%s was treated as private egress", raw)
		}
	}
}

func TestEgressProxyTargetPolicy(t *testing.T) {
	proxy, err := newEgressProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "::1"} {
		if proxy.publicTarget(context.Background(), host) {
			t.Fatalf("private target %s was accepted", host)
		}
	}
	if !proxy.publicTarget(context.Background(), "1.1.1.1") {
		t.Fatal("a public literal was rejected")
	}
}

// A CONNECT request to an allowlisted name that resolves to loopback must be
// refused before a tunnel exists.
func TestEgressProxyRejectsReboundAllowlistedName(t *testing.T) {
	proxy, err := newEgressProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	proxy.resolve = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	request := httptest.NewRequest(http.MethodConnect, "http://api.github.com:443", nil)
	request.Host = "api.github.com:443"
	request.Header.Set("Proxy-Authorization", proxyAuth(proxy))
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("rebound CONNECT = %d, want 403", recorder.Code)
	}
}

func trustProxyCA(t *testing.T, proxy *egressProxy) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(proxy.caPEM) {
		t.Fatal("proxy CA could not be loaded")
	}
	return pool
}

func proxyClient(t *testing.T, proxy *egressProxy) *http.Client {
	t.Helper()
	parsed, err := url.Parse(proxy.url())
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(parsed),
		TLSClientConfig: &tls.Config{RootCAs: trustProxyCA(t, proxy)},
	}}
}

func TestEgressProxyMITMAllowsReadsAndBlocksWrites(t *testing.T) {
	var posts, gets int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		} else {
			gets++
		}
		_, _ = io.WriteString(w, "UPSTREAM_BODY")
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
	client := proxyClient(t, proxy)
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("proxied GET failed: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "UPSTREAM_BODY" {
		t.Fatalf("proxied GET = %d %q", response.StatusCode, body)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("write"))
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("proxied POST errored unexpectedly: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("proxied POST = %d, want 403", response.StatusCode)
	}
	if posts != 0 || gets != 1 {
		t.Fatalf("upstream received posts=%d gets=%d; opaque CONNECT write was forwarded", posts, gets)
	}
}

func TestEgressProxyPlainHTTPBlocksWrites(t *testing.T) {
	var posts, gets int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		} else {
			gets++
		}
		_, _ = io.WriteString(w, "PLAIN_BODY")
	}))
	defer server.Close()
	proxy, err := newEgressProxy(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	proxy.allowTest = true
	client := proxyClient(t, proxy)
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("proxied plain GET failed: %v", err)
	}
	response.Body.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("write"))
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("proxied plain POST errored unexpectedly: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("proxied plain POST = %d, want 403", response.StatusCode)
	}
	if posts != 0 || gets != 1 {
		t.Fatalf("plain upstream received posts=%d gets=%d", posts, gets)
	}
}

func TestEgressProxyHoldsExternalWriteForApproval(t *testing.T) {
	var gets, posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		} else {
			gets.Add(1)
		}
		_, _ = io.WriteString(w, "UPSTREAM")
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
	var approved atomic.Bool
	var calls atomic.Int32
	var seenMethod, seenTarget, seenDigest string
	proxy.approve = func(_ context.Context, method, target, digest string) (bool, string) {
		calls.Add(1)
		seenMethod, seenTarget, seenDigest = method, target, digest
		return approved.Load(), "fixture denied"
	}
	client := proxyClient(t, proxy)
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if calls.Load() != 0 {
		t.Fatal("a read-only request asked for external-write approval")
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/publish", strings.NewReader("payload"))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || posts.Load() != 0 {
		t.Fatalf("unapproved POST = %d, upstream posts=%d", response.StatusCode, posts.Load())
	}
	if calls.Load() != 1 || seenMethod != "POST" || !strings.HasSuffix(seenTarget, "/publish") || seenDigest == "" {
		t.Fatalf("approval evidence = calls:%d method:%s target:%s digest:%s", calls.Load(), seenMethod, seenTarget, seenDigest)
	}
	approved.Store(true)
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/publish", strings.NewReader("payload"))
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || posts.Load() != 1 {
		t.Fatalf("approved POST = %d, upstream posts=%d", response.StatusCode, posts.Load())
	}
	if gets.Load() != 1 {
		t.Fatalf("GET count = %d", gets.Load())
	}
}
