package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type fileTaskRunner struct{ t *testing.T }

func (runner fileTaskRunner) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	if slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "promote_to_task" }) {
		text := request.Messages[0].Content[0].Text
		goal := strings.SplitN(strings.SplitN(text, "Original user task:\n", 2)[1], "\n", 2)[0]
		raw, _ := json.Marshal(map[string]string{"title": "文件交付测试", "goal": goal, "evidence_quote": goal})
		_, err := execute(ctx, "promote_to_task", raw)
		return err
	}
	if !request.TrustedExecution || request.WorkingDirectory == "" {
		return fmt.Errorf("free file task did not receive trusted execution")
	}
	if !slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "run_command" }) {
		return fmt.Errorf("command tool missing")
	}
	if strings.Contains(request.Messages[0].Content[0].Text, "保留报告") {
		data, _ := json.Marshal(map[string]any{"type": "note", "title": "原报告", "body": "保留原报告正文", "findings": []w.Finding{}, "source_ids": []string{}, "limitations": []string{"保留这条限制"}})
		if _, err := execute(ctx, "publish_artifact", data); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(request.WorkingDirectory, "outputs", "report.bin"), []byte{4, 5, 6}, 0600); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "报告与文件一起交付。"})
	}
	if strings.Contains(request.Messages[0].Content[0].Text, "自动收集") {
		if err := os.WriteFile(filepath.Join(request.WorkingDirectory, "outputs", "collected.bin"), []byte{7, 8, 9}, 0600); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "文件已保存在输出目录。"})
	}
	if err := os.WriteFile(filepath.Join(request.WorkingDirectory, "output.custom"), []byte{0, 1, 2, 255}, 0600); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"paths": []string{"output.custom"}, "title": "真实文件快照", "body": "已生成并核对文件字节。"})
	if _, err := execute(ctx, "publish_file", raw); err != nil {
		return err
	}
	// The eventual download must use the immutable capture, never this live file.
	if err := os.WriteFile(filepath.Join(request.WorkingDirectory, "output.custom"), []byte("later changes"), 0600); err != nil {
		return err
	}
	return observe("text", map[string]any{"text": "文件已交付。"})
}

func TestFreeTaskPublishesImmutableDownloadWithoutEngineeringMode(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(area, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine := &workflow.Engine{Store: db, Model: testutil.Model{}, Fallback: fileTaskRunner{t}, AllowedRoots: []string{root}, AllowedModels: []string{"default"}, Executions: execution.New(db, filepath.Join(area, "executions"), "test")}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	api := newDeliveryAPI(t, area, db, engine)
	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "文件任务", Root: root}, nil, 200, &workspace)
	setGlobalMode(t, api, w.ExecutionFullAccess)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+workspace.ID+"/agents", nil, nil, 200, &members)
	member := members.Items[0]
	member.Executor = "claude"
	member.Tools = tools.AllNames
	api.call("PATCH", "/api/v1/agents/"+member.ID, member, map[string]string{"If-Match": fmt.Sprint(member.Revision)}, 200, &member)
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+workspace.ID+"/threads", w.Thread{Title: "文件", Workflow: "free", AgentIDs: []string{member.ID}, CoordinatorID: member.ID}, nil, 200, &thread)
	files := liveSubmitFileTask(t, api, thread, "交付处理后的文件。", "fixture")
	if len(files) != 1 || !bytes.Equal(files[0].data, []byte{0, 1, 2, 255}) {
		t.Fatalf("wrong immutable download: %+v", files)
	}
	var artifacts Page[w.Artifact]
	api.call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, &artifacts)
	a := artifacts.Items[0]
	url := api.server.URL + fmt.Sprintf("/api/v1/artifacts/%s/versions/%d/files/%s", a.ID, a.Version, a.Files[0].SourceID)
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("file download bypassed authentication")
	}
	collected := liveSubmitFileTask(t, api, thread, "自动收集：生成文件并保存到输出目录。", "auto-collect")
	if len(collected) != 1 || collected[0].name != "collected.bin" || !bytes.Equal(collected[0].data, []byte{7, 8, 9}) {
		t.Fatalf("automatic file delivery failed: %+v", collected)
	}
	// A successful report keeps its evidence and limitations when the collector appends files.
	combined := liveSubmitFileTask(t, api, thread, "保留报告：交付报告和文件。", "keep-report")
	if len(combined) != 1 || combined[0].name != "report.bin" {
		t.Fatalf("old outputs were republished: %+v", combined)
	}
	api.call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, &artifacts)
	last := artifacts.Items[len(artifacts.Items)-1]
	if last.Body != "保留原报告正文" || !slices.Equal(last.Limitations, []string{"保留这条限制"}) {
		t.Fatalf("collector discarded report content: %+v", last)
	}
}
