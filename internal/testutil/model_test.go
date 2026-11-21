package testutil

import (
	"context"

	"encoding/json"
	"github.com/AcSunday/cocogem/internal/model"
	"strings"
	"testing"
)

func fixtureStep(t *testing.T, system string, tools ...string) model.Response {
	t.Helper()
	r := model.Request{System: system}
	for _, name := range tools {
		r.Tools = append(r.Tools, model.Tool{Name: name})
	}
	resp, err := Model{Delay: 1}.Complete(context.Background(), r, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestFixtureRolesReadDifferentSourcesAndReplyDifferently(t *testing.T) {
	urls := map[string]bool{}
	for _, role := range []string{"researcher", "critic", "coordinator"} {
		resp := fixtureStep(t, "You are x (@ragdoll). Current role="+role+" workflow=research.", "fetch_url")
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
		resp, err := Model{Delay: 1}.Complete(context.Background(), model.Request{System: system}, func(s string) error { streamed.WriteString(s); return nil })
		if err != nil || resp.Blocks[0].Text != streamed.String() {
			t.Fatal("stream does not match final text", err)
		}
		texts[streamed.String()] = true
	}
	if len(texts) != 4 {
		t.Fatal("members repeat the same reply")
	}
}

func TestFixtureUsesGrantedCapabilitiesAndPublishesANote(t *testing.T) {
	read := fixtureStep(t, "You are x. Current role=responder (write step). This task may use the member's granted tools in the bound project.", "read_file")
	if len(read.Blocks) != 1 || read.Blocks[0].Name != "read_file" {
		t.Fatalf("a writable project step did not read a file: %+v", read.Blocks)
	}
	publish := fixtureStep(t, "You are x. Current role=responder (write step).", "publish_artifact")
	if len(publish.Blocks) != 1 || publish.Blocks[0].Name != "publish_artifact" {
		t.Fatalf("a publishing step did not publish: %+v", publish.Blocks)
	}
	var in struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(publish.Blocks[0].Input, &in); err != nil || in.Type != "note" {
		t.Fatalf("fixture artifact type = %q, %v", in.Type, err)
	}
	// A read-only step never publishes.
	readOnly := fixtureStep(t, "You are x. Current role=critic (read-only step).", "publish_artifact")
	if len(readOnly.Blocks) == 1 && readOnly.Blocks[0].Name == "publish_artifact" {
		t.Fatal("a read-only step published an artifact")
	}
}

func TestFixtureChecksReturnStructuredCorrections(t *testing.T) {
	for _, handle := range []string{"ragdoll", "maine", "orange"} {
		resp := fixtureStep(t, "(@"+handle+") role=checker workflow=discussion")
		var check struct {
			Status     string `json:"status"`
			Correction string `json:"correction"`
		}
		if err := json.Unmarshal([]byte(resp.Blocks[0].Text), &check); err != nil {
			t.Fatal(err)
		}
		if check.Status != "confirmed" && check.Status != "disputed" || check.Status == "disputed" && check.Correction == "" {
			t.Fatalf("invalid contributor check: %+v", check)
		}
	}
}
