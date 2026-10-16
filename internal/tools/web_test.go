package tools

import (
	"context"
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
