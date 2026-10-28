package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

const testPassword = "a local test passphrase"

func authServer(t *testing.T, file string) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Host: "local.test", WebDir: dir, AuthFile: file}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func authCall(server *Server, method, path string, value any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(value)
	request := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestPasswordLifecycleAndRestart(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "auth.json")
	server := authServer(t, file)
	status := authCall(server, "GET", "/auth/status", nil, nil, "")
	if !strings.Contains(status.Body.String(), `"initialized":false`) {
		t.Fatal(status.Body.String())
	}
	validateResponse(t, "/auth/status", "get", 200, status.Body.Bytes())
	for _, path := range []string{"/api/v1/system", "/api/v1/workspaces", "/api/v1/runs/private/events", "/api/v1/artifacts/private/versions/1/export"} {
		if got := authCall(server, "GET", path, nil, nil, ""); got.Code != 401 {
			t.Fatalf("%s is public: %d", path, got.Code)
		}
	}
	if got := authCall(server, "POST", "/auth/login", PasswordInput{Password: testPassword}, nil, ""); got.Code != 409 {
		t.Fatal("login before initialization", got.Body.String())
	}
	response := authCall(server, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	validateResponse(t, "/auth/setup", "post", 200, response.Body.Bytes())
	cookie := response.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 86400 {
		t.Fatal("unsafe session cookie", cookie)
	}
	var session SessionReply
	json.Unmarshal(response.Body.Bytes(), &session)
	before, err := os.ReadFile(file)
	if err != nil || bytes.Contains(before, []byte(testPassword)) || bytes.Contains(response.Body.Bytes(), []byte("hash")) {
		t.Fatal("password persistence or disclosure", err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("password file permissions", err)
	}
	if got := authCall(server, "POST", "/auth/setup", PasswordInput{Password: "different passphrase"}, nil, ""); got.Code != 409 {
		t.Fatal("password was overwritten", got.Body.String())
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("setup rewrote the password")
	}
	if got := authCall(server, "POST", "/api/v1/auth/logout", map[string]any{}, cookie, "bad"); got.Code != 403 {
		t.Fatal("logout bypassed CSRF", got.Code)
	}
	if got := authCall(server, "POST", "/api/v1/auth/logout", map[string]any{}, cookie, session.CSRFToken); got.Code != 200 || got.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout failed", got.Body.String())
	}
	if got := authCall(server, "GET", "/api/v1/system", nil, cookie, ""); got.Code != 401 {
		t.Fatal("logged out session still works")
	}
	server = authServer(t, file)
	if got := authCall(server, "GET", "/auth/status", nil, nil, ""); !strings.Contains(got.Body.String(), `"initialized":true`) {
		t.Fatal("restart forgot password")
	}
	if got := authCall(server, "POST", "/auth/login", PasswordInput{Password: "incorrect passphrase"}, nil, ""); got.Code != 401 {
		t.Fatal("wrong password accepted")
	}
	response = authCall(server, "POST", "/auth/login", PasswordInput{Password: testPassword}, nil, "")
	if response.Code != 200 || response.Result().Cookies()[0].Value == cookie.Value {
		t.Fatal("restart login failed or session was not rotated", response.Body.String())
	}
	validateResponse(t, "/auth/login", "post", 200, response.Body.Bytes())
	cookie = response.Result().Cookies()[0]
	server.auth.mu.Lock()
	entry := server.auth.sessions[cookie.Value]
	entry.expires = time.Now().Add(-time.Second)
	server.auth.sessions[cookie.Value] = entry
	server.auth.mu.Unlock()
	if got := authCall(server, "GET", "/api/v1/system", nil, cookie, ""); got.Code != 401 {
		t.Fatal("expired session accepted")
	}
}

func TestPasswordValidationAndRequestBoundaries(t *testing.T) {
	t.Parallel()
	server := authServer(t, filepath.Join(t.TempDir(), "auth.json"))
	for _, password := range []string{"", "short", strings.Repeat("a", 129)} {
		if got := authCall(server, "POST", "/auth/setup", PasswordInput{Password: password}, nil, ""); got.Code != 400 {
			t.Fatal("invalid password accepted", got.Code)
		}
	}
	for _, header := range []string{"Origin", "Host", "Content-Type"} {
		request := httptest.NewRequest("POST", "http://local.test/auth/setup", strings.NewReader(`{"password":"a safe test passphrase"}`))
		request.Header.Set("Content-Type", "application/json")
		if header == "Host" {
			request.Host = "evil.example"
		} else {
			request.Header.Set(header, "https://evil.example")
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != 403 && response.Code != 415 {
			t.Fatalf("%s bypass: %d", header, response.Code)
		}
	}
	if got := authCall(server, "POST", "/auth/setup", map[string]string{"password": testPassword, "admin": "true"}, nil, ""); got.Code != 400 {
		t.Fatal("unknown field accepted")
	}
	if got := authCall(server, "POST", "/bootstrap", map[string]string{"token": "legacy"}, nil, ""); got.Code == 200 {
		t.Fatal("old bootstrap endpoint accepted")
	}
}

func TestPasswordConcurrentSetupAndRateLimit(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "auth.json")
	servers := []*Server{authServer(t, file), authServer(t, file)}
	var group sync.WaitGroup
	codes := make([]int, 2)
	for i, server := range servers {
		group.Go(func() {
			codes[i] = authCall(server, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, "").Code
		})
	}
	group.Wait()
	if !(codes[0] == 200 && codes[1] == 409 || codes[1] == 200 && codes[0] == 409) {
		t.Fatal("setup was not exclusive", codes)
	}
	server := authServer(t, file)
	for range 5 {
		if got := authCall(server, "POST", "/auth/login", PasswordInput{Password: "wrong password phrase"}, nil, ""); got.Code != 401 {
			t.Fatal(got.Body.String())
		}
	}
	if got := authCall(server, "POST", "/auth/login", PasswordInput{Password: testPassword}, nil, ""); got.Code != 429 || got.Header().Get("Retry-After") == "" {
		t.Fatal("missing brute-force limit", got.Body.String())
	}
	server.auth.mu.Lock()
	server.auth.blockedUntil = time.Now().Add(-time.Second)
	server.auth.mu.Unlock()
	if got := authCall(server, "POST", "/auth/login", PasswordInput{Password: testPassword}, nil, ""); got.Code != 200 {
		t.Fatal("cooldown never recovered", got.Body.String())
	}
}

func TestPasswordPersistenceFailsClosed(t *testing.T) {
	t.Parallel()
	for _, contents := range []string{"", `{}`, `{"version":2}`, "broken"} {
		file := filepath.Join(t.TempDir(), "auth.json")
		os.WriteFile(file, []byte(contents), 0600)
		if _, err := newAuthentication(file); err == nil {
			t.Fatal("corrupt file enabled setup", contents)
		}
	}
	if _, err := newAuthentication(""); err == nil {
		t.Fatal("missing path accepted")
	}
	server := authServer(t, filepath.Join(t.TempDir(), "missing", "auth.json"))
	if got := authCall(server, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, ""); got.Code != 500 || len(got.Result().Cookies()) != 0 {
		t.Fatal("failed save established a session", got.Body.String())
	}
	if server.auth.password != nil {
		t.Fatal("failed save initialized authentication")
	}
}

type revokingWriter struct {
	*httptest.ResponseRecorder
	revoke func()
}

func (rw *revokingWriter) Flush() {
	rw.ResponseRecorder.Flush()
	if rw.revoke != nil {
		revoke := rw.revoke
		rw.revoke = nil
		revoke()
	}
}

func TestEventStreamStopsAfterSessionRevocation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := w.Workspace{ID: w.NewID(), Name: "private", Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: thread.ID, Status: "running", CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		return tx.InsertRun(run, "key", "hash")
	}); err != nil {
		t.Fatal(err)
	}
	server := authServer(t, filepath.Join(dir, "auth.json"))
	server.store = db
	response := authCall(server, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	request := httptest.NewRequest("GET", "http://local.test/api/v1/runs/"+run.ID+"/events", nil).WithContext(ctx)
	request.AddCookie(cookie)
	writer := &revokingWriter{ResponseRecorder: httptest.NewRecorder(), revoke: func() {
		server.auth.mu.Lock()
		delete(server.auth.sessions, cookie.Value)
		server.auth.mu.Unlock()
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			return tx.AppendEvent(run.ID, "", "", "private_event", map[string]string{"text": "private-after-lock"})
		}); err != nil {
			t.Fatal(err)
		}
	}}
	server.Handler().ServeHTTP(writer, request)
	if ctx.Err() != nil || strings.Contains(writer.Body.String(), "private-after-lock") {
		t.Fatal("stream kept reading after lock", writer.Body.String())
	}
}
