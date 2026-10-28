package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
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

// searchResultPage recognizes a search engine listing. It is a lead page, not
// the original source its links point to.
func searchResultPage(u *url.URL) bool {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	query := u.Query()
	switch {
	case host == "so.com" || strings.HasSuffix(host, ".so.com"):
		return strings.TrimSuffix(u.Path, "/") == "/s" && query.Get("q") != ""
	case host == "bing.com" || strings.HasSuffix(host, ".bing.com"):
		return strings.TrimSuffix(u.Path, "/") == "/search" && query.Get("q") != ""
	case host == "sogou.com" || strings.HasSuffix(host, ".sogou.com"):
		return strings.TrimSuffix(u.Path, "/") == "/web" && query.Get("query") != ""
	case host == "baidu.com" || strings.HasSuffix(host, ".baidu.com"):
		return strings.TrimSuffix(u.Path, "/") == "/s" && query.Get("wd") != ""
	case host == "google.com" || strings.HasSuffix(host, ".google.com"):
		return strings.TrimSuffix(u.Path, "/") == "/search" && query.Get("q") != ""
	case host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com"):
		return strings.HasPrefix(u.Path, "/html") && query.Get("q") != ""
	case host == "search.brave.com":
		return strings.TrimSuffix(u.Path, "/") == "/search" && query.Get("q") != ""
	}
	return false
}

// IsSearchResultURL reports whether raw is a search engine listing page.
func IsSearchResultURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && searchResultPage(u)
}

// Web captures public sources using an SSRF-resistant transport.
type Web struct {
	client          *http.Client
	searchKey       string
	fallback        bool
	proxyConfigured bool
	proxyURL        string
}

// NewWeb never inherits ambient proxy settings or browser authentication.
func NewWeb(searchKey string) *Web {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return newWeb(searchKey, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func newWeb(searchKey string, lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) *Web {
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableKeepAlives: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := lookup(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("web DNS resolution failed")
		}
		if len(ips) == 0 {
			return nil, errors.New("no public address")
		}
		// Dial only addresses validated above. A reserved answer (for example a
		// polluted Teredo AAAA) must not block a public A for the same host.
		public := make([]netip.Addr, 0, len(ips))
		for _, ip := range ips {
			if PublicIP(ip) {
				public = append(public, ip)
			}
		}
		if len(public) == 0 {
			return nil, errors.New("DNS resolved to a nonpublic address")
		}
		var lastErr error
		for _, ip := range public {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &Web{searchKey: searchKey, client: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("too many redirects")
		}
		_, err := ValidateURL(req.URL.String())
		return err
	}}}
}

// NewWebWithFallback uses Brave when configured and a keyless DuckDuckGo HTML
// search otherwise. Explicit NewWeb("") remains disabled for callers/tests.
func NewWebWithFallback(searchKey string) *Web {
	web := NewWeb(searchKey)
	web.fallback = searchKey == ""
	return web
}

// NewWebKeyless enables only the keyless public search provider.
func NewWebKeyless() *Web {
	web := NewWeb("")
	web.fallback = true
	return web
}

// UseProxy opts into a trusted public-web proxy. That proxy resolves target
// names, so URL and redirect policy still apply but address checks are
// delegated to this explicit trust boundary. It never reads ambient proxy
// settings by itself; the caller must pass one explicitly.
func (wb *Web) UseProxy(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("invalid web proxy URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5" {
		return errors.New("web proxy must use http, https or socks5")
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsed),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		DisableKeepAlives:     true,
	}
	wb.client.Transport = transport
	wb.proxyConfigured = true
	wb.proxyURL = parsed.String()
	return nil
}

// ProxyURL returns the resolved public-web proxy for native web children. It
// is never written to events or browser state.
func (wb *Web) ProxyURL() string { return wb.proxyURL }

// SearchEnabled distinguishes missing credentials from an empty result set.
func (wb *Web) SearchEnabled() bool { return wb.searchKey != "" || wb.fallback }

// SearchProvider names the active provider without exposing credentials.
func (wb *Web) SearchProvider() string {
	if wb.searchKey != "" {
		return "brave"
	}
	if wb.fallback {
		return "bing+duckduckgo"
	}
	return ""
}

// Fetch records actual text, limiting decompressed bytes before HTML parsing.
func (wb *Web) Fetch(ctx context.Context, raw string) (w.Source, error) {
	u, err := ValidateURL(raw)
	if err != nil {
		return w.Source{}, err
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
	if text == "" {
		// A client-rendered or blocked page yields no evidence; reporting it as
		// a successful capture makes the model treat emptiness as a result.
		return w.Source{}, errors.New("source returned no readable text (the page may require JavaScript or block automated access); try a different URL")
	}
	truncated := len(text) > 32768
	if truncated {
		text = string([]rune(text)[:min(len([]rune(text)), 10000)])
	}
	kind := "web"
	if searchResultPage(resp.Request.URL) {
		kind = "search_results"
	}
	return w.Source{Kind: kind, Title: boundedText(title, 300), URL: resp.Request.URL.String(), Content: text, CapturedAt: w.Now(), Truncated: truncated}, nil
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
	if wb.searchKey == "" {
		if wb.proxyConfigured {
			if sources, err := wb.searchDuckDuckGo(ctx, query); err == nil {
				return sources, nil
			}
			return wb.searchBingRSS(ctx, query)
		}
		if sources, err := wb.searchBingRSS(ctx, query); err == nil {
			return sources, nil
		}
		return wb.searchDuckDuckGo(ctx, query)
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
		return nil, fmt.Errorf("search request failed: %w", err)
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
		out = append(out, w.Source{Kind: "search_snippet", Title: boundedText(r.Title, 300), URL: r.URL, Content: boundedText(r.Description, 1500), CapturedAt: w.Now()})
	}
	return out, nil
}

func boundedText(value string, characters int) string {
	runes := []rune(value)
	if len(runes) > characters {
		return string(runes[:characters])
	}
	return value
}

func (wb *Web) searchDuckDuckGo(ctx context.Context, query string) ([]w.Source, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://html.duckduckgo.com/html/?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Cocogem/0.1)")
	request.Header.Set("Accept", "text/html")
	client := *wb.client
	client.Timeout = 8 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned HTTP %d", response.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil || len(page) > 2*1024*1024 {
		return nil, errors.New("search response exceeds budget")
	}
	links, snippets := duckDuckGoResults(page)
	if len(links) == 0 {
		return nil, errors.New("search returned no usable results")
	}
	out := []w.Source{}
	seen := map[string]bool{}
	for i, link := range links {
		target := duckDuckGoTarget(link.href)
		if target == "" {
			continue
		}
		if _, err := ValidateURL(target); err != nil || seen[target] {
			continue
		}
		seen[target] = true
		description := ""
		if i < len(snippets) {
			description = snippets[i]
		}
		out = append(out, w.Source{Kind: "search_snippet", Title: boundedText(link.title, 300), URL: target, Content: boundedText(description, 1500), CapturedAt: w.Now()})
		if len(out) == 10 {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("search returned no usable results")
	}
	return out, nil
}

type duckLink struct{ href, title string }

func duckDuckGoResults(page []byte) ([]duckLink, []string) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, nil
	}
	links := []duckLink{}
	snippets := []string{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			classes := classSet(node)
			if classes["result__a"] {
				href := ""
				for _, attr := range node.Attr {
					if attr.Key == "href" {
						href = attr.Val
					}
				}
				links = append(links, duckLink{href: href, title: strings.TrimSpace(nodeText(node))})
			}
			if classes["result__snippet"] {
				snippets = append(snippets, strings.Join(strings.Fields(nodeText(node)), " "))
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return links, snippets
}

func classSet(node *html.Node) map[string]bool {
	out := map[string]bool{}
	for _, attr := range node.Attr {
		if attr.Key == "class" {
			for _, value := range strings.Fields(attr.Val) {
				out[value] = true
			}
		}
	}
	return out
}

func nodeText(node *html.Node) string {
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.ElementNode && (current.Data == "script" || current.Data == "style") {
			return
		}
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
			text.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return text.String()
}

func duckDuckGoTarget(href string) string {
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if parsed.Scheme == "" {
		if parsed.Host != "" {
			parsed.Scheme = "https"
		} else {
			base, baseErr := url.Parse("https://html.duckduckgo.com")
			if baseErr != nil {
				return ""
			}
			parsed = base.ResolveReference(parsed)
		}
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if (host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com")) && strings.HasPrefix(parsed.Path, "/l/") {
		return parsed.Query().Get("uddg")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

func (wb *Web) searchBingRSS(ctx context.Context, query string) ([]w.Source, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.bing.com/search?format=rss&q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Cocogem/0.1)")
	request.Header.Set("Accept", "application/rss+xml")
	client := *wb.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("too many search redirects")
		}
		_, err := ValidateURL(req.URL.String())
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("search request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned HTTP %d", response.StatusCode)
	}
	var feed struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description string `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&feed); err != nil {
		return nil, errors.New("invalid search response")
	}
	out := []w.Source{}
	seen := map[string]bool{}
	for _, item := range feed.Channel.Items {
		if _, err := ValidateURL(item.Link); err != nil || seen[item.Link] {
			continue
		}
		seen[item.Link] = true
		title := strings.TrimSpace(item.Title)
		if _, text, err := extractHTML(item.Title); err == nil && strings.TrimSpace(text) != "" {
			title = strings.TrimSpace(text)
		}
		description := strings.TrimSpace(item.Description)
		if _, text, err := extractHTML(item.Description); err == nil && strings.TrimSpace(text) != "" {
			description = strings.Join(strings.Fields(text), " ")
		}
		out = append(out, w.Source{Kind: "search_snippet", Title: boundedText(title, 300), URL: item.Link, Content: boundedText(description, 1500), CapturedAt: w.Now()})
		if len(out) == 10 {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("search returned no usable results")
	}
	return out, nil
}
