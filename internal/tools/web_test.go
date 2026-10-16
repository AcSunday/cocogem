package tools

import (
	"context"
	"io"
	"net/http"
	"net/netip"
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
			web := NewWeb("", false)
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
	web := NewWeb("test-key", false)
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
	production := NewWeb("", false)
	transport := production.client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("inherited proxy")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("dialed private address")
	}
}

func TestHTMLAndDemoIsolation(t *testing.T) {
	title, body, err := extractHTML("<title>Title</title><p>Visible</p><script>secretScript()</script>")
	if err != nil || title != "Title" || strings.Contains(body, "secretScript") {
		t.Fatal(title, body, err)
	}
	w := NewWeb("", true)
	if _, err := w.Fetch(context.Background(), "https://example.com/private"); err == nil {
		t.Fatal("demo escaped fixture")
	}
	if _, err := NewWeb("", false).Search(context.Background(), "q"); err == nil {
		t.Fatal("missing key hidden")
	}
}

func TestExternalSourceMetadataIsBounded(t *testing.T) {
	web := NewWeb("", false)
	web.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<title>" + strings.Repeat("X", 120000) + "</title><p>body</p>")), Request: r}, nil
	})
	source, err := web.Fetch(context.Background(), "https://example.com")
	if err != nil || len(source.Title) > 300 || source.Content != "body" {
		t.Fatal(len(source.Title), err)
	}
}
