package nativecli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenCodeMCPReadiness(t *testing.T) {
	for _, test := range []struct {
		name, body string
		kind       FailureKind
	}{
		{"connected", `{"data":[{"name":"cocogem","status":{"status":"connected"}}]}`, ""},
		{"foreign server", `{"data":[{"name":"other","status":{"status":"connected"}}]}`, FailureProtocol},
		{"failed connection", `{"data":[{"name":"cocogem","status":{"status":"failed"}}]}`, FailureToolUnavailable},
		{"malformed", `invalid`, FailureProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "opencode" || password != "synthetic" || r.URL.Query().Get("location[directory]") != "/public fixture" {
					t.Error("private server discovery lost its scope or authentication")
				}
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			err := waitOpenCodeMCP(t.Context(), server.Client(), server.URL, "/public fixture", "synthetic")
			if FailureKindOf(err) != test.kind || test.kind == "" && err != nil {
				t.Fatalf("err=%v want=%v", err, test.kind)
			}
		})
	}
}

func TestOpenCodeMCPReadinessCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"data":[]}`) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := waitOpenCodeMCP(ctx, server.Client(), server.URL, "/public", "synthetic")
	if FailureKindOf(err) != FailureToolUnavailable {
		t.Fatal(err)
	}
}
