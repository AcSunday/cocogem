package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// This opt-in journey exercises the new default policy (project_autonomous) with
// one real installed CLI and the user's existing provider configuration. It never
// touches user files, deploys anything or writes to the network; every path is a
// temporary directory owned by the test.
//
// It proves: an in-project read runs autonomously, an out-of-project read stops
// before any effect and appears on the real permission endpoint, one allow_once
// decision lets exactly that operation continue, the run delivers real output
// bytes as an immutable authenticated download, and the root budget is unchanged.
func TestLivePermissionFlowDefaultAutonomous(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_PERMISSION_FLOW") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_PERMISSION_FLOW=1 for the real provider approval journey")
	}
	cli := os.Getenv("COCOGEM_LIVE_PERMISSION_CLI")
	if cli == "" {
		cli = "claude"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	area := t.TempDir()
	root := filepath.Join(area, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("INSIDE_PROJECT_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	const outsideContent = "OUTSIDE_APPROVED_PLAIN_BYTES"
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte(outsideContent), 0600); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(outside); err == nil {
		outside = resolved
	}
	const expectedResult = "APPROVED_FLOW_RESULT_BYTES_42"

	db, err := store.Open(filepath.Join(area, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := nativecli.Provider{}
	engine := &workflow.Engine{
		Store: db, Model: provider, Runners: map[string]agent.Runner{cli: provider}, Adapters: provider,
		AllowedRoots: []string{area}, AllowedModels: []string{"default"}, Web: tools.NewWeb(""),
		Executions:        execution.New(db, filepath.Join(area, "executions"), "live-permission"),
		NativeSessionRoot: filepath.Join(area, "sessions"),
	}
	ctx, cancelEngine := context.WithCancel(ctx)
	defer cancelEngine()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancelEngine(); engine.Wait() }()

	if err := os.WriteFile(filepath.Join(area, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	app, err := New(Config{
		Host: server.Listener.Addr().String(), AuthFile: filepath.Join(area, "auth.json"), WebDir: area,
		Models: []string{"default"}, Roots: []string{area}, ModelConfigured: true, Model: provider,
		Executors: []ExecutorInfo{{ID: cli, Installed: true, Supported: true}},
	}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = app.Handler()
	server.Start()
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	api := &deliveryAPI{t: t, server: server, client: client}
	var session SessionReply
	api.call("POST", "/auth/setup", PasswordInput{Password: "live-permission-password"}, nil, 200, &session)
	api.csrf = session.CSRFToken

	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "真实审批", Root: root}, nil, 200, &workspace)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+workspace.ID+"/agents", nil, nil, 200, &members)
	if len(members.Items) == 0 {
		t.Fatal("no workspace member")
	}
	member := members.Items[0]
	member.Executor, member.Model = cli, "default"
	api.call("PATCH", "/api/v1/agents/"+member.ID, member, map[string]string{"If-Match": fmt.Sprint(member.Revision)}, 200, &member)
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+workspace.ID+"/threads", w.Thread{Title: "真实审批会话", AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Workflow: "free"}, nil, 200, &thread)

	goal := fmt.Sprintf("按顺序完成：1) 读取项目内文件 %s；2) 读取项目外文件 %s（读取后确认它确实存在）；3) 把最终结果写入 outputs/result.txt，内容为 %q；4) 用 publish_file 交付 outputs/result.txt。不要编造内容，不要访问网络，不要执行部署或安装。", filepath.Join(root, "inside.txt"), outside, expectedResult)
	var created struct {
		Task w.Task `json:"task"`
	}
	api.call("POST", "/api/v1/workspaces/"+workspace.ID+"/tasks", map[string]any{
		"thread_id": thread.ID, "owner_member_id": member.ID, "goal": goal,
		"source_message_id": "live-permission-source", "accepted_by": "user",
	}, nil, 200, &created)
	if created.Task.ID == "" {
		t.Fatal("task was not created")
	}
	var budgetBefore w.BudgetAccount
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/budget", nil, nil, 200, &budgetBefore)

	var started w.Run
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/start", map[string]any{}, map[string]string{"Idempotency-Key": "live-permission-start-0001"}, 202, &started)
	if started.ID == "" {
		t.Fatal("task did not start")
	}

	decisions := 0
	unexpected := []string{}
	deadline := time.Now().Add(4 * time.Minute)
	var final w.Run
	for time.Now().Before(deadline) {
		var requests PermissionRequestList
		api.call("GET", "/api/v1/threads/"+thread.ID+"/permission-requests", nil, nil, 200, &requests)
		for _, request := range requests.Items {
			if request.Status != w.PermissionPending {
				continue
			}
			expected := request.ToolName == "read_file" && request.Action == "read" && request.NormalizedResourceScope == outside
			decision := w.DecisionDeny
			if expected {
				decision = w.DecisionAllowOnce
			} else {
				unexpected = append(unexpected, request.ToolName+" "+request.Action+" "+request.NormalizedResourceScope)
			}
			var decided w.ToolPermissionRequest
			api.call("POST", "/api/v1/permission-requests/"+request.ID+"/decisions",
				PermissionDecisionInput{Decision: decision, RequestID: "live-decision-" + request.ID[:8]},
				map[string]string{"If-Match": fmt.Sprint(request.Revision)}, 200, &decided)
			if expected {
				decisions++
				if decided.Status != w.PermissionAllowedOnce {
					t.Fatalf("allowed decision status = %s", decided.Status)
				}
			}
		}
		var view struct {
			Run w.Run `json:"run"`
		}
		api.call("GET", "/api/v1/runs/"+started.ID, nil, nil, 200, &view)
		final = view.Run
		if w.Terminal(final.Status) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !w.Terminal(final.Status) {
		t.Fatalf("live run did not settle within the bounded window: %+v", final)
	}
	if len(unexpected) > 0 {
		t.Fatalf("unexpected permission requests were denied: %v", unexpected)
	}
	if decisions != 1 {
		t.Fatalf("expected exactly one accepted decision, got %d (run status %s: %s)", decisions, final.Status, final.Error)
	}
	if final.Status != "succeeded" {
		t.Fatalf("live run = %+v", final)
	}
	if final.TaskID != created.Task.ID || final.TaskRevision != created.Task.CurrentRevision {
		t.Fatalf("live run changed the accepted objective: %+v", final)
	}
	// Real immutable bytes: the delivered file is downloadable with a matching hash.
	var deliveries struct {
		Artifacts []w.Artifact `json:"artifacts"`
	}
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/deliveries", nil, nil, 200, &deliveries)
	var fileArtifact w.Artifact
	var fileEntry w.ArtifactFile
	for _, artifact := range deliveries.Artifacts {
		for _, file := range artifact.Files {
			if strings.HasSuffix(file.Name, "result.txt") {
				fileArtifact, fileEntry = artifact, file
			}
		}
	}
	if fileEntry.SourceID == "" {
		t.Fatalf("no delivered result.txt artifact: %+v", deliveries.Artifacts)
	}
	body := downloadFile(t, client, server.URL, artifactFileURL(fileArtifact, fileEntry))
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != fileEntry.SHA256 {
		t.Fatalf("downloaded bytes do not match the recorded hash: %d bytes", len(body))
	}
	if !strings.Contains(string(body), expectedResult) {
		t.Fatalf("delivered file lacks the requested content: %q", string(body))
	}
	// The root budget is the same ledger.
	var budgetAfter w.BudgetAccount
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/budget", nil, nil, 200, &budgetAfter)
	if budgetAfter.ID != budgetBefore.ID {
		t.Fatalf("live flow reset the root budget: %+v -> %+v", budgetBefore, budgetAfter)
	}
	t.Logf("live permission flow: cli=%s decisions=%d bytes=%d sha256=%s", cli, decisions, len(body), fileEntry.SHA256)
}

func artifactFileURL(artifact w.Artifact, file w.ArtifactFile) string {
	return fmt.Sprintf("/api/v1/artifacts/%s/versions/%d/files/%s", artifact.ID, artifact.Version, file.SourceID)
}

// downloadFile fetches an authenticated immutable artifact and returns its bytes.
func downloadFile(t *testing.T, client *http.Client, base, path string) []byte {
	t.Helper()
	response, err := client.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("download status=%d body=%s", response.StatusCode, raw)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
