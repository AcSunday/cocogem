package nativecli

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// modelGateway is a loopback reverse proxy for the CLI's model API. A sandboxed
// CLI process cannot reach the public model endpoint directly; it connects to
// this private listener instead. The gateway is a host process, so the model
// connection still reuses the user's existing endpoint and credentials while
// the sandbox remains limited to loopback tools.
type modelGateway struct {
	listener net.Listener
	server   *http.Server
	target   *url.URL
}

func newModelGateway(baseURL string) (*modelGateway, error) {
	target, err := url.Parse(baseURL)
	if err != nil || target.Scheme != "http" && target.Scheme != "https" || target.Host == "" || target.User != nil {
		return nil, errors.New("model endpoint is invalid for the loopback gateway")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("open private model gateway")
	}
	gateway := &modelGateway{listener: listener, target: target}
	gateway.server = &http.Server{Handler: gateway, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 0, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 * 1024}
	go func() { _ = gateway.server.Serve(listener) }()
	return gateway, nil
}

func (g *modelGateway) url() string { return "http://" + g.listener.Addr().String() }

func (g *modelGateway) addr() string { return g.listener.Addr().String() }

func (g *modelGateway) close() { _ = g.server.Close() }

func (g *modelGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(g.target.Path, "/") + "/" + strings.TrimPrefix(r.URL.Path, "/")
	if g.target.Path == "" || g.target.Path == "/" {
		path = r.URL.Path
	}
	target := *g.target
	target.Path = path
	target.RawQuery = r.URL.RawQuery
	outbound, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "invalid model gateway request", http.StatusBadRequest)
		return
	}
	outbound.Header = r.Header.Clone()
	outbound.Header.Del("Proxy-Authorization")
	outbound.Header.Del("Proxy-Connection")
	outbound.Host = g.target.Host
	transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 120 * time.Second}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(outbound)
	transport.CloseIdleConnections()
	if err != nil {
		http.Error(w, "model endpoint unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF && r.Context().Err() == nil {
				return
			}
			return
		}
	}
}
