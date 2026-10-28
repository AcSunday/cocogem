package tools

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestURLAndAddressPolicy(t *testing.T) {
	for _, raw := range []string{"http://localhost/a", "http://127.0.0.1", "http://[::1]", "http://169.254.169.254/metadata", "https://10.0.0.1", "file:///etc/passwd", "http://user:pass@example.com", "https://example.com:8080", "http://100.64.0.1", "https://192.0.2.1"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ValidateURL(raw); err == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
	if _, err := ValidateURL("https://example.com/article"); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "224.0.0.1"} {
		if PublicIP(netip.MustParseAddr(ip)) {
			t.Fatal(ip)
		}
	}
	if !PublicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public IP rejected")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicCaptureBudgetsAndPrivateRedirect(t *testing.T) {
	for _, tc := range []struct {
		name, content, contentType string
		code                       int
		bad                        bool
	}{{"html", "<title>Evidence</title><p>Original text</p><script>ignore()</script>", "text/html", 200, false}, {"unsupported", "binary", "application/pdf", 200, true}, {"oversized", strings.Repeat("x", 2*1024*1024+1), "text/plain", 200, true}, {"HTTP failure", "not found", "text/plain", 404, true}} {
		t.Run(tc.name, func(t *testing.T) {
			web := NewWeb("")
			web.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.code, Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.content)), Request: r}, nil
			})
			source, err := web.Fetch(context.Background(), "https://example.com/article")
			if (err != nil) != tc.bad {
				t.Fatal(err)
			}
			if !tc.bad && (source.Title != "Evidence" || strings.Contains(source.Content, "ignore()")) {
				t.Fatal(source)
			}
		})
	}
	web := NewWeb("test-key")
	calls := 0
	web.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, err := web.Fetch(context.Background(), "https://example.com"); err == nil || calls != 1 {
		t.Fatal("private redirect followed", calls, err)
	}
	calls = 0
	if _, err := web.Search(context.Background(), "q"); err == nil || calls != 1 {
		t.Fatal("search credentials followed redirect", calls, err)
	}
	production := NewWeb("")
	transport := production.client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("inherited proxy")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("dialed private address")
	}
}

type testConn struct{ net.Conn }

func TestDialerUsesOnlyPublicResolvedAddresses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []netip.Addr
		lookupErr error
		dialFails map[string]bool
		wantDials []string
		wantErr   string
	}{
		{
			name:      "reserved before public",
			addresses: []netip.Addr{netip.MustParseAddr("2001::1"), netip.MustParseAddr("8.8.8.8")},
			wantDials: []string{"8.8.8.8:443"},
		},
		{
			name:      "reserved after public",
			addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2001::1")},
			wantDials: []string{"8.8.8.8:443"},
		},
		{
			name:      "retries only public addresses",
			addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")},
			dialFails: map[string]bool{"8.8.8.8:443": true},
			wantDials: []string{"8.8.8.8:443", "1.1.1.1:443"},
		},
		{
			name:      "reserved only",
			addresses: []netip.Addr{netip.MustParseAddr("2001::1"), netip.MustParseAddr("127.0.0.1")},
			wantErr:   "nonpublic",
		},
		{name: "empty resolution", wantErr: "no public address"},
		{name: "resolution failure", lookupErr: errors.New("boom"), wantErr: "DNS resolution failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups := 0
			lookup := func(context.Context, string, string) ([]netip.Addr, error) {
				lookups++
				return tc.addresses, tc.lookupErr
			}
			dials := []string{}
			dial := func(_ context.Context, network, address string) (net.Conn, error) {
				if network != "tcp" {
					t.Fatalf("dialed network %q", network)
				}
				dials = append(dials, address)
				if tc.dialFails[address] {
					return nil, errors.New("connection refused")
				}
				return testConn{}, nil
			}
			web := newWeb("", lookup, dial)
			transport := web.client.Transport.(*http.Transport)
			_, err := transport.DialContext(context.Background(), "tcp", "example.com:443")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if lookups != 1 {
				t.Fatalf("DNS resolved %d times, want 1", lookups)
			}
			if !slices.Equal(dials, tc.wantDials) {
				t.Fatalf("dialed %v, want %v", dials, tc.wantDials)
			}
			for _, address := range dials {
				host, _, splitErr := net.SplitHostPort(address)
				ip, parseErr := netip.ParseAddr(host)
				if splitErr != nil || parseErr != nil || !PublicIP(ip) {
					t.Fatalf("dialed unverified address %q", address)
				}
			}
		})
	}
}

func TestFetchRejectsPagesWithoutReadableText(t *testing.T) {
	for _, tc := range []struct{ name, content, contentType string }{
		{"client-rendered html", `<html><head><title>DeBank</title></head><body><div id="root"></div><script>render()</script></body></html>`, "text/html"},
		{"blank text", " \n\t ", "text/plain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			web := NewWeb("")
			web.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.content)), Request: r}, nil
			})
			source, err := web.Fetch(context.Background(), "https://debank.com/")
			if err == nil || !strings.Contains(err.Error(), "no readable text") {
				t.Fatalf("page without text was reported as a successful capture: source=%+v err=%v", source, err)
			}
		})
	}
}

func TestFetchMarksSearchResultPagesAsLeads(t *testing.T) {
	for _, raw := range []string{
		"https://www.so.com/s?q=DeBank",
		"https://m.so.com/s?q=DeBank",
		"https://cn.bing.com/search?q=DeBank",
		"https://www.sogou.com/web?query=DeBank",
		"https://www.baidu.com/s?wd=DeBank",
		"https://html.duckduckgo.com/html/?q=DeBank",
		"https://search.brave.com/search?q=DeBank",
		"https://www.google.com/search?q=DeBank",
	} {
		t.Run(raw, func(t *testing.T) {
			web := NewWeb("")
			web.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<title>Results</title><p>result listing</p>")), Request: r}, nil
			})
			source, err := web.Fetch(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if source.Kind != "search_results" {
				t.Fatalf("search results page captured as %q", source.Kind)
			}
		})
	}
	web := NewWeb("")
	web.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<title>Article</title><p>original text</p>")), Request: r}, nil
	})
	source, err := web.Fetch(context.Background(), "https://example.org/article?q=DeBank")
	if err != nil || source.Kind != "web" {
		t.Fatalf("original page kind=%q err=%v", source.Kind, err)
	}
}

func TestHTMLAndSearchAvailability(t *testing.T) {
	title, body, err := extractHTML("<title>Title</title><p>Visible</p><script>secretScript()</script>")
	if err != nil || title != "Title" || strings.Contains(body, "secretScript") {
		t.Fatal(title, body, err)
	}
	if _, err := NewWeb("").Search(context.Background(), "q"); err == nil {
		t.Fatal("missing key hidden")
	}
}

func TestFetchUsesTransportForFormerFixtureURL(t *testing.T) {
	web := NewWeb("")
	calls := 0
	web.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("Transport response")), Request: request}, nil
	})
	source, err := web.Fetch(t.Context(), "https://example.org/cocogem/research")
	if err != nil || calls != 1 || source.Content != "Transport response" || web.SearchEnabled() {
		t.Fatalf("fixture intercepted the request: %+v calls=%d error=%v", source, calls, err)
	}
}

func TestExternalSourceMetadataIsBounded(t *testing.T) {
	web := NewWeb("")
	web.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<title>" + strings.Repeat("X", 120000) + "</title><p>body</p>")), Request: r}, nil
	})
	source, err := web.Fetch(context.Background(), "https://example.com")
	if err != nil || len(source.Title) > 300 || source.Content != "body" {
		t.Fatal(len(source.Title), err)
	}
}

func TestKeylessSearchFallbackCapturesSnippets(t *testing.T) {
	web := NewWebWithFallback("")
	if !web.SearchEnabled() || web.SearchProvider() != "bing+duckduckgo" {
		t.Fatalf("fallback provider unavailable: %s", web.SearchProvider())
	}
	web.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		page := `<html><body>
<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fdebank">DeBank overview</a>
<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fdebank">DeBank is a Web3 portfolio and DeFi data platform.</a>
<a class="result__a" href="//duckduckgo.com/l/?uddg=http%3A%2F%2F127.0.0.1%2Fprivate">unsafe</a>
<a class="result__snippet">unsafe snippet</a>
</body></html>`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(page)), Request: request}, nil
	})
	sources, err := web.Search(context.Background(), "debank")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].URL != "https://example.com/debank" || !strings.Contains(sources[0].Content, "Web3 portfolio") || sources[0].Kind != "search_snippet" {
		t.Fatalf("unexpected fallback results: %+v", sources)
	}
	if NewWeb("").SearchEnabled() {
		t.Fatal("explicit empty constructor enabled search")
	}
}

func TestLiveKeylessSearch(t *testing.T) {
	if os.Getenv("COCOGEM_LIVE_DDG") != "1" {
		t.Skip("set COCOGEM_LIVE_DDG=1 for a public search connectivity check")
	}
	sources, err := NewWebWithFallback("").Search(context.Background(), "debank company")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 || sources[0].Kind != "search_snippet" || !strings.HasPrefix(sources[0].URL, "http") {
		t.Fatalf("unexpected live search: %+v", sources)
	}
	t.Logf("provider=%s results=%d first=%s %s", NewWebWithFallback("").SearchProvider(), len(sources), sources[0].Title, sources[0].URL)
}

func TestKeylessBingRSSFallback(t *testing.T) {
	web := NewWebWithFallback("")
	web.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Host, "bing.com") {
			feed := `<?xml version="1.0"?><rss><channel><item><title>DeBank careers</title><link>https://example.com/jobs</link><description>Open roles and compensation.</description></item><item><title>Reviews</title><link>http://127.0.0.1/private</link><description>unsafe</description></item></channel></rss>`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/rss+xml"}}, Body: io.NopCloser(strings.NewReader(feed)), Request: request}, nil
		}
		return nil, io.ErrUnexpectedEOF
	})
	sources, err := web.Search(context.Background(), "debank salary")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].URL != "https://example.com/jobs" || !strings.Contains(sources[0].Content, "compensation") {
		t.Fatalf("unexpected Bing fallback: %+v", sources)
	}
}
