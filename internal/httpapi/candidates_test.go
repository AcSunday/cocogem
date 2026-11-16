package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestCandidateEndpointsApplyDiscardAndProtectExternalEdits is the A04/A29 HTTP
// journey: the bound project stays untouched until an explicit apply, a changed
// project file refuses the overwrite with 409, and discard leaves the project
// byte-identical while reclaiming the candidate directory.
func TestCandidateEndpointsApplyDiscardAndProtectExternalEdits(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.txt"), []byte("baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := &workflow.Engine{
		Store: db, Limits: workflow.DefaultLimits(),
		Executions: execution.New(db, filepath.Join(dir, "executions"), "owner"),
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir,
		Models: []string{"default"}, ModelConfigured: true,
	}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	auth := authCall(server, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, "")
	if auth.Code != 200 {
		t.Fatal(auth.Body.String())
	}
	cookie := auth.Result().Cookies()[0]
	var session SessionReply
	if err := json.Unmarshal(auth.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}

	ws := w.Workspace{ID: "ws", Name: "candidate", Root: project, Revision: 1, CreatedAt: w.Now()}
	thread := w.Thread{ID: "thread", WorkspaceID: ws.ID, Title: "candidate", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return tx.SaveThread(thread)
	}); err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: "run", WorkspaceID: ws.ID, ThreadID: thread.ID, Workspace: ws, WriteBranch: true}
	first, err := engine.Executions.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "main.txt"), []byte("candidate change"), 0600); err != nil {
		t.Fatal(err)
	}
	// The project is untouched before apply.
	if data, err := os.ReadFile(filepath.Join(project, "main.txt")); err != nil || string(data) != "baseline" {
		t.Fatalf("candidate wrote through before apply: %q %v", data, err)
	}

	list := apiCall(server, "GET", "/api/v1/threads/thread/candidates", nil, cookie, session.CSRFToken, nil)
	if list.Code != 200 {
		t.Fatalf("list candidates = %d %s", list.Code, list.Body.String())
	}
	var listed CandidateList
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Execution.ID != first.ID {
		t.Fatalf("candidate list = %+v", listed)
	}
	// A changed candidate without test evidence is never presented as verified.
	if listed.Items[0].Verification.State != execution.CandidateUnverifiedMutation {
		t.Fatalf("verification = %+v", listed.Items[0].Verification)
	}

	applied := apiCall(server, "POST", "/api/v1/threads/thread/candidates/"+first.ID+"/apply", map[string]any{}, cookie, session.CSRFToken, nil)
	if applied.Code != 200 {
		t.Fatalf("apply candidate = %d %s", applied.Code, applied.Body.String())
	}
	var settled w.WorkspaceExecution
	if err := json.Unmarshal(applied.Body.Bytes(), &settled); err != nil {
		t.Fatal(err)
	}
	if settled.Status != "applied" {
		t.Fatalf("applied status = %q", settled.Status)
	}
	if data, err := os.ReadFile(filepath.Join(project, "main.txt")); err != nil || string(data) != "candidate change" {
		t.Fatalf("apply did not write back: %q %v", data, err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatalf("applied candidate directory was not reclaimed: %v", err)
	}

	// A new candidate derives from the applied state; an external project edit
	// must refuse the second apply instead of overwriting the user's bytes.
	second, err := engine.Executions.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("applied candidate was reused")
	}
	if err := os.WriteFile(filepath.Join(second.Path, "main.txt"), []byte("second change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.txt"), []byte("external edit"), 0600); err != nil {
		t.Fatal(err)
	}
	conflict := apiCall(server, "POST", "/api/v1/threads/thread/candidates/"+second.ID+"/apply", map[string]any{}, cookie, session.CSRFToken, nil)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting apply = %d %s", conflict.Code, conflict.Body.String())
	}
	if data, err := os.ReadFile(filepath.Join(project, "main.txt")); err != nil || string(data) != "external edit" {
		t.Fatalf("conflicting apply changed the project: %q %v", data, err)
	}
	// Discard reclaims the candidate and still leaves the project untouched.
	discarded := apiCall(server, "POST", "/api/v1/threads/thread/candidates/"+second.ID+"/discard", map[string]any{}, cookie, session.CSRFToken, nil)
	if discarded.Code != 200 {
		t.Fatalf("discard candidate = %d %s", discarded.Code, discarded.Body.String())
	}
	if data, err := os.ReadFile(filepath.Join(project, "main.txt")); err != nil || string(data) != "external edit" {
		t.Fatalf("discard changed the project: %q %v", data, err)
	}
	if _, err := os.Stat(second.Path); !os.IsNotExist(err) {
		t.Fatalf("discarded candidate directory was not reclaimed: %v", err)
	}
}
