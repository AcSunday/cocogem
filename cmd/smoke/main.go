// Command smoke verifies a built server and its persisted workspaces through real HTTP.
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
	fmt.Println("PASS: production HTTP workspaces, process shutdown, backup and restore (no model calls)")
}

func smoke(binary, web string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "cocogem-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "data")
	client, stop, err := start(ctx, binary, web, data)
	if err != nil {
		return err
	}
	defer func() {
		if stop != nil {
			_ = stop()
		}
	}()
	var ws struct {
		ID string `json:"id"`
	}
	if err := client.call("POST", "/api/v1/workspaces", map[string]any{"name": "Smoke workspace"}, &ws); err != nil {
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
	if err := client.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", map[string]any{"title": "Smoke conversation", "agent_ids": ids, "coordinator_id": ids[2], "workflow": "research"}, &thread); err != nil {
		return err
	}
	if err := stop(); err != nil {
		return err
	}
	stop = nil
	backup := filepath.Join(dir, "backup")
	if err := copyDirectory(data, backup); err != nil {
		return err
	}
	restored := filepath.Join(dir, "restored")
	if err := copyDirectory(backup, restored); err != nil {
		return err
	}
	client, stop, err = start(ctx, binary, web, restored)
	if err != nil {
		return err
	}
	var restoredThread struct {
		Thread struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"thread"`
	}
	if err := client.call("GET", "/api/v1/threads/"+thread.ID, nil, &restoredThread); err != nil {
		return err
	}
	if restoredThread.Thread.ID != thread.ID || restoredThread.Thread.Title != "Smoke conversation" {
		return errors.New("backup restore changed the conversation")
	}
	return nil
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

func start(ctx context.Context, binary, web, data string) (*apiClient, func() error, error) {
	cmd := exec.CommandContext(ctx, binary, "--listen", "127.0.0.1:18745", "--web-dir", web)
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
	jar, _ := cookiejar.New(nil)
	client := &apiClient{http: &http.Client{Jar: jar, Timeout: 20 * time.Second}, base: strings.TrimSuffix(startup, "/")}
	var response struct {
		Token string `json:"csrf_token"`
	}
	for attempts := 0; attempts < 30; attempts++ {
		var status struct {
			Initialized bool `json:"initialized"`
		}
		err = client.call("GET", "/auth/status", nil, &status)
		if err == nil {
			path := "/auth/setup"
			if status.Initialized {
				path = "/auth/login"
			}
			err = client.call("POST", path, map[string]string{"password": "isolated-smoke-password"}, &response)
		}
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
