package nativecli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// egressProxy is the only outbound path for a sandboxed native command. It is
// a loopback HTTP/CONNECT proxy protected by a random token and a private CA.
//
// CONNECT is terminated with a per-host certificate signed by that CA, so the
// proxy can enforce methods instead of blindly tunneling opaque TLS:
//   - GET/HEAD/OPTIONS may reach public hosts (dependency downloads, public
//     reads).
//   - POST/PUT/PATCH/DELETE are refused before the upstream request exists.
//
// The sandboxed command receives the CA only through private environment
// variables. A client that ignores them fails closed because direct network is
// denied by the kernel.
type egressProxy struct {
	listener net.Listener
	server   *http.Server
	token    string
	onBlock  func(host, method string)
	resolve  func(context.Context, string) ([]net.IPAddr, error)
	caPEM    []byte
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey

	mu            sync.Mutex
	certs         map[string]*tls.Certificate
	blocked       []string
	closed        bool
	allowTest     bool // tests may target loopback; production never does
	upstreamRoots *x509.CertPool
	approve       func(ctx context.Context, method, target, digest string) (bool, string)
}

func newEgressProxy(onBlock func(host, method string)) (*egressProxy, error) {
	var secret [24]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("open native egress proxy")
	}
	caCert, caKey, caPEM, err := newProxyCA()
	if err != nil {
		listener.Close()
		return nil, err
	}
	p := &egressProxy{
		listener: listener, token: hex.EncodeToString(secret[:]), onBlock: onBlock,
		resolve: net.DefaultResolver.LookupIPAddr, caPEM: caPEM, caCert: caCert, caKey: caKey,
		certs: map[string]*tls.Certificate{},
	}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *egressProxy) url() string {
	return "http://cocogem:" + p.token + "@" + p.listener.Addr().String()
}

func (p *egressProxy) addr() string { return p.listener.Addr().String() }

func (p *egressProxy) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *egressProxy) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	_ = p.server.Close()
}

func (p *egressProxy) record(host, method string) {
	p.mu.Lock()
	if len(p.blocked) < 20 {
		p.blocked = append(p.blocked, method+" "+safeToken(host))
	}
	p.mu.Unlock()
	if p.onBlock != nil {
		p.onBlock(safeToken(host), safeToken(method))
	}
}

func (p *egressProxy) drainBlocked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]string{}, p.blocked...)
	p.blocked = nil
	return out
}

func (p *egressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	p.forward(w, r)
}

func (p *egressProxy) authorized(r *http.Request) bool {
	header := r.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	user, password, ok := strings.Cut(string(raw), ":")
	if !ok || user != "cocogem" {
		return false
	}
	return subtleConstantTimeEqual(password, p.token)
}

func (p *egressProxy) connect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name, port = host, "443"
	}
	if (port != "443" && !p.allowTest) || !p.publicTarget(r.Context(), name) {
		p.record(host, "CONNECT")
		http.Error(w, "external host is not authorized for native commands", http.StatusForbidden)
		return
	}
	leaf, err := p.leaf(name)
	if err != nil {
		http.Error(w, "proxy certificate unavailable", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunneling unavailable", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	connection := net.Conn(client)
	if buffered != nil {
		connection = &bufferedConn{Conn: client, reader: buffered.Reader}
	}
	tlsConn := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{*leaf}, NextProtos: []string{"http/1.1"}})
	defer tlsConn.Close()
	if err := tlsConn.HandshakeContext(r.Context()); err != nil {
		return
	}
	reader := bufio.NewReader(tlsConn)
	for {
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		request.RemoteAddr = r.RemoteAddr
		if !p.authorizeWrite(r.Context(), request, "https://"+host) {
			writeProxyResponse(tlsConn, http.StatusForbidden, "external write was not authorized")
			return
		}
		p.forwardTLS(tlsConn, request, host, name)
		if request.Close || p.isClosed() {
			return
		}
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func readOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func writeProxyResponse(w io.Writer, status int, message string) {
	body := message + "\n"
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), len(body), body)
}

func (p *egressProxy) forwardTLS(w io.Writer, request *http.Request, host, sni string) {
	target := *request.URL
	target.Scheme = "https"
	target.Host = host
	if target.Path == "" {
		target.Path = "/"
	}
	upstream, err := p.roundTrip(&target, request, sni)
	if err != nil {
		writeProxyResponse(w, http.StatusBadGateway, "upstream unavailable")
		return
	}
	defer upstream.Body.Close()
	header := "HTTP/1.1 " + upstream.Status + "\r\n"
	for key, values := range upstream.Header {
		if strings.EqualFold(key, "Connection") || strings.EqualFold(key, "Transfer-Encoding") || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			header += key + ": " + value + "\r\n"
		}
	}
	header += "Transfer-Encoding: chunked\r\n\r\n"
	if _, err := io.WriteString(w, header); err != nil {
		return
	}
	buffered := bufio.NewWriter(w)
	chunked := httputil.NewChunkedWriter(buffered)
	_, _ = io.Copy(chunked, upstream.Body)
	_ = chunked.Close()
	_, _ = buffered.WriteString("\r\n")
	_ = buffered.Flush()
}

// authorizeWrite holds a mutating external request until the host approves the
// exact method, URL and body digest. Read-only methods pass without a prompt.
func (p *egressProxy) authorizeWrite(ctx context.Context, request *http.Request, base string) bool {
	if readOnlyMethod(request.Method) {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		p.record(request.URL.Host, request.Method)
		return false
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	target := base + request.URL.RequestURI()
	if p.approve == nil {
		p.record(target, request.Method)
		return false
	}
	allowed, _ := p.approve(ctx, request.Method, target, digest)
	if !allowed {
		p.record(target, request.Method)
		return false
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	return true
}

func (p *egressProxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.Host == "" || r.URL.Scheme != "http" {
		http.Error(w, "invalid proxy target", http.StatusBadRequest)
		return
	}
	if !p.authorizeWrite(r.Context(), r, "http://"+r.URL.Host) {
		http.Error(w, "external write was not authorized", http.StatusForbidden)
		return
	}
	if !p.publicTarget(r.Context(), r.URL.Hostname()) {
		p.record(r.URL.Host, r.Method)
		http.Error(w, "private or loopback targets are not authorized", http.StatusForbidden)
		return
	}
	target := *r.URL
	target.Scheme = "http"
	upstream, err := p.roundTrip(&target, r, "")
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upstream.Body.Close()
	for key, values := range upstream.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(upstream.StatusCode)
	_, _ = io.Copy(w, upstream.Body)
}

func (p *egressProxy) roundTrip(target *url.URL, original *http.Request, sni string) (*http.Response, error) {
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: p.dialContext,
		TLSClientConfig: &tls.Config{
			ServerName: sni,
			MinVersion: tls.VersionTLS12,
			RootCAs:    p.upstreamRoots,
		},
		ResponseHeaderTimeout: 20 * time.Second,
	}
	request, err := http.NewRequestWithContext(original.Context(), original.Method, target.String(), original.Body)
	if err != nil {
		return nil, err
	}
	request.Header = original.Header.Clone()
	request.ContentLength = original.ContentLength
	request.Header.Del("Proxy-Authorization")
	request.Header.Del("Proxy-Connection")
	request.Host = target.Host
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	transport.CloseIdleConnections()
	return response, err
}

func (p *egressProxy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := p.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if unsafeEgressIP(address.IP) && !(p.allowTest && address.IP.IsLoopback()) {
			continue
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
	}
	return nil, errors.New("no public address")
}

func (p *egressProxy) publicTarget(ctx context.Context, host string) bool {
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return !unsafeEgressIP(net.ParseIP(host)) || p.allowTest && net.ParseIP(host).IsLoopback()
	}
	addresses, err := p.lookup(ctx, host)
	if err != nil || len(addresses) == 0 {
		return false
	}
	allowed := false
	for _, address := range addresses {
		if unsafeEgressIP(address.IP) {
			return false
		}
		allowed = true
	}
	return allowed
}

func (p *egressProxy) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	resolve := p.resolve
	if resolve == nil {
		resolve = net.DefaultResolver.LookupIPAddr
	}
	if p.allowTest && (host == "127.0.0.1" || host == "localhost") {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	return resolve(ctx, host)
}

func (p *egressProxy) leaf(host string) (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if certificate, ok := p.certs[host]; ok {
		return certificate, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, err
	}
	certificate := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	p.certs[host] = certificate
	return certificate, nil
}

func newProxyCA() (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Cocogem native egress CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certificate, key, pemBytes, nil
}

// unsafeEgressIP rejects every non-public destination, including addresses an
// attacker-controlled DNS name could rebind to.
func unsafeEgressIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

func subtleConstantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for i := 0; i < len(left); i++ {
		diff |= left[i] ^ right[i]
	}
	return diff == 0
}
