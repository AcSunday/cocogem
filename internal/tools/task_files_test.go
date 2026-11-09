package tools

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestTaskCommandHelper(t *testing.T) {
	if os.Getenv("COCOGEM_TASK_COMMAND_HELPER") != "1" {
		return
	}
	if err := os.WriteFile("result.unlisted", []byte{0, 1, 2, 3, 255}, 0600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestTaskCommandAndArbitraryFileSnapshot(t *testing.T) {
	t.Setenv("COCOGEM_TASK_COMMAND_HELPER", "1")
	root := t.TempDir()
	var captured []w.Source
	scope := Scope{Root: root, WorkDir: root, Capture: func(s w.Source) (w.Source, error) { s.ID = "captured"; captured = append(captured, s); return s, nil }}
	raw, _ := json.Marshal(map[string]any{"argv": []string{os.Args[0], "-test.run=^TestTaskCommandHelper$"}})
	if _, err := ExecuteCommand(t.Context(), raw, scope); err == nil {
		t.Fatal("controlled execution ran a command")
	}
	scope.ExecutionMode = w.ExecutionFullAccess
	result, err := ExecuteCommand(t.Context(), raw, scope)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(result), &receipt); err != nil || receipt.ExitCode != 0 {
		t.Fatalf("command receipt=%s, %v", result, err)
	}
	source, file, err := TaskFile(t.Context(), root, "result.unlisted")
	if err != nil || file.Size != 5 || source.OriginalDigest == "" {
		t.Fatalf("file snapshot=%+v, %v", file, err)
	}
	if err := os.WriteFile(filepath.Join(root, "result.unlisted"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if source.OriginalContent != "AAECA/8=" {
		t.Fatal("captured bytes changed with the output file")
	}
	if _, _, err := TaskFile(t.Context(), root, "../outside"); err == nil {
		t.Fatal("published a path outside the task")
	}
	if err := os.Symlink(filepath.Join(root, "result.unlisted"), filepath.Join(root, "link")); err == nil {
		if _, _, err := TaskFile(t.Context(), root, "link"); err == nil {
			t.Fatal("published a symlink")
		}
	}
	if len(captured) != 1 || captured[0].Kind != "command" {
		t.Fatal("missing command observation")
	}
}

func TestImageContentCarriesPixels(t *testing.T) {
	pixels := image.NewRGBA(image.Rect(0, 0, 13, 7))
	pixels.Set(5, 3, color.RGBA{255, 0, 0, 255})
	var data bytes.Buffer
	if err := png.Encode(&data, pixels); err != nil {
		t.Fatal(err)
	}
	encoded, err := ImageContent(data.Bytes(), "image-source")
	if err != nil {
		t.Fatal(err)
	}
	var result ImageObservation
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	if result.Width != 13 || result.Height != 7 || result.SourceID != "image-source" || result.Data == "" {
		t.Fatalf("image result=%+v", result)
	}
	if _, err := ImageContent([]byte("not pixels"), "bad"); err == nil {
		t.Fatal("accepted corrupt image")
	}
}
