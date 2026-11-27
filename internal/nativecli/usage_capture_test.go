package nativecli

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func almostEqualCost(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestClaudeCapturesCacheTokensAndReportedCost(t *testing.T) {
	var captured map[string]any
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		if kind == "usage" {
			captured = data
		}
		return nil
	})
	frames := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":["mcp__cocogem__read_file"]}`,
		`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.0145,"usage":{"input_tokens":200,"output_tokens":75,"cache_read_input_tokens":8000,"cache_creation_input_tokens":450}}`,
	}, "\n") + "\n"
	if err := parseClaude(strings.NewReader(frames), gate, &liveness{}); err != nil {
		t.Fatalf("parseClaude failed: %v", err)
	}
	if err := gate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	if captured["known"] != true || captured["input_tokens"] != 200 || captured["output_tokens"] != 75 ||
		captured["cache_read_tokens"] != 8000 || captured["cache_creation_tokens"] != 450 {
		t.Fatalf("unexpected Claude usage: %+v", captured)
	}
	if captured["cost_known"] != true || !almostEqualCost(captured["reported_cost_usd"].(float64), 0.0145) {
		t.Fatalf("unexpected Claude reported cost: %+v", captured)
	}
}

func TestCodexExecNormalizesCachedInputTokensWithoutDoubleCounting(t *testing.T) {
	var captured map[string]any
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		if kind == "usage" {
			captured = data
		}
		return nil
	})
	frames := strings.Join([]string{
		`{"type":"thread.started","thread_id":"` + fakeSession + `"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"msg_1","type":"agent_message","text":"ok"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":120}}`,
	}, "\n") + "\n"
	if err := parseCodex(strings.NewReader(frames), gate, &liveness{}); err != nil {
		t.Fatalf("parseCodex failed: %v", err)
	}
	if err := gate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	if captured["known"] != true || captured["input_tokens"] != 200 || captured["cache_read_tokens"] != 800 || captured["output_tokens"] != 120 {
		t.Fatalf("expected disjoint uncached input=200 + cache_read=800 + output=120, got %+v", captured)
	}
	if _, hasCost := captured["reported_cost_usd"]; hasCost {
		t.Fatalf("Codex did not report cost, should omit reported_cost_usd: %+v", captured)
	}
}

func TestCodexAppServerCapturesTokenUsageAcrossTurnsAndDeduplicates(t *testing.T) {
	var captured map[string]any
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		if kind == "usage" {
			captured = data
		}
		return nil
	})
	srv := &codexAppServer{gate: gate}
	// First model step in a resumed thread where prior thread total was 5000.
	first := json.RawMessage(`{"threadId":"t1","turnId":"turn1","tokenUsage":{"total":{"inputTokens":6000,"cachedInputTokens":4800,"outputTokens":250},"last":{"inputTokens":1000,"cachedInputTokens":800,"outputTokens":50}}}`)
	if err := srv.handleTokenUsage(first); err != nil {
		t.Fatal(err)
	}
	// Duplicate notification with identical total must be ignored.
	if err := srv.handleTokenUsage(first); err != nil {
		t.Fatal(err)
	}
	// Second model step in the same turn.
	second := json.RawMessage(`{"threadId":"t1","turnId":"turn1","tokenUsage":{"total":{"inputTokens":6500,"cachedInputTokens":5200,"outputTokens":320},"last":{"inputTokens":500,"cachedInputTokens":400,"outputTokens":70}}}`)
	if err := srv.handleTokenUsage(second); err != nil {
		t.Fatal(err)
	}
	if err := gate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	// Turn sum of last: raw input = 1500, cached = 1200 -> uncached input = 300, cache_read = 1200, output = 120.
	if captured["known"] != true || captured["input_tokens"] != 300 || captured["cache_read_tokens"] != 1200 || captured["output_tokens"] != 120 {
		t.Fatalf("unexpected Codex app-server usage: %+v", captured)
	}
}

func TestPiAccumulatesCacheTokensAndReportedCostAcrossResponses(t *testing.T) {
	var captured map[string]any
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		if kind == "usage" {
			captured = data
		}
		return nil
	})
	frames := strings.Join([]string{
		`{"type":"session","id":"` + piSession + `"}`,
		`{"type":"message_start","message":{"role":"assistant","usage":{"input":100,"output":20,"cacheRead":500,"cacheWrite":50,"cost":{"total":0.0025}}}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse","usage":{"input":100,"output":25,"cacheRead":500,"cacheWrite":50,"cost":{"total":0.003}}}}`,
		`{"type":"message_start","message":{"role":"assistant","usage":{"input":80,"output":10,"cacheRead":700,"cacheWrite":0,"cost":{"total":0.001}}}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"done"}],"usage":{"input":80,"output":30,"cacheRead":700,"cacheWrite":0,"cost":{"total":0.002}}}}`,
		`{"type":"agent_settled","aborted":false}`,
	}, "\n") + "\n"
	if err := parsePi(strings.NewReader(frames), gate, &liveness{}); err != nil {
		t.Fatalf("parsePi failed: %v", err)
	}
	if err := gate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	if captured["known"] != true || captured["input_tokens"] != 180 || captured["output_tokens"] != 55 ||
		captured["cache_read_tokens"] != 1200 || captured["cache_creation_tokens"] != 50 {
		t.Fatalf("unexpected Pi usage: %+v", captured)
	}
	if captured["cost_known"] != true || !almostEqualCost(captured["reported_cost_usd"].(float64), 0.005) {
		t.Fatalf("unexpected Pi cost: %+v", captured)
	}
}

func TestOpenCodeAccumulatesCacheTokensAndCost(t *testing.T) {
	var captured map[string]any
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		if kind == "usage" {
			captured = data
		}
		return nil
	})
	stream := openCodeStart() +
		openCodeSSE("session.step.ended", map[string]any{
			"finish": "tool-calls",
			"cost":   0.004,
			"tokens": map[string]any{
				"input":  150,
				"output": 40,
				"cache":  map[string]any{"read": 1000, "write": 200},
			},
		}) +
		openCodeSSE("session.step.started", map[string]any{"assistantMessageID": "msg_oc_2"}) +
		openCodeSSE("session.text.started", map[string]any{"assistantMessageID": "msg_oc_2", "ordinal": 0}) +
		openCodeSSE("session.text.delta", map[string]any{"assistantMessageID": "msg_oc_2", "ordinal": 0, "delta": "done"}) +
		openCodeSSE("session.text.ended", map[string]any{"assistantMessageID": "msg_oc_2", "ordinal": 0, "text": "done"}) +
		openCodeSSE("session.step.ended", map[string]any{
			"assistantMessageID": "msg_oc_2",
			"finish":             "stop",
			"cost":               0.0025,
			"tokens": map[string]any{
				"input":  50,
				"output": 30,
				"cache":  map[string]any{"read": 1200, "write": 0},
			},
		}) +
		openCodeSSE("session.execution.succeeded", nil)
	if err := parseOpenCodeEvents(t.Context(), newOpenCodeEvents(strings.NewReader(stream)), openCodeSession, gate, &liveness{}, nil, "", ""); err != nil {
		t.Fatalf("parseOpenCodeEvents failed: %v", err)
	}
	if err := gate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	if captured["known"] != true || captured["input_tokens"] != 200 || captured["output_tokens"] != 70 ||
		captured["cache_read_tokens"] != 2200 || captured["cache_creation_tokens"] != 200 {
		t.Fatalf("unexpected OpenCode usage: %+v", captured)
	}
	if captured["cost_known"] != true || !almostEqualCost(captured["reported_cost_usd"].(float64), 0.0065) {
		t.Fatalf("unexpected OpenCode cost: %+v", captured)
	}
}
