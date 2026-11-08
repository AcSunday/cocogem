package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

const MaxTaskFileBytes = 32 << 20

// ReadBinary snapshots a regular file; no extension decides whether bytes are allowed.
func ReadBinary(ctx context.Context, root, path string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, local, err := openPath(root, path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	before, err := r.Lstat(local)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("only regular files are supported")
	}
	f, err := r.OpenFile(local, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || linkCount(info) != 1 {
		return nil, errors.New("only regular files without hard links are supported")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds the %d MiB transfer limit", limit>>20)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file grew beyond the transfer limit")
	}
	return data, ctx.Err()
}

func FileMediaType(name string, data []byte) string {
	if media := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); media != "" {
		return media
	}
	return http.DetectContentType(data)
}

// TaskFile snapshots bytes independently of the live execution directory.
func TaskFile(ctx context.Context, root, path string) (w.Source, w.ArtifactFile, error) {
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(root, path)
		if err != nil {
			return w.Source{}, w.ArtifactFile{}, err
		}
	}
	data, err := ReadBinary(ctx, root, path, MaxTaskFileBytes)
	if err != nil {
		return w.Source{}, w.ArtifactFile{}, err
	}
	name := filepath.Base(path)
	media := FileMediaType(name, data)
	sha := sha256Hex(data)
	source := w.Source{Kind: "file_output", Title: name, Path: path, MediaType: media, SHA256: sha, OriginalDigest: sha, OriginalContent: base64.StdEncoding.EncodeToString(data), Content: fmt.Sprintf("Output file %s (%s), %d bytes, SHA-256 %s", name, media, len(data), sha), CapturedAt: w.Now(), ParseStatus: "available"}
	source.FileSize = len(data)
	if info, err := os.Lstat(filepath.Join(root, path)); err == nil {
		source.FileModifiedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	return source, w.ArtifactFile{Name: name, MediaType: media, Size: len(data), SHA256: sha}, nil
}

// OutputFingerprint keeps even unpublishable old files from blocking a later cleanup task.
func OutputFingerprint(ctx context.Context, root, path string) (string, error) {
	data, err := ReadBinary(ctx, root, path, MaxTaskFileBytes)
	if err == nil {
		return sha256Hex(data), nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	info, statErr := os.Lstat(filepath.Join(root, path))
	if statErr != nil {
		return "", statErr
	}
	return fmt.Sprintf("unavailable:%s:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano()), nil
}

// ImageObservation is converted to MCP image content, never sent as base64 prose.
type ImageObservation struct {
	SourceID  string `json:"source_id"`
	MediaType string `json:"image_media_type"`
	Data      string `json:"image_data"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

func ImageContent(data []byte, sourceID string) (string, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("image decoder unavailable for this format; use an installed converter through run_command")
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 20_000_000 {
		return "", errors.New("image exceeds the 20 megapixel decoding limit")
	}
	pixels, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("image data could not be decoded")
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		return "", err
	}
	if encoded.Len() > 8<<20 {
		return "", errors.New("image exceeds the 8 MiB pixel transfer limit; resize it before viewing")
	}
	out, err := json.Marshal(ImageObservation{SourceID: sourceID, MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(encoded.Bytes()), Width: config.Width, Height: config.Height})
	return string(out), err
}

func ViewImage(ctx context.Context, scope Scope, path string) (string, error) {
	for _, root := range []string{scope.WorkDir, scope.Root} {
		if root == "" {
			continue
		}
		local := path
		if filepath.IsAbs(path) {
			local, _ = filepath.Rel(root, path)
		}
		data, err := ReadBinary(ctx, root, local, 8<<20)
		if err != nil {
			continue
		}
		// Decode before capture so malformed bytes never become a successful visual read.
		if _, err := ImageContent(data, ""); err != nil {
			return "", err
		}
		source := w.Source{Kind: "image", Title: filepath.Base(local), Path: local, MediaType: FileMediaType(local, data), SHA256: sha256Hex(data), OriginalDigest: sha256Hex(data), OriginalContent: base64.StdEncoding.EncodeToString(data), Content: "Image pixels provided by view_image; visual interpretation is model-assessed.", CapturedAt: w.Now(), ParseStatus: "available"}
		source, err = scope.Capture(source)
		if err != nil {
			return "", err
		}
		return ImageContent(data, source.ID)
	}
	return "", errors.New("image is unavailable inside this task or its bound project")
}

type commandOutput struct {
	mu        sync.Mutex
	data      bytes.Buffer
	truncated bool
}

func (o *commandOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	left := (48 << 10) - o.data.Len()
	if len(data) > left {
		o.truncated = true
		_, _ = o.data.Write(data[:max(left, 0)])
	} else {
		_, _ = o.data.Write(data)
	}
	return len(data), nil
}

func ExecuteCommand(ctx context.Context, raw json.RawMessage, scope Scope) (string, error) {
	switch scope.ExecutionMode {
	case w.ExecutionFullAccess:
		// The user's explicit trust choice; unchanged behavior.
	case w.ExecutionProjectAutonomous:
		// A project-scoped command runs under an OS sandbox: writes are confined
		// to the project (plus a private temp dir) and secret paths stay
		// unreadable. An approved outside target widens the write set only to
		// that exact scope.
		return executeProjectCommand(ctx, raw, scope)
	default:
		// In a stricter mode the command runs only after its exact invocation was
		// approved. It still executes under the same OS sandbox, so the approval
		// cannot turn into an unconstrained shell.
		if !scope.ApprovedCommand {
			return "", errors.New("command execution requires an approved scope or project-autonomous mode")
		}
		if scope.WorkDir == "" {
			scope.WorkDir = scope.Root
		}
		return executeProjectCommand(ctx, raw, scope)
	}
	var in struct {
		Argv    []string `json:"argv"`
		CWD     string   `json:"cwd"`
		Timeout int      `json:"timeout_seconds"`
	}
	if err := Decode(raw, &in); err != nil {
		return "", err
	}
	if len(in.Argv) == 0 || len(in.Argv) > 128 || in.Argv[0] == "" {
		return "", errors.New("argv must contain an executable and at most 128 arguments")
	}
	for _, arg := range in.Argv {
		if strings.ContainsRune(arg, 0) {
			return "", errors.New("command arguments cannot contain NUL")
		}
	}
	if in.Timeout == 0 {
		in.Timeout = 120
	}
	if in.Timeout < 1 || in.Timeout > 600 {
		return "", errors.New("command timeout must be between 1 and 600 seconds")
	}
	r, local, err := openPath(scope.WorkDir, in.CWD)
	if err != nil {
		return "", err
	}
	r.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(in.Timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, in.Argv[0], in.Argv[1:]...)
	cmd.Dir = filepath.Join(scope.WorkDir, local)
	if err := configureTestProcess(cmd); err != nil {
		return "", err
	}
	cmd.Cancel = func() error { terminateTestGroup(cmd); return nil }
	output := &commandOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	started := w.Now()
	runErr := cmd.Run()
	cleanupErr := cleanupTestGroup(cmd)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	output.mu.Lock()
	text, truncated := output.data.String(), output.truncated
	output.mu.Unlock()
	record := struct {
		Argv        []string `json:"argv"`
		CWD         string   `json:"cwd"`
		Started     string   `json:"started_at"`
		Finished    string   `json:"finished_at"`
		ExitCode    int      `json:"exit_code"`
		TimedOut    bool     `json:"timed_out"`
		Output      string   `json:"output"`
		Truncated   bool     `json:"truncated"`
		Observation string   `json:"observation"`
	}{in.Argv, cmd.Dir, started, w.Now(), exitCode, ctx.Err() != nil, strings.ToValidUTF8(text, "�"), truncated, "host_observed_exit; external_effects=unknown; process_group_only"}
	if runErr != nil && exitCode == -1 {
		record.Output += "\n" + runErr.Error()
	}
	if cleanupErr != nil {
		record.Observation = "process_group_stop=uncertain; external_effects=unknown"
		record.Output += "\n" + cleanupErr.Error()
	}
	encoded, _ := json.Marshal(record)
	source, err := scope.Capture(w.Source{Kind: "command", Title: in.Argv[0], Content: string(encoded), ExitCode: exitCode, TimedOut: record.TimedOut, Truncated: truncated, CapturedAt: w.Now()})
	if err != nil {
		return "", err
	}
	result := map[string]any{"source_id": source.ID, "exit_code": exitCode, "timed_out": record.TimedOut, "cwd": cmd.Dir, "output": record.Output, "truncated": truncated, "external_effects": "unknown"}
	encoded, err = json.Marshal(result)
	return string(encoded), err
}
