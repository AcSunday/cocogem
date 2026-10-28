package nativecli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func runNativeFixture(t *testing.T, request model.Request, root string) (args, env, cwd string) {
	t.Helper()
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	cwdPath := filepath.Join(dir, "cwd.txt")
	envPath := filepath.Join(dir, "env.txt")
	script := filepath.Join(dir, "claude")
	body := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.312 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo "--restricted --tools --strict-mcp-config --setting-sources --permission-prompts --no-session-persistence --settings --disable-slash-commands"; exit 0; fi
printf '%s\n' "$@" > "$COCOGEM_CAPTURE_ARGS"
pwd > "$COCOGEM_CAPTURE_CWD"
env > "$COCOGEM_CAPTURE_ENV"
cat > /dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111","tools":[]}'
printf '%s\n' '{"type":"assistant","message":{"id":"msg1","content":[{"type":"text","text":"fixture"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"11111111-1111-4111-8111-111111111111"}'
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCOGEM_CAPTURE_ARGS", argsPath)
	t.Setenv("COCOGEM_CAPTURE_CWD", cwdPath)
	t.Setenv("COCOGEM_CAPTURE_ENV", envPath)
	if request.Executor == "" {
		request.Executor = "claude"
	}
	if request.Model == "" {
		request.Model = "default"
	}
	if request.System == "" {
		request.System = "fixture system prompt"
	}
	if len(request.Messages) == 0 {
		request.Messages = []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "fixture"}}}}
	}
	request.WorkspaceRoot = root
	options := runOptions{
		skipCheck: true,
		adapter:   agent.AdapterInfo{ID: "claude", Version: "2.1.312", Supported: true, Adapter: "claude-stream-json", Isolation: "enforced"},
		claudeSettings: map[string]any{
			"env": map[string]string{"ANTHROPIC_API_KEY": "public-fixture"},
		},
	}
	p := Provider{command: func(_ string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		return `{"ok":true}`, nil
	}, func(string, map[string]any) error { return nil }, options); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	return read(argsPath), read(envPath), strings.TrimSpace(read(cwdPath))
}

func capturedArg(raw, name string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if line == name && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}

func TestNativeRunSeparatesReadableRootFromSettings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("PUBLIC_FIXTURE"), 0600); err != nil {
		t.Fatal(err)
	}
	args, env, cwd := runNativeFixture(t, model.Request{NativeTools: []string{"Read", "WebSearch"}}, root)
	if capturedArg(args, "--tools") != "Read,WebSearch" || capturedArg(args, "--allowedTools") != "Read,WebSearch,mcp__cocogem__*" {
		t.Fatalf("native subset not forwarded: %s", args)
	}
	if strings.Contains(args, "--add-dir") {
		t.Fatalf("--add-dir would bypass deny matching: %s", args)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if cwd != resolvedRoot {
		t.Fatalf("native file cwd must be the workspace root for deny enforcement: %q want %q", cwd, resolvedRoot)
	}
	// The fake CLI runs from the test binary; the proxy child env is only set
	// for native web, so memory isolation is checked through the environment
	// captured by the fixture.
	if !strings.Contains(env, "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1") {
		t.Fatal("project memory was not disabled for a native file step")
	}
	if strings.Contains(env, "COCOGEM_MCP_TOKEN") {
		t.Fatal("settings environment leaked into the readable working directory")
	}
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, "CLAUDE_CONFIG_DIR=") {
			if !strings.Contains(line, filepath.Join("config")) {
				t.Fatalf("settings directory was not separated: %q", line)
			}
		}
	}
}

func TestNativeRunDropsFileToolsForUnsafeRoot(t *testing.T) {
	args, _, cwd := runNativeFixture(t, model.Request{NativeTools: []string{"Read"}, NativeProxy: "http://127.0.0.1:7897"}, "/")
	if strings.Contains(args, "--add-dir") || capturedArg(args, "--tools") == "Read" {
		t.Fatalf("native file tools were exposed for the filesystem root: %s", args)
	}
	if cwd == "/" {
		t.Fatal("unsafe root was used as the native cwd")
	}
	if capturedArg(args, "--allowedTools") != "mcp__cocogem__*" {
		t.Fatalf("MCP fallback was removed with the unsafe native file grant: %s", args)
	}
}

func TestNativeFileToolsDropWhenSettingsShareTheAliasedRootLocation(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The isolated settings directory is created under TMPDIR. Pointing the
	// workspace root at an alias of that same physical location must not expose
	// it through native Read/Glob/Grep.
	t.Setenv("TMPDIR", base)
	root := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(base, root); err != nil {
		t.Fatal(err)
	}
	args, _, _ := runNativeFixture(t, model.Request{NativeTools: []string{"Read", "Glob", "Grep"}}, root)
	if capturedArg(args, "--tools") != "" {
		t.Fatalf("native file tools remained enabled for a root containing the settings directory: %s", args)
	}
	if capturedArg(args, "--allowedTools") != "mcp__cocogem__*" {
		t.Fatalf("MCP fallback was removed with the degraded native file tools: %s", args)
	}
}

func TestNativeWebReceivesResolvedProxyWithoutLosingLoopbackBypass(t *testing.T) {
	t.Setenv("NO_PROXY", "internal.example")
	t.Setenv("no_proxy", "lowercase.example")
	args, env, _ := runNativeFixture(t, model.Request{NativeTools: []string{"WebSearch", "WebFetch"}, NativeProxy: "http://127.0.0.1:7897"}, "")
	if strings.Contains(args, "--add-dir") {
		t.Fatalf("web-only step added a file root: %s", args)
	}
	for _, want := range []string{"HTTPS_PROXY=http://127.0.0.1:7897", "http_proxy=http://127.0.0.1:7897"} {
		if !strings.Contains(env, want) {
			t.Fatalf("native proxy missing %q", want)
		}
	}
	noProxy := map[string]string{}
	for _, line := range strings.Split(env, "\n") {
		for _, key := range []string{"NO_PROXY=", "no_proxy="} {
			if strings.HasPrefix(line, key) {
				noProxy[key] = line
			}
		}
	}
	// Both existing spellings must survive and both child variables must carry
	// the merged bypass list; overwriting only one would force a direct model
	// endpoint through the proxy.
	for _, key := range []string{"NO_PROXY=", "no_proxy="} {
		for _, want := range []string{"127.0.0.1", "localhost", "internal.example", "lowercase.example"} {
			if !strings.Contains(noProxy[key], want) {
				t.Fatalf("%s %q missing %q", key, noProxy[key], want)
			}
		}
	}
	if strings.Contains(env, "PROXY=http://127.0.0.1:7897\n") && strings.Contains(env, "COCOGEM_CAPTURE") {
		// The proxy is child-only; nothing above should persist in the test process.
	}
	if os.Getenv("HTTPS_PROXY") == "http://127.0.0.1:7897" {
		t.Fatal("native proxy was written to the host environment")
	}
}

// Runtime files for every provider live under the attempt's .cocogem
// directory. A workspace root such as "/" contains that temporary directory,
// so both boundaries must reject the component: Claude's native deny rules and
// the host MCP tools.DeniedPath check.
func TestNativeRuntimeConfigurationIsDeniedToNativeAndMCPFileTools(t *testing.T) {
	const marker = "COCOGEM_FAKE_PRIVATE_CONNECTION"
	scripts := map[string]string{
		"claude": `import sys,json,os,urllib.request
args=sys.argv[1:]
config=json.load(open(args[args.index('--mcp-config')+1]))['mcpServers']['cocogem']
settings=args[args.index('--settings')+1]
headers=dict(config['headers']);headers['Content-Type']='application/json'
def rpc(method,params):
 req=urllib.request.Request(config['url'],data=json.dumps({'jsonrpc':'2.0','id':1,'method':method,'params':params}).encode(),headers=headers)
 with urllib.request.urlopen(req,timeout=10) as r:return json.load(r)
session='00000000-0000-4000-8000-000000000001'
print(json.dumps({'type':'system','subtype':'init','session_id':session,'tools':['mcp__cocogem__read_file']}),flush=True)
rpc('tools/list',{})
for path in (args[args.index('--mcp-config')+1],settings):
 rpc('tools/call',{'name':'read_file','arguments':{'path':os.path.realpath(path).lstrip('/')}})
print(json.dumps({'type':'result','subtype':'success','session_id':session,'is_error':False,'result':'fixture done'}),flush=True)
`,
		"codex": `import sys,json,os,urllib.request
args=sys.argv[1:]
url=None
for value in args:
 if value.startswith('mcp_servers.cocogem.url='): url=json.loads(value.split('=',1)[1])
token=os.environ['COCOGEM_MCP_TOKEN']
path=os.path.join(os.environ['CODEX_HOME'],'models.json')
headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'}
def rpc(method,params):
 req=urllib.request.Request(url,data=json.dumps({'jsonrpc':'2.0','id':1,'method':method,'params':params}).encode(),headers=headers)
 with urllib.request.urlopen(req,timeout=10) as r:return json.load(r)
session='00000000-0000-4000-8000-000000000001'
print(json.dumps({'type':'thread.started','thread_id':session}),flush=True)
print(json.dumps({'type':'turn.started'}),flush=True)
rpc('tools/list',{})
rpc('tools/call',{'name':'read_file','arguments':{'path':os.path.realpath(path).lstrip('/')}})
print(json.dumps({'type':'turn.completed','usage':{}}),flush=True)
`,
		"gemini": `import sys,json,os,urllib.request
path=os.path.join(os.getcwd(),'.gemini','settings.json')
config=next(iter(json.load(open(path))['mcpServers'].values()))
headers=dict(config['headers']);headers['Content-Type']='application/json'
def rpc(method,params):
 req=urllib.request.Request(config['url'],data=json.dumps({'jsonrpc':'2.0','id':1,'method':method,'params':params}).encode(),headers=headers)
 with urllib.request.urlopen(req,timeout=10) as r:return json.load(r)
session='00000000-0000-4000-8000-000000000001'
print(json.dumps({'type':'init','session_id':session}),flush=True)
rpc('tools/call',{'name':'read_file','arguments':{'path':os.path.realpath(path).lstrip('/')}})
print(json.dumps({'type':'result','status':'success','stats':{}}),flush=True)
`,
	}
	for _, executor := range []string{"claude", "codex", "gemini"} {
		t.Run(executor, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "cli.py")
			if err := os.WriteFile(script, []byte(scripts[executor]), 0600); err != nil {
				t.Fatal(err)
			}
			request := testRequest()
			request.Executor = executor
			request.WorkspaceRoot = "/"
			request.NativeTools = []string{"Read"}
			attempts, leaked := 0, false
			execute := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
				out, err := tools.Execute(ctx, name, input, tools.Scope{Root: "/", Capture: func(s w.Source) (w.Source, error) { return s, nil }})
				if name == "read_file" {
					attempts++
					if err == nil || strings.Contains(out, marker) || strings.Contains(out, "Authorization") {
						leaked = true
					}
				}
				return out, err
			}
			options := runOptions{
				skipCheck:      true,
				adapter:        agent.AdapterInfo{ID: executor, Supported: true},
				claudeSettings: map[string]any{"env": map[string]string{"ANTHROPIC_AUTH_TOKEN": marker}},
				model:          marker,
				codexProbe:     executor == "codex",
			}
			p := Provider{command: func(_ string, args ...string) *exec.Cmd {
				return exec.Command("python3", append([]string{script}, args...)...)
			}}
			if err := p.run(t.Context(), request, execute, func(string, map[string]any) error { return nil }, options); err != nil {
				t.Fatal(err)
			}
			if attempts == 0 {
				t.Fatal("the MCP file fallback was never exercised")
			}
			if leaked {
				t.Fatal("the MCP file fallback exposed runtime configuration when the workspace root contained the temporary directory")
			}
		})
	}
}
