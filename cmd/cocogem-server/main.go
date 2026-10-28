package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/httpapi"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
)

type rootsFlag []string

func (r *rootsFlag) String() string { return strings.Join(*r, ",") }
func (r *rootsFlag) Set(value string) error {
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return errors.New("allow-root must be a directory")
	}
	*r = append(*r, resolved)
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func newWeb(searchKey, provider, proxy string) (*tools.Web, error) {
	if provider == "" {
		provider = "auto"
	}
	var web *tools.Web
	switch provider {
	case "auto":
		web = tools.NewWebWithFallback(searchKey)
	case "brave":
		web = tools.NewWeb(searchKey)
	case "bing", "duckduckgo", "public":
		web = tools.NewWebKeyless()
	case "off":
		web = tools.NewWeb("")
	default:
		return nil, fmt.Errorf("COCOGEM_SEARCH_PROVIDER must be auto, brave, bing, duckduckgo, public or off")
	}
	if proxy == "system" {
		detected, err := detectSystemProxy()
		if err != nil {
			return nil, err
		}
		proxy = detected
	}
	if proxy == "env" {
		proxy = ""
		for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
			if value := os.Getenv(key); value != "" {
				proxy = value
				break
			}
		}
	}
	if proxy != "" {
		if err := web.UseProxy(proxy); err != nil {
			return nil, err
		}
	}
	return web, nil
}

// loadLimits resolves the execution policy once at startup; explicit zero
// disables a check, while invalid or negative values refuse to start.
func loadLimits() (workflow.Limits, error) {
	limits := workflow.DefaultLimits()
	limits.StallAction = os.Getenv("COCOGEM_STALL_ACTION")
	if limits.StallAction == "" {
		limits.StallAction = "warn"
	}
	if limits.StallAction != "warn" && limits.StallAction != "stop" {
		return limits, errors.New("COCOGEM_STALL_ACTION must be warn or stop")
	}
	for name, target := range map[string]*time.Duration{
		"COCOGEM_RUN_TIMEOUT":            &limits.RunTimeout,
		"COCOGEM_STEP_TIMEOUT":           &limits.StepTimeout,
		"COCOGEM_FIRST_RESPONSE_TIMEOUT": &limits.FirstResponse,
		"COCOGEM_STALL_TIMEOUT":          &limits.Stall,
	} {
		value := os.Getenv(name)
		if value == "" {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 0 {
			return limits, fmt.Errorf("%s must be a non-negative duration such as 0, 2h or 90m", name)
		}
		*target = parsed
	}
	return limits, nil
}

func limitSeconds(limits workflow.Limits) map[string]int {
	return map[string]int{
		"run_seconds":            int(limits.RunTimeout.Seconds()),
		"step_seconds":           int(limits.StepTimeout.Seconds()),
		"first_response_seconds": int(limits.FirstResponse.Seconds()),
		"stall_seconds":          int(limits.Stall.Seconds()),
	}
}
func run() error {
	var roots rootsFlag
	listen := flag.String("listen", "127.0.0.1:8741", "Loopback listener")
	webDir := flag.String("web-dir", "", "Built frontend directory")
	flag.Var(&roots, "allow-root", "Restrict workspace directories to this root or its descendants (repeatable; optional)")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("v0.1 only permits loopback listeners")
	}
	if *webDir == "" {
		exe, _ := os.Executable()
		candidate := filepath.Join(filepath.Dir(exe), "web")
		if _, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil {
			*webDir = candidate
		} else {
			*webDir = "web/dist"
		}
	}
	dataDir := os.Getenv("COCOGEM_DATA_DIR")
	if dataDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dataDir = filepath.Join(base, "cocogem")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dataDir, 0700); err != nil {
		return err
	}
	models := []string{"default"}
	searchKey := os.Getenv("BRAVE_SEARCH_API_KEY")
	searchProvider := strings.ToLower(strings.TrimSpace(os.Getenv("COCOGEM_SEARCH_PROVIDER")))
	if searchProvider == "" {
		searchProvider = "auto"
	}
	web, err := newWeb(searchKey, searchProvider, os.Getenv("COCOGEM_WEB_PROXY"))
	if err != nil {
		return err
	}
	limits, err := loadLimits()
	if err != nil {
		return err
	}
	native := nativecli.Provider{}
	runners := map[string]agent.Runner{}
	discover := func(ctx context.Context) []httpapi.ExecutorInfo {
		result := []httpapi.ExecutorInfo{}
		for _, item := range nativecli.Discover(ctx) {
			result = append(result, httpapi.ExecutorInfo{ID: item.ID, Installed: item.Installed, Version: item.Version, Supported: item.Supported, Reason: item.Reason, Adapter: item.Adapter, Isolation: item.Isolation, Protocol: "runtime", Capabilities: item.Capabilities, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Identity: item.Identity})
		}
		return result
	}
	executors := discover(context.Background())
	configured := false
	for _, item := range executors {
		if item.Installed {
			runners[item.ID] = native
		}
		if item.Installed && item.Supported {
			configured = true
		}
	}
	db, err := store.Open(filepath.Join(dataDir, "state.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine := &workflow.Engine{Store: db, AllowedRoots: roots, AllowedModels: models, Model: native, Runners: runners, Fallback: native, Adapters: native, Limits: limits, Web: web, NativeProxy: web.ProxyURL()}
	server, err := httpapi.New(httpapi.Config{Host: *listen, AuthFile: filepath.Join(dataDir, "auth.json"), WebDir: *webDir, Models: models, Roots: roots, ModelConfigured: configured, SearchConfigured: web.SearchEnabled(), SearchProvider: web.SearchProvider(), Model: native, Executors: executors, Discover: discover, Limits: limitSeconds(limits)}, db, engine)
	if err != nil {
		return err
	}
	if err := engine.Start(ctx); err != nil {
		return err
	}
	defer func() { stop(); engine.Wait() }()
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	fmt.Printf("Cocogem: http://%s/\n", *listen)
	fmt.Printf("Data: %s\n", dataDir)
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	select {
	case err := <-engine.Errors:
		stop()
		_ = httpServer.Close()
		return fmt.Errorf("scheduler failed: %w", err)
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
			return err
		}
		return nil
	}
	return nil
}
