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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/AcSunday/cocogem/internal/httpapi"
	"github.com/AcSunday/cocogem/internal/model"
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
func run() error {
	var roots rootsFlag
	listen := flag.String("listen", "127.0.0.1:8741", "Loopback listener")
	webDir := flag.String("web-dir", "", "Built frontend directory")
	demo := flag.Bool("demo", false, "Use offline deterministic fixtures only")
	flag.Var(&roots, "allow-root", "Allowed source directory (repeatable)")
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
	if *demo {
		dataDir = filepath.Join(dataDir, "demo")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dataDir, 0700); err != nil {
		return err
	}
	models := strings.FieldsFunc(os.Getenv("COCOGEM_MODELS"), func(r rune) bool { return r == ',' })
	for i := range models {
		models[i] = strings.TrimSpace(models[i])
	}
	models = slices.DeleteFunc(models, func(s string) bool { return s == "" })
	key := os.Getenv("ANTHROPIC_API_KEY")
	searchKey := os.Getenv("BRAVE_SEARCH_API_KEY")
	configured := key != "" && len(models) > 0
	var provider model.Model
	if configured {
		provider = &model.Anthropic{APIKey: key}
	}
	if *demo {
		models = []string{"demo"}
		provider = model.Demo{}
		root := filepath.Join(dataDir, "fixtures")
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Cocogem public demo fixture\n\nStable identity is separate from display names and model settings.\nWorkflows have finite dependencies; read-only tools capture sources.\nThis example is synthetic and does not establish real-provider quality.\n"), 0600); err != nil {
			return err
		}
		roots = []string{root}
	}
	if len(models) == 0 {
		models = []string{"not-configured"}
	}
	db, err := store.Open(filepath.Join(dataDir, "state.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine := &workflow.Engine{Store: db, AllowedRoots: roots, AllowedModels: models, Model: provider, Web: tools.NewWeb(searchKey, *demo), Demo: *demo}
	server, err := httpapi.New(httpapi.Config{Host: *listen, WebDir: *webDir, Models: models, Roots: roots, Demo: *demo, ModelConfigured: configured, SearchConfigured: searchKey != "", Model: provider}, db, engine)
	if err != nil {
		return err
	}
	if err := engine.Start(ctx); err != nil {
		return err
	}
	defer func() { stop(); engine.Wait() }()
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	fmt.Printf("Cocogem: http://%s/#bootstrap=%s\n", *listen, server.BootstrapToken())
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
