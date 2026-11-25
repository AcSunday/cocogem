package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

// TestHTTPEgressForwardsOnlyAllowlistedHost proves the production handler is real
// and closed: an approved HTTPS destination on the exact allowlist is forwarded
// once with the exact method/body, and any other host is refused before a request
// is made.
func TestHTTPEgressForwardsOnlyAllowlistedHost(t *testing.T) {
	var hits atomic.Int32
	var method, body string
	server := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		method = r.Method
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		rw.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	egress := NewHTTPEgress([]string{parsed.Host}, server.Client())
	request, err := DecodeExternalRequest([]byte(`{"kind":"deploy","method":"POST","url":"` + server.URL + `/v1/release","body":"payload"}`))
	if err != nil {
		t.Fatal(err)
	}
	status, err := egress.Forward(context.Background(), request)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("allowed forward = %d %v", status, err)
	}
	if hits.Load() != 1 || method != "POST" || body != "payload" {
		t.Fatalf("forwarded %d %s %q", hits.Load(), method, body)
	}
	// A different host is refused with no request.
	refused, _ := DecodeExternalRequest([]byte(`{"kind":"deploy","method":"POST","url":"https://other.example.test/v1/release","body":"x"}`))
	if _, err := egress.Forward(context.Background(), refused); err == nil {
		t.Fatal("a non-allowlisted host was forwarded")
	}
	if hits.Load() != 1 {
		t.Fatalf("refused request still reached the server: %d", hits.Load())
	}
	// Plain HTTP is refused even when the host matches.
	plain := ExternalRequest{Kind: "deploy", Method: "POST", URL: "http://" + parsed.Host + "/v1/release"}
	if _, err := egress.Forward(context.Background(), plain); err == nil {
		t.Fatal("plain HTTP was accepted")
	}
	if hits.Load() != 1 {
		t.Fatalf("plain HTTP reached the server: %d", hits.Load())
	}
	// An empty allowlist is not an implicit public path.
	if _, err := NewHTTPEgress(nil, server.Client()).Forward(context.Background(), request); err == nil {
		t.Fatal("an empty allowlist forwarded a request")
	}
}

// TestHTTPEgressDoesNotFollowRedirects proves an approved request cannot be steered
// past the allowlist or off HTTPS by a redirect issued from an allowed host: the
// redirect response is returned as a failure and the second target sees no request.
func TestHTTPEgressDoesNotFollowRedirects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(http.Handler) *httptest.Server
	}{
		{"other host over https", httptest.NewTLSServer},
		{"downgrade to http", httptest.NewServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var targetHits atomic.Int32
			target := tc.target(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				targetHits.Add(1)
				rw.WriteHeader(http.StatusAccepted)
			}))
			defer target.Close()
			allowed := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				http.Redirect(rw, r, target.URL+"/steal", http.StatusTemporaryRedirect)
			}))
			defer allowed.Close()
			parsed, err := url.Parse(allowed.URL)
			if err != nil {
				t.Fatal(err)
			}
			client := allowed.Client()
			egress := NewHTTPEgress([]string{parsed.Host}, client)
			request := ExternalRequest{Kind: "deploy", Method: "POST", URL: allowed.URL + "/v1/release", Body: "approved-payload"}
			status, err := egress.Forward(context.Background(), request)
			if err == nil || status != http.StatusTemporaryRedirect {
				t.Fatalf("redirect forward = %d %v, want HTTP 307 and an error", status, err)
			}
			if hits := targetHits.Load(); hits != 0 {
				t.Fatalf("redirect target received %d request(s) outside the approved destination", hits)
			}
			if client.CheckRedirect != nil {
				t.Fatal("NewHTTPEgress mutated the caller's client")
			}
		})
	}
}
