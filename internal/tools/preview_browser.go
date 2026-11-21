package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// failReasonAborted is the CDP reason code for an aborted request.
const failReasonAborted = "Aborted"

// RunPreview renders one host-derived page in a headless Chromium whose request
// interceptor admits only the exact probe origin plus data/blob assets. Every
// other request, including another loopback port or a public URL, is aborted
// before a socket exists.
//
// root is the bound project (never model input); in.Entry is a project-relative
// HTML entry or in.Argv is an exact preview command.
func RunPreview(ctx context.Context, root string, in PreviewInput) (PreviewResult, error) {
	started := time.Now()
	engine, unavailable := browserEngine()
	if engine == "" {
		return PreviewResult{EngineUnavailable: unavailable}, nil
	}
	mode := strings.TrimSpace(strings.ToLower(in.Mode))
	if mode == "" {
		mode = "static"
	}
	if mode != "static" && mode != "command" {
		return PreviewResult{}, errors.New("preview mode must be static or command")
	}
	width, height := in.Width, in.Height
	if width == 0 {
		width = previewWidthDefault
	}
	if height == 0 {
		height = previewHeightDefault
	}
	if width < 200 || height < 200 || width > previewMaxDimension || height > previewMaxDimension {
		return PreviewResult{}, errors.New("viewport must be between 200 and 2560 pixels")
	}
	timeout := previewTimeout
	if in.TimeoutSeconds != 0 {
		if in.TimeoutSeconds < 1 || in.TimeoutSeconds > 120 {
			return PreviewResult{}, errors.New("timeout_seconds must be between 1 and 120")
		}
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	var origin string
	var previewCmd *exec.Cmd
	switch mode {
	case "static":
		var closeOrigin func()
		var err error
		origin, closeOrigin, err = ServeStaticProject(ctx, root, in.Entry)
		if err != nil {
			return PreviewResult{}, err
		}
		defer closeOrigin()
	case "command":
		var closeCmd func()
		var err error
		origin, previewCmd, closeCmd, err = startPreviewCommand(ctx, root, in.Argv, !in.TrustedCommand)
		if err != nil {
			return PreviewResult{}, err
		}
		defer func() {
			stopPreviewCommand(previewCmd)
			if closeCmd != nil {
				closeCmd()
			}
		}()
	}
	// A cold Chrome start (process launch, target creation, domain enablement)
	// is not part of the page-render budget the caller asked for: a loaded CI
	// runner can take tens of seconds before the first page exists, and charging
	// that to the render timeout made an otherwise healthy probe fail with a
	// CDP deadline error instead of rendering.
	hardCtx, hardCancel := context.WithTimeout(ctx, timeout+previewStartupAllowance)
	defer hardCancel()
	profile, profileErr := os.MkdirTemp("", "cocogem-preview-profile-*")
	if profileErr != nil {
		return PreviewResult{}, errors.New("a private browser profile could not be created")
	}
	defer os.RemoveAll(profile)
	allocatorCtx, allocatorCancel := chromedp.NewExecAllocator(hardCtx,
		chromedp.ExecPath(engine),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Headless,
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.UserDataDir(profile),
		chromedp.Flag("disable-dev-shm-usage", true),
		// No DNS resolution for arbitrary hosts: even a mis-scoped request
		// cannot reach the network. The probe's own loopback literal stays
		// resolvable, and the CDP interceptor still decides every request.
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1"),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-extensions", true),
	)

	defer allocatorCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx)
	defer browserCancel()

	// Force the lazy target setup here, inside the startup allowance, so the
	// render budget below starts counting only when a page target exists. A
	// failure here is reported as a start failure, not as a render timeout.
	if err := chromedp.Run(browserCtx); err != nil {
		return PreviewResult{}, fmt.Errorf("the browser could not start: %w", err)
	}
	probeCtx, probeCancel := context.WithTimeout(browserCtx, timeout)
	defer probeCancel()

	probeOrigin := PreviewOrigin(origin)
	wsGuardScript := fmt.Sprintf(`(() => {
		const allowedOrigin = %q;
		const allowedWS = allowedOrigin.replace(/^http:/, 'ws:').replace(/^https:/, 'wss:');
		const NativeWS = window.WebSocket;
		if (typeof NativeWS === 'function') {
			window.WebSocket = function(url, protocols) {
				const resolved = new URL(String(url), window.location.href).href;
				if (!(resolved === allowedWS || resolved.startsWith(allowedWS + '/'))) {
					throw new DOMException('Blocked non-same-origin WebSocket in preview_page: ' + resolved, 'SecurityError');
				}
				return protocols === undefined ? new NativeWS(url) : new NativeWS(url, protocols);
			};
			window.WebSocket.prototype = NativeWS.prototype;
		}
	})();`, probeOrigin)
	// Enable request interception, runtime error capture, auto-attach for child
	// targets (popups/OOPIFs/workers) and a same-origin WebSocket guard before
	// any page script runs.
	if err := chromedp.Run(probeCtx,
		cdpruntime.Enable(),
		target.SetAutoAttach(true, false).WithFlatten(true),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(wsGuardScript).Do(ctx)
			return err
		}),
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{
			URLPattern: "*", RequestStage: fetch.RequestStageRequest,
		}}),
	); err != nil {
		return PreviewResult{}, fmt.Errorf("the browser request interceptor could not start: %w", err)
	}
	var errMu sync.Mutex
	var consoleErrors []string
	var pageErrors []string
	chromedp.ListenTarget(browserCtx, func(ev interface{}) {
		switch e := ev.(type) {
		case *fetch.EventRequestPaused:
			if e.Request == nil {
				return
			}
			// The handler must not call Run on the same target while the navigating
			// action is in flight; chromedp's own interception example dispatches
			// the continue/fail command from a separate goroutine.
			if !previewAllowedURL(probeOrigin, e.Request.URL) {
				go func() {
					_ = chromedp.Run(browserCtx, fetch.FailRequest(e.RequestID, failReasonAborted))
				}()
				return
			}
			sendCtx, sendCancel := context.WithTimeout(context.WithoutCancel(browserCtx), 10*time.Second)
			go func() {
				defer sendCancel()
				_ = chromedp.Run(sendCtx, fetch.ContinueRequest(e.RequestID).WithURL(e.Request.URL).WithMethod(e.Request.Method))
			}()
		case *cdpruntime.EventConsoleAPICalled:
			if e.Type == cdpruntime.APITypeError {
				parts := make([]string, 0, len(e.Args))
				for _, arg := range e.Args {
					if arg.Description != "" {
						parts = append(parts, arg.Description)
					} else if len(arg.Value) > 0 {
						parts = append(parts, string(arg.Value))
					}
				}
				if msg := strings.TrimSpace(strings.Join(parts, " ")); msg != "" {
					errMu.Lock()
					if len(consoleErrors) < 20 {
						consoleErrors = append(consoleErrors, boundedField(msg, 300))
					}
					errMu.Unlock()
				}
			}
		case *cdpruntime.EventExceptionThrown:
			if e.ExceptionDetails != nil {
				msg := e.ExceptionDetails.Text
				if e.ExceptionDetails.Exception != nil && e.ExceptionDetails.Exception.Description != "" {
					msg = e.ExceptionDetails.Exception.Description
				}
				if msg = strings.TrimSpace(msg); msg != "" {
					errMu.Lock()
					if len(pageErrors) < 20 {
						pageErrors = append(pageErrors, boundedField(msg, 300))
					}
					errMu.Unlock()
				}
			}
		}
	})

	result := PreviewResult{Origin: probeOrigin}
	var title string
	var screenshot []byte
	var axRaw []byte
	runErr := chromedp.Run(probeCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := chromedp.EmulateViewport(int64(width), int64(height)).Do(ctx); err != nil {
				return err
			}
			if err := chromedp.Navigate(probeOrigin + "/").Do(ctx); err != nil {
				return err
			}
			_ = chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
			var pageTitle string
			if err := chromedp.Title(&pageTitle).Do(ctx); err == nil {
				title = pageTitle
			}
			// The AX tree is supporting evidence; a page without one still has
			// a screenshot, so this never fails the whole probe.
			if nodes, err := accessibility.GetFullAXTree().Do(ctx); err == nil && len(nodes) > 0 {
				axRaw, _ = json.Marshal(nodes)
			}
			return nil
		}),
	)
	if runErr == nil {
		// The capture must run on the browser's chromedp context, never on the
		// outer request context: the latter carries no browser target.
		screenCtx, screenCancel := context.WithTimeout(browserCtx, 10*time.Second)
		screenshot, runErr = captureWholeScreenshot(screenCtx, browserCtx)
		screenCancel()
	}
	errMu.Lock()
	result.ConsoleErrors = append(result.ConsoleErrors, consoleErrors...)
	result.PageErrors = append(result.PageErrors, pageErrors...)
	errMu.Unlock()
	if runErr != nil || len(screenshot) == 0 {
		message := "浏览器渲染失败：" + boundedField(errorText(runErr), 300)
		result.ConsoleErrors = append(result.ConsoleErrors, message)
		return result, nil
	}
	result.Title = boundedField(title, 300)
	result.ScreenshotPNG = screenshot
	result.AXTree = previewAXTree(axRaw)
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}

// captureWholeScreenshot takes one bounded whole-surface PNG capture.
func captureWholeScreenshot(shotCtx, _ context.Context) ([]byte, error) {
	var data []byte
	err := chromedp.Run(shotCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		captured, err := page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithFromSurface(true).Do(ctx)
		if err == nil {
			data = captured
		}
		return err
	}))
	return data, err
}

// errorText keeps a nil error readable in the observation.
func errorText(err error) string {
	if err == nil {
		return "no body node was rendered"
	}
	return err.Error()
}

// startPreviewCommand runs one project preset preview command inside the bound
// project root on a random loopback port, wrapped in the host OS sandbox unless
// full_access is active, in its own process group.
func startPreviewCommand(ctx context.Context, root string, argv []string, sandboxed bool) (string, *exec.Cmd, func(), error) {
	if root == "" {
		return "", nil, nil, errors.New("no source directory is bound")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, nil, errors.New("source directory is unavailable")
	}
	if len(argv) == 0 || len(argv) > 16 || argv[0] == "" {
		return "", nil, nil, errors.New("preview command must provide an executable and at most 16 arguments")
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return "", nil, nil, errors.New("preview command arguments cannot contain NUL")
		}
	}
	port := freeLoopbackPort()
	if port == 0 {
		return "", nil, nil, errors.New("no local port is available for the preview command")
	}
	origin := "http://127.0.0.1:" + itoa(port)
	tempDir, err := os.MkdirTemp("", "cocogem-preview-cmd-*")
	if err != nil {
		return "", nil, nil, errors.New("could not create preview temporary directory")
	}
	if resolvedTemp, err := filepath.EvalSymlinks(tempDir); err == nil {
		tempDir = resolvedTemp
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	env := []string{
		"HOST=127.0.0.1", "PORT=" + itoa(port),
		"COCOGEM_PREVIEW_ORIGIN=" + origin,
		"PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + tempDir, "TMPDIR=" + tempDir, "LANG=C", "LC_ALL=C", "TZ=UTC",
		"NO_PROXY=127.0.0.1,localhost,::1", "no_proxy=127.0.0.1,localhost,::1",
	}
	execArgv := argv
	if sandboxed {
		spec := sandboxSpec{
			Root:          resolvedRoot,
			Temp:          tempDir,
			AllowLoopback: true,
			AllowTmp:      true,
			Toolchain:     toolchainSpec{Command: argv, Display: strings.Join(argv, " "), Env: env},
		}
		if worktreeGit, commonGit := WorktreeMeta(resolvedRoot); worktreeGit != "" {
			spec.ExtraWrites = append(spec.ExtraWrites, worktreeGit)
			if commonGit != "" {
				spec.ExtraReads = append(spec.ExtraReads, commonGit)
			}
			spec.DenyWrites = append(spec.DenyWrites, GitMetadataDenies(resolvedRoot)...)
		}
		plan, err := prepareCommandSandbox(ctx, spec)
		if err != nil {
			cleanup()
			return "", nil, nil, err
		}
		execArgv = append(append([]string{}, plan.Prefix...), argv...)
	}
	cmd := exec.Command(execArgv[0], execArgv[1:]...)
	cmd.Dir = resolvedRoot
	cmd.Env = env
	if err := configureTestProcess(cmd); err != nil {
		cleanup()
		return "", nil, nil, err
	}
	output := &boundedBuffer{limit: 8 << 10}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", nil, nil, errors.New("the preview command could not start")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			stopPreviewCommand(cmd)
			cleanup()
			return "", nil, nil, ctx.Err()
		}
		response, err := client.Get(origin + "/")
		if err == nil {
			response.Body.Close()
			return origin, cmd, cleanup, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopPreviewCommand(cmd)
	cleanup()
	return "", nil, nil, errors.New("the preview command did not answer on its assigned port: " + boundedField(strings.TrimSpace(output.String()), 400))
}

// freeLoopbackPort asks the kernel for an unused loopback port.
func freeLoopbackPort() int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return 0
	}
	port := addr.Port
	_ = listener.Close()
	return port
}
