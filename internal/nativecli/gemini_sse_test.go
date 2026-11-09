package nativecli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type geminiSSETestResult struct {
	Body        string  `json:"body"`
	Error       string  `json:"error"`
	NodeOptions *string `json:"nodeOptions"`
	PrivateEnv  bool    `json:"privateEnv"`
}

func runGeminiSSEFetch(t *testing.T, url, options string) geminiSSETestResult {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required to exercise the Gemini preload")
	}
	dir := filepath.Join(t.TempDir(), "runtime with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	script := `(async () => {
  const result = {nodeOptions: process.env.NODE_OPTIONS ?? null, privateEnv: 'COCOGEM_GEMINI_ORIGINAL_NODE_OPTIONS' in process.env};
  try {
    const response = await fetch(process.argv[1]);
    const parts = [];
    for await (const chunk of response.body) parts.push(Buffer.from(chunk));
    result.body = Buffer.concat(parts).toString('base64');
  } catch (error) { result.error = error.message; }
  process.stdout.write(JSON.stringify(result));
})().catch(() => process.exit(1));`
	cmd := exec.CommandContext(ctx, "node", "-e", script, url)
	cmd.Env = overrideEnv(os.Environ(), map[string]string{
		"NODE_OPTIONS": options, "GEMINI_CLI_NO_RELAUNCH": "1",
	})
	if err := configureGeminiSSE(cmd, dir); err != nil {
		t.Fatal(err)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node stream test: %v: %s", err, output)
	}
	var result geminiSSETestResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode node result: %v: %s", err, output)
	}
	return result
}

func TestGeminiSSEFiltersHeartbeatAndPreservesData(t *testing.T) {
	long := strings.Repeat("x", 70*1024)
	for _, tc := range []struct {
		name, path, contentType, input, want string
	}{
		{"comments and CRLF", "/v1/models/a:streamGenerateContent", "text/event-stream", ": PING\r\n\r\ndata: {\"text\":\"猫\"}\r\n\r\n: keepalive\n\ndata: {\"n\":2}\n\n", "\r\ndata: {\"text\":\"猫\"}\r\n\r\n\ndata: {\"n\":2}\n\n"},
		{"CR line endings", "/v1/models/a:streamGenerateContent", "text/event-stream; charset=utf-8", ": PING\r\rdata: {}\r\r", "\rdata: {}\r\r"},
		{"oversized data line", "/v1/models/a:streamGenerateContent", "text/event-stream", "data: " + long + "\n\n: PING\n\ndata: {}\n\n", "data: " + long + "\n\n\ndata: {}\n\n"},
		{"incomplete data", "/v1/models/a:streamGenerateContent", "text/event-stream", ": PING\n\ndata: incomplete", "\ndata: incomplete"},
		{"unrelated route", "/v1/other", "text/event-stream", ": PING\n\ndata: {}\n\n", ": PING\n\ndata: {}\n\n"},
		{"non-SSE response", "/v1/models/a:streamGenerateContent", "application/json", ": PING\n\ndata: {}\n\n", ": PING\n\ndata: {}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				// Exercise both short and oversized response writes.
				for _, b := range []byte(tc.input) {
					_, _ = w.Write([]byte{b})
				}
			}))
			defer server.Close()
			result := runGeminiSSEFetch(t, server.URL+tc.path, "--no-warnings")
			if result.Error != "" {
				t.Fatalf("stream failed: %s", result.Error)
			}
			body, err := base64.StdEncoding.DecodeString(result.Body)
			if err != nil || !bytes.Equal(body, []byte(tc.want)) {
				t.Fatalf("stream mismatch: err=%v got bytes=%d want bytes=%d", err, len(body), len(tc.want))
			}
			if result.NodeOptions == nil || *result.NodeOptions != "--no-warnings" || result.PrivateEnv {
				t.Fatalf("native subprocess environment was not restored: %+v", result)
			}
		})
	}
}

func TestGeminiSSEHandlesBytewiseChunks(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required to exercise the Gemini preload")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "gemini-sse.cjs")
	if err := os.WriteFile(shim, geminiSSEShim, 0600); err != nil {
		t.Fatal(err)
	}
	script := `globalThis.fetch = async () => new Response(new ReadableStream({
  start(controller) {
    for (const byte of Buffer.from(': PING\r\n\r\ndata: {"text":"猫"}\r\n\r\n')) controller.enqueue(Uint8Array.of(byte));
    controller.close();
  }
}), {headers: {'content-type': 'text/event-stream'}});
require(process.argv[1]);
(async () => {
  const response = await fetch('https://model.invalid/v1/models/a:streamGenerateContent');
  const chunks = [];
  for await (const chunk of response.body) chunks.push(Buffer.from(chunk));
  process.stdout.write(Buffer.concat(chunks));
})().catch(() => process.exit(1));`
	cmd := exec.CommandContext(t.Context(), "node", "-e", script, shim)
	cmd.Env = overrideEnv(os.Environ(), map[string]string{"GEMINI_CLI_NO_RELAUNCH": "1"})
	output, err := cmd.CombinedOutput()
	want := "\r\ndata: {\"text\":\"猫\"}\r\n\r\n"
	if err != nil || string(output) != want {
		t.Fatalf("bytewise stream=%q error=%v, want %q", output, err, want)
	}
}

func TestGeminiSSEClassifiesProviderErrorsWithoutLeakingPayload(t *testing.T) {
	for _, tc := range []struct{ code, marker string }{
		{"server_error", "TRANSIENT"},
		{"429", "QUOTA"},
		{"401", "AUTHENTICATION"},
		{"permission_denied", "PERMISSION"},
		{"unrecognized", "UNKNOWN"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, ": PING\n\ndata: {\"type\":\"error\",\"error\":{\"code\":%q,\"message\":\"private-secret\"}}\n\n", tc.code)
			}))
			defer server.Close()
			result := runGeminiSSEFetch(t, server.URL+"/v1/models/a:streamGenerateContent", "")
			if result.Error != "COCOGEM_GEMINI_SSE_ERROR_"+tc.marker || strings.Contains(result.Error, "private-secret") {
				t.Fatalf("unexpected stream failure: %+v", result)
			}
		})
	}
}

func TestGeminiSSEFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    FailureKind
	}{
		{"[API Error: COCOGEM_GEMINI_SSE_ERROR_TRANSIENT]", FailureProviderTransient},
		{"[API Error: COCOGEM_GEMINI_SSE_ERROR_QUOTA]", FailureQuota},
		{"[API Error: COCOGEM_GEMINI_SSE_ERROR_AUTHENTICATION]", FailureAuthentication},
		{"[API Error: COCOGEM_GEMINI_SSE_ERROR_PERMISSION]", FailurePermission},
		{"[API Error: COCOGEM_GEMINI_SSE_ERROR_UNKNOWN]", FailureUnknown},
		{"[API Error: Incomplete JSON segment at the end]", FailureProtocol},
	} {
		t.Run(tc.message, func(t *testing.T) {
			err := geminiSSEFailure(tc.message + " private-secret")
			if FailureKindOf(err) != tc.want || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("error=%v want kind=%s", err, tc.want)
			}
		})
	}
}
