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
