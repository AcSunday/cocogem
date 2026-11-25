package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestInstalledGeminiManagedResumeRetainsHistory(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_GEMINI") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_GEMINI=1 to exercise native session persistence")
	}
	const firstReply = "INTRO_FIRST_ROUND"
	const followUp = "INTRO_CONTINUATION"
	const finalReply = "INTRO_HISTORY_CONFIRMED"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "countTokens") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"totalTokens":1}`)
			return
		}
		var body struct {
			Contents []struct {
				Role  string
				Parts []struct{ Text string }
			}
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		sawFirstReply, sawFollowUp := false, false
		for _, message := range body.Contents {
			for _, part := range message.Parts {
				sawFirstReply = sawFirstReply || message.Role == "model" && part.Text == firstReply
				sawFollowUp = sawFollowUp || message.Role == "user" && strings.Contains(part.Text, followUp)
			}
		}
		reply := firstReply
		if sawFollowUp && sawFirstReply {
			reply = finalReply
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":%q}]},\"finishReason\":\"STOP\"}]}\n\n", reply)
	}))
	defer server.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("GEMINI_API_KEY", "fixture-key")
	t.Setenv("GOOGLE_API_KEY", "fixture-key")
	t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)
	t.Setenv("GEMINI_CLI_AUTH_OVERRIDE", "gemini-api-key")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "false")
	t.Setenv("GOOGLE_GENAI_USE_GCA", "false")

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p := Provider{}
	request := testRequest()
	request.Executor, request.Model = "gemini", "gemini-2.5-pro"
	request.ExecutionMode, request.TrustedExecution = "full_access", true
	request.SessionStorageRoot = t.TempDir()
	request.RequiredTools = []string{"read_file"}
	info, err := p.InspectExecutionConnection(ctx, "gemini", request.Model, "")
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedConnection = model.Connection{Identity: info.Identity, ResolvedModel: info.ResolvedModel}
	for _, turn := range []struct{ mode, input, want string }{
		{"fresh", "你们分别介绍下自己", firstReply},
		{"resume", followUp, finalReply},
	} {
		request.SessionMode = turn.mode
		request.Messages[0].Content[0].Text = turn.input
		var id, mode string
		var reply strings.Builder
		err := p.Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
			return "", errors.New("unexpected tool call")
		}, func(kind string, data map[string]any) error {
			switch kind {
			case "native_session":
				id, _ = data["session_id"].(string)
				mode, _ = data["mode"].(string)
			case "text":
				reply.WriteString(data["text"].(string))
			}
			return nil
		})
		if err != nil || id == "" || mode != turn.mode || turn.mode == "resume" && id != request.SessionID || strings.TrimSpace(reply.String()) != turn.want {
			t.Fatalf("%s turn: id=%q mode=%q reply=%q err=%v", turn.mode, id, mode, reply.String(), err)
		}
		request.SessionID = id
	}
}
