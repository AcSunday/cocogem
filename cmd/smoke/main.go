// Command smoke verifies a built server and its persisted demo through real HTTP.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	server := flag.String("server", "dist/cocogem-server", "Built server executable")
	web := flag.String("web-dir", "dist/web", "Built frontend directory")
	flag.Parse()
	if err := smoke(*server, *web); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("PASS: real HTTP Research, evidence, export, process shutdown, backup and restore")
}

func smoke(binary, web string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "cocogem-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	client, stop, err := start(ctx, binary, web, dir)
	if err != nil {
		return err
	}
	defer func() {
		if stop != nil {
			_ = stop()
		}
	}()
	var system struct {
		Roots []string `json:"roots"`
	}
	if err := client.call("GET", "/api/v1/system", nil, &system); err != nil {
		return err
	}
	var ws struct {
		ID string `json:"id"`
	}
	if err := client.call("POST", "/api/v1/workspaces", map[string]any{"name": "Public smoke", "root": system.Roots[0]}, &ws); err != nil {
		return err
	}
	var members struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := client.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, &members); err != nil {
		return err
	}
	if len(members.Items) != 3 {
		return errors.New("default team is missing")
	}
	ids := []string{members.Items[0].ID, members.Items[1].ID, members.Items[2].ID}
	var thread struct {
		ID string `json:"id"`
	}
	if err := client.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", map[string]any{"title": "Smoke research", "agent_ids": ids, "coordinator_id": ids[2], "workflow": "research"}, &thread); err != nil {
		return err
	}
	input := map[string]any{"message": "Research the public demo and preserve uncertainty.", "workflow": "research", "assignments": map[string]string{"researcher": ids[0], "critic": ids[1], "coordinator": ids[2]}, "research_scope": "given_sources"}
	var preview struct {
		ID string `json:"proposal_id"`
	}
	if err := client.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, &preview); err != nil {
		return err
	}
	input["proposal_id"] = preview.ID
	var run struct {
		ID string `json:"id"`
	}
	if err := client.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, &run); err != nil {
		return err
	}
	if err := client.finished(ctx, run.ID); err != nil {
		return err
	}
	var artifacts struct {
		Items []struct {
			ID        string   `json:"id"`
			Body      string   `json:"body"`
			SourceIDs []string `json:"source_ids"`
		} `json:"items"`
	}
	if err := client.call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, &artifacts); err != nil {
		return err
	}
	if len(artifacts.Items) != 1 || len(artifacts.Items[0].SourceIDs) == 0 {
		return errors.New("report evidence missing")
	}
	export, err := client.http.Get(client.base + "/api/v1/artifacts/" + artifacts.Items[0].ID + "/versions/1/export")
	if err != nil {
		return err
	}
	content, err := io.ReadAll(export.Body)
	export.Body.Close()
	if err != nil || !bytes.Contains(content, []byte("离线演示")) {
		return errors.New("export is not labeled")
	}
	if err := stop(); err != nil {
		return err
	}
	stop = nil
	backup := filepath.Join(dir, "backup")
	if err := copyDirectory(filepath.Join(dir, "demo"), backup); err != nil {
		return err
	}
	restored := filepath.Join(dir, "restored")
	if err := copyDirectory(backup, filepath.Join(restored, "demo")); err != nil {
		return err
	}
	client, stop, err = start(ctx, binary, web, restored)
	if err != nil {
		return err
	}
	var restoredArtifacts struct {
		Items []struct {
			Body string `json:"body"`
		} `json:"items"`
	}
	if err := client.call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, &restoredArtifacts); err != nil {
		return err
	}
	if len(restoredArtifacts.Items) != 1 || restoredArtifacts.Items[0].Body != artifacts.Items[0].Body {
		return errors.New("backup restore changed the report")
	}
	var source map[string]any
	return client.call("GET", "/api/v1/sources/"+artifacts.Items[0].SourceIDs[0], nil, &source)
}

type apiClient struct {
	http       *http.Client
	base, csrf string
}

func (c *apiClient) call(method, path string, input, out any) error {
	var body io.Reader
	if input != nil {
		b, _ := json.Marshal(input)
		body = bytes.NewReader(b)
	}
	request, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", c.csrf)
	request.Header.Set("Idempotency-Key", "smoke-test-single-submission")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		b, _ := io.ReadAll(response.Body)
		return fmt.Errorf("%s %s: %d %s", method, path, response.StatusCode, b)
	}
	return json.NewDecoder(response.Body).Decode(out)
}
func (c *apiClient) finished(ctx context.Context, id string) error {
	request, err := http.NewRequestWithContext(ctx, "GET", c.base+"/api/v1/runs/"+id+"/events", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return err
		}
		if event.Type == "run_finished" {
			if event.Data.Status != "succeeded" {
				return fmt.Errorf("run ended %s", event.Data.Status)
			}
			return nil
		}
	}
	return errors.New("event stream ended without run completion")
}

func start(ctx context.Context, binary, web, data string) (*apiClient, func() error, error) {
	cmd := exec.CommandContext(ctx, binary, "--demo", "--demo-pace", "0", "--listen", "127.0.0.1:18745", "--web-dir", web)
	cmd.Env = append(os.Environ(), "COCOGEM_DATA_DIR="+data)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var stopOnce bool
	stop := func() error {
		if stopOnce {
			return nil
		}
		stopOnce = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			return errors.New("server shutdown timed out")
		}
	}
	scanner := bufio.NewScanner(stdout)
	startup := ""
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "Cocogem: ") {
			startup = strings.TrimPrefix(scanner.Text(), "Cocogem: ")
			break
		}
	}
	if startup == "" {
		_ = stop()
		return nil, nil, errors.New("missing startup link")
	}
	parts := strings.SplitN(startup, "#bootstrap=", 2)
	if len(parts) != 2 {
		_ = stop()
		return nil, nil, errors.New("invalid startup link")
	}
	jar, _ := cookiejar.New(nil)
	client := &apiClient{http: &http.Client{Jar: jar, Timeout: 20 * time.Second}, base: strings.TrimSuffix(parts[0], "/")}
	var response struct {
		Token string `json:"csrf_token"`
	}
	for attempts := 0; attempts < 30; attempts++ {
		err = client.call("POST", "/bootstrap", map[string]string{"token": parts[1]}, &response)
		if err == nil {
			client.csrf = response.Token
			return client, stop, nil
		}
		select {
		case <-ctx.Done():
			_ = stop()
			return nil, nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	_ = stop()
	return nil, nil, err
}
func copyDirectory(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0600)
	})
}
