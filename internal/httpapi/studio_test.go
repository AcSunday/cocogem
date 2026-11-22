package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestWorkspaceStudioSnapshotAggregatesActivityRelaysPermissionsAndArtifacts(t *testing.T) {
	s, db, cookie, csrf := pagingServer(t, workflow.DefaultLimits())

	var workspace w.Workspace
	rw := apiCall(s, "POST", "/api/v1/workspaces", WorkspaceInput{Name: "猫猫研发室"}, cookie, csrf, nil)
	if rw.Code != 200 {
		t.Fatalf("create workspace: %d %s", rw.Code, rw.Body.String())
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}

	var agentsResp struct {
		Items []w.Agent `json:"items"`
	}
	rw = apiCall(s, "GET", "/api/v1/workspaces/"+workspace.ID+"/agents", nil, cookie, csrf, nil)
	if rw.Code != 200 {
		t.Fatalf("list agents: %d %s", rw.Code, rw.Body.String())
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &agentsResp); err != nil {
		t.Fatal(err)
	}
	if len(agentsResp.Items) < 3 {
		t.Fatalf("expected at least 3 default agents, got %d", len(agentsResp.Items))
	}
	a0, a1, a2 := agentsResp.Items[0], agentsResp.Items[1], agentsResp.Items[2]

	now := w.Now()
	thread := w.Thread{
		ID:          w.NewID(),
		WorkspaceID: workspace.ID,
		Title:       "Fix auth token refresh race",
		Revision:    1,
		UpdatedAt:   now,
	}
	run := w.Run{
		ID:          w.NewID(),
		WorkspaceID: workspace.ID,
		ThreadID:    thread.ID,
		Status:      "running",
		CreatedAt:   now,
		StartedAt:   now,
	}
	step1 := w.Step{
		ID:          w.NewID(),
		RunID:       run.ID,
		AgentID:     a0.ID,
		AgentName:   a0.DisplayName,
		Role:        "research",
		Instruction: "Inspect concurrent token refresh calls",
		Status:      "running",
		StartedAt:   now,
	}
	step2 := w.Step{
		ID:          w.NewID(),
		RunID:       run.ID,
		AgentID:     a1.ID,
		AgentName:   a1.DisplayName,
		FromAgentID: a0.ID,
		FromStepID:  step1.ID,
		Role:        "review",
		Instruction: "Verify singleflight mutex diff",
		Status:      "running",
		StartedAt:   now,
	}
	perm := w.ToolPermissionRequest{
		ID:                      w.NewID(),
		WorkspaceID:             workspace.ID,
		ThreadID:                thread.ID,
		RunID:                   run.ID,
		StepID:                  step2.ID,
		AttemptID:               w.NewID(),
		MemberID:                a2.ID,
		MemberName:              a2.DisplayName,
		ToolCallID:              "call-1",
		Origin:                  w.OriginHost,
		ToolName:                "run_command",
		Action:                  "execute",
		NormalizedResourceScope: "/tmp/repo",
		ArgumentsDigest:         "digest-1",
		Reason:                  "commit fix",
		OperationSummary:        "git commit -m fix",
		Status:                  w.PermissionPending,
		PermissionEpoch:         1,
		RequestID:               w.NewID(),
		Revision:                1,
		CreatedAt:               now,
	}
	artV1 := w.Artifact{
		ID:        "art-1",
		Version:   1,
		Revision:  1,
		ThreadID:  thread.ID,
		RunID:     run.ID,
		StepID:    step1.ID,
		Type:      "report",
		Title:     "Auth Race Audit v1",
		Body:      "v1",
		CreatedAt: now,
	}
	artV2 := w.Artifact{
		ID:        "art-1",
		Version:   2,
		Revision:  1,
		ThreadID:  thread.ID,
		RunID:     run.ID,
		StepID:    step2.ID,
		Type:      "report",
		Title:     "Auth Race Audit v2",
		Body:      "v2",
		CreatedAt: now,
	}

	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "studio-key-123456", "studio-hash"); err != nil {
			return err
		}
		if err := tx.SaveStep(step1); err != nil {
			return err
		}
		if err := tx.SaveStep(step2); err != nil {
			return err
		}
		if err := tx.SavePermissionRequest(perm); err != nil {
			return err
		}
		if err := tx.SaveArtifact(artV1); err != nil {
			return err
		}
		return tx.SaveArtifact(artV2)
	}); err != nil {
		t.Fatal(err)
	}

	rw = apiCall(s, "GET", "/api/v1/workspaces/"+workspace.ID+"/studio", nil, cookie, csrf, nil)
	if rw.Code != 200 {
		t.Fatalf("studio endpoint got %d: %s", rw.Code, rw.Body.String())
	}
	var snap StudioSnapshot
	if err := json.Unmarshal(rw.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.WorkspaceID != workspace.ID || len(snap.Threads) != 1 {
		t.Fatalf("unexpected studio header/threads: %+v", snap)
	}
	if snap.Threads[0].PendingPermissions != 1 || snap.Threads[0].ActiveRunID != run.ID {
		t.Fatalf("unexpected studio thread summary: %+v", snap.Threads[0])
	}
	if len(snap.Relays) != 1 || snap.Relays[0].FromAgentID != a0.ID || snap.Relays[0].ToAgentID != a1.ID {
		t.Fatalf("expected 1 A2A relay, got %+v", snap.Relays)
	}
	if len(snap.PendingPermissions) != 1 || snap.PendingPermissions[0].Request.ID != perm.ID {
		t.Fatalf("expected 1 pending permission, got %+v", snap.PendingPermissions)
	}
	if len(snap.Artifacts) != 1 || snap.Artifacts[0].Version != 2 || snap.Artifacts[0].Title != "Auth Race Audit v2" {
		t.Fatalf("expected latest artifact version v2 only, got %+v", snap.Artifacts)
	}

	byAgent := map[string]StudioAgentActivity{}
	for _, ag := range snap.Agents {
		byAgent[ag.AgentID] = ag
	}
	if byAgent[a0.ID].State != "researching" || byAgent[a0.ID].Station != "research" {
		t.Fatalf("agent 0 state=%+v want researching/research", byAgent[a0.ID])
	}
	if byAgent[a1.ID].State != "reviewing" || byAgent[a1.ID].Station != "review" {
		t.Fatalf("agent 1 state=%+v want reviewing/review", byAgent[a1.ID])
	}
	if byAgent[a2.ID].State != "waiting_permission" || byAgent[a2.ID].Station != "gate" {
		t.Fatalf("agent 2 state=%+v want waiting_permission/gate", byAgent[a2.ID])
	}
}
