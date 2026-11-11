package nativecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Gemini uses Node's dotenv syntax. Parse it without running the user's shell
// or importing unrelated variables into the scoped CLI process.
func geminiConnectionFileEnv(ctx context.Context) (map[string]string, error) {
	home := os.Getenv("GEMINI_CLI_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, errors.New("cannot locate Gemini connection configuration")
		}
	}
	raw, err := readConnectionConfig(filepath.Join(home, ".gemini", ".env"))
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	allowed := make([]string, 0, len(geminiConnectionEnv))
	for _, key := range geminiConnectionEnv {
		if key != "GEMINI_CLI_HOME" && key != "GEMINI_CONFIG_DIR" {
			allowed = append(allowed, key)
		}
	}
	keys, err := json.Marshal(allowed)
	if err != nil {
		return nil, errors.New("cannot encode Gemini connection allowlist")
	}
	const script = `const {parseEnv}=require('node:util'); const {readFileSync}=require('node:fs'); const values=parseEnv(readFileSync(0,'utf8')); const selected=Object.create(null); for(const key of JSON.parse(process.argv[1])) if(Object.hasOwn(values,key)) selected[key]=values[key]; process.stdout.write(JSON.stringify(selected));`
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", script, string(keys))
	cmd.Env = []string{}
	cmd.Stdin = bytes.NewReader(raw)
	output := &boundedBuffer{limit: 6 * 1024 * 1024}
	cmd.Stdout, cmd.Stderr = output, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("cannot parse Gemini connection environment")
	}
	var values map[string]string
	if json.Unmarshal([]byte(output.String()), &values) != nil {
		return nil, errors.New("cannot parse Gemini connection environment")
	}
	return values, nil
}
