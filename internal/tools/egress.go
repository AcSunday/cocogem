package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
)

// ExternalRequest is one mutating external operation (publish, deploy, release)
// the host has NOT forwarded yet. The host holds it until the user approves the
// exact method, target and body digest.
type ExternalRequest struct {
	// Kind separates a publish from a deploy; it is shown to the user and never
	// inferred from a command name.
	Kind   string `json:"kind"`
	Method string `json:"method"`
	URL    string `json:"url"`
	Body   string `json:"body,omitempty"`
	// Digest binds the approval to the exact bytes and target.
	Digest string `json:"digest"`
}

// Digest returns the stable identity of this operation.
func (r ExternalRequest) ComputeDigest() string {
	sum := sha256.Sum256([]byte(r.Kind + "\x00" + r.Method + "\x00" + r.URL + "\x00" + r.Body))
	return hex.EncodeToString(sum[:])
}

// Summary is the bounded, secret-free description shown in the approval card.
func (r ExternalRequest) Summary() string {
	target := r.URL
	if len(target) > 300 {
		target = target[:300]
	}
	body := strings.TrimSpace(r.Body)
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	if body == "" {
		return r.Kind + " " + r.Method + " " + target
	}
	return r.Kind + " " + r.Method + " " + target + " (" + body + ")"
}

// Egress forwards exactly one approved external request. A nil Egress means this
// host has no external-write capability: the tool fails closed instead of
// pretending the operation ran.
type Egress interface {
	Forward(context.Context, ExternalRequest) (int, error)
}

// ErrEgressUnavailable is returned when no outbound integration is configured.
var ErrEgressUnavailable = errors.New("egress_unavailable: this host has no configured external publish/deploy integration")

// ExternalPublishDefinition is offered only when an egress integration exists.
// Public web reads and dependency retrieval stay on their own tools and never
// pass through this approval path.
func ExternalPublishDefinition() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":   map[string]any{"type": "string", "enum": []string{"publish", "deploy", "release", "install"}},
			"method": map[string]any{"type": "string", "enum": []string{"POST", "PUT", "PATCH", "DELETE"}},
			"url":    map[string]any{"type": "string", "description": "Exact destination URL"},
			"body":   map[string]any{"type": "string", "description": "Exact request body"},
		},
		"required":             []string{"kind", "method", "url"},
		"additionalProperties": false,
	}
}

// DecodeExternalRequest parses and normalizes one external operation.
func DecodeExternalRequest(raw json.RawMessage) (ExternalRequest, error) {
	var in struct {
		Kind   string `json:"kind"`
		Method string `json:"method"`
		URL    string `json:"url"`
		Body   string `json:"body"`
	}
	if err := Decode(raw, &in); err != nil {
		return ExternalRequest{}, err
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	switch kind {
	case "publish", "deploy", "release", "install":
	default:
		return ExternalRequest{}, errors.New("kind must be publish, deploy, release or install")
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
	default:
		return ExternalRequest{}, errors.New("external writes must use POST, PUT, PATCH or DELETE")
	}
	url := strings.TrimSpace(in.URL)
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return ExternalRequest{}, errors.New("external operation requires an absolute http(s) URL")
	}
	if len(in.Body) > 1<<20 {
		return ExternalRequest{}, errors.New("external operation body exceeds 1 MiB")
	}
	request := ExternalRequest{Kind: kind, Method: method, URL: url, Body: in.Body}
	request.Digest = request.ComputeDigest()
	return request, nil
}

// Forward performs one already-approved external operation.
func (e EgressForwarder) Forward(ctx context.Context, request ExternalRequest) (int, error) {
	if e.Egress == nil {
		return 0, ErrEgressUnavailable
	}
	return e.Egress.Forward(ctx, request)
}

// EgressForwarder carries the configured outbound integration.
type EgressForwarder struct{ Egress Egress }

// HTTPEgress forwards approved external operations to an explicit host allowlist.
// It is the real production handler: only an exactly registered host may receive
// a request, only HTTPS is accepted, and no other configuration is inferred.
type HTTPEgress struct {
	Client *http.Client
	// Hosts is the exact set of allowed destination host:port values.
	Hosts []string
}

// NewHTTPEgress builds the allowlisted egress handler. An empty allowlist means
// it refuses every destination rather than silently allowing the public web.
func NewHTTPEgress(hosts []string, client *http.Client) *HTTPEgress {
	cleaned := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			cleaned = append(cleaned, host)
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPEgress{Client: client, Hosts: cleaned}
}

// Forward performs exactly the approved request against an allowed host.
func (e *HTTPEgress) Forward(ctx context.Context, request ExternalRequest) (int, error) {
	if e == nil || len(e.Hosts) == 0 {
		return 0, ErrEgressUnavailable
	}
	url, err := neturl.Parse(request.URL)
	if err != nil || url.Scheme != "https" || url.Host == "" {
		return 0, errors.New("external operation requires an allowed HTTPS destination")
	}
	target := strings.ToLower(url.Host)
	if !slices.Contains(e.Hosts, target) {
		return 0, errors.New("external operation destination is not configured")
	}
	body := strings.NewReader(request.Body)
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, request.URL, body)
	if err != nil {
		return 0, errors.New("could not build the approved external request")
	}
	if request.Body != "" {
		httpRequest.Header.Set("Content-Type", "application/octet-stream")
	}
	response, err := e.Client.Do(httpRequest)
	if err != nil {
		return 0, errors.New("external operation could not be delivered")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return response.StatusCode, fmt.Errorf("external operation returned HTTP %d", response.StatusCode)
	}
	return response.StatusCode, nil
}
