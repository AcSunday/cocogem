package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type captureModel struct {
	requests []model.Request
}

func (m *captureModel) Complete(_ context.Context, request model.Request, _ func(string) error) (model.Response, error) {
	m.requests = append(m.requests, request)
	return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: "OK"}}}, nil
}

func TestMemberExecutionOptionsCRUD(t *testing.T) {
	s, _, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	var agent w.Agent
	create := func(value w.Agent, wantStatus int) {
		t.Helper()
		rw := apiCall(s, "POST", "/api/v1/agents", value, cookie, csrf, nil)
		if rw.Code != wantStatus {
			t.Fatalf("create got %d want %d: %s", rw.Code, wantStatus, rw.Body.String())
		}
		if wantStatus == 200 {
			if err := json.Unmarshal(rw.Body.Bytes(), &agent); err != nil {
				t.Fatal(err)
			}
		}
	}
	create(w.Agent{DisplayName: "options", Handle: "options", Executor: "codex", Model: "default", ContextSize: 2500, ThinkingLevel: "ultra", Tools: []string{}}, 200)
	if agent.ContextSize != 2500 || agent.ThinkingLevel != "ultra" {
		t.Fatalf("execution options were not persisted: %+v", agent)
	}
	create(w.Agent{DisplayName: "low", Handle: "low", Executor: "claude", Model: "default", ContextSize: w.MinContextTokens - 1, Tools: []string{}}, 400)
	create(w.Agent{DisplayName: "bad", Handle: "bad", Executor: "claude", Model: "default", ThinkingLevel: "ultra", Tools: []string{}}, 400)
	create(w.Agent{DisplayName: "gemini", Handle: "gemini", Executor: "gemini", Model: "default", ThinkingLevel: "high", Tools: []string{}}, 400)
	create(w.Agent{DisplayName: "pi", Handle: "pi-agent", Executor: "pi", Model: "default", ThinkingLevel: "high", Tools: []string{}}, 200)
	create(w.Agent{DisplayName: "pi-bad", Handle: "pi-bad", Executor: "pi", Model: "default", ThinkingLevel: "ultra", Tools: []string{}}, 400)

	agent.ContextSize, agent.ThinkingLevel = 4096, "low"
	rw := apiCall(s, "PATCH", "/api/v1/agents/"+agent.ID, agent, cookie, csrf, map[string]string{"If-Match": strconv.Itoa(agent.Revision)})
	if rw.Code != 200 {
		t.Fatalf("update got %d: %s", rw.Code, rw.Body.String())
	}
	var updated w.Agent
	if err := json.Unmarshal(rw.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ContextSize != 4096 || updated.ThinkingLevel != "low" {
		t.Fatalf("updated options were not persisted: %+v", updated)
	}
	updated.ContextSize, updated.ThinkingLevel = 0, ""
	rw = apiCall(s, "PATCH", "/api/v1/agents/"+updated.ID, updated, cookie, csrf, map[string]string{"If-Match": strconv.Itoa(updated.Revision)})
	if rw.Code != 200 {
		t.Fatalf("clearing options got %d: %s", rw.Code, rw.Body.String())
	}
	var cleared w.Agent
	if err := json.Unmarshal(rw.Body.Bytes(), &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.ContextSize != 0 || cleared.ThinkingLevel != "" {
		t.Fatalf("options were not cleared: %+v", cleared)
	}
	// No legacy 100000 ceiling: an explicit member budget may exceed it.
	create(w.Agent{DisplayName: "large", Handle: "large", Executor: "claude", Model: "default", ContextSize: 500000, Tools: []string{}}, 200)
	if agent.ContextSize != 500000 {
		t.Fatalf("large context limit was not persisted: %+v", agent)
	}
}

func TestVerifyForwardsExecutionOptionsAndKeysVerification(t *testing.T) {
	s, _, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	capture := &captureModel{}
	s.config.Model = capture
	verify := func(value VerifyInput, wantStatus int) VerifyReply {
		t.Helper()
		rw := apiCall(s, "POST", "/api/v1/system/verify", value, cookie, csrf, nil)
		if rw.Code != wantStatus {
			t.Fatalf("verify %+v got %d want %d: %s", value, rw.Code, wantStatus, rw.Body.String())
		}
		var reply VerifyReply
		if wantStatus == 200 {
			if err := json.Unmarshal(rw.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
		}
		return reply
	}
	reply := verify(VerifyInput{Executor: "claude", Model: "default", ContextSize: 64000, ThinkingLevel: "high"}, 200)
	if reply.ContextSize != 64000 || reply.ThinkingLevel != "high" {
		t.Fatalf("verify reply lost execution options: %+v", reply)
	}
	if len(capture.requests) != 1 || capture.requests[0].ContextSize != 64000 || capture.requests[0].ThinkingLevel != "high" {
		t.Fatalf("verify did not forward execution options to the provider: %+v", capture.requests)
	}
	verify(VerifyInput{Executor: "claude", Model: "default", ThinkingLevel: "low"}, 200)
	s.mu.Lock()
	keys := map[string]bool{"claude:default:context=64000:thinking=high": s.verified["claude:default:context=64000:thinking=high"] != "", "claude:default:thinking=low": s.verified["claude:default:thinking=low"] != ""}
	s.mu.Unlock()
	for key, ok := range keys {
		if !ok {
			t.Fatalf("verification key %q was not recorded separately", key)
		}
	}
	calls := len(capture.requests)
	verify(VerifyInput{Executor: "claude", Model: "default", ThinkingLevel: "ultra"}, 400)
	verify(VerifyInput{Executor: "gemini", Model: "default", ThinkingLevel: "high"}, 400)
	verify(VerifyInput{Executor: "claude", Model: "default", ContextSize: 512}, 400)
	if len(capture.requests) != calls {
		t.Fatal("invalid execution options still reached the provider")
	}
}

type modelOptionsFixture struct {
	testutil.Model
	calls int
}

func (m *modelOptionsFixture) ModelOptions(_ context.Context, executor, selected string) (agent.ModelOptions, error) {
	m.calls++
	return agent.ModelOptions{ResolvedModel: "fixture/model", ThinkingLevels: []string{"low", "high", "custom"}}, nil
}
func TestModelOptionsAreAuthenticatedAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fixture := &modelOptionsFixture{}
	app, err := New(Config{Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Model: fixture}, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/system/model-options?executor=opencode&model=fixture%2Fmodel"
	if rw := apiCall(app, "GET", path, nil, nil, "", nil); rw.Code != 401 || fixture.calls != 0 {
		t.Fatal("model catalog leaked before local authentication")
	}
	setup := apiCall(app, "POST", "/auth/setup", PasswordInput{Password: "local-options-password"}, nil, "", nil)
	cookie := setup.Result().Cookies()[0]
	rw := apiCall(app, "GET", path, nil, cookie, "", nil)
	if rw.Code != http.StatusOK || fixture.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rw.Code, fixture.calls, rw.Body)
	}
	validateResponse(t, "/api/v1/system/model-options", "get", 200, rw.Body.Bytes())
	var options ModelOptions
	if json.Unmarshal(rw.Body.Bytes(), &options) != nil || options.ResolvedModel != "fixture/model" || len(options.ThinkingLevels) != 3 {
		t.Fatal("model capabilities lost")
	}
	for _, path := range []string{"/api/v1/system/model-options?executor=other", "/api/v1/system/model-options?executor=opencode&model=-bad"} {
		if rw := apiCall(app, "GET", path, nil, cookie, "", nil); rw.Code != 400 {
			t.Fatalf("bad catalog request accepted: %s", rw.Body)
		}
	}
	if fixture.calls != 1 {
		t.Fatal("invalid requests reached the CLI")
	}
}
