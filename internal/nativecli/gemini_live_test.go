package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestLiveGeminiReadOnlyDocumentComparison(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_GEMINI") != "1" {
		t.Skip("opt-in real Gemini document comparison using synthetic inputs")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	modelName := os.Getenv("COCOGEM_GEMINI_MODEL")
	if modelName == "" {
		modelName = "default"
	}
	request := model.Request{Executor: "gemini", Model: modelName, System: "Read old.txt and new.txt with the supplied read_file tool, then concisely compare their changes in Chinese. Do not run commands, write files or call other tools.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "新文档对比原文档有什么变化？只比较 old.txt 和 new.txt。"}}}}, Tools: []model.Tool{{Name: "read_file", Description: "Read one of the synthetic documents", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "enum": []string{"old.txt", "new.txt"}}}, "required": []string{"path"}, "additionalProperties": false}}}, RequiredTools: []string{"read_file"}}
	read := map[string]bool{}
	var reply strings.Builder
	err := (Provider{}).Run(ctx, request, func(_ context.Context, name string, raw json.RawMessage) (string, error) {
		var in struct {
			Path string `json:"path"`
		}
		if name != "read_file" || json.Unmarshal(raw, &in) != nil {
			return "", errors.New("unexpected tool")
		}
		read[in.Path] = true
		switch in.Path {
		case "old.txt":
			return "审批时限为 30 天。提交纸质申请。", nil
		case "new.txt":
			return "审批时限为 45 天。支持线上申请。", nil
		default:
			return "", errors.New("unknown synthetic document")
		}
	}, func(kind string, data map[string]any) error {
		if kind == "text" {
			text, _ := data["text"].(string)
			reply.WriteString(text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !read["old.txt"] || !read["new.txt"] || !strings.Contains(reply.String(), "30") || !strings.Contains(reply.String(), "45") {
		t.Fatalf("comparison was not verified: old=%v new=%v reply_bytes=%d", read["old.txt"], read["new.txt"], reply.Len())
	}
}
