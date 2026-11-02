package nativecli

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeConnectionConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENCODE_CONFIG_DIR", dir)
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"providers":{"fixture":{"settings":{"timeout":60000}}}}`)
	t.Setenv("COCOGEM_SYNTHETIC_KEY", "synthetic-only")
	if err := os.WriteFile(filepath.Join(dir, "key.txt"), []byte("synthetic-file-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := `{
 // retain endpoint strings including slashes and commas
 "model":"fixture/model#high",
 "providers":{"fixture":{"settings":{"baseURL":"https://example.invalid/v1", "apiKey":"{env:COCOGEM_SYNTHETIC_KEY}", "fileKey":"{file:key.txt}",},}},
 "plugins":["unsafe-plugin"],"instructions":["private.md"],"mcp":{"servers":{"unrelated":{}}},
 "agents":{"user":{"system":"unrelated"}},"permissions":[{"action":"*","resource":"*","effect":"allow"}],
}`
	if err := os.WriteFile(filepath.Join(dir, "opencode.jsonc"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := openCodeConnectionConfig()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(config)
	for _, unexpected := range []string{"unsafe-plugin", "private.md", "unrelated", "permissions", "instructions"} {
		if strings.Contains(string(encoded), unexpected) {
			t.Fatalf("forwarded %s", unexpected)
		}
	}
	settings := config["providers"].(map[string]any)["fixture"].(map[string]any)["settings"].(map[string]any)
	if config["model"] != "fixture/model#high" || settings["apiKey"] != "synthetic-only" || settings["fileKey"] != "synthetic-file-key" || settings["timeout"] != float64(60000) {
		t.Fatalf("connection forwarding failed: %v", config)
	}
}

func TestOpenCodeConfigRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct{ name, raw string }{
		{"unterminated comment", `{"model":"fixture/model" /*`},
		{"malformed", `{"model":`},
		{"oversized", strings.Repeat(" ", 1024*1024+1)},
		{"missing file", `{"providers":{"fixture":{"settings":{"apiKey":"{file:missing}"}}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := mergeOpenCodeConfig(map[string]any{}, []byte(test.raw), t.TempDir()); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestOpenCodeCredentialSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.db")
	t.Setenv("OPENCODE_DB", path)
	db, err := openOpenCodeFixtureDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, active := range []int{0, 1} {
		_, err = db.Exec(`INSERT INTO credential (id,integration_id,label,value,active,time_created,time_updated) VALUES (?, 'fixture', 'public fixture', '{"type":"key","key":"synthetic-only"}', ?, 1, 1)`, string(rune('a'+active)), active)
		if err != nil {
			t.Fatal(err)
		}
	}
	credentials, err := openCodeCredentials(context.Background())
	if err != nil || len(credentials) != 1 || credentials[0].ID != "b" {
		t.Fatalf("credentials=%d err=%v", len(credentials), err)
	}
}

func openOpenCodeFixtureDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE credential (id TEXT PRIMARY KEY, integration_id TEXT, label TEXT NOT NULL, value TEXT NOT NULL, connector_id TEXT, method_id TEXT, active INTEGER, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
