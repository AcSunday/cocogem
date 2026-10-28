package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// detectSystemProxy resolves COCOGEM_WEB_PROXY=system. Tests replace it.
var detectSystemProxy = readSystemProxy

// readSystemProxy returns the OS-configured web proxy endpoint. It reads only
// proxy host/port, never PAC scripts, subscriptions or credentials. An empty
// result with no error means no proxy is configured; a read failure is an
// error so startup never silently falls back to a direct connection.
func readSystemProxy() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return "", fmt.Errorf("read macOS system proxy: %w", err)
	}
	return parseSystemProxy(string(output)), nil
}

// parseSystemProxy picks one endpoint, preferring HTTPS, then HTTP, then SOCKS.
// macOS stores HTTPS proxy endpoints as plain HTTP CONNECT proxies.
func parseSystemProxy(output string) string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " : ")
		if !ok {
			continue
		}
		values[key] = strings.TrimSpace(value)
	}
	for _, candidate := range []struct{ enable, host, port, scheme string }{
		{"HTTPSEnable", "HTTPSProxy", "HTTPSPort", "http"},
		{"HTTPEnable", "HTTPProxy", "HTTPPort", "http"},
		{"SOCKSEnable", "SOCKSProxy", "SOCKSPort", "socks5"},
	} {
		if values[candidate.enable] != "1" {
			continue
		}
		host, port := values[candidate.host], values[candidate.port]
		if host == "" || port == "" {
			continue
		}
		if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
			continue
		}
		return (&url.URL{Scheme: candidate.scheme, Host: net.JoinHostPort(host, port)}).String()
	}
	return ""
}
