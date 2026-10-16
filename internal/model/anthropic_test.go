package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamingToolsAndTermination(t *testing.T) {
	stream := `data: {"type":"message_start","message":{"usage":{"input_tokens":12}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"read_file","input":{}}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"README.md\"}"}}

data: {"type":"content_block_stop","index":1}

data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}

data: {"type":"message_stop"}

`
	var text strings.Builder
	got, err := parseStream(strings.NewReader(stream), func(s string) error { _, err := text.WriteString(s); return err })
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "你好" || len(got.Blocks) != 2 || string(got.Blocks[1].Input) != `{"path":"README.md"}` || got.InputTokens != 12 {
		t.Fatalf("%+v %s", got, text.String())
	}
	for _, tc := range []struct{ name, input string }{
		{"missing stop", strings.ReplaceAll(stream, `data: {"type":"message_stop"}`, "")},
		{"unclosed tool", strings.ReplaceAll(stream, `data: {"type":"content_block_stop","index":1}`, "")},
		{"truncation", strings.ReplaceAll(stream, `"tool_use"},"usage"`, `"max_tokens"},"usage"`)},
		{"invalid event", "data: broken\n\n"},
		{"missing block", "data: {\"type\":\"content_block_delta\",\"index\":7}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseStream(strings.NewReader(tc.input), func(string) error { return nil }); err == nil {
				t.Fatal("ambiguous stream accepted")
			}
		})
	}
}

func TestAnthropicWireAndHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-only" {
			t.Error("missing auth")
		}
		var input Request
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if !input.Stream {
			t.Error("not streaming")
		}
		rw.WriteHeader(429)
		fmt.Fprint(rw, "secret diagnostics")
	}))
	defer server.Close()
	a := Anthropic{APIKey: "test-only", Endpoint: server.URL, Client: server.Client()}
	_, err := a.Complete(context.Background(), Request{Model: "test"}, func(string) error { return nil })
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
