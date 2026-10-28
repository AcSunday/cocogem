package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
)

func TestBrowseDirectories(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	root := filepath.Join(base, "project")
	child := filepath.Join(root, "中文 项目")
	empty := filepath.Join(child, "empty")
	outside := filepath.Join(base, "outside")
	for _, path := range []string{empty, outside, filepath.Join(root, ".ssh"), filepath.Join(root, "secrets"), filepath.Join(root, "node_modules")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "private-file.txt"), []byte("not listed"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"outside-link", "inside-link"} {
		target := outside
		if link == "inside-link" {
			target = child
		}
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, path, wantPath, parent string
		allowed                      []string
		items                        []Directory
		fail                         bool
	}{
		{name: "default home", wantPath: base, parent: filepath.Dir(base), items: []Directory{{Name: "outside", Path: outside}, {Name: "project", Path: root}}},
		{name: "virtual allowed roots", allowed: []string{root, outside}, items: []Directory{{Name: root, Path: root}, {Name: outside, Path: outside}}},
		{name: "filters files hidden sensitive and symlinks", path: root, allowed: []string{root}, wantPath: root, items: []Directory{{Name: "中文 项目", Path: child}}},
		{name: "nested parent", path: child, allowed: []string{root}, wantPath: child, parent: root, items: []Directory{{Name: "empty", Path: empty}}},
		{name: "empty folder", path: empty, allowed: []string{root}, wantPath: empty, parent: child},
		{name: "outside boundary", path: outside, allowed: []string{root}, fail: true},
		{name: "parent traversal", path: root + "/..", allowed: []string{root}, fail: true},
		{name: "symlink escape", path: filepath.Join(root, "outside-link"), allowed: []string{root}, fail: true},
		{name: "sensitive path", path: filepath.Join(root, ".ssh"), fail: true},
		{name: "file", path: filepath.Join(root, "private-file.txt"), fail: true},
		{name: "missing", path: filepath.Join(root, "missing"), fail: true},
		{name: "relative", path: "project", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := browseDirectories(t.Context(), tc.path, tc.allowed)
			if (err != nil) != tc.fail {
				t.Fatalf("listing=%+v error=%v want failure=%v", got, err, tc.fail)
			}
			if tc.fail {
				return
			}
			wantItems := tc.items
			if wantItems == nil {
				wantItems = []Directory{}
			}
			if got.Path != tc.wantPath || got.Parent != tc.parent || got.Truncated || !reflect.DeepEqual(got.Items, wantItems) {
				t.Fatalf("listing=%+v want path=%q parent=%q items=%+v", got, tc.wantPath, tc.parent, wantItems)
			}
		})
	}
	t.Run("unreadable folder", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions")
		}
		if err := os.Chmod(empty, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(empty, 0700) })
		if _, err := browseDirectories(t.Context(), empty, nil); err == nil {
			t.Fatal("unreadable directory accepted")
		}
		listing, err := browseDirectories(t.Context(), child, nil)
		if err != nil || len(listing.Items) != 0 {
			t.Fatalf("unreadable child offered: %+v %v", listing, err)
		}
	})
}

func TestBrowseDirectoriesIsBounded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 1002 {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("folder-%04d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listing, err := browseDirectories(t.Context(), root, nil)
	if err != nil || !listing.Truncated || len(listing.Items) != 1000 {
		t.Fatalf("unbounded directory listing: %d truncated=%v err=%v", len(listing.Items), listing.Truncated, err)
	}
}

func TestDirectoryBrowserRequiresLocalSessionAndRespectsScope(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(base, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(Config{Host: "local.test", AuthFile: filepath.Join(base, "auth.json"), WebDir: base, Roots: []string{root}}, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	setup := authCall(s, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, "")
	if setup.Code != 200 {
		t.Fatal(setup.Body.String())
	}
	cookie := setup.Result().Cookies()[0]
	for _, tc := range []struct {
		name, path, origin, host string
		authenticated            bool
		status                   int
	}{
		{name: "locked", status: 401},
		{name: "root list", authenticated: true, status: 200},
		{name: "open directory", path: root, authenticated: true, status: 200},
		{name: "out of scope", path: filepath.Dir(root), authenticated: true, status: 400},
		{name: "foreign origin", authenticated: true, origin: "https://example.com", status: 403},
		{name: "foreign host", authenticated: true, host: "foreign.test", status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://local.test/api/v1/directories?path="+url.QueryEscape(tc.path), nil)
			if tc.authenticated {
				req.AddCookie(cookie)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			rw := httptest.NewRecorder()
			s.Handler().ServeHTTP(rw, req)
			if rw.Code != tc.status {
				t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
			}
			validateResponse(t, "/api/v1/directories", "get", tc.status, rw.Body.Bytes())
			if tc.status == 200 {
				var listing DirectoryListing
				if err := json.Unmarshal(rw.Body.Bytes(), &listing); err != nil {
					t.Fatal(err)
				}
				if listing.Path != tc.path {
					t.Fatalf("path=%q wanted=%q", listing.Path, tc.path)
				}
			}
		})
	}
}
