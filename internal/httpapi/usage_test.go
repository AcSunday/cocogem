package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestUsageEndpointAuthValidationAndReport(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(w.Workspace{ID: "ws-u", Name: "UsageProj", Revision: 1, CreatedAt: w.Now()}); err != nil {
			return err
		}
		if err := tx.SaveAgent(w.Agent{ID: "ag-u", DisplayName: "Coder", Handle: "coder", Executor: "claude", Model: "claude-sonnet-4-6", Revision: 1}); err != nil {
			return err
		}
		if err := tx.SaveThread(w.Thread{ID: "th-u", WorkspaceID: "ws-u", Title: "Token audit", CoordinatorID: "ag-u", AgentIDs: []string{"ag-u"}, Revision: 1, UpdatedAt: w.Now()}); err != nil {
			return err
		}
		if err := tx.InsertRun(w.Run{ID: "run-u", WorkspaceID: "ws-u", ThreadID: "th-u", Status: "succeeded", CreatedAt: w.Now()}, "k-u", "h-u"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: "step-u", RunID: "run-u", AgentID: "ag-u", AgentName: "Coder", Status: "succeeded"}); err != nil {
			return err
		}
		return tx.SaveAttempt(w.Attempt{
			ID: "att-u", RunID: "run-u", StepID: "step-u", Number: 1, Status: "succeeded",
			Executor: "claude", Model: "claude-sonnet-4-6", ResolvedModel: "claude-sonnet-4-6",
			UsageKnown: true, UsageCompleteness: "known",
			InputTokens: 120, OutputTokens: 80, CacheReadTokens: 1000, CacheCreationTokens: 200,
			ReportedCostUSD: 0.005, CostKnown: true,
			StartedAt: w.Now(), FinishedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}

	engine := &workflow.Engine{Store: db, AllowedModels: []string{"default"}, Model: testutil.Model{}, Web: testutil.Web{}}
	srv, err := New(Config{Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, ModelConfigured: true, Model: testutil.Model{}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	// Unauthenticated request is rejected.
	unauth := httptest.NewRecorder()
	handler.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "http://local.test/api/v1/usage", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauth.Code)
	}

	// Setup local password and obtain session cookie.
	setupBody, _ := json.Marshal(PasswordInput{Password: "test-password-1234"})
	setupRec := httptest.NewRecorder()
	setupReq := httptest.NewRequest(http.MethodPost, "http://local.test/auth/setup", bytes.NewReader(setupBody))
	setupReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(setupRec, setupReq)
	if setupRec.Code != http.StatusOK {
		t.Fatalf("setup status = %d: %s", setupRec.Code, setupRec.Body.String())
	}
	cookies := setupRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("missing session cookie after setup")
	}
	cookie := cookies[0]

	// Invalid range returns 400.
	badRange := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodGet, "http://local.test/api/v1/usage?range=year", nil)
	badReq.AddCookie(cookie)
	handler.ServeHTTP(badRange, badReq)
	if badRange.Code != http.StatusBadRequest {
		t.Fatalf("invalid range status = %d, want 400", badRange.Code)
	}

	// Invalid tz_offset returns 400.
	badTZ := httptest.NewRecorder()
	badTZReq := httptest.NewRequest(http.MethodGet, "http://local.test/api/v1/usage?tz_offset=9999", nil)
	badTZReq.AddCookie(cookie)
	handler.ServeHTTP(badTZ, badTZReq)
	if badTZ.Code != http.StatusBadRequest {
		t.Fatalf("invalid tz_offset status = %d, want 400", badTZ.Code)
	}

	// Valid query returns full usage report.
	okRec := httptest.NewRecorder()
	okReq := httptest.NewRequest(http.MethodGet, "http://local.test/api/v1/usage?range=7d&tz_offset=480", nil)
	okReq.AddCookie(cookie)
	handler.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("usage status = %d: %s", okRec.Code, okRec.Body.String())
	}
	var report project.UsageReport
	if err := json.Unmarshal(okRec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Totals.TotalTokens != 1400 || report.Totals.CacheReadTokens != 1000 || report.Totals.AttemptsKnown != 1 || report.Totals.AttemptsWithCost != 1 {
		t.Fatalf("unexpected usage report: %+v", report.Totals)
	}
}
