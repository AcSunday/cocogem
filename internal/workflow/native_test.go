package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestNativeSourceOnlyComesFromActualSuccessfulOutput(t *testing.T) {
	run := w.Run{ThreadID: "thread", Workspace: w.Workspace{Root: "/workspace"}}
	input := json.RawMessage(`{"file_path":"/workspace/pkg/main.go"}`)
	if _, ok := nativeSource(run, map[string]any{"name": "Read", "status": "error", "output": "denied", "input": input, "origin": "native"}); ok {
		t.Fatal("a failed native result became evidence")
	}
	if _, ok := nativeSource(run, map[string]any{"name": "Read", "status": "ok", "output": "", "input": input, "origin": "native"}); ok {
		t.Fatal("an empty native result became evidence")
	}
	source, ok := nativeSource(run, map[string]any{"name": "Read", "status": "ok", "output": "1\tpackage main", "input": input, "origin": "native"})
	if !ok || source.Kind != "native_file" || source.Path != "pkg/main.go" || source.Content != "1\tpackage main" {
		t.Fatalf("native file source = %+v ok=%v", source, ok)
	}
	web, ok := nativeSource(run, map[string]any{"name": "WebFetch", "status": "ok", "output": "summary", "input": json.RawMessage(`{"url":"https://example.org/a"}`), "origin": "native"})
	if !ok || web.Kind != "native_web" || web.URL != "https://example.org/a" {
		t.Fatalf("native web source = %+v ok=%v", web, ok)
	}
	search, ok := nativeSource(run, map[string]any{"name": "WebSearch", "status": "ok", "output": `[{"url":"https://example.org"}]`, "input": json.RawMessage(`{"query":"fixture"}`), "origin": "native"})
	if !ok || search.Kind != "native_search" || !strings.Contains(search.Title, "fixture") {
		t.Fatalf("native search source = %+v ok=%v", search, ok)
	}
	if _, ok := nativeSource(run, map[string]any{"name": "Bash", "status": "ok", "output": "x"}); ok {
		t.Fatal("an unknown native tool name became evidence")
	}
	if _, ok := nativeSource(run, map[string]any{"name": "WebFetch", "status": "ok", "output": " \n\t", "input": json.RawMessage(`{"url":"https://example.org/blank"}`), "origin": "native"}); ok {
		t.Fatal("a blank web page became evidence")
	}
	if _, ok := nativeSource(run, map[string]any{"name": "Read", "status": "ok", "output": " \n\t", "input": input, "origin": "native"}); ok {
		t.Fatal("blank output without file metadata became evidence")
	}
}

func TestNativeFileMetadataCannotBypassEventBound(t *testing.T) {
	meta, err := json.Marshal(map[string]any{"filePath": "evidence.txt", "content": strings.Repeat("x", 128<<10), "numLines": 1, "startLine": 1, "totalLines": 1})
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"name": "Read", "origin": "native", "output": "visible text", "file": json.RawMessage(meta)}
	out := nativeEventPayload(data)
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 65536 {
		t.Fatalf("file.content bypassed the event size boundary: %d bytes", len(encoded))
	}
	file, ok := out["file"].(map[string]any)
	if !ok || file["filePath"] != "evidence.txt" || file["totalLines"] != 1 {
		t.Fatalf("file range metadata was lost: %+v", out["file"])
	}
	if _, ok := data["file"].(json.RawMessage); !ok {
		t.Fatal("event projection mutated the capture input")
	}
}

func TestNativeReadUsesReturnedFileBodyWithoutHostReread(t *testing.T) {
	run := w.Run{ThreadID: "thread", Workspace: w.Workspace{Root: "/workspace"}}
	body := strings.Repeat("actual returned line\n", 1000)
	source, ok := nativeSource(run, map[string]any{
		"name": "Read", "status": "ok", "origin": "native",
		"input":  json.RawMessage(`{"file_path":"/workspace/evidence.txt"}`),
		"output": "1\tactual returned line",
		"file":   map[string]any{"filePath": "/workspace/evidence.txt", "content": body, "numLines": 1000, "startLine": 1, "totalLines": 1000},
	})
	if !ok || source.Content != body {
		t.Fatalf("CLI-returned file body was not preserved: bytes=%d ok=%v", len(source.Content), ok)
	}
	if source.StartLine != 1 || source.EndLine != 1000 || source.Truncated {
		t.Fatalf("complete returned range was mislabeled: %+v", source)
	}
	// A present content field is the actual returned body even when the display
	// copy is longer; completeness is never guessed from string length.
	shorter, ok := nativeSource(run, map[string]any{
		"name": "Read", "status": "ok", "origin": "native",
		"input":  json.RawMessage(`{"file_path":"/workspace/evidence.txt"}`),
		"output": "1\tactual\n2\tdisplay-only tail",
		"file":   map[string]any{"filePath": "/workspace/evidence.txt", "content": "actual", "numLines": 1, "startLine": 1, "totalLines": 1},
	})
	if !ok || shorter.Content != "actual" {
		t.Fatalf("longer display copy overrode the actual returned content: %q ok=%v", shorter.Content, ok)
	}
}

func TestNativeReadKeepsCLIFileRangeAndTruncation(t *testing.T) {
	run := w.Run{ThreadID: "thread", Workspace: w.Workspace{Root: "/workspace"}}
	input := json.RawMessage(`{"file_path":"/workspace/big.md"}`)
	read := func(file any, output string) map[string]any {
		data := map[string]any{"name": "Read", "status": "ok", "output": output, "input": input, "origin": "native"}
		if file != nil {
			data["file"] = file
		}
		return data
	}
	partial, ok := nativeSource(run, read(map[string]any{"filePath": "/workspace/big.md", "numLines": 10, "startLine": 1, "totalLines": 3000}, "1\tfirst"))
	if !ok || partial.StartLine != 1 || partial.EndLine != 10 || !partial.Truncated {
		t.Fatalf("partial CLI read was not bounded by its own range: %+v ok=%v", partial, ok)
	}
	full, ok := nativeSource(run, read(map[string]any{"filePath": "/workspace/big.md", "numLines": 10, "startLine": 1, "totalLines": 10}, "1\tfirst"))
	if !ok || full.StartLine != 1 || full.EndLine != 10 || full.Truncated {
		t.Fatalf("complete CLI read was not preserved: %+v ok=%v", full, ok)
	}
	tail, ok := nativeSource(run, read(map[string]any{"filePath": "/workspace/big.md", "numLines": 10, "startLine": 2991, "totalLines": 3000}, "2991\tlast"))
	if !ok || tail.StartLine != 2991 || tail.EndLine != 3000 || !tail.Truncated {
		t.Fatalf("tail segment was treated as the whole file: %+v ok=%v", tail, ok)
	}
	unknown, ok := nativeSource(run, read(nil, "1\tfirst"))
	if !ok || unknown.StartLine != 0 || unknown.EndLine != 0 || !unknown.Truncated {
		t.Fatalf("read without CLI metadata claimed completeness: %+v ok=%v", unknown, ok)
	}
	empty, ok := nativeSource(run, read(map[string]any{"filePath": "/workspace/big.md", "numLines": 0, "startLine": 0, "totalLines": 0}, ""))
	if !ok || empty.Content != "" || empty.Truncated || empty.Path != "big.md" {
		t.Fatalf("known empty file was confused with a blank page: %+v ok=%v", empty, ok)
	}
}

func TestNativeEvidenceCannotMasqueradeAsOriginal(t *testing.T) {
	artifact := func(kind string, sourceIDs ...string) w.Artifact {
		return w.Artifact{Type: kind, Title: "title", Body: "body", SourceIDs: sourceIDs,
			Findings: []w.Finding{{Title: "f", Detail: "d", Status: "supported", Severity: "high", SourceIDs: sourceIDs}}}
	}
	sources := []w.Source{
		{ID: "native-search", Kind: "native_search"},
		{ID: "native-web", Kind: "native_web"},
		{ID: "native-file", Kind: "native_file"},
		{ID: "web", Kind: "web", URL: "https://example.org"},
	}
	if err := ValidateArtifact(artifact("research_report", "native-search"), "research_report", sources); err == nil {
		t.Fatal("native search lead supported a research finding")
	}
	if err := ValidateArtifact(artifact("research_report", "native-web"), "research_report", sources); err != nil {
		t.Fatalf("native web observation was not accepted as bounded evidence: %v", err)
	}
	if err := ValidateArtifact(artifact("research_report", "native-file"), "research_report", sources); err != nil {
		t.Fatalf("native file capture was not accepted: %v", err)
	}
	code := artifact("code_analysis", "native-file")
	code.Findings[0].Status = "confirmed"
	if err := ValidateArtifact(code, "code_analysis", sources); err != nil {
		t.Fatalf("native file was not accepted for code evidence: %v", err)
	}
	code.SourceIDs = []string{"native-web"}
	code.Findings[0].SourceIDs = []string{"native-web"}
	if err := ValidateArtifact(code, "code_analysis", sources); err == nil {
		t.Fatal("native web observation counted as code file evidence")
	}
}

func TestNativeWebDisclosureIsAddedToArtifacts(t *testing.T) {
	artifact := w.Artifact{Title: "report", SourceIDs: []string{"w"}, Limitations: []string{"scope", "原生工具曾失败一次"}}
	ensureNativeObservationDisclosure(&artifact, []w.Source{{ID: "w", Kind: "native_web"}})
	if len(artifact.Limitations) != 3 || artifact.Limitations[2] != nativeWebLimitation {
		t.Fatalf("native web limitation missing or suppressed by an unrelated word: %v", artifact.Limitations)
	}
	ensureNativeObservationDisclosure(&artifact, []w.Source{{ID: "w", Kind: "native_web"}})
	if len(artifact.Limitations) != 3 {
		t.Fatal("native web limitation was duplicated")
	}
}

func TestLegacyWebSearchPageRemainsALead(t *testing.T) {
	sources := []w.Source{
		{ID: "legacy-serp", ThreadID: "thread", Kind: "web", URL: "https://www.bing.com/search?q=debank"},
		{ID: "legacy-google", ThreadID: "thread", Kind: "web", URL: "https://www.google.com/search?q=debank"},
		{ID: "article", ThreadID: "thread", Kind: "web", URL: "https://docs.example/debank"},
	}
	out, err := (&Engine{}).listSources(t.Context(), w.Run{ThreadID: "thread"}, sources, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Sources []struct {
			SourceID string `json:"source_id"`
			Strength string `json:"evidence_strength"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	strength := map[string]string{}
	for _, item := range result.Sources {
		strength[item.SourceID] = item.Strength
	}
	if strength["legacy-serp"] != "lead" || strength["legacy-google"] != "lead" || strength["article"] != "original" {
		t.Fatalf("legacy search result pages were not classified as leads: %s", out)
	}
}

func TestListSourcesPaginatesMetadataWithoutBodies(t *testing.T) {
	run := w.Run{ThreadID: "thread"}
	sources := make([]w.Source, 0, 45)
	for i := 0; i < 45; i++ {
		kind := "native_search"
		if i%3 == 0 {
			kind = "native_web"
		}
		sources = append(sources, w.Source{ID: fmt.Sprintf("source-%02d", i), ThreadID: "thread", Kind: kind, Title: "title", Content: "SECRET_CAPTURE_BODY", CapturedAt: w.Now()})
	}
	sources = append(sources, w.Source{ID: "other-thread", ThreadID: "elsewhere", Kind: "web", Content: "SECRET_CAPTURE_BODY"})
	e := Engine{}
	page := func(cursor string) ([]map[string]any, string, bool) {
		raw, _ := json.Marshal(map[string]any{"cursor": cursor})
		encoded, err := e.listSources(t.Context(), run, sources, raw)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(encoded, "SECRET_CAPTURE_BODY") {
			t.Fatal("list_sources returned captured bodies")
		}
		var result struct {
			Sources []map[string]any `json:"sources"`
			Next    string           `json:"next_cursor"`
			HasMore bool             `json:"has_more"`
		}
		if json.Unmarshal([]byte(encoded), &result) != nil {
			t.Fatal("invalid list_sources payload")
		}
		return result.Sources, result.Next, result.HasMore
	}
	first, cursor, more := page("")
	if len(first) != sourceListPage || !more || cursor == "" {
		t.Fatalf("first page len=%d more=%v cursor=%q", len(first), more, cursor)
	}
	if first[0]["evidence_strength"] != "observation" || first[1]["evidence_strength"] != "lead" {
		t.Fatalf("evidence strength = %v", first[:2])
	}
	second, secondCursor, more := page(cursor)
	if len(second) != sourceListPage || !more || secondCursor == "" {
		t.Fatalf("second page len=%d more=%v", len(second), more)
	}
	third, cursor, more := page(secondCursor)
	if len(third) != 5 || more || cursor != "" {
		t.Fatalf("last page len=%d more=%v cursor=%q", len(third), more, cursor)
	}
	for _, item := range third {
		if item["source_id"] == "other-thread" {
			t.Fatal("list_sources crossed the thread boundary")
		}
	}
	if _, err := e.listSources(t.Context(), run, sources, json.RawMessage(`{"cursor":"not-a-cursor"}`)); err == nil {
		t.Fatal("malformed cursor was accepted")
	}
	changed := append(append([]w.Source{}, sources...), w.Source{ID: "new", ThreadID: "thread", Kind: "web"})
	raw, _ := json.Marshal(map[string]any{"cursor": secondCursor})
	if _, err := e.listSources(t.Context(), run, changed, raw); err == nil {
		t.Fatal("cursor from a different source set was accepted")
	}
}

func TestListSourcesPagesByWireBytesAndKeepsEveryEntry(t *testing.T) {
	run := w.Run{ThreadID: "thread"}
	sources := make([]w.Source, 20)
	for i := range sources {
		sources[i] = w.Source{ID: fmt.Sprintf("source-%02d", i), ThreadID: "thread", Kind: "web", Title: strings.Repeat("😀", 300), URL: "https://example.org/" + strings.Repeat("😀", 500), CapturedAt: w.Now()}
	}
	e := Engine{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page <= len(sources); page++ {
		raw, _ := json.Marshal(map[string]any{"cursor": cursor})
		encoded, err := e.listSources(t.Context(), run, sources, raw)
		if err != nil {
			t.Fatalf("legal Unicode metadata made list_sources unusable: %v", err)
		}
		if len(encoded) > 65536 {
			t.Fatalf("list_sources page exceeded the MCP wire limit: %d bytes", len(encoded))
		}
		var result struct {
			Sources []struct {
				SourceID string `json:"source_id"`
			} `json:"sources"`
			Next    string `json:"next_cursor"`
			HasMore bool   `json:"has_more"`
		}
		if err := json.Unmarshal([]byte(encoded), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Sources) == 0 {
			t.Fatal("a legal page dropped every remaining entry")
		}
		for _, item := range result.Sources {
			if seen[item.SourceID] {
				t.Fatalf("source %s appeared twice across pages", item.SourceID)
			}
			seen[item.SourceID] = true
		}
		if !result.HasMore {
			if result.Next != "" {
				t.Fatal("terminal page still returned a cursor")
			}
			break
		}
		if result.Next == "" {
			t.Fatal("non-terminal page returned no cursor")
		}
		cursor = result.Next
	}
	if len(seen) != len(sources) {
		t.Fatalf("cursor paging lost entries: got %d want %d", len(seen), len(sources))
	}
}

func TestCancelMemberSurvivesCanceledRequestContext(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedPublicationRun(t, s, "free", "responder")
	started, stopped := make(chan struct{}), make(chan struct{})
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, _ agent.Observe) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not start")
	}
	// The revocation is already committed when the browser disconnects.
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		profile, err := tx.Agent(run.Agents[0].ID)
		if err != nil {
			return err
		}
		profile.Disabled = true
		profile.Revision++
		return tx.SaveAgent(profile)
	}); err != nil {
		t.Fatal(err)
	}
	requestCtx, requestCancel := context.WithCancel(t.Context())
	requestCancel()
	if err := e.CancelMember(requestCtx, "", run.Agents[0].ID); err != nil {
		t.Fatalf("committed revocation lost its cancellation when the request disconnected: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked native process kept running")
	}
	if got := awaitRun(t, s, run.ID); got.Status != "canceled" {
		t.Fatalf("revoked run settled as %s: %s", got.Status, got.Error)
	}
}

func TestNativeCaptureAndListSourcesDrivePublication(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedPublicationRun(t, s, "research", "coordinator")
	var publishedSourceID string
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		input := json.RawMessage(`{"file_path":"/workspace/README.md"}`)
		if err := observe("tool_started", map[string]any{"tool_call_id": "cli:read-1", "name": "Read", "input": input, "origin": "native"}); err != nil {
			return err
		}
		finished := map[string]any{"tool_call_id": "cli:read-1", "name": "Read", "input": input, "status": "ok", "output": "1\tPUBLIC_FIXTURE", "origin": "native"}
		if err := observe("tool_finished", finished); err != nil {
			return err
		}
		if err := observe("tool_finished", finished); err != nil {
			return err
		}
		listed, err := execute(ctx, "list_sources", json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		var page struct {
			Sources []struct {
				SourceID         string `json:"source_id"`
				Kind             string `json:"kind"`
				EvidenceStrength string `json:"evidence_strength"`
			} `json:"sources"`
		}
		if err := json.Unmarshal([]byte(listed), &page); err != nil || len(page.Sources) != 1 {
			return fmt.Errorf("list_sources = %s err=%v", listed, err)
		}
		publishedSourceID = page.Sources[0].SourceID
		if page.Sources[0].Kind != "native_file" || page.Sources[0].EvidenceStrength != "original" {
			return fmt.Errorf("unexpected source metadata: %+v", page.Sources[0])
		}
		pageRaw, _ := json.Marshal(map[string]any{"source_id": publishedSourceID, "max_bytes": 8})
		readPage, err := execute(ctx, "read_source", pageRaw)
		if err != nil {
			return err
		}
		var read struct {
			Content string `json:"content"`
			HasMore bool   `json:"has_more"`
		}
		if json.Unmarshal([]byte(readPage), &read) != nil || read.Content == "" || !read.HasMore {
			return fmt.Errorf("native source was not pageable: %s", readPage)
		}
		payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "原生观察", "body": "正文", "source_ids": []string{publishedSourceID}, "findings": []any{map[string]any{"title": "f", "detail": "d", "status": "supported", "source_ids": []string{publishedSourceID}}}, "limitations": []string{}})
		_, err = execute(ctx, "publish_artifact", payload)
		return err
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	if got := awaitRun(t, s, run.ID); got.Status != "succeeded" {
		t.Fatalf("run status=%s error=%s", got.Status, got.Error)
	}
	if publishedSourceID == "" {
		t.Fatal("list_sources returned no server-assigned ID")
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		source, err := tx.Source(publishedSourceID)
		if err != nil {
			return err
		}
		if source.Kind != "native_file" || source.Content != "1\tPUBLIC_FIXTURE" || source.CaptureStepID == "" {
			return fmt.Errorf("captured native source = %+v", source)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFailedNativeResultCreatesNoEvidence(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedPublicationRun(t, s, "research", "coordinator")
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		if err := observe("tool_finished", map[string]any{"tool_call_id": "cli:read-1", "name": "Read", "input": json.RawMessage(`{"file_path":"/workspace/README.md"}`), "status": "error", "output": "permission denied", "origin": "native"}); err != nil {
			return err
		}
		listed, err := execute(ctx, "list_sources", json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		if !strings.Contains(listed, `"sources":[]`) {
			return errors.New("failed native result appeared in list_sources")
		}
		payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "x", "body": "y", "source_ids": []string{"invented"}, "findings": []any{}, "limitations": []string{}})
		if _, err := execute(ctx, "publish_artifact", payload); err == nil {
			return errors.New("an invented source ID was accepted")
		}
		return nil
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "required artifact") {
		t.Fatalf("run status=%s error=%s", got.Status, got.Error)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		sources, err := tx.Sources(run.ThreadID)
		if err != nil {
			return err
		}
		if len(sources) != 0 {
			return fmt.Errorf("failed native result created sources: %+v", sources)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
