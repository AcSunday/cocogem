package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AcSunday/cocogem/internal/workflow"
)

func TestRemovedModesCannotStartOrCreateData(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "server")
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build server: %s: %v", output, err)
	}
	for _, argument := range []string{"--demo", "--demo-pace=0"} {
		t.Run(argument, func(t *testing.T) {
			data := filepath.Join(dir, "unexpected-data")
			command := exec.CommandContext(t.Context(), binary, argument)
			command.Env = append(os.Environ(), "COCOGEM_DATA_DIR="+data)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "flag provided but not defined") {
				t.Fatalf("removed mode still accepted: %s: %v", output, err)
			}
			if _, err := os.Stat(data); !os.IsNotExist(err) {
				t.Fatalf("removed mode created data: %v", err)
			}
		})
	}
	dependencies, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil || strings.Contains(string(dependencies), "/internal/testutil") {
		t.Fatalf("product links test fixtures: %s: %v", dependencies, err)
	}
}

func TestLongTaskLimitsAreConfigurable(t *testing.T) {
	for _, key := range []string{"COCOGEM_RUN_TIMEOUT", "COCOGEM_STEP_TIMEOUT", "COCOGEM_FIRST_RESPONSE_TIMEOUT", "COCOGEM_STALL_TIMEOUT", "COCOGEM_STALL_ACTION"} {
		t.Setenv(key, "")
	}
	limits, err := loadLimits()
	if err != nil {
		t.Fatal(err)
	}
	if limits.RunTimeout != 0 || limits.StepTimeout != 0 || limits.FirstResponse.String() != "10m0s" || limits.Stall.String() != "15m0s" || limits.StallAction != "warn" {
		t.Fatalf("unset limits did not use the long-task defaults: %+v", limits)
	}
	for _, key := range []string{"COCOGEM_RUN_TIMEOUT", "COCOGEM_STEP_TIMEOUT", "COCOGEM_FIRST_RESPONSE_TIMEOUT", "COCOGEM_STALL_TIMEOUT"} {
		t.Setenv(key, "0")
	}
	limits, err = loadLimits()
	if err != nil {
		t.Fatal(err)
	}
	if limits.RunTimeout != 0 || limits.StepTimeout != 0 || limits.FirstResponse != 0 || limits.Stall != 0 || limits.StallAction != "warn" {
		t.Fatalf("explicit zero was not preserved: %+v", limits)
	}
	t.Setenv("COCOGEM_RUN_TIMEOUT", "2h")
	t.Setenv("COCOGEM_STEP_TIMEOUT", "45m")
	t.Setenv("COCOGEM_FIRST_RESPONSE_TIMEOUT", "3m")
	t.Setenv("COCOGEM_STALL_TIMEOUT", "8m")
	t.Setenv("COCOGEM_STALL_ACTION", "stop")
	limits, err = loadLimits()
	if err != nil {
		t.Fatal(err)
	}
	if limits.RunTimeout.String() != "2h0m0s" || limits.StepTimeout.String() != "45m0s" || limits.FirstResponse.String() != "3m0s" || limits.Stall.String() != "8m0s" || limits.StallAction != "stop" {
		t.Fatalf("configured limits not applied: %+v", limits)
	}
	t.Setenv("COCOGEM_RUN_TIMEOUT", "not-a-duration")
	if _, err := loadLimits(); err == nil {
		t.Fatal("invalid duration accepted")
	}
	t.Setenv("COCOGEM_RUN_TIMEOUT", "-1s")
	if _, err := loadLimits(); err == nil {
		t.Fatal("negative duration accepted")
	}
	t.Setenv("COCOGEM_RUN_TIMEOUT", "0")
	t.Setenv("COCOGEM_STALL_ACTION", "pause")
	if _, err := loadLimits(); err == nil {
		t.Fatal("invalid stall action accepted")
	}
	if seconds := limitSeconds(workflow.DefaultLimits()); seconds["run_seconds"] != 0 || seconds["step_seconds"] != 0 || seconds["first_response_seconds"] != 600 || seconds["stall_seconds"] != 900 {
		t.Fatalf("system limits did not reflect disabled wall-clock limits: %v", seconds)
	}
}

func TestSearchProviderSelection(t *testing.T) {
	for _, test := range []struct {
		key, provider string
		enabled       bool
		name          string
	}{
		{"", "auto", true, "bing+duckduckgo"},
		{"brave-key", "auto", true, "brave"},
		{"", "bing", true, "bing+duckduckgo"},
		{"", "off", false, ""},
		{"", "brave", false, ""},
	} {
		web, err := newWeb(test.key, test.provider, "")
		if err != nil {
			t.Fatal(err)
		}
		if web.SearchEnabled() != test.enabled || web.SearchProvider() != test.name {
			t.Fatalf("%s/%s: enabled=%v provider=%s", test.key, test.provider, web.SearchEnabled(), web.SearchProvider())
		}
	}
	if _, err := newWeb("", "unexpected", ""); err == nil {
		t.Fatal("unknown search provider accepted")
	}
}

func TestWebProxyConfiguration(t *testing.T) {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(key, "")
	}
	if _, err := newWeb("", "auto", "env"); err != nil {
		t.Fatalf("empty env proxy configuration must go direct: %v", err)
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:18080")
	web, err := newWeb("", "auto", "env")
	if err != nil || !web.SearchEnabled() {
		t.Fatalf("env proxy was not applied: %v", err)
	}
	if _, err := newWeb("", "auto", "file:///tmp/proxy"); err == nil {
		t.Fatal("unsupported proxy scheme accepted")
	}
}

func TestWebProxySelectionPriority(t *testing.T) {
	var envHits, explicitHits atomic.Int32
	newProxy := func(hits *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "proxied evidence")
		}))
	}
	envProxy, explicitProxy := newProxy(&envHits), newProxy(&explicitHits)
	defer envProxy.Close()
	defer explicitProxy.Close()
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(key, "")
	}

	t.Setenv("HTTPS_PROXY", envProxy.URL)
	web, err := newWeb("", "auto", "env")
	if err != nil {
		t.Fatal(err)
	}
	if source, err := web.Fetch(context.Background(), "http://example.com/env"); err != nil || source.Content != "proxied evidence" || envHits.Load() != 1 {
		t.Fatalf("env proxy was not used: source=%+v hits=%d err=%v", source, envHits.Load(), err)
	}

	web, err = newWeb("", "auto", explicitProxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	if source, err := web.Fetch(context.Background(), "http://example.com/explicit"); err != nil || source.Content != "proxied evidence" || explicitHits.Load() != 1 || envHits.Load() != 1 {
		t.Fatalf("explicit proxy did not win over env: source=%+v env=%d explicit=%d err=%v", source, envHits.Load(), explicitHits.Load(), err)
	}

	t.Setenv("HTTPS_PROXY", "file:///not-a-proxy")
	if _, err := newWeb("", "auto", ""); err != nil {
		t.Fatalf("explicit empty configuration consulted env: %v", err)
	}
}

func TestSystemProxyReadFailureStopsStartup(t *testing.T) {
	original := detectSystemProxy
	defer func() { detectSystemProxy = original }()
	detectSystemProxy = func() (string, error) { return "", errors.New("scutil timed out") }
	if _, err := newWeb("", "auto", "system"); err == nil || !strings.Contains(err.Error(), "scutil timed out") {
		t.Fatalf("system proxy read failure was hidden by a direct fallback: %v", err)
	}
	detectSystemProxy = func() (string, error) { return "", nil }
	if _, err := newWeb("", "auto", "system"); err != nil {
		t.Fatalf("no configured system proxy must allow direct access: %v", err)
	}
}

func TestParseSystemProxy(t *testing.T) {
	const full = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 192.168.0.0/16
  }
  HTTPEnable : 1
  HTTPPort : 18080
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 18443
  HTTPSProxy : proxy.example
  SOCKSEnable : 1
  SOCKSPort : 11080
  SOCKSProxy : 127.0.0.1
}`
	for _, tc := range []struct{ name, text, want string }{
		{"https preferred", full, "http://proxy.example:18443"},
		{"http fallback", "HTTPEnable : 1\nHTTPPort : 18080\nHTTPProxy : 127.0.0.1", "http://127.0.0.1:18080"},
		{"socks fallback", "SOCKSEnable : 1\nSOCKSPort : 11080\nSOCKSProxy : 127.0.0.1", "socks5://127.0.0.1:11080"},
		{"ipv6 host", "HTTPEnable : 1\nHTTPPort : 18080\nHTTPProxy : ::1", "http://[::1]:18080"},
		{"disabled", "HTTPEnable : 0\nHTTPPort : 18080\nHTTPProxy : 127.0.0.1", ""},
		{"disabled https falls back to socks", "HTTPSEnable : 0\nHTTPSPort : 18443\nHTTPSProxy : proxy.example\nSOCKSEnable : 1\nSOCKSPort : 11080\nSOCKSProxy : 127.0.0.1", "socks5://127.0.0.1:11080"},
		{"pac only", "ProxyAutoConfigEnable : 1\nProxyAutoConfigURLString : http://127.0.0.1:18080/proxy.pac", ""},
		{"incomplete", "HTTPSEnable : 1\nHTTPSProxy : proxy.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSystemProxy(tc.text); got != tc.want {
				t.Fatalf("parseSystemProxy()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestSystemProxyIsOptInAndRoutesWebTraffic(t *testing.T) {
	requests := []string{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "proxied evidence")
	}))
	defer proxy.Close()
	calls := 0
	original := detectSystemProxy
	detectSystemProxy = func() (string, error) {
		calls++
		return proxy.URL, nil
	}
	defer func() { detectSystemProxy = original }()

	if _, err := newWeb("", "auto", ""); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("system proxy was consulted without explicit opt-in")
	}
	web, err := newWeb("", "auto", "system")
	if err != nil || calls != 1 {
		t.Fatalf("system proxy was not applied: calls=%d err=%v", calls, err)
	}
	if web.ProxyURL() != proxy.URL {
		t.Fatalf("resolved proxy was not exposed for native children: %q", web.ProxyURL())
	}
	source, err := web.Fetch(context.Background(), "http://example.com/article")
	if err != nil || source.Content != "proxied evidence" || len(requests) != 1 {
		t.Fatalf("request did not reach the proxy: source=%+v requests=%v err=%v", source, requests, err)
	}
	if _, err := web.Fetch(context.Background(), "http://example.com/redirect"); err == nil {
		t.Fatal("private redirect was followed")
	}
	if len(requests) != 2 {
		t.Fatalf("private redirect escaped the URL policy: %v", requests)
	}
}

func TestLiveSystemProxySearch(t *testing.T) {
	if os.Getenv("COCOGEM_LIVE_SYSTEM_PROXY") != "1" {
		t.Skip("set COCOGEM_LIVE_SYSTEM_PROXY=1 to exercise the configured OS proxy")
	}
	if proxy, err := readSystemProxy(); err != nil {
		t.Fatal(err)
	} else if proxy == "" {
		t.Skip("no OS-configured HTTP/HTTPS/SOCKS proxy")
	}
	web, err := newWeb("", "auto", "system")
	if err != nil {
		t.Fatal(err)
	}
	sources, err := web.Search(context.Background(), "golang")
	if err != nil || len(sources) == 0 {
		t.Fatalf("live search through the system proxy failed: %v", err)
	}
	source, err := web.Fetch(context.Background(), "https://example.com/")
	if err != nil || source.Content == "" {
		t.Fatalf("live fetch through the system proxy failed: %v", err)
	}
	t.Logf("provider=%s results=%d first=%s fetched=%s", web.SearchProvider(), len(sources), sources[0].URL, source.URL)
}
