package nativecli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	_ "modernc.org/sqlite"
)

type openCodeCredential struct {
	ID, Integration, Label, Value string
	Connector, Method             sql.NullString
	Created, Updated              int64
}

// Only connection fields cross the boundary; user agents, plugins, skills,
// instructions, permissions and MCP servers never enter the scoped config.
func openCodeConnectionConfig() (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("cannot locate OpenCode configuration")
	}
	dir := os.Getenv("OPENCODE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(xdgRoot("XDG_CONFIG_HOME", filepath.Join(home, ".config")), "opencode")
	}
	config := map[string]any{}
	paths := []string{filepath.Join(dir, "config.json"), filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc")}
	if file := os.Getenv("OPENCODE_CONFIG"); file != "" {
		paths = append(paths, file)
	}
	for _, path := range paths {
		raw, err := readConnectionConfig(path)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			continue
		}
		if err := mergeOpenCodeConfig(config, raw, filepath.Dir(path)); err != nil {
			return nil, err
		}
	}
	if raw := os.Getenv("OPENCODE_CONFIG_CONTENT"); raw != "" {
		if err := mergeOpenCodeConfig(config, []byte(raw), dir); err != nil {
			return nil, err
		}
	}
	return config, nil
}

func xdgRoot(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

var openCodeVariable = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

func mergeOpenCodeConfig(config map[string]any, raw []byte, dir string) error {
	var input map[string]any
	if len(raw) > 1024*1024 || json.Unmarshal(openCodeJSON(raw), &input) != nil {
		return errors.New("OpenCode connection configuration must be valid JSON/JSONC within 1 MiB")
	}
	for _, key := range []string{"model", "providers", "provider", "enabled_providers", "disabled_providers"} {
		value, ok := input[key]
		if !ok {
			continue
		}
		encoded, _ := json.Marshal(value)
		var expansionErr error
		encoded = openCodeVariable.ReplaceAllFunc(encoded, func(match []byte) []byte {
			parts := openCodeVariable.FindSubmatch(match)
			value := os.Getenv(string(parts[2]))
			if string(parts[1]) == "file" {
				path := string(parts[2])
				if strings.HasPrefix(path, "~/") {
					home, _ := os.UserHomeDir()
					path = filepath.Join(home, path[2:])
				} else if !filepath.IsAbs(path) {
					path = filepath.Join(dir, path)
				}
				data, err := readConnectionConfig(path)
				if err != nil || data == nil {
					expansionErr = errors.New("cannot resolve OpenCode connection file reference")
				}
				value = strings.TrimSpace(string(data))
			}
			quoted, _ := json.Marshal(value)
			return quoted[1 : len(quoted)-1]
		})
		if expansionErr != nil || json.Unmarshal(encoded, &value) != nil {
			return errors.New("cannot resolve OpenCode connection variables")
		}
		if current, ok := config[key].(map[string]any); ok {
			if next, ok := value.(map[string]any); ok {
				mergeOpenCodeMap(current, next)
				continue
			}
		}
		config[key] = value
	}
	return nil
}

func mergeOpenCodeMap(dst, src map[string]any) {
	for key, value := range src {
		if child, ok := dst[key].(map[string]any); ok {
			if next, ok := value.(map[string]any); ok {
				mergeOpenCodeMap(child, next)
				continue
			}
		}
		dst[key] = value
	}
}

// Strip JSONC comments and trailing commas while preserving quoted strings.
func openCodeJSON(raw []byte) []byte {
	out := append([]byte{}, raw...)
	quoted, escaped := false, false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		if c != '/' || i+1 >= len(out) {
			continue
		}
		if out[i+1] == '/' {
			for ; i < len(out) && out[i] != '\n'; i++ {
				out[i] = ' '
			}
		} else if out[i+1] == '*' {
			out[i], out[i+1] = ' ', ' '
			i += 2
			for ; i+1 < len(out) && !(out[i] == '*' && out[i+1] == '/'); i++ {
				out[i] = ' '
			}
			if i+1 >= len(out) {
				return nil
			}
			out[i], out[i+1] = ' ', ' '
			i++
		}
	}
	quoted, escaped = false, false
	for i, c := range out {
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
		} else if c == ',' {
			for j := i + 1; j < len(out); j++ {
				if strings.ContainsRune(" \t\r\n", rune(out[j])) {
					continue
				}
				if out[j] == '}' || out[j] == ']' {
					out[i] = ' '
				}
				break
			}
		}
	}
	return out
}

func openCodeCredentials(ctx context.Context) ([]openCodeCredential, error) {
	home, _ := os.UserHomeDir()
	path := os.Getenv("OPENCODE_DB")
	if path == "" {
		path = filepath.Join(xdgRoot("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "opencode", "opencode.db")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		return nil, errors.New("cannot read OpenCode credentials")
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT id, integration_id, label, value, connector_id, method_id, time_created, time_updated FROM credential WHERE active = 1 LIMIT 101")
	if err != nil {
		return nil, errors.New("cannot read OpenCode credential store; check the installed CLI database version")
	}
	defer rows.Close()
	var credentials []openCodeCredential
	for rows.Next() {
		var item openCodeCredential
		if rows.Scan(&item.ID, &item.Integration, &item.Label, &item.Value, &item.Connector, &item.Method, &item.Created, &item.Updated) != nil || len(item.Value) > 1024*1024 {
			return nil, errors.New("invalid OpenCode credential record")
		}
		credentials = append(credentials, item)
		if len(credentials) > 100 {
			return nil, errors.New("OpenCode has too many active credentials for a scoped snapshot")
		}
	}
	if rows.Err() != nil {
		return nil, errors.New("cannot read OpenCode credentials")
	}
	return credentials, nil
}

func openCodeScopedEnv(dir string) map[string]string {
	return map[string]string{
		"PWD": dir, "OPENCODE_CONFIG_DIR": dir, "OPENCODE_CONFIG": "", "OPENCODE_CONFIG_CONTENT": "{}", "OPENCODE_CLI_CONFIG_CONTENT": "{}",
		"OPENCODE_DB": filepath.Join(dir, "opencode.db"), "OPENCODE_CONFIG_PROJECT_DISABLE": "1", "OPENCODE_DISABLE_PROJECT_CONFIG": "1",
		"OPENCODE_DISABLE_CLAUDE_CODE": "1", "OPENCODE_DISABLE_EXTERNAL_SKILLS": "1", "OPENCODE_DISABLE_MODELS_FETCH": "1",
		"OPENCODE_DISABLE_FILEWATCHER": "1", "OPENCODE_DISABLE_FFF": "1", "OPENCODE_SERVER": "", "OPENCODE_SERVER_URL": "",
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_DATA_HOME": filepath.Join(dir, "data"), "XDG_STATE_HOME": filepath.Join(dir, "state"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"),
	}
}

func (p Provider) openCodeAPI(ctx context.Context, dir string, env []string, operation string) ([]byte, error) {
	command := p.command
	if command == nil {
		command = exec.Command
	}
	cmd := command("opencode", "api", operation, "--standalone")
	cmd.Dir, cmd.Env = dir, overrideEnv(env, openCodeScopedEnv(dir))
	output := &boundedBuffer{limit: 8 * 1024 * 1024}
	cmd.Stdout, cmd.Stderr = output, &boundedBuffer{limit: 8192, truncate: true}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := runProbe(ctx, cmd); err != nil {
		return nil, errors.New("OpenCode could not resolve its connection; check local provider/model configuration")
	}
	return []byte(output.String()), nil
}

func (p Provider) writeOpenCodeConfig(ctx context.Context, dir string, plan connectionPlan, config map[string]any) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return errors.New("cannot encode scoped OpenCode configuration")
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), raw, 0600); err != nil {
		return errors.New("cannot write scoped OpenCode configuration")
	}
	if len(plan.openCodeCredentials) == 0 {
		return nil
	}
	// Let this CLI version create its own schema, then copy only active
	// credentials. Conversation history and user databases stay outside scope.
	if _, err := p.openCodeAPI(ctx, dir, plan.openCodeEnv, "config.get"); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "opencode.db"))
	if err != nil {
		return errors.New("cannot open scoped OpenCode credential store")
	}
	defer db.Close()
	for _, item := range plan.openCodeCredentials {
		_, err := db.ExecContext(ctx, "INSERT OR REPLACE INTO credential (id, integration_id, label, value, connector_id, method_id, active, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)", item.ID, item.Integration, item.Label, item.Value, item.Connector, item.Method, item.Created, item.Updated)
		if err != nil {
			return errors.New("cannot copy scoped OpenCode credentials")
		}
	}
	return nil
}

func (p Provider) openCodePlan(ctx context.Context, selected string) (connectionPlan, error) {
	plan := connectionPlan{executor: "opencode", openCodeEnv: os.Environ()}
	config, err := openCodeConnectionConfig()
	if err != nil {
		return plan, err
	}
	plan.openCodeConfig = config
	plan.openCodeCredentials, err = openCodeCredentials(ctx)
	if err != nil {
		return plan, err
	}
	dir, err := os.MkdirTemp("", "cocogem-opencode-connection-*")
	if err != nil {
		return plan, errors.New("cannot create isolated OpenCode connection directory")
	}
	defer os.RemoveAll(dir)
	if err := p.writeOpenCodeConfig(ctx, dir, plan, config); err != nil {
		return plan, err
	}
	operation := "model.default"
	if selected != "" && selected != "default" {
		operation = "model.list"
	}
	raw, err := p.openCodeAPI(ctx, dir, plan.openCodeEnv, operation)
	if err != nil {
		return plan, err
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return plan, errors.New("OpenCode returned an invalid model catalog")
	}
	var models []map[string]any
	if operation == "model.default" {
		var item map[string]any
		if json.Unmarshal(response.Data, &item) == nil && item != nil {
			models = append(models, item)
		}
	} else {
		_ = json.Unmarshal(response.Data, &models)
	}
	base, variant, _ := strings.Cut(selected, "#")
	for _, item := range models {
		provider, _ := item["providerID"].(string)
		id, _ := item["id"].(string)
		if provider == "" || id == "" || operation != "model.default" && base != provider+"/"+id {
			continue
		}
		resolved := provider + "/" + id
		if variant != "" {
			resolved += "#" + variant
		} else if configured, ok := config["model"].(string); ok && strings.HasPrefix(configured, resolved+"#") && operation == "model.default" {
			resolved = configured
		}
		if configured, ok := config["model"].(map[string]any); ok && operation == "model.default" {
			if configured["providerID"] == provider && configured["model"] == id {
				if variant, ok := configured["variant"].(string); ok && variant != "" {
					resolved += "#" + variant
				}
			}
		}
		plan.openCodeModel = resolved
		if variants, ok := item["variants"].([]any); ok {
			for _, value := range variants {
				if variant, ok := value.(map[string]any); ok {
					if id, ok := variant["id"].(string); ok && id != "" {
						plan.openCodeVariants = append(plan.openCodeVariants, id)
					}
				}
			}
		}
		encoded, _ := json.Marshal(item)
		if _, variant, ok := strings.Cut(resolved, "#"); ok {
			if _, err := openCodeModelVariant(plan, variant); err != nil {
				return plan, err
			}
		}
		plan.openCodeCatalog = piConfigIdentity(encoded)
		plan.info = agent.ConnectionInfo{ResolvedModel: resolved, Identity: connectionDigest(map[string]string{"executor": "opencode", "model": resolved, "connection": plan.openCodeCatalog})}
		return plan, nil
	}
	return plan, errors.New("OpenCode has no matching configured model; use provider/model or provider/model#variant")
}

// writeOpenCodePlugin installs the private execute.before host gate. A local
// plugin path must be a directory; index.js is the server part.
func writeOpenCodePlugin(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("create private OpenCode plugin directory")
	}
	code := `import { Plugin } from "@opencode/plugin"
import fs from "node:fs"

const endpoint = process.env.COCOGEM_PLUGIN_URL
const tokenFile = process.env.COCOGEM_PLUGIN_TOKEN_FILE
let token = ""
try {
  token = fs.readFileSync(tokenFile, "utf8").trim()
  fs.unlinkSync(tokenFile)
} catch {}

async function host(path, body) {
  const response = await fetch(endpoint + path, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
    body: JSON.stringify(body),
  })
  if (!response.ok) {
    let detail = ""
    try { detail = await response.text() } catch {}
    throw new Error("host approval endpoint failed: " + response.status + " " + detail)
  }
  return response.json()
}

export default Plugin.define({
  id: "cocogem-native-gate",
  async setup(ctx) {
    await ctx.tool.hook("execute.before", async (event) => {
      if (typeof event.tool === "string" && event.tool.startsWith("cocogem_")) return
      const decision = await host("/permission", { id: event.id, tool: event.tool, input: event.input })
      if (!decision.allow) throw new Error(decision.reason || "permission_denied")
    })
    await ctx.tool.transform((editor) => {
      for (const name of ["bash", "shell"]) {
        if (!editor.get(name)) continue
        editor.update(name, (tool) => {
          const original = tool.execute
          tool.execute = async (input, context) => {
            const command = input.command || input.cmd || ""
            if (!command) return original(input, context)
            const result = await host("/execute", { id: context.id, tool: "shell", input })
            if (!result.ok) throw new Error(result.error || "native command failed")
            return { content: result.output || "", metadata: { exit_code: result.exit_code, duration_ms: result.duration_ms } }
          }
        })
      }
      const root = process.env.COCOGEM_PROJECT_ROOT || ""
      const outside = (value) => {
        if (!root || !value) return false
        const prefix = root.endsWith("/") ? root : root + "/"
        return !(value === root || value.startsWith(prefix))
      }
      for (const name of ["read", "write", "edit"]) {
        if (!editor.get(name)) continue
        editor.update(name, (tool) => {
          const original = tool.execute
          tool.execute = async (input, context) => {
            const path = input.path || input.filePath || ""
            if (!outside(path)) return original(input, context)
            const result = await host("/file", { id: context.id, tool: name, input })
            if (!result.ok) throw new Error(result.error || "native file operation failed")
            return { content: result.content || "", metadata: {} }
          }
        })
      }
    })
  },
})
`
	return os.WriteFile(filepath.Join(dir, "index.js"), []byte(code), 0600)
}
