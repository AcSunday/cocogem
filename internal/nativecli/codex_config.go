package nativecli

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/net/http/httpguts"
)

// Only connection fields enter the child configuration. Hooks, tools and plugins stay host-owned.
type codexConnection struct {
	Name               string            `toml:"name,omitempty"`
	BaseURL            string            `toml:"base_url,omitempty"`
	EnvKey             string            `toml:"env_key,omitempty"`
	BearerToken        string            `toml:"experimental_bearer_token,omitempty"`
	RequiresOpenAIAuth *bool             `toml:"requires_openai_auth,omitempty"`
	WireAPI            string            `toml:"wire_api,omitempty"`
	HTTPHeaders        map[string]string `toml:"http_headers,omitempty"`
	EnvHTTPHeaders     map[string]string `toml:"env_http_headers,omitempty"`
	QueryParams        map[string]string `toml:"query_params,omitempty"`
	SupportsWebsockets *bool             `toml:"supports_websockets,omitempty"`
	Auth               any               `toml:"auth,omitempty"`
	AWS                any               `toml:"aws,omitempty"`
	GatewayOAuth       any               `toml:"gateway_oauth,omitempty"`
}

type codexConfig struct {
	Model          string                     `toml:"model,omitempty"`
	ModelProvider  string                     `toml:"model_provider,omitempty"`
	ModelProviders map[string]codexConnection `toml:"model_providers,omitempty"`
}

func codexHome() (string, error) {
	if configured := os.Getenv("CODEX_HOME"); configured != "" {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return "", errors.New("locate native Codex configuration")
		}
		return absolute, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("locate native Codex configuration")
	}
	return filepath.Join(home, ".codex"), nil
}

func readCodexConnection() (codexConfig, string, error) {
	home, err := codexHome()
	if err != nil {
		return codexConfig{}, "", err
	}
	path := filepath.Join(home, "config.toml")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return codexConfig{}, home, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return codexConfig{}, "", errors.New("native Codex configuration is unavailable or exceeds its size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return codexConfig{}, "", errors.New("open native Codex configuration")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return codexConfig{}, "", errors.New("read native Codex configuration within budget")
	}
	var config codexConfig
	if toml.Unmarshal(raw, &config) != nil {
		return codexConfig{}, "", errors.New("native Codex configuration is not valid TOML")
	}
	return config, home, nil
}

func codexConnectionConfig(model string) (resolved string, args []string, env map[string]string, err error) {
	config, _, err := readCodexConnection()
	if err != nil {
		return "", nil, nil, err
	}
	resolved = model
	if resolved == "" || resolved == "default" {
		resolved = config.Model
	}
	if !modelIDPattern.MatchString(resolved) || resolved == "default" {
		return "", nil, nil, errors.New("Codex 默认模型未设置；请在本机 Codex 配置或成员设置中填写明确模型 ID")
	}
	if config.ModelProvider == "" {
		config.ModelProvider = "openai"
	}
	connection, ok := config.ModelProviders[config.ModelProvider]
	if !ok && config.ModelProvider == "openai" {
		return resolved, nil, nil, nil
	}
	if !ok {
		return "", nil, nil, errors.New("native Codex selected provider has no connection configuration")
	}
	if connection.Auth != nil || connection.AWS != nil || connection.GatewayOAuth != nil {
		return "", nil, nil, errors.New("native Codex command-backed or delegated authentication is not supported by this adapter")
	}
	if connection.WireAPI != "" && connection.WireAPI != "responses" {
		return "", nil, nil, errors.New("native Codex provider must support the Responses protocol")
	}
	if connection.BaseURL != "" {
		parsed, parseErr := url.Parse(connection.BaseURL)
		if parseErr != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
			return "", nil, nil, errors.New("native Codex provider base URL is invalid or embeds credentials")
		}
	}
	if connection.Name == "" {
		connection.Name = "Cocogem native connection"
	}
	if connection.EnvKey != "" && !envNamePattern.MatchString(connection.EnvKey) {
		return "", nil, nil, errors.New("native Codex provider has an invalid credential environment name")
	}
	env = map[string]string{}
	if connection.BearerToken != "" {
		if connection.EnvKey != "" {
			return "", nil, nil, errors.New("native Codex provider defines ambiguous token sources")
		}
		connection.EnvKey = "COCOGEM_CODEX_CONNECTION_TOKEN"
		env[connection.EnvKey] = connection.BearerToken
		connection.BearerToken = ""
	}
	headers := map[string]string{}
	index := 0
	for header, value := range connection.HTTPHeaders {
		if !httpguts.ValidHeaderFieldName(header) {
			return "", nil, nil, errors.New("native Codex provider has an invalid header name")
		}
		key := "COCOGEM_CODEX_CONNECTION_HEADER_" + strconv.Itoa(index)
		index++
		env[key] = value
		headers[header] = key
	}
	for header, key := range connection.EnvHTTPHeaders {
		if !httpguts.ValidHeaderFieldName(header) || !envNamePattern.MatchString(key) {
			return "", nil, nil, errors.New("native Codex provider has an invalid header environment name")
		}
		headers[header] = key
	}
	connection.HTTPHeaders = nil
	connection.EnvHTTPHeaders = headers
	if len(connection.QueryParams) > 0 {
		return "", nil, nil, errors.New("native Codex provider query parameters require a reviewed authentication adapter")
	}
	connection.Name = "Cocogem native connection"
	profile := codexConfig{ModelProvider: "cocogem_connection", ModelProviders: map[string]codexConnection{"cocogem_connection": connection}}
	var encoded bytes.Buffer
	if toml.NewEncoder(&encoded).SetTablesInline(true).Encode(profile) != nil {
		return "", nil, nil, errors.New("encode scoped Codex connection")
	}
	// --ignore-user-config also ignores --profile; only nonsecret connection overrides enter argv.
	for _, line := range strings.Split(strings.TrimSpace(encoded.String()), "\n") {
		args = append(args, "-c", line)
	}
	return resolved, args, env, nil
}

var envNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,127}$`)
