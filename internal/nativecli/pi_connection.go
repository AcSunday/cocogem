package nativecli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
)

func piScopedEnv(dir string) map[string]string {
	return map[string]string{"PI_CODING_AGENT_DIR": dir, "PI_OFFLINE": "1", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0"}
}

func writePiConfig(workdir string, plan connectionPlan) (string, error) {
	dir := filepath.Join(workdir, "pi")
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", errors.New("create scoped Pi configuration")
	}
	settings := plan.piSettings
	if len(settings) == 0 {
		settings = []byte("{}")
	}
	for name, data := range map[string][]byte{"auth.json": plan.piAuth, "models.json": plan.piModels, "settings.json": settings} {
		if len(data) == 0 {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return "", errors.New("write scoped Pi configuration")
		}
	}
	return dir, nil
}

// Resolve with Pi's own model catalog and auth rules, without sending a prompt.
func (p Provider) piPlan(ctx context.Context, model string) (connectionPlan, error) {
	plan := connectionPlan{executor: "pi", piEnv: os.Environ()}
	if dir := piAgentDir(); dir != "" {
		for name, target := range map[string]*[]byte{"auth.json": &plan.piAuth, "models.json": &plan.piModels, "settings.json": &plan.piSettings} {
			data, err := readPiFile(filepath.Join(dir, name))
			if err != nil {
				return plan, err
			}
			*target = data
		}
	}
	plan.piSettings = piSettingsForScope(plan.piSettings)
	base, err := os.MkdirTemp("", "cocogem-pi-connection-*")
	if err != nil {
		return plan, errors.New("create isolated Pi connection directory")
	}
	defer os.RemoveAll(base)
	workdir := filepath.Join(base, ".cocogem")
	if err := os.Mkdir(workdir, 0700); err != nil {
		return plan, errors.New("create isolated Pi connection directory")
	}
	dir, err := writePiConfig(workdir, plan)
	if err != nil {
		return plan, err
	}
	args := []string{"--mode", "rpc", "--no-session", "--no-extensions", "--no-mcp", "--no-tools", "--no-context-files", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-approve"}
	if model != "" && model != "default" {
		if strings.HasPrefix(model, "-") || len(model) > 200 || strings.ContainsAny(model, "\r\n\x00") {
			return plan, errors.New("invalid CLI model")
		}
		args = append(args, "--model", model)
	}
	command := p.command
	if command == nil {
		command = exec.Command
	}
	cmd := command("pi", args...)
	cmd.Dir = workdir
	if cmd.Env != nil {
		plan.piEnv = append([]string{}, cmd.Env...)
	}
	cmd.Env = overrideEnv(plan.piEnv, piScopedEnv(dir))
	cmd.Stdin = strings.NewReader("{\"type\":\"get_state\",\"id\":\"cocogem-connection\"}\n")
	output := &boundedBuffer{limit: 1024 * 1024}
	cmd.Stdout, cmd.Stderr = output, &boundedBuffer{limit: 8192, truncate: true}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := runProbe(probeCtx, cmd); err != nil {
		if ctx.Err() != nil {
			return plan, ctx.Err()
		}
		return plan, errors.New("Pi could not resolve its configured model; check local Pi configuration")
	}
	var state struct {
		Model struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			API      string `json:"api"`
			BaseURL  string `json:"baseUrl"`
		} `json:"model"`
		Thinking string `json:"thinkingLevel"`
	}
	found := false
	scanner := bufio.NewScanner(strings.NewReader(output.String()))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var response struct {
			ID      string          `json:"id"`
			Type    string          `json:"type"`
			Command string          `json:"command"`
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			return plan, errors.New("Pi returned an invalid connection state")
		}
		if response.ID == "cocogem-connection" && response.Type == "response" && response.Command == "get_state" {
			if found || !response.Success || json.Unmarshal(response.Data, &state) != nil {
				return plan, errors.New("Pi returned an invalid connection state")
			}
			found = true
		}
	}
	if scanner.Err() != nil || !found || state.Model.ID == "" || state.Model.Provider == "" || state.Model.API == "" {
		return plan, errors.New("Pi has no resolved model; check local Pi configuration")
	}
	plan.piModel, plan.piThinking = state.Model.Provider+"/"+state.Model.ID, state.Thinking
	fields := map[string]any{
		"executor": "pi", "model": state.Model, "thinking": state.Thinking,
		"settings": connectionDigest(string(plan.piSettings)),
		"models":   piConfigIdentity(plan.piModels),
		"auth":     piConfigIdentity(plan.piAuth),
	}
	// These values choose upstream endpoints or cloud projects at request time.
	for _, entry := range plan.piEnv {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "AZURE_OPENAI_BASE_URL", "AZURE_OPENAI_RESOURCE_NAME", "AZURE_OPENAI_API_VERSION", "AZURE_OPENAI_DEPLOYMENT_NAME_MAP",
			"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_BEDROCK_RUNTIME",
			"GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_APPLICATION_CREDENTIALS",
			"CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_GATEWAY_ID":
			fields[key] = value
		}
	}
	plan.info = agent.ConnectionInfo{ResolvedModel: plan.piModel, Identity: connectionDigest(fields)}
	return plan, nil
}

// Rotating credentials stay in the private launch snapshot, outside identity.
func piConfigIdentity(raw []byte) string {
	var value any
	if len(raw) == 0 {
		return ""
	}
	if json.Unmarshal(raw, &value) != nil {
		return "invalid"
	}
	var redact func(any)
	redact = func(value any) {
		switch current := value.(type) {
		case map[string]any:
			for key, child := range current {
				switch strings.ToLower(key) {
				case "key", "apikey", "access", "refresh", "expires", "idtoken", "authorization", "x-api-key":
					delete(current, key)
				default:
					redact(child)
				}
			}
		case []any:
			for _, child := range current {
				redact(child)
			}
		}
	}
	redact(value)
	return connectionDigest(value)
}
