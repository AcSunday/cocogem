package nativecli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

// This opt-in test calls the user's existing default provider with public
// synthetic input, without reading workspace files or creating artifacts.
func TestLiveOpenCodeDefaultConnection(t *testing.T) {
	if os.Getenv("COCOGEM_LIVE_OPENCODE") != "1" {
		t.Skip("set COCOGEM_LIVE_OPENCODE=1 to call the configured OpenCode provider")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	plan, err := (Provider{}).openCodePlan(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := exec.CommandContext(ctx, "opencode", "api", "model.default", "--standalone").Output()
	if err != nil {
		t.Fatal("native OpenCode could not resolve its configured default")
	}
	var native struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &native) != nil {
		t.Fatal("native OpenCode returned an invalid default model")
	}
	identity := connectionDigest(map[string]any{"executor": "opencode", "model": plan.openCodeModel, "connection": piConfigIdentity(native.Data)})
	if identity != plan.info.Identity {
		t.Fatal("scoped OpenCode connection differs from the native default")
	}
	response, err := (Provider{}).Complete(ctx, model.Request{
		Executor: "opencode", Model: "default",
		System:   "This is a public synthetic connection check. Do not use tools.",
		Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Reply with exactly COCOGEM_OPENCODE_OK."}}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Blocks) != 1 || strings.TrimSpace(response.Blocks[0].Text) != "COCOGEM_OPENCODE_OK" {
		t.Fatal("configured provider did not return the expected synthetic reply")
	}
}
