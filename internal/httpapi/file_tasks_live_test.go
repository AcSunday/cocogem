package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
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

// This opt-in journey uses the user's configured CLI/provider and real files.
func TestLiveGeneralFileTasks(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_FILE_TASKS") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_FILE_TASKS=1 for real provider file acceptance")
	}
	clis := strings.Split(os.Getenv("COCOGEM_FILE_TASK_CLIS"), ",")
	if clis[0] == "" {
		clis = []string{"claude"}
	}
	for _, cli := range clis {
		t.Run(cli, func(t *testing.T) { liveFileTask(t, cli) })
	}
}

func liveFileTask(t *testing.T, cli string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Minute)
	defer cancel()
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
	provider := nativecli.Provider{}
	engine := &workflow.Engine{Store: db, Model: provider, Runners: map[string]agent.Runner{cli: provider}, Adapters: provider, AllowedRoots: []string{root}, AllowedModels: []string{"default"}, Web: tools.NewWeb(""), Executions: execution.New(db, filepath.Join(area, "executions"), "live-files"), NativeSessionRoot: filepath.Join(area, "sessions")}
	coordinatorCLI := os.Getenv("COCOGEM_FILE_TASK_COORDINATOR")
	if coordinatorCLI != "" {
		engine.Runners[coordinatorCLI] = provider
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	api := newDeliveryAPI(t, area, db, engine)
	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "真实文件任务", Root: root}, nil, 200, &workspace)
	setGlobalMode(t, api, w.ExecutionFullAccess)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+workspace.ID+"/agents", nil, nil, 200, &members)
	author := members.Items[0]
	author.Executor = cli
	author.Model = "default"
	author.Tools = tools.AllNames
	api.call("PATCH", "/api/v1/agents/"+author.ID, author, map[string]string{"If-Match": fmt.Sprint(author.Revision)}, 200, &author)
	ids := []string{author.ID}
	coordinatorID := author.ID
	if coordinatorCLI != "" {
		coordinator := members.Items[1]
		coordinator.Executor = coordinatorCLI
		coordinator.Tools = tools.AllNames
		api.call("PATCH", "/api/v1/agents/"+coordinator.ID, coordinator, map[string]string{"If-Match": fmt.Sprint(coordinator.Revision)}, 200, &coordinator)
		ids = append(ids, coordinator.ID)
		coordinatorID = coordinator.ID
	}
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+workspace.ID+"/threads", w.Thread{Title: "文件交付验收", AgentIDs: ids, CoordinatorID: coordinatorID, Workflow: "free"}, nil, 200, &thread)
	paper := os.Getenv("COCOGEM_FILE_TASK_PAPER")
	if paper != "" {
		data, err := os.ReadFile(paper)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "research"), 0700); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(paper)
		if err := os.WriteFile(filepath.Join(root, "research", name), data, 0400); err != nil {
			t.Fatal(err)
		}
		prompt := "research目录下有一份论文：" + name + "。这是一篇未完成的论文，让布偶读完论文内容并针对存在问题进行修改，最后交付给我改完的论文。"
		files := liveSubmitFileTask(t, api, thread, prompt, "paper")
		if coordinatorCLI != "" {
			executed := false
			if err := db.Read(ctx, func(tx *store.Tx) error {
				runs, err := tx.Runs(thread.ID)
				if err != nil {
					return err
				}
				for _, run := range runs {
					steps, err := tx.Steps(run.ID)
					if err != nil {
						return err
					}
					for _, step := range steps {
						if step.AgentID == author.ID && step.Status == "succeeded" {
							executed = true
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !executed {
				t.Fatal("the requested author did not execute the document work")
			}
			t.Logf("coordinator %s handed the document task to the requested %s member", coordinatorCLI, cli)
		}
		if len(files) == 0 {
			t.Fatal("no document file delivered")
		}
		for _, file := range files {
			if strings.EqualFold(filepath.Ext(file.name), ".docx") {
				if bytes.Equal(file.data, data) {
					t.Fatal("document was delivered unchanged")
				}
				archive, err := zip.NewReader(bytes.NewReader(file.data), int64(len(file.data)))
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, part := range archive.File {
					if part.Name == "word/document.xml" {
						found = true
					}
				}
				if !found {
					t.Fatal("output is not a Word document")
				}
				if output := os.Getenv("COCOGEM_FILE_TASK_OUTPUT"); output != "" {
					if err := os.MkdirAll(output, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(output, file.name), file.data, 0600); err != nil {
						t.Fatal(err)
					}
					t.Logf("verified downloaded document: %s", filepath.Join(output, file.name))
				}
			}
		}
	}
	// Non-text visual facts cannot be inferred from the filename or metadata.
	pixels := image.NewRGBA(image.Rect(0, 0, 180, 120))
	for y := range 120 {
		for x := range 180 {
			c := color.RGBA{240, 240, 240, 255}
			if x >= 20 && x < 70 && y >= 30 && y < 90 {
				c = color.RGBA{235, 20, 20, 255}
			}
			if (x-125)*(x-125)+(y-60)*(y-60) < 25*25 {
				c = color.RGBA{20, 40, 235, 255}
			}
			pixels.Set(x, y, c)
		}
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, pixels); err != nil {
		t.Fatal(err)
	}
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", "visual-check.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(imageData.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, "POST", api.server.URL+"/api/v1/threads/"+thread.ID+"/sources", &form)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", api.csrf)
	response, err := api.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded w.SourceImport
	err = json.NewDecoder(response.Body).Decode(&uploaded)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusCreated {
		t.Fatalf("upload=%d: %v", response.StatusCode, err)
	}
	files := liveSubmitFileTask(t, api, thread, "查看已上传的 visual-check.png，实际读取图像像素，判断两个彩色图形各是什么形状、颜色和左右关系。把判断写入 visual-result.txt 并交付这个文件。文件名和尺寸不能作为图像内容证据。", "image", uploaded.ID)
	if len(files) == 0 {
		t.Fatal("no image-analysis output delivered")
	}
	text := ""
	for _, file := range files {
		text += strings.ToLower(string(file.data))
	}
	if !(strings.Contains(text, "红") || strings.Contains(text, "red")) || !(strings.Contains(text, "蓝") || strings.Contains(text, "blue")) || !(strings.Contains(text, "圆") || strings.Contains(text, "circle")) {
		t.Fatalf("model did not identify image facts: %s", text)
	}
	t.Logf("real %s image recognition and file download passed", cli)
}

type downloadedFile struct {
	name string
	data []byte
}

func liveSubmitFileTask(t *testing.T, api *deliveryAPI, thread w.Thread, prompt, key string, sourceIDs ...string) []downloadedFile {
	t.Helper()
	in := w.RunInput{Message: prompt, Workflow: "free", Targets: []string{thread.CoordinatorID}, ResearchScope: "auto"}
	in.SourceIDs = sourceIDs
	var preview RoutingPreview
	api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", in, nil, 200, &preview)
	if preview.ProposalID == "" {
		t.Fatalf("routing did not accept file task: %+v", preview)
	}
	in.ProposalID = preview.ProposalID
	var run w.Run
	api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", in, map[string]string{"Idempotency-Key": "live-file-acceptance-" + key}, 202, &run)
	deadline := time.NewTimer(30 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("real file task timed out")
		case <-ticker.C:
		}
		var view RunView
		api.call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
		if !w.Terminal(view.Run.Status) {
			continue
		}
		if view.Run.Status != "succeeded" {
			var current ThreadView
			api.call("GET", "/api/v1/threads/"+thread.ID, nil, nil, 200, &current)
			for _, message := range current.Messages.Items {
				if message.RunID == run.ID && message.Role != "user" {
					t.Logf("failed task response: %.3500s", message.Content)
				}
			}
			t.Fatalf("real task status=%s error=%s", view.Run.Status, view.Run.Error)
		}
		var artifacts Page[w.Artifact]
		api.call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, &artifacts)
		out := []downloadedFile{}
		for _, artifact := range artifacts.Items {
			if artifact.RunID != run.ID {
				continue
			}
			for _, file := range artifact.Files {
				response, err := api.client.Get(api.server.URL + fmt.Sprintf("/api/v1/artifacts/%s/versions/%d/files/%s", artifact.ID, artifact.Version, file.SourceID))
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("download status=%d err=%v", response.StatusCode, err)
				}
				if len(data) != file.Size || digestBytes(data) != file.SHA256 {
					t.Fatal("download bytes do not match deliverable")
				}
				out = append(out, downloadedFile{file.Name, data})
			}
		}
		if len(out) == 0 {
			var threadView ThreadView
			api.call("GET", "/api/v1/threads/"+thread.ID, nil, nil, 200, &threadView)
			for _, message := range threadView.Messages.Items {
				if message.RunID == run.ID && message.Role != "user" {
					t.Logf("file task final response: %.2500s", message.Content)
				}
			}
			body, _ := json.Marshal(view.Run.Delivery)
			t.Fatalf("task succeeded without a real file: %s", body)
		}
		return out
	}
}
