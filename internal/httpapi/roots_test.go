package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestWorkspaceDirectorySettings(t *testing.T) {
	t.Parallel()
	for _, restricted := range []bool{false, true} {
		name := "page-selected"
		if restricted {
			name = "host-limited"
		}
		t.Run(name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(base, "index.html"), []byte("directory settings test"), 0600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(base, "资料 项目")
			nested := filepath.Join(root, "notes")
			outside := filepath.Join(base, "outside")
			for _, path := range []string{nested, outside} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			db, err := store.Open(filepath.Join(base, "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := Config{Host: "local.test", AuthFile: filepath.Join(base, "auth.json"), WebDir: base, Models: []string{"default"}}
			if restricted {
				cfg.Roots = []string{root}
			}
			s, err := New(cfg, db, nil)
			if err != nil {
				t.Fatal(err)
			}
			setup := authCall(s, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, "")
			if setup.Code != 200 {
				t.Fatal(setup.Body.String())
			}
			cookie := setup.Result().Cookies()[0]
			var session SessionReply
			if err := json.Unmarshal(setup.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			call := func(method, path string, input any, revision int) *httptest.ResponseRecorder {
				t.Helper()
				b, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(b))
				req.AddCookie(cookie)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-CSRF-Token", session.CSRFToken)
				if revision > 0 {
					req.Header.Set("If-Match", strconv.Itoa(revision))
				}
				rw := httptest.NewRecorder()
				s.Handler().ServeHTTP(rw, req)
				return rw
			}
			response := call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "目录绑定", Root: root + "/notes/.."}, 0)
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			var ws w.Workspace
			if err := json.Unmarshal(response.Body.Bytes(), &ws); err != nil {
				t.Fatal(err)
			}
			if ws.Root != root {
				t.Fatalf("root not canonicalized: %q", ws.Root)
			}
			validateResponse(t, "/api/v1/workspaces", "post", 200, response.Body.Bytes())
			response = call("PATCH", "/api/v1/workspaces/"+ws.ID, WorkspaceInput{Name: ws.Name, Root: nested}, ws.Revision)
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			if err := json.Unmarshal(response.Body.Bytes(), &ws); err != nil {
				t.Fatal(err)
			}
			if ws.Root != nested {
				t.Fatalf("subdirectory missing: %+v", ws)
			}
			response = call("PATCH", "/api/v1/workspaces/"+ws.ID, WorkspaceInput{Name: ws.Name, Root: filepath.Join(base, "missing")}, ws.Revision)
			if response.Code != 400 {
				t.Fatal("invalid path accepted", response.Body.String())
			}
			if restricted {
				response = call("PATCH", "/api/v1/workspaces/"+ws.ID, WorkspaceInput{Name: ws.Name, Root: outside}, ws.Revision)
				if response.Code != 400 {
					t.Fatal("host limit bypassed", response.Body.String())
				}
			}
			if err := db.Read(t.Context(), func(tx *store.Tx) error {
				persisted, err := tx.Workspace(ws.ID)
				if err == nil && (persisted.Root != nested || persisted.Revision != ws.Revision) {
					t.Fatalf("failed save changed binding %+v", persisted)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "queued", Revision: 1}
				if err := tx.SaveThread(th); err != nil {
					return err
				}
				return tx.InsertRun(w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "queued", CreatedAt: w.Now()}, w.NewID(), "hash")
			}); err != nil {
				t.Fatal(err)
			}
			response = call("PATCH", "/api/v1/workspaces/"+ws.ID, WorkspaceInput{Name: ws.Name, Root: ""}, ws.Revision)
			if response.Code != http.StatusConflict {
				t.Fatal("active run binding changed", response.Body.String())
			}
			response = call("GET", "/api/v1/workspaces", nil, 0)
			var listed struct{ Items []w.Workspace }
			if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Items) != 1 || listed.Items[0].Root != nested {
				t.Fatalf("directory not persisted: %+v", listed.Items)
			}
		})
	}
}
