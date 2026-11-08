package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// collectOutputFiles captures the designated output directory when a native
// agent finishes with prose instead of calling the explicit delivery tool.
// collectsAutonomousOutputs reports whether a project-autonomous step may have
// produced real files in its task directory. The file tools are already bounded
// to that directory by the host and the CLI sandbox.
func (x *stepRuntime) collectsAutonomousOutputs() bool {
	if x.run.EffectiveExecutionMode != w.ExecutionProjectAutonomous || x.run.TaskDirectory == "" {
		return false
	}
	return slices.Contains(x.allowed, "publish_file") || slices.Contains(x.allowed, "write_file")
}

func (x *stepRuntime) collectOutputFiles(ctx context.Context) error {
	if !slices.Contains(x.allowed, "publish_file") {
		return nil
	}
	allowed, err := x.engine.mayPublish(ctx, x.run, x.step)
	if err != nil {
		return err
	}
	if !allowed || x.run.TaskDirectory == "" {
		return nil
	}
	x.mu.Lock()
	published := x.published
	delegated := x.delegated
	x.mu.Unlock()
	if delegated {
		return nil
	}
	if published != nil && len(published.Files) > 0 {
		return nil
	}
	dir := filepath.Join(x.run.TaskDirectory, "outputs")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	paths := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if before := x.run.OutputBaseline[entry.Name()]; before != "" {
			digest, err := tools.OutputFingerprint(ctx, x.run.TaskDirectory, filepath.Join("outputs", entry.Name()))
			if err != nil {
				return err
			}
			if before == digest {
				continue
			}
		}
		paths = append(paths, filepath.Join("outputs", entry.Name()))
	}
	if len(paths) == 0 {
		return nil
	}
	title, body := "任务文件", "已生成可下载的任务文件。"
	if published != nil {
		title, body = published.Title, published.Body
	}
	raw, _ := json.Marshal(map[string]any{"paths": paths, "title": title, "body": body})
	_, err = x.publishFiles(ctx, raw)
	return err
}

func (x *stepRuntime) viewImage(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Path     string `json:"path"`
		SourceID string `json:"source_id"`
	}
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	if (in.Path == "") == (in.SourceID == "") {
		return "", errors.New("provide exactly one image path or source_id")
	}
	if in.Path != "" && x.run.TaskDirectory != "" {
		local := in.Path
		if filepath.IsAbs(local) {
			local, _ = filepath.Rel(x.run.TaskDirectory, local)
		}
		if strings.HasPrefix(filepath.ToSlash(filepath.Clean(local)), "inputs/") {
			x.mu.Lock()
			snapshot := slices.Clone(x.sources)
			x.mu.Unlock()
			for _, source := range snapshot {
				if filepath.Base(local) == source.ID+"-"+filepath.Base(source.Title) {
					in.SourceID = source.ID
					break
				}
			}
			if in.SourceID == "" {
				return "", errors.New("input image is not in this attempt's authorized source scope")
			}
		}
	}
	if in.SourceID == "" {
		workDir := ""
		if x.run.EffectiveExecutionMode == w.ExecutionFullAccess && slices.Contains(x.allowed, "run_command") {
			workDir = x.run.TaskDirectory
		}
		return tools.ViewImage(ctx, tools.Scope{Root: x.run.ProjectRoot(), WorkDir: workDir, Capture: x.capture}, in.Path)
	}
	x.mu.Lock()
	snapshot := slices.Clone(x.sources)
	x.mu.Unlock()
	read, _ := json.Marshal(map[string]any{"source_id": in.SourceID})
	if _, err := x.engine.readSourceForAttempt(ctx, x.run, snapshot, read, x.attemptSnapshot().ID); err != nil {
		return "", err
	}
	var source w.Source
	if err := x.engine.Store.Read(ctx, func(tx *store.Tx) error { var err error; source, err = tx.Source(in.SourceID); return err }); err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(source.OriginalContent)
	if err != nil {
		return "", err
	}
	return tools.ImageContent(data, source.ID)
}

func (x *stepRuntime) publishFiles(ctx context.Context, raw json.RawMessage) (string, error) {
	if !slices.Contains(x.allowed, "publish_file") {
		return "", errors.New("file publication is not granted for this step")
	}
	if err := x.engine.Store.Read(ctx, func(tx *store.Tx) error {
		if err := x.engine.validateMember(tx, x.run, x.step.AgentID); err != nil {
			return err
		}
		allowed, err := filterAuthorizedTools(tx, x.run, x.step.AgentID, []string{"publish_file"})
		if err != nil {
			return err
		}
		if x.run.GrantSnapshot != "" && len(allowed) == 0 {
			return errors.New("file publication authorization was revoked")
		}
		return nil
	}); err != nil {
		return "", err
	}
	allowed, err := x.engine.mayPublish(ctx, x.run, x.step)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", errors.New("leave exact file paths for the final publisher; this step is not the final delivery")
	}
	var in struct {
		Paths []string `json:"paths"`
		Title string   `json:"title"`
		Body  string   `json:"body"`
	}
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	if len(in.Paths) == 0 || len(in.Paths) > 10 || in.Title == "" || in.Body == "" {
		return "", errors.New("file delivery requires 1–10 output paths, title and explanation")
	}
	if x.run.TaskDirectory == "" {
		return "", errors.New("task output directory is unavailable")
	}
	files := []w.ArtifactFile{}
	refs := []string{}
	for _, path := range in.Paths {
		local := path
		if filepath.IsAbs(local) {
			local, _ = filepath.Rel(x.run.TaskDirectory, local)
		}
		if strings.HasPrefix(filepath.ToSlash(filepath.Clean(local)), "inputs/") || filepath.Clean(local) == "inputs" {
			return "", errors.New("input originals cannot be republished as output files; create a reviewed result in outputs/")
		}
		source, file, err := tools.TaskFile(ctx, x.run.TaskDirectory, path)
		if err != nil {
			return "", err
		}
		x.mu.Lock()
		known := slices.Clone(x.sources)
		x.mu.Unlock()
		modified, modifiedErr := time.Parse(time.RFC3339Nano, source.FileModifiedAt)
		started, startedErr := time.Parse(time.RFC3339Nano, x.run.CreatedAt)
		if modifiedErr == nil && startedErr == nil && modified.Before(started) {
			index := slices.IndexFunc(known, func(previous w.Source) bool {
				return previous.Kind == "file_output" && previous.SHA256 == file.SHA256 && previous.Title == file.Name
			})
			if index < 0 {
				return "", errors.New("existing file is outside this execution's output scope")
			}
			check, _ := json.Marshal(map[string]any{"source_id": known[index].ID})
			if _, err := x.engine.readSourceForAttempt(ctx, x.run, known, check, x.attemptSnapshot().ID); err != nil {
				return "", err
			}
		}
		attemptID := x.attemptSnapshot().ID
		if err := x.engine.Store.Read(ctx, func(tx *store.Tx) error {
			var err error
			source.Visibility, source.ExposureDigest, err = project.InheritAttemptDisclosure(tx, x.run, attemptID, known)
			return err
		}); err != nil {
			return "", err
		}
		source, err = x.capture(source)
		if err != nil {
			return "", err
		}
		file.SourceID = source.ID
		files = append(files, file)
		refs = append(refs, source.ID)
	}
	x.mu.Lock()
	previous := x.published
	x.mu.Unlock()
	typeName := x.required
	if typeName == "" {
		typeName = "note"
	}
	findings := []w.Finding{}
	limitations := []string{}
	if previous != nil {
		typeName = previous.Type
		findings = previous.Findings
		limitations = previous.Limitations
		refs = append(slices.Clone(previous.SourceIDs), refs...)
		files = append(slices.Clone(previous.Files), files...)
	}
	payload, _ := json.Marshal(map[string]any{"type": typeName, "title": in.Title, "body": in.Body, "findings": findings, "source_ids": refs, "limitations": limitations, "files": files})
	return x.acceptArtifact(ctx, payload)
}
