package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	w "github.com/AcSunday/cocogem/internal/workspace"
	"golang.org/x/net/html"
)

var reserved = []netip.Prefix{
	netip.MustParsePrefix("fec0::/10"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"),
}

// PublicIP excludes local, reserved and metadata-network destinations.
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range reserved {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateURL restricts the transport before DNS and at each redirect.
func ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || len(raw) > 4000 {
		return nil, errors.New("invalid public URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("only public HTTP(S) URLs are supported")
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return nil, errors.New("nonstandard web ports are not allowed")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, errors.New("local destinations are not allowed")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !PublicIP(ip) {
		return nil, errors.New("nonpublic destinations are not allowed")
	}
	return u, nil
}

// Web captures public sources using an SSRF-resistant transport.
type Web struct {
	client    *http.Client
	searchKey string
	demo      bool
}

// NewWeb never inherits ambient proxy settings or browser authentication.
func NewWeb(searchKey string, demo bool) *Web {
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableKeepAlives: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("web DNS resolution failed")
		}
		if len(ips) == 0 {
			return nil, errors.New("no public address")
		}
		for _, ip := range ips {
			if !PublicIP(ip) {
				return nil, errors.New("DNS resolved to a nonpublic address")
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return &Web{searchKey: searchKey, demo: demo, client: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("too many redirects")
		}
		_, err := ValidateURL(req.URL.String())
		return err
	}}}
}

// SearchEnabled distinguishes missing credentials from an empty result set.
func (wb *Web) SearchEnabled() bool { return wb.demo || wb.searchKey != "" }

// Fetch records actual text, limiting decompressed bytes before HTML parsing.
func (wb *Web) Fetch(ctx context.Context, raw string) (w.Source, error) {
	u, err := ValidateURL(raw)
	if err != nil {
		return w.Source{}, err
	}
	if wb.demo {
		if u.String() != "https://example.org/cocogem/research" {
			return w.Source{}, errors.New("demo mode only reads bundled sources")
		}
		return w.Source{Kind: "web", Title: "离线演示：Agent 协作方法", URL: u.String(), Content: "示例资料：工具循环负责调用模型和工具。多成员协作需要明确身份、来源、有限步骤以及失败恢复。该资料仅为离线演示，不能证明真实模型效果。", CapturedAt: w.Now()}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return w.Source{}, err
	}
	req.Header.Set("User-Agent", "Cocogem/0.1 (public-source research)")
	resp, err := wb.client.Do(req)
	if err != nil {
		return w.Source{}, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return w.Source{}, fmt.Errorf("source returned HTTP %d", resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "text/plain") && !strings.HasPrefix(ct, "text/html") && !strings.HasPrefix(ct, "application/xhtml+xml") {
		return w.Source{}, errors.New("source is not a supported text page")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil {
		return w.Source{}, err
	}
	if len(b) > 2*1024*1024 {
		return w.Source{}, errors.New("page exceeds 2 MiB capture budget")
	}
	title := u.Hostname()
	text := string(b)
	if !strings.HasPrefix(ct, "text/plain") {
		title, text, err = extractHTML(text)
		if err != nil {
			return w.Source{}, err
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	truncated := len(text) > 32768
	if truncated {
		text = string([]rune(text)[:min(len([]rune(text)), 10000)])
	}
	return w.Source{Kind: "web", Title: title, URL: resp.Request.URL.String(), Content: text, CapturedAt: w.Now(), Truncated: truncated}, nil
}

func extractHTML(input string) (string, string, error) {
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return "", "", err
	}
	var title, text strings.Builder
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, inTitle bool) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
			return
		}
		inTitle = inTitle || n.Type == html.ElementNode && n.Data == "title"
		if n.Type == html.TextNode {
			if inTitle {
				title.WriteString(n.Data)
			} else {
				text.WriteString(n.Data)
				text.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inTitle)
		}
	}
	walk(doc, false)
	return strings.TrimSpace(title.String()), text.String(), nil
}

// Search captures search snippets separately from fetched original text.
func (wb *Web) Search(ctx context.Context, query string) ([]w.Source, error) {
	if !wb.SearchEnabled() {
		return nil, errors.New("web search is not configured")
	}
	if len(query) == 0 || len(query) > 2000 {
		return nil, errors.New("invalid search query")
	}
	if wb.demo {
		return []w.Source{{Kind: "search_snippet", Title: "离线演示：Agent 协作方法", URL: "https://example.org/cocogem/research", Content: "演示摘要，需读取原文后核对结论。", CapturedAt: w.Now()}}, nil
	}
	u := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query) + "&count=10"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", wb.searchKey)
	req.Header.Set("Accept", "application/json")
	client := *wb.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("search request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("search returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&result); err != nil {
		return nil, errors.New("invalid search response")
	}
	out := []w.Source{}
	for _, r := range result.Web.Results[:min(10, len(result.Web.Results))] {
		if _, err := ValidateURL(r.URL); err != nil {
			continue
		}
		out = append(out, w.Source{Kind: "search_snippet", Title: r.Title, URL: r.URL, Content: r.Description, CapturedAt: w.Now()})
	}
	return out, nil
}
