package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

type previewWeb struct{}

func (previewWeb) SearchEnabled() bool { return true }
func (previewWeb) Fetch(context.Context, string) (w.Source, error) {
	return w.Source{Kind: "web", Title: "Fetched", URL: "https://example.org/page", Content: strings.Repeat("page text ", 1200), CapturedAt: w.Now()}, nil
}
func (previewWeb) Search(context.Context, string) ([]w.Source, error) {
	out := []w.Source{}
	for i := 0; i < 10; i++ {
		out = append(out, w.Source{Kind: "search_snippet", Title: fmt.Sprintf("Result %d", i), URL: "https://example.org/" + strings.Repeat("u", 3500), Content: strings.Repeat("搜", 3000), CapturedAt: w.Now()})
	}
	return out, nil
}

func previewScope(root string, web WebClient) Scope {
	return Scope{Root: root, Web: web, Capture: func(source w.Source) (w.Source, error) {
		if source.ID == "" {
			source.ID = w.NewID()
		}
		return source, nil
	}}
}

func TestWebSearchAggregateStaysLegalAndUnderWireBudget(t *testing.T) {
	result, err := Execute(context.Background(), "web_search", json.RawMessage(`{"query":"bounded"}`), previewScope("", previewWeb{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) > 65536 {
		t.Fatalf("aggregate observation exceeded 64 KiB: %d", len(result))
	}
	var value struct {
		Sources   []map[string]any `json:"sources"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(result), &value); err != nil {
		t.Fatalf("aggregate observation is not legal JSON: %v", err)
	}
	if len(value.Sources) != 10 || !value.Truncated {
		t.Fatalf("search structure was not preserved: %+v", value)
	}
	for _, source := range value.Sources {
		if source["source_id"] == "" || source["source_id"] == nil {
			t.Fatalf("source identity missing: %+v", source)
		}
		content, _ := source["content"].(string)
		if len(content) > w.MaxTextPageBytes {
			t.Fatalf("per-source preview unbounded: %d", len(content))
		}
		if source["has_more"] == nil {
			t.Fatalf("has_more missing: %+v", source)
		}
	}
}

func TestFetchAndReadFileReturnPreviewEnvelope(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := "line one\n" + strings.Repeat("内容", 5000) + "\nline last\n"
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		raw   string
		field string
	}{
		{"read_file", `{"path":"big.txt"}`, "id"},
		{"fetch_url", `{"url":"https://example.org/page"}`, "id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Execute(context.Background(), tc.name, json.RawMessage(tc.raw), previewScope(root, previewWeb{}))
			if err != nil {
				t.Fatal(err)
			}
			if len(result) > 65536 {
				t.Fatalf("preview envelope exceeded 64 KiB: %d", len(result))
			}
			var value map[string]any
			if err := json.Unmarshal([]byte(result), &value); err != nil {
				t.Fatalf("preview is not legal JSON: %v", err)
			}
			if value["source_id"] == nil || value["source_id"] == "" || value["id"] == nil || value["id"] == "" {
				t.Fatalf("capture identity missing: %+v", value)
			}
			if value["source_truncated"] == nil || value["preview_truncated"] != true || value["has_more"] == nil {
				t.Fatalf("preview/capture truncation not distinguished: %+v", value)
			}
			if value["next_cursor"] == nil {
				t.Fatalf("large preview has no cursor: %+v", value)
			}
			content, _ := value["content"].(string)
			if content == "" || len(content) > w.MaxTextPageBytes {
				t.Fatalf("bounded content missing: %d", len(content))
			}
		})
	}
}
