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

func TestFixtureEngineeringAnalysisStaysReadOnly(t *testing.T) {
	read := fixtureStep(t, "role=analyst workflow=engineering", "read_file")
	if len(read.Blocks) != 1 || read.Blocks[0].Name != "read_file" {
		t.Fatalf("engineering analyst did not read a file: %+v", read.Blocks)
	}
	publish := fixtureStep(t, "role=reviewer workflow=engineering type=code_analysis", "publish_artifact")
	if len(publish.Blocks) != 1 || publish.Blocks[0].Name != "publish_artifact" {
		t.Fatalf("engineering reviewer did not publish: %+v", publish.Blocks)
	}
	var in struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(publish.Blocks[0].Input, &in); err != nil || in.Type != "code_analysis" {
		t.Fatalf("engineering analysis artifact type = %q, %v", in.Type, err)
	}
	final := fixtureStep(t, "role=final_reviewer workflow=code_analysis type=code_analysis", "publish_artifact")
	if len(final.Blocks) != 1 || final.Blocks[0].Name != "publish_artifact" {
		t.Fatalf("final reviewer did not publish the read-only analysis: %+v", final.Blocks)
	}
	if err := json.Unmarshal(final.Blocks[0].Input, &in); err != nil || in.Type != "code_analysis" {
		t.Fatalf("final reviewer artifact type = %q, %v", in.Type, err)
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
