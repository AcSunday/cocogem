package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func pagingServer(t *testing.T, limits workflow.Limits) (*Server, *store.Store, *http.Cookie, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	engine := &workflow.Engine{Store: db, Limits: limits}
	server, err := New(Config{Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, ModelConfigured: true, Model: testutil.Model{}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	response := authCall(server, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, "")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	var session SessionReply
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return server, db, cookie, session.CSRFToken
}

func apiCall(server *Server, method, path string, value any, cookie *http.Cookie, csrf string, headers map[string]string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(value)
	request := httptest.NewRequest(method, "http://local.test"+path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func seedThread(t *testing.T, db *store.Store) (w.Thread, w.Run) {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "paging", Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "paging", Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: thread.ID, Status: "succeeded", CreatedAt: w.Now()}
	err := db.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		return tx.InsertRun(run, w.NewID(), "hash")
	})
	if err != nil {
		t.Fatal(err)
	}
	return thread, run
}

func seedRun(t *testing.T, db *store.Store, thread w.Thread) w.Run {
	t.Helper()
	run := w.Run{ID: w.NewID(), WorkspaceID: thread.WorkspaceID, ThreadID: thread.ID, Status: "succeeded", CreatedAt: w.Now()}
	if err := db.Write(context.Background(), func(tx *store.Tx) error {
		return tx.InsertRun(run, w.NewID(), "hash")
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

func seedMessage(t *testing.T, db *store.Store, message w.Message) w.Message {
	t.Helper()
	if err := db.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveMessage(message) }); err != nil {
		t.Fatal(err)
	}
	return message
}

func seedChunkedMessage(t *testing.T, db *store.Store, thread w.Thread, run w.Run, chunks []string) w.Message {
	t.Helper()
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	preview := chunks[0]
	if len(preview) > w.MaxMessagePreviewBytes {
		preview = preview[:w.MaxMessagePreviewBytes]
	}
	message := w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Role: "assistant", Content: preview, ContentFormat: w.ContentFormatChunksV1, ContentBytes: total, ContentTruncated: total > len(preview), Status: "completed", CreatedAt: w.Now()}
	if err := db.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		for i, chunk := range chunks {
			if err := tx.AppendMessageChunk(message.ID, i+1, chunk); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return message
}

func threadPageCursor(threadID string, position int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"thread_id":%q,"position":%d}`, threadID, position)))
}

func TestMessageContentPagesChunkedAndInlineBodies(t *testing.T) {
	server, db, cookie, _ := pagingServer(t, workflow.DefaultLimits())
	thread, run := seedThread(t, db)

	chunks := make([]string, 10)
	for i := range chunks {
		chunks[i] = "第" + string(rune('一'+i)) + "块：中文证据。"
	}
	full := strings.Join(chunks, "")
	chunked := seedChunkedMessage(t, db, thread, run, chunks)

	rw := authCall(server, "GET", "/api/v1/runs/"+run.ID+"/messages/"+chunked.ID+"/content", nil, cookie, "")
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	validateResponse(t, "/api/v1/runs/{id}/messages/{messageID}/content", "get", 200, rw.Body.Bytes())
	var page MessageContentPage
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.MessageID != chunked.ID || page.ContentFormat != w.ContentFormatChunksV1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first page = %+v", page)
	}
	if page.Content != strings.Join(chunks[:8], "") || page.ContentBytes != len(full) || !page.ContentTruncated {
		t.Fatalf("first page body = %q", page.Content)
	}
	rw = authCall(server, "GET", "/api/v1/runs/"+run.ID+"/messages/"+chunked.ID+"/content?cursor="+page.NextCursor, nil, cookie, "")
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var rest MessageContentPage
	if err := json.Unmarshal(rw.Body.Bytes(), &rest); err != nil {
		t.Fatal(err)
	}
	if rest.HasMore || rest.NextCursor != "" || page.Content+rest.Content != full {
		t.Fatalf("rest page = %+v", rest)
	}

	inlineBody := strings.Repeat("中文证据", 10000)
	inline := seedMessage(t, db, w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Role: "user", Content: inlineBody, Status: "completed", CreatedAt: w.Now()})
	assembled := strings.Builder{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		path := "/api/v1/runs/" + run.ID + "/messages/" + inline.ID + "/content"
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		rw = authCall(server, "GET", path, nil, cookie, "")
		if rw.Code != 200 {
			t.Fatal(rw.Body.String())
		}
		var part MessageContentPage
		if err := json.Unmarshal(rw.Body.Bytes(), &part); err != nil {
			t.Fatal(err)
		}
		if part.ContentFormat != w.ContentFormatInline || !utf8.ValidString(part.Content) {
			t.Fatalf("inline page = %+v", part)
		}
		assembled.WriteString(part.Content)
		if !part.HasMore {
			if part.NextCursor != "" {
				t.Fatal("cursor returned without more content")
			}
			break
		}
		cursor = part.NextCursor
	}
	if assembled.String() != inlineBody {
		t.Fatalf("inline body was not reconstructed: %d of %d bytes", assembled.Len(), len(inlineBody))
	}
}

func TestMessageContentCarriesAuthorAndModelSnapshot(t *testing.T) {
	server, db, cookie, _ := pagingServer(t, workflow.DefaultLimits())
	thread, run := seedThread(t, db)
	message := seedMessage(t, db, w.Message{
		ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, StepID: "step-1",
		AgentID: "agent-1", AgentName: "研究成员", Model: "requested-model",
		ResolvedModel: "resolved-model", Role: "assistant",
		Content: strings.Repeat("正文", 5000), Status: "completed", CreatedAt: w.Now(),
	})
	rw := authCall(server, "GET", "/api/v1/runs/"+run.ID+"/messages/"+message.ID+"/content?limit=1", nil, cookie, "")
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	validateResponse(t, "/api/v1/runs/{id}/messages/{messageID}/content", "get", 200, rw.Body.Bytes())
	var page MessageContentPage
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Message.ID != message.ID || page.Message.AgentID != "agent-1" || page.Message.AgentName != "研究成员" ||
		page.Message.StepID != "step-1" || page.Message.Model != "requested-model" || page.Message.ResolvedModel != "resolved-model" {
		t.Fatalf("message snapshot = %+v", page.Message)
	}
	if page.Message.Content != "" {
		t.Fatalf("content endpoint duplicated %d body bytes", len(page.Message.Content))
	}
	if page.Content == "" || !page.HasMore {
		t.Fatalf("body page = %+v", page)
	}
}

func TestThreadTimelineBoundsLegacyInlineAndContentRestoresIt(t *testing.T) {
	server, db, cookie, _ := pagingServer(t, workflow.DefaultLimits())
	thread, run := seedThread(t, db)
	body := strings.Repeat("中文证据", 12000)
	message := seedMessage(t, db, w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Role: "assistant", Content: body, Status: "completed", CreatedAt: w.Now()})

	rw := authCall(server, "GET", "/api/v1/threads/"+thread.ID, nil, cookie, "")
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var view ThreadView
	if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Messages.Items) != 1 {
		t.Fatalf("timeline items = %d", len(view.Messages.Items))
	}
	preview := view.Messages.Items[0]
	if preview.ID != message.ID || preview.Format() != w.ContentFormatInline || preview.ContentBytes != len(body) || !preview.ContentTruncated {
		t.Fatalf("timeline preview metadata = %+v", preview)
	}
	if len(preview.Content) > w.MaxMessagePreviewBytes || !utf8.ValidString(preview.Content) || !strings.HasPrefix(body, preview.Content) {
		t.Fatalf("timeline preview is not a bounded UTF-8 prefix: %d bytes", len(preview.Content))
	}

	assembled := strings.Builder{}
	cursor := ""
	for pages := 0; pages < 64; pages++ {
		path := "/api/v1/runs/" + run.ID + "/messages/" + message.ID + "/content?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		rw = authCall(server, "GET", path, nil, cookie, "")
		if rw.Code != 200 {
			t.Fatal(rw.Body.String())
		}
		var part MessageContentPage
		if err := json.Unmarshal(rw.Body.Bytes(), &part); err != nil {
			t.Fatal(err)
		}
		if part.ContentFormat != w.ContentFormatInline || part.ContentBytes != len(body) || !utf8.ValidString(part.Content) {
			t.Fatalf("content page = %+v", part)
		}
		assembled.WriteString(part.Content)
		if !part.HasMore {
			if part.NextCursor != "" {
				t.Fatal("cursor returned without more content")
			}
			break
		}
		cursor = part.NextCursor
	}
	if assembled.String() != body {
		t.Fatalf("content endpoint restored %d of %d bytes", assembled.Len(), len(body))
	}
}

func TestMessageContentRejectsCrossRunAndInvalidCursor(t *testing.T) {
	server, db, cookie, _ := pagingServer(t, workflow.DefaultLimits())
	thread, run := seedThread(t, db)
	otherRun := seedRun(t, db, thread)

	a := seedChunkedMessage(t, db, thread, run, []string{"甲", "乙", "丙"})
	b := seedChunkedMessage(t, db, thread, otherRun, []string{"另一条", "消息"})
	otherThread, _ := seedThread(t, db)
	mismatched := seedChunkedMessage(t, db, otherThread, run, []string{"错线程"})

	get := func(path string) *httptest.ResponseRecorder {
		return authCall(server, "GET", path, nil, cookie, "")
	}
	for _, path := range []string{
		"/api/v1/runs/" + run.ID + "/messages/" + b.ID + "/content",
		"/api/v1/runs/" + run.ID + "/messages/" + mismatched.ID + "/content",
		"/api/v1/runs/" + run.ID + "/messages/" + w.NewID() + "/content",
		"/api/v1/runs/" + w.NewID() + "/messages/" + a.ID + "/content",
	} {
		if rw := get(path); rw.Code != 404 {
			t.Fatalf("%s = %d %s", path, rw.Code, rw.Body.String())
		}
	}
	if rw := authCall(server, "GET", "/api/v1/runs/"+run.ID+"/messages/"+a.ID+"/content", nil, nil, ""); rw.Code != 401 {
		t.Fatal("content was public", rw.Code)
	}

	rw := get("/api/v1/runs/" + run.ID + "/messages/" + a.ID + "/content?limit=1")
	var first MessageContentPage
	if err := json.Unmarshal(rw.Body.Bytes(), &first); err != nil || first.NextCursor == "" {
		t.Fatal(rw.Body.String())
	}
	rw = get("/api/v1/runs/" + otherRun.ID + "/messages/" + b.ID + "/content?cursor=" + first.NextCursor)
	if rw.Code != 400 || !strings.Contains(rw.Body.String(), "invalid_cursor") {
		t.Fatalf("foreign cursor = %d %s", rw.Code, rw.Body.String())
	}
	for _, limit := range []string{"0", "-1", "33", "abc"} {
		if rw := get("/api/v1/runs/" + run.ID + "/messages/" + a.ID + "/content?limit=" + limit); rw.Code != 400 {
			t.Fatalf("limit %s = %d", limit, rw.Code)
		}
	}
	if rw := get("/api/v1/runs/" + run.ID + "/messages/" + a.ID + "/content?limit=32"); rw.Code != 200 {
		t.Fatalf("maximum limit rejected: %d %s", rw.Code, rw.Body.String())
	}
}

func TestEventHistoryPagesPersistedEventsAsJSON(t *testing.T) {
	server, db, cookie, _ := pagingServer(t, workflow.DefaultLimits())
	_, run := seedThread(t, db)
	_, otherRun := seedThread(t, db)
	if err := db.Write(context.Background(), func(tx *store.Tx) error {
		for _, kind := range []string{"one", "two", "three", "four", "five"} {
			if err := tx.AppendEvent(run.ID, "", "", kind, map[string]int{"n": 1}); err != nil {
				return err
			}
		}
		return tx.AppendEvent(otherRun.ID, "", "", "foreign", map[string]int{"n": 1})
	}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		return authCall(server, "GET", path, nil, cookie, "")
	}
	rw := get("/api/v1/runs/" + run.ID + "/event-history")
	if rw.Code != 200 || rw.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("history = %d %s", rw.Code, rw.Header().Get("Content-Type"))
	}
	validateResponse(t, "/api/v1/runs/{id}/event-history", "get", 200, rw.Body.Bytes())
	var page EventPage
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 || page.NextCursor != "" {
		t.Fatalf("default page = %+v", page)
	}
	rw = get("/api/v1/runs/" + run.ID + "/event-history?limit=2")
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Seq != 1 || page.Items[1].Seq != 2 || page.NextCursor != "2" {
		t.Fatalf("first page = %+v", page)
	}
	rw = get("/api/v1/runs/" + run.ID + "/event-history?after=2&limit=2")
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Seq != 3 || page.NextCursor != "4" {
		t.Fatalf("second page = %+v", page)
	}
	rw = get("/api/v1/runs/" + run.ID + "/event-history?after=4")
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Type != "five" || page.NextCursor != "" {
		t.Fatalf("tail page = %+v", page)
	}
	rw = get("/api/v1/runs/" + otherRun.ID + "/event-history?after=1")
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("events crossed runs: %+v", page)
	}
	for _, query := range []string{"after=-1", "after=abc", "limit=0", "limit=501", "limit=abc"} {
		rw = get("/api/v1/runs/" + run.ID + "/event-history?" + query)
		if rw.Code != 400 || !strings.Contains(rw.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s = %d %s", query, rw.Code, rw.Body.String())
		}
	}
	if rw := get("/api/v1/runs/" + w.NewID() + "/event-history"); rw.Code != 404 {
		t.Fatalf("missing run = %d", rw.Code)
	}
}

func TestRunCursorSeedsRecentEventHistory(t *testing.T) {
	server, db, cookie, _ := pagingServer(t, workflow.DefaultLimits())
	_, run := seedThread(t, db)
	if err := db.Write(context.Background(), func(tx *store.Tx) error {
		for i := 1; i <= 600; i++ {
			if err := tx.AppendEvent(run.ID, "", "", fmt.Sprintf("kind-%03d", i), map[string]int{"n": i}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rw := authCall(server, "GET", "/api/v1/runs/"+run.ID, nil, cookie, "")
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var view RunView
	if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Cursor != 600 {
		t.Fatalf("run cursor = %d", view.Cursor)
	}
	rw = authCall(server, "GET", fmt.Sprintf("/api/v1/runs/%s/event-history?after=%d&limit=500", run.ID, view.Cursor-500), nil, cookie, "")
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var page EventPage
	if err := json.Unmarshal(rw.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 500 {
		t.Fatalf("recent history seed = %d items", len(page.Items))
	}
	if page.Items[0].Seq != 101 || page.Items[499].Seq != 600 || page.NextCursor != "" {
		t.Fatalf("recent history range = %d..%d next %q", page.Items[0].Seq, page.Items[499].Seq, page.NextCursor)
	}
}

func TestThreadViewPagesRecentMessagesAndOmitsSourceBodies(t *testing.T) {
	server, db, cookie, _ := pagingServer(t, workflow.DefaultLimits())
	thread, run := seedThread(t, db)
	contents := make([]string, 35)
	for i := range contents {
		contents[i] = fmt.Sprintf("message-%02d", i)
		seedMessage(t, db, w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Role: "assistant", Content: contents[i], Status: "completed", CreatedAt: w.Now()})
	}
	sourceBody := strings.Repeat("完整来源正文", 6000)
	source := w.Source{ID: w.NewID(), WorkspaceID: thread.WorkspaceID, ThreadID: thread.ID, RunID: run.ID, Kind: "web", Title: "来源", URL: "https://example.test/a", Content: sourceBody, CapturedAt: w.Now()}
	if err := db.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveSource(source) }); err != nil {
		t.Fatal(err)
	}

	get := func(path string) ThreadView {
		rw := authCall(server, "GET", path, nil, cookie, "")
		if rw.Code != 200 {
			t.Fatalf("%s = %d %s", path, rw.Code, rw.Body.String())
		}
		var view ThreadView
		if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	view := get("/api/v1/threads/" + thread.ID)
	if len(view.Messages.Items) != 30 || view.Messages.Items[0].Content != contents[5] || view.Messages.Items[29].Content != contents[34] {
		t.Fatalf("default page length=%d first=%q", len(view.Messages.Items), view.Messages.Items[0].Content)
	}
	if view.Messages.NextCursor == "" {
		t.Fatal("missing older cursor")
	}
	if len(view.Sources) != 1 || view.Sources[0].Content != "" || view.Sources[0].Title != source.Title || view.Sources[0].URL != source.URL {
		t.Fatalf("source metadata = %+v", view.Sources)
	}
	rw := authCall(server, "GET", "/api/v1/sources/"+source.ID, nil, cookie, "")
	var detail w.Source
	if err := json.Unmarshal(rw.Body.Bytes(), &detail); err != nil || detail.Content != sourceBody {
		t.Fatalf("source detail lost body: %d %s", rw.Code, rw.Body.String())
	}

	view = get("/api/v1/threads/" + thread.ID + "?limit=10")
	if len(view.Messages.Items) != 10 || view.Messages.Items[0].Content != contents[25] {
		t.Fatalf("limit page = %+v", view.Messages.Items)
	}
	firstCursor := view.Messages.NextCursor
	older := get("/api/v1/threads/" + thread.ID + "?limit=10&cursor=" + firstCursor)
	if len(older.Messages.Items) != 10 || older.Messages.Items[0].Content != contents[15] {
		t.Fatalf("older page = %+v", older.Messages.Items)
	}
	seedMessage(t, db, w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Role: "user", Content: "new-after-page", Status: "completed", CreatedAt: w.Now()})
	again := get("/api/v1/threads/" + thread.ID + "?limit=10&cursor=" + firstCursor)
	if len(again.Messages.Items) != 10 || again.Messages.Items[0].Content != contents[15] {
		t.Fatalf("cursor shifted after insert: %+v", again.Messages.Items)
	}
	for i := range older.Messages.Items {
		if older.Messages.Items[i].ID != again.Messages.Items[i].ID {
			t.Fatal("older page changed after a new message")
		}
	}

	otherThread, _ := seedThread(t, db)
	for name, cursor := range map[string]string{
		"foreign thread":   threadPageCursor(otherThread.ID, 10),
		"missing position": threadPageCursor(thread.ID, 999),
		"huge position":    threadPageCursor(thread.ID, 1<<62),
	} {
		rw = authCall(server, "GET", "/api/v1/threads/"+thread.ID+"?cursor="+cursor, nil, cookie, "")
		if rw.Code != 400 || !strings.Contains(rw.Body.String(), "invalid_cursor") {
			t.Fatalf("%s cursor = %d %s", name, rw.Code, rw.Body.String())
		}
	}
	for _, limit := range []string{"0", "101", "abc"} {
		if rw := authCall(server, "GET", "/api/v1/threads/"+thread.ID+"?limit="+limit, nil, cookie, ""); rw.Code != 400 {
			t.Fatalf("thread limit %s = %d", limit, rw.Code)
		}
	}
}

func TestSubmitFreezesExecutionPolicyAtEnqueue(t *testing.T) {
	limits := workflow.Limits{RunTimeout: 90 * time.Minute, StepTimeout: 0, FirstResponse: 0, Stall: 45 * time.Minute, StallAction: "stop"}
	server, db, cookie, csrf := pagingServer(t, limits)
	rw := apiCall(server, "POST", "/api/v1/workspaces", map[string]string{"name": "策略空间"}, cookie, csrf, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var ws w.Workspace
	json.Unmarshal(rw.Body.Bytes(), &ws)
	rw = apiCall(server, "GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, cookie, csrf, nil)
	var agents struct {
		Items []w.Agent `json:"items"`
	}
	json.Unmarshal(rw.Body.Bytes(), &agents)
	if len(agents.Items) < 1 {
		t.Fatal(rw.Body.String())
	}
	agent := agents.Items[0]
	th := w.Thread{Title: "策略", AgentIDs: []string{agent.ID}, CoordinatorID: agent.ID, Workflow: "free"}
	rw = apiCall(server, "POST", "/api/v1/workspaces/"+ws.ID+"/threads", th, cookie, csrf, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	json.Unmarshal(rw.Body.Bytes(), &th)

	in := w.RunInput{Message: "请用一句话总结。", Workflow: "free", Targets: []string{agent.ID}}
	rw = apiCall(server, "POST", "/api/v1/threads/"+th.ID+"/routing-preview", in, cookie, csrf, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var preview struct {
		ProposalID string `json:"proposal_id"`
	}
	json.Unmarshal(rw.Body.Bytes(), &preview)
	in.ProposalID = preview.ProposalID
	rw = apiCall(server, "POST", "/api/v1/threads/"+th.ID+"/runs", in, cookie, csrf, map[string]string{"Idempotency-Key": "freeze-policy-test-key"})
	if rw.Code != 202 {
		t.Fatal(rw.Body.String())
	}
	validateResponse(t, "/api/v1/threads/{id}/runs", "post", 202, rw.Body.Bytes())
	var run w.Run
	json.Unmarshal(rw.Body.Bytes(), &run)
	want := w.ExecutionPolicy{RunTimeout: 90 * time.Minute, StepTimeout: 0, FirstResponse: 0, Stall: 45 * time.Minute, StallAction: "stop"}
	if run.ExecutionPolicy == nil || *run.ExecutionPolicy != want {
		t.Fatalf("frozen policy = %+v, want %+v", run.ExecutionPolicy, want)
	}

	server.engine.Limits = workflow.Limits{RunTimeout: time.Minute, FirstResponse: time.Second, StallAction: "warn"}
	rw = apiCall(server, "GET", "/api/v1/runs/"+run.ID, nil, cookie, csrf, nil)
	if rw.Code != 200 {
		t.Fatal(rw.Body.String())
	}
	var view RunView
	json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Run.ExecutionPolicy == nil || *view.Run.ExecutionPolicy != want {
		t.Fatalf("persisted policy changed: %+v", view.Run.ExecutionPolicy)
	}
	var stored w.Run
	if err := db.Read(context.Background(), func(tx *store.Tx) error { var err error; stored, err = tx.Run(run.ID); return err }); err != nil {
		t.Fatal(err)
	}
	if stored.ExecutionPolicy == nil || *stored.ExecutionPolicy != want {
		t.Fatalf("store policy = %+v", stored.ExecutionPolicy)
	}

	rw = apiCall(server, "GET", "/api/v1/system", nil, cookie, csrf, nil)
	var info SystemInfo
	json.Unmarshal(rw.Body.Bytes(), &info)
	if _, exists := info.Limits["tool_calls"]; exists {
		t.Fatal("tool_calls limit is still advertised")
	}
	if info.Limits["run_seconds"] != 60 || info.Limits["step_seconds"] != 0 || info.Limits["first_response_seconds"] != 1 || info.Limits["stall_seconds"] != 0 {
		t.Fatalf("limits = %+v", info.Limits)
	}
	server.config.Limits = map[string]int{"tool_calls": 30, "handoffs": 3}
	rw = apiCall(server, "GET", "/api/v1/system", nil, cookie, csrf, nil)
	if err := json.Unmarshal(rw.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if _, exists := info.Limits["tool_calls"]; exists {
		t.Fatal("a legacy config overlay readvertised tool_calls")
	}
	if info.Limits["handoffs"] != 3 {
		t.Fatalf("config overlay ignored: %+v", info.Limits)
	}
}
