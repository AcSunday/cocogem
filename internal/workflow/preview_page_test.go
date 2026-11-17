package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestPreviewPageCapturesAnImageSourceWithoutTouchingTheTree is the A14
// regression: a rendered page becomes an immutable image source, and the
// candidate tree digest is unchanged because the host writes nothing there.
func TestPreviewPageCapturesAnImageSourceWithoutTouchingTheTree(t *testing.T) {
	if _, err := os.Stat("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"); err != nil {
		if _, err := os.Stat("/usr/bin/google-chrome"); err != nil {
			t.Skip("no Chromium-family browser is installed")
		}
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	site := filepath.Join(root, "site")
	if err := os.MkdirAll(site, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("<!doctype html><title>Workflow Probe</title><body><h1>OK</h1></body>"), 0600); err != nil {
		t.Fatal(err)
	}
	ws := w.Workspace{ID: "ws", Root: root, Revision: 1, CreatedAt: w.Now()}
	// A host-tool step needs no CLI; the executor stays empty so the fixture
	// exercises only the preview tool boundary.
	member := w.Agent{ID: "member", WorkspaceID: ws.ID, DisplayName: "复核", Handle: "review", Model: "default", Revision: 1}
	th := w.Thread{ID: "th", WorkspaceID: ws.ID, CoordinatorID: member.ID, AgentIDs: []string{member.ID}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveMember(w.WorkspaceMember{WorkspaceID: ws.ID, AgentID: member.ID, Enabled: true, Revision: 1}); err != nil {
			return err
		}
		if err := tx.SaveGrant(w.Grant{ID: "grant-ws-member", WorkspaceID: ws.ID, SubjectMemberID: member.ID, Mode: w.ExecutionWorkspaceAutonomous, SourceMessageID: "fixture", Status: "active", Epoch: 1, Revision: 1, CreatedAt: w.Now()}); err != nil {
			return err
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		// A captured source needs the run and attempt rows, exactly like
		// production: the source capture also records attempt exposure.
		if err := tx.InsertRun(w.Run{ID: "run", WorkspaceID: ws.ID, ThreadID: th.ID, Status: "running", CreatedAt: w.Now()}, "key", "hash"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: "step", RunID: "run", AgentID: member.ID, Status: "running"}); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{ID: "attempt", RunID: "run", StepID: "step", Number: 1, Status: "running", StartedAt: w.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	before, err := workspaceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: db, AllowedRoots: []string{root}, AllowedModels: []string{"default"}}
	runtime := &stepRuntime{
		ctx: t.Context(), engine: &engine, allowed: []string{"preview_page"},
		run: w.Run{
			ID: "run", WorkspaceID: ws.ID, ThreadID: th.ID, Workspace: ws,
			Agents: []w.Agent{member}, GrantSnapshot: "snapshot", GrantActions: []string{"read", "preview_page"},
			RequestedExecutionMode: w.ExecutionWorkspaceAutonomous, EffectiveExecutionMode: w.ExecutionWorkspaceAutonomous,
			PermissionEpoch: 1,
		},
		step: w.Step{ID: "step", RunID: "run", AgentID: member.ID, Role: "reviewer", Status: "running"}, attempt: w.Attempt{ID: "attempt", Status: "running"},
		nativeCaptured: map[string]bool{},
	}
	var captured w.Source
	raw, _ := json.Marshal(map[string]any{"mode": "static", "entry": "site/index.html", "timeout_seconds": 30})
	output, err := runtime.executeTool(agent.WithToolCall(t.Context(), "preview-call"), "preview_page", raw)
	if err != nil {
		t.Fatalf("preview_page failed: %v", err)
	}
	if !strings.Contains(output, `"rendered":true`) {
		t.Fatalf("preview observation = %s", output)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		sources, err := tx.Sources(th.ID)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if source.Kind == "image" {
				captured = source
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read captured sources: %v", err)
	}
	if captured.Kind != "image" || captured.SHA256 == "" || captured.OriginalContent == "" {
		t.Fatalf("captured source = %+v", captured)
	}
	after, err := workspaceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("preview_page changed the project tree: %s -> %s", before, after)
	}
}

// workspaceTreeDigest is the same stable content digest the host uses for
// candidate evidence, kept local so this test never calls an unexported path.
func workspaceTreeDigest(root string) (string, error) {
	return tools.WorkspaceDigest(root)
}
