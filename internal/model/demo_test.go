package model

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func demoStep(t *testing.T, system string, tools ...string) Response {
	t.Helper()
	r := Request{System: system}
	for _, name := range tools {
		r.Tools = append(r.Tools, Tool{Name: name})
	}
	resp, err := Demo{Delay: 1}.Complete(context.Background(), r, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestDemoRolesReadDifferentSourcesAndReplyDifferently(t *testing.T) {
	urls := map[string]bool{}
	for _, role := range []string{"researcher", "critic", "coordinator"} {
		resp := demoStep(t, "You are x (@ragdoll). Current role="+role+" workflow=research.", "fetch_url")
		var in struct{ URL string }
		if err := json.Unmarshal(resp.Blocks[0].Input, &in); err != nil {
			t.Fatal(err)
		}
		urls[in.URL] = true
	}
	if len(urls) != 3 {
		t.Fatalf("roles share sources: %v", urls)
	}
	texts := map[string]bool{}
	for _, system := range []string{"(@ragdoll) role=researcher", "(@maine) role=critic", "(@ragdoll) role=discussant", "(@maine) role=discussant"} {
		var streamed strings.Builder
		resp, err := Demo{Delay: 1}.Complete(context.Background(), Request{System: system}, func(s string) error { streamed.WriteString(s); return nil })
		if err != nil || resp.Blocks[0].Text != streamed.String() {
			t.Fatal("stream does not match final text", err)
		}
		texts[streamed.String()] = true
	}
	if len(texts) != 4 {
		t.Fatal("members repeat the same reply")
	}
}
