package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// This opt-in acceptance drives the production four-step engineering plan
// through the real native CLI and OS test sandbox. It uses only a synthetic
// temporary project and the user's existing CLI connection; it never reads a
// user project, credential file or real database.
func TestLiveEngineeringDevelopAcceptance(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_ENGINEERING") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_ENGINEERING=1 to run the real native engineering acceptance")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("real run_test sandbox is verified on macOS; Linux run_test fails closed")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec is unavailable")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatal("the real claude CLI is required for this acceptance")
	}
	// Some Claude CLI releases reject the full MCP tool inventory upstream
	// unless tool search is disabled. Keep this in the child environment only;
	// no user settings or native CLI configuration is rewritten.
	t.Setenv("ENABLE_TOOL_SEARCH", "false")

	logPath := os.Getenv("COCOGEM_LIVE_ENGINEERING_LOG")
	if logPath == "" {
		logPath = filepath.Join(os.TempDir(), "cocogem-live-engineering.log")
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatalf("open live engineering log: %v", err)
	}
	defer logFile.Close()
	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		_, _ = fmt.Fprintf(logFile, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 13*time.Minute)
	defer cancel()
	provider := nativecli.Provider{}
	adapter, err := provider.Check(ctx, "claude")
	if err != nil {
		logf("preflight executor=claude failed error=%q", liveSafeError(err.Error()))
		t.Fatalf("real claude preflight failed: %v", err)
	}
	if !adapter.Supported || adapter.Isolation != "enforced" {
		logf("preflight executor=claude unsupported=%t isolation=%s reason=%q", !adapter.Supported, adapter.Isolation, liveSafeError(adapter.Reason))
		t.Fatalf("real claude isolation is not enforced: supported=%t isolation=%s", adapter.Supported, adapter.Isolation)
	}
	logf("preflight executor=claude version=%s adapter=%s isolation=%s", liveSafeError(adapter.Version), liveSafeError(adapter.Adapter), liveSafeError(adapter.Isolation))

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(liveEngineeringGoMod), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte(liveEngineeringBroken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "calc_test.go"), []byte(liveEngineeringTest), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "calc.go")); err != nil || string(got) != liveEngineeringBroken {
		t.Fatalf("synthetic fixture is not the deliberately failing implementation: %q %v", got, err)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run, steps := seedLiveEngineeringRun(t, s, root)
	logf("run seeded id=%s root=%s steps=%d", run.ID, filepath.Base(root), len(steps))

	engine := &Engine{
		Store:         s,
		Model:         provider,
		Runners:       map[string]agent.Runner{"claude": provider},
		Adapters:      provider,
		AllowedRoots:  []string{root},
		AllowedModels: []string{"default"},
		Limits: Limits{
			RunTimeout:    12 * time.Minute,
			StepTimeout:   5 * time.Minute,
			FirstResponse: 2 * time.Minute,
			Stall:         3 * time.Minute,
			StallAction:   "stop",
		},
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		engine.Wait()
		writeLiveEngineeringSummary(t, s, run.ID, run.ThreadID, logf)
	}()

	result := awaitLiveEngineeringRun(t, s, run.ID, ctx)
	logf("run settled status=%s failure_kind=%s error=%q", result.Status, result.FailureKind, liveSafeError(result.Error))
	if result.Status != "succeeded" {
		t.Fatalf("live engineering run status=%s failure_kind=%s", result.Status, result.FailureKind)
	}

	var final w.Run
	var storedSteps []w.Step
	var attempts []w.Attempt
	var sources []w.Source
	var artifacts []w.Artifact
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		if final, err = tx.Run(run.ID); err != nil {
			return err
		}
		if storedSteps, err = tx.Steps(run.ID); err != nil {
			return err
		}
		if attempts, err = tx.Attempts(run.ID); err != nil {
			return err
		}
		if sources, err = tx.Sources(run.ThreadID); err != nil {
			return err
		}
		artifacts, err = tx.Artifacts(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	verifyLiveEngineeringEvidence(t, root, final, steps, storedSteps, attempts, sources, artifacts)
	logf("acceptance verified run=%s sources=%d artifacts=%d attempts=%d", final.ID, len(sources), len(artifacts), len(attempts))
}

const (
	liveEngineeringGoMod  = "module example.test/calc\n\ngo 1.20\n"
	liveEngineeringBroken = "package calc\n\nfunc Add(a, b int) int { return a - b }\n"
	liveEngineeringTest   = `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatalf("Add(2,3)=%d, want 5", Add(2, 3))
	}
}
`
	liveEngineeringTask = `这是隔离的临时合成 Go 工程验收，只操作当前临时工作区，不读取用户项目、凭证或网络。calc.go 的 Add 故意写成 a - b，calc_test.go 断言 Add(2, 3) == 5，所以当前测试失败。
所有文件读取、写入和测试必须使用 MCP 工具 mcp__cocogem__read_file / write_file / run_test / list_sources / read_source；不要用内置 Read/Write/Edit/Bash。内置 Read 不返回完整文件 SHA-256，不能代替 read_file。
按四步协作：
1 开发者：read_file 读取 calc.go 取得完整 sha256，write_file 只把 return a - b 改成 return a + b，保持其他字节不变，然后 run_test（go，默认 120 秒）直到 exit_code 0。不要发布最终产物。
2 复核者：独立 read_file 检查 calc.go，list_sources 查看 calc.go 的最新 diff 和测试 source，自己 run_test；不要发布任何 artifact，直接用普通步骤回复说明是否有缺陷，并写出真实的 diff/test source ID。不要写文件。
3 开发者：读取上游复核者的确切普通回复及其 source ID；若无缺陷，不要为制造活动重写文件，只再 run_test 确认 exit_code 0；若有缺陷才修复并复测，然后简短结束。不要发布任何 artifact。
4 最终复核者：read_file 检查最终 calc.go，list_sources 找 calc.go 最新 diff 和最后一次写入后的成功 test，read_source 核对内容，自己再 run_test。只发布一份 engineering_report；findings 使用 confirmed 或 insufficient_evidence，confirmed/disputed 必须带 severity 和真实 file/diff/test source_ids。source_ids 必须包含 calc.go 最新 diff ID 和最后写入后的成功 test ID，不得编造 ID；limitations 写明只验证 go test。回复保持简短。`
)

func seedLiveEngineeringRun(t *testing.T, s *store.Store, root string) (w.Run, []w.Step) {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "live engineering acceptance", Root: root, Revision: 1}
	developerTools := []string{"read_file", "list_files", "read_source", "write_file", "patch_file", "run_test", "publish_artifact"}
	reviewerTools := []string{"read_file", "list_files", "read_source", "run_test", "publish_artifact"}
	developer := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "开发成员", Handle: "live-developer", RoleDescription: "实现并验证合成 Go 工程修复。", Executor: "claude", Model: "default", Tools: developerTools, Revision: 1}
	reviewer := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "复核成员", Handle: "live-reviewer", RoleDescription: "独立读取证据并复测，不写文件。", Executor: "claude", Model: "default", Tools: reviewerTools, Revision: 1}
	agents := []w.Agent{developer, reviewer}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "live engineering acceptance", AgentIDs: []string{developer.ID, reviewer.ID}, CoordinatorID: reviewer.ID, Workflow: "engineering", Revision: 1, UpdatedAt: w.Now()}
	input := w.RunInput{
		Message:           liveEngineeringTask,
		Workflow:          "engineering",
		EngineeringAction: w.EngineeringDevelop,
		Assignments:       map[string]string{"developer": developer.ID, "reviewer": reviewer.ID},
		ResearchScope:     "given_sources",
	}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "queued", CreatedAt: w.Now(), Agents: agents, CoordinatorID: thread.CoordinatorID, Input: input}
	steps, err := Plan(run.ID, input, agents)
	if err != nil {
		t.Fatal(err)
	}
	wantRoles := []string{"developer", "reviewer", "developer", "final_reviewer"}
	if len(steps) != len(wantRoles) {
		t.Fatalf("engineering plan has %d steps, want %d", len(steps), len(wantRoles))
	}
	for i := range wantRoles {
		if steps[i].Role != wantRoles[i] {
			t.Fatalf("plan step %d role=%s want %s", i, steps[i].Role, wantRoles[i])
		}
	}
	if steps[0].AgentID != developer.ID || steps[2].AgentID != developer.ID || steps[1].AgentID != reviewer.ID || steps[3].AgentID != reviewer.ID {
		t.Fatal("engineering plan did not use the two configured members")
	}
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, member := range agents {
			if err := tx.SaveAgent(member); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "hash"); err != nil {
			return err
		}
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return run, steps
}

func awaitLiveEngineeringRun(t *testing.T, s *store.Store, id string, ctx context.Context) w.Run {
	t.Helper()
	for {
		wake := s.Changed()
		var result w.Run
		if err := s.Read(ctx, func(tx *store.Tx) error {
			var err error
			result, err = tx.Run(id)
			return err
		}); err != nil {
			if ctx.Err() != nil {
				t.Fatalf("live engineering run did not settle before its bounded deadline: %v", ctx.Err())
			}
			t.Fatal(err)
		}
		if w.Terminal(result.Status) {
			return result
		}
		select {
		case <-ctx.Done():
			t.Fatalf("live engineering run did not settle before its bounded deadline (status=%s)", result.Status)
		case <-wake:
		}
	}
}

func verifyLiveEngineeringEvidence(t *testing.T, root string, run w.Run, planned, stored []w.Step, attempts []w.Attempt, sources []w.Source, artifacts []w.Artifact) {
	t.Helper()
	if run.Status != "succeeded" || run.FailureKind != "" {
		t.Fatalf("run is not a clean success: status=%s failure_kind=%s error=%q", run.Status, run.FailureKind, liveSafeError(run.Error))
	}
	if len(stored) != len(planned) {
		t.Fatalf("stored steps=%d planned=%d", len(stored), len(planned))
	}
	roles := map[string]string{}
	for _, step := range planned {
		roles[step.ID] = step.Role
	}
	storedByID := map[string]w.Step{}
	for _, step := range stored {
		storedByID[step.ID] = step
		if step.Status != "succeeded" {
			t.Fatalf("step role=%s status=%s failure_kind=%s error=%q", step.Role, step.Status, step.FailureKind, liveSafeError(step.Error))
		}
	}
	for _, step := range planned {
		if storedByID[step.ID].ID == "" {
			t.Fatalf("planned step role=%s is missing from the run", step.Role)
		}
	}
	if len(attempts) != len(planned) {
		t.Fatalf("attempts=%d want one successful attempt per planned step (%d)", len(attempts), len(planned))
	}
	for _, attempt := range attempts {
		role := roles[attempt.StepID]
		if attempt.Status != "succeeded" {
			t.Fatalf("attempt role=%s number=%d status=%s failure_kind=%s error=%q", role, attempt.Number, attempt.Status, attempt.FailureKind, liveSafeError(attempt.Error))
		}
		if attempt.Executor != "claude" || attempt.Model != "default" || attempt.ResolvedModel == "" || attempt.ResolvedModel == "default" {
			t.Fatalf("attempt role=%s did not use the real claude default connection: executor=%s model=%s resolved=%s", role, attempt.Executor, attempt.Model, attempt.ResolvedModel)
		}
		if attempt.ConnectionID == "" || attempt.CLIVersion == "" || attempt.CLIIdentity == "" || attempt.SessionID == "" || attempt.SessionMode != "fresh" {
			t.Fatalf("attempt role=%s lacks native session identity: connection=%t version=%q identity=%t session=%t mode=%s", role, attempt.ConnectionID != "", liveSafeError(attempt.CLIVersion), attempt.CLIIdentity != "", attempt.SessionID != "", attempt.SessionMode)
		}
	}

	content, err := os.ReadFile(filepath.Join(root, "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "func Add(a, b int) int { return a + b }") || strings.Contains(string(content), "a - b") {
		t.Fatalf("final calc.go is not the real fix: %q", content)
	}
	for name, want := range map[string]string{"calc_test.go": liveEngineeringTest, "go.mod": liveEngineeringGoMod} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s changed during acceptance: %q %v", name, got, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected directory %s in the synthetic project", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if !slices.Equal(names, []string{"calc.go", "calc_test.go", "go.mod"}) {
		t.Fatalf("synthetic project has unexpected delivered files: %v", names)
	}

	sourceByID := map[string]w.Source{}
	var latestDiff w.Source
	var lastWrite time.Time
	actualChange := false
	passingByStep := map[string][]w.Source{}
	for _, source := range sources {
		if source.RunID != run.ID {
			continue
		}
		sourceByID[source.ID] = source
		switch source.Kind {
		case "diff":
			if source.Path != "calc.go" {
				t.Fatalf("acceptance changed an unexpected path: %s", source.Path)
			}
			if strings.Contains(source.Content, "-func Add(a, b int) int { return a - b }") && strings.Contains(source.Content, "+func Add(a, b int) int { return a + b }") {
				actualChange = true
			}
			moment := parseSourceTime(source.CapturedAt)
			if latestDiff.ID == "" || moment.After(lastWrite) {
				latestDiff, lastWrite = source, moment
			}
		case "test":
			if source.ExitCode == 0 && !source.TimedOut {
				passingByStep[source.CaptureStepID] = append(passingByStep[source.CaptureStepID], source)
			}
		}
	}
	if latestDiff.ID == "" || latestDiff.SHA256 == "" {
		t.Fatal("no real captured diff source exists for calc.go")
	}
	if !actualChange {
		t.Fatal("no captured diff source shows the real Add a-b to a+b change")
	}
	current, err := tools.CurrentSHA256(root, "calc.go")
	if err != nil || current != latestDiff.SHA256 {
		t.Fatalf("captured diff no longer matches the delivered calc.go: source=%q current=%q err=%v", latestDiff.SHA256, current, err)
	}
	if latestDiff.CaptureStepID != planned[0].ID && latestDiff.CaptureStepID != planned[2].ID {
		t.Fatalf("a non-developer step captured the write diff: %s", roles[latestDiff.CaptureStepID])
	}
	for _, step := range planned[:3] {
		if len(passingByStep[step.ID]) == 0 {
			t.Fatalf("step role=%s has no passing host test source; independent retest evidence is missing", step.Role)
		}
	}
	digest, err := tools.WorkspaceDigest(root)
	if err != nil || digest == "" {
		t.Fatalf("delivered workspace digest is unavailable: %v", err)
	}
	qualifyingTests := map[string]bool{}
	for _, source := range sources {
		if source.Kind != "test" || source.ExitCode != 0 || source.TimedOut || source.SHA256 == "" {
			continue
		}
		if !parseSourceTime(source.CapturedAt).Before(lastWrite) && source.SHA256 == digest {
			qualifyingTests[source.ID] = true
		}
	}
	if len(qualifyingTests) == 0 {
		t.Fatal("no passing host test captured after the last write matches the delivered workspace")
	}

	var report *w.Artifact
	for i := range artifacts {
		artifact := &artifacts[i]
		if artifact.RunID != run.ID || artifact.Type != "engineering_report" {
			continue
		}
		if report == nil || artifact.Version > report.Version {
			report = artifact
		}
	}
	if report == nil {
		t.Fatal("final reviewer did not publish an engineering_report")
	}
	if report.StepID != planned[3].ID {
		t.Fatalf("engineering_report came from role=%s, not final_reviewer", roles[report.StepID])
	}
	if len(report.SourceIDs) == 0 {
		t.Fatal("engineering_report has no source references")
	}
	refs := map[string]bool{}
	for _, id := range report.SourceIDs {
		source, ok := sourceByID[id]
		if !ok {
			t.Fatalf("engineering_report cites a source that does not exist: %s", id)
		}
		if !slices.Contains([]string{"file", "native_file", "diff", "test"}, source.Kind) {
			t.Fatalf("engineering_report cites non-engineering evidence kind=%s id=%s", source.Kind, id)
		}
		refs[id] = true
	}
	if !refs[latestDiff.ID] {
		t.Fatalf("engineering_report does not cite the latest calc.go diff %s", latestDiff.ID)
	}
	if !slices.ContainsFunc(report.SourceIDs, func(id string) bool { return qualifyingTests[id] }) {
		t.Fatal("engineering_report does not cite any passing test captured after the last write whose tree matches the delivery")
	}
	for _, finding := range report.Findings {
		if (finding.Status == "confirmed" || finding.Status == "disputed") && finding.Severity == "" {
			t.Fatalf("engineering finding %q claims %s without severity", finding.Title, finding.Status)
		}
		for _, id := range finding.SourceIDs {
			source, ok := sourceByID[id]
			if !ok {
				t.Fatalf("engineering finding cites a source that does not exist: %s", id)
			}
			if !refs[id] || !slices.Contains([]string{"file", "native_file", "diff", "test"}, source.Kind) {
				t.Fatalf("engineering finding cites invalid evidence: kind=%s id=%s", source.Kind, id)
			}
		}
	}
}

func writeLiveEngineeringSummary(t *testing.T, s *store.Store, runID, threadID string, logf func(string, ...any)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var run w.Run
	var steps []w.Step
	var attempts []w.Attempt
	var sources []w.Source
	var artifacts []w.Artifact
	var events []w.Event
	if err := s.Read(ctx, func(tx *store.Tx) error {
		var err error
		if run, err = tx.Run(runID); err != nil {
			return err
		}
		if steps, err = tx.Steps(runID); err != nil {
			return err
		}
		if attempts, err = tx.Attempts(runID); err != nil {
			return err
		}
		if sources, err = tx.Sources(threadID); err != nil {
			return err
		}
		if artifacts, err = tx.Artifacts(threadID); err != nil {
			return err
		}
		events, _ = tx.Events(runID, 0)
		return nil
	}); err != nil {
		logf("summary unavailable error=%q", liveSafeError(err.Error()))
		return
	}
	roles := map[string]string{}
	for _, step := range steps {
		roles[step.ID] = step.Role
	}
	toolCounts := map[string]map[string]int{}
	for _, event := range events {
		if event.Type != "tool_finished" {
			continue
		}
		var data struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if json.Unmarshal(event.Data, &data) != nil || data.Name == "" {
			continue
		}
		counts := toolCounts[event.StepID]
		if counts == nil {
			counts = map[string]int{}
			toolCounts[event.StepID] = counts
		}
		counts[data.Name]++
		if data.Status != "ok" {
			counts[data.Name+"!error"]++
		}
	}
	logf("summary run status=%s failure_kind=%s error=%q", run.Status, run.FailureKind, liveSafeError(run.Error))
	for _, step := range steps {
		logf("summary step role=%s status=%s failure_kind=%s attempts=%d error=%q", step.Role, step.Status, step.FailureKind, step.AttemptCount, liveSafeError(step.Error))
		if names := sortedCounts(toolCounts[step.ID]); len(names) > 0 {
			logf("summary tools role=%s %s", step.Role, strings.Join(names, " "))
		}
	}
	for _, attempt := range attempts {
		logf("summary attempt role=%s number=%d status=%s executor=%s model=%s resolved=%s connection=%t cli_version=%s cli_identity=%t session=%t mode=%s failure_kind=%s error=%q", roles[attempt.StepID], attempt.Number, attempt.Status, attempt.Executor, attempt.Model, attempt.ResolvedModel, attempt.ConnectionID != "", liveSafeError(attempt.CLIVersion), attempt.CLIIdentity != "", attempt.SessionID != "", attempt.SessionMode, attempt.FailureKind, liveSafeError(attempt.Error))
	}
	for _, source := range sources {
		if source.RunID != runID || (source.Kind != "diff" && source.Kind != "test") {
			continue
		}
		logf("summary source kind=%s path=%s sha=%s exit_code=%d timed_out=%t role=%s id=%s captured_at=%s", source.Kind, source.Path, shortLiveHash(source.SHA256), source.ExitCode, source.TimedOut, roles[source.CaptureStepID], source.ID, source.CapturedAt)
	}
	for _, artifact := range artifacts {
		if artifact.RunID != runID {
			continue
		}
		statuses := map[string]int{}
		for _, finding := range artifact.Findings {
			statuses[finding.Status]++
		}
		logf("summary artifact type=%s version=%d source_refs=%d findings=%s limitations=%d body_bytes=%d", artifact.Type, artifact.Version, len(artifact.SourceIDs), formatCountMap(statuses), len(artifact.Limitations), len(artifact.Body))
	}
}

func sortedCounts(counts map[string]int) []string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, fmt.Sprintf("%s=%d", name, counts[name]))
	}
	return out
}

func formatCountMap(counts map[string]int) string {
	parts := sortedCounts(counts)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

func shortLiveHash(value string) string {
	if len(value) > 16 {
		return value[:16]
	}
	return value
}

var (
	liveBearerPattern = regexp.MustCompile(`(?i)bearer\s+[^\s"']+`)
	liveSecretPattern = regexp.MustCompile(`(?i)(?:sk-[a-z0-9_-]{8,}|(?:api[_-]?key|auth[_-]?token|token|secret)\s*[=:]\s*[^\s"']*)`)
)

// liveSafeError keeps concrete step reasons while redacting credential-shaped
// values and bounded output. It never writes prompts, tool input or tool output.
func liveSafeError(value string) string {
	value = liveBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = liveSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, "=:"); index >= 0 {
			return match[:index+1] + "[redacted]"
		}
		return "[redacted]"
	})
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= 600 {
		return value
	}
	end := 600
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…"
}
