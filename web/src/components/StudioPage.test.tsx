import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { StudioPage } from "./StudioPage";
import type { StudioSnapshot, Workspace } from "../api/client";

const snapshot: StudioSnapshot = {
  workspace_id: "ws-1",
  workspace_name: "猫猫研究室",
  updated_at: "2026-11-22T12:00:00Z",
  threads: [
    {
      id: "th-1",
      title: "Fix auth token refresh race",
      status: "running",
      active_run_id: "run-1",
      pending_permissions: 1,
      updated_at: "2026-11-22T12:00:00Z",
    },
  ],
  agents: [
    {
      agent_id: "ag-1",
      display_name: "布偶猫",
      handle: "ragdoll",
      avatar: "cat:ragdoll",
      executor: "claude",
      model: "default",
      enabled: true,
      state: "researching",
      station: "research",
      thread_id: "th-1",
      thread_title: "Fix auth token refresh race",
      run_id: "run-1",
      step_id: "step-1",
      role: "research",
      activity_summary: "Inspecting refresh mutex",
    },
    {
      agent_id: "ag-2",
      display_name: "缅因猫",
      handle: "maine",
      avatar: "cat:maine",
      executor: "codex",
      model: "default",
      enabled: true,
      state: "waiting_permission",
      station: "gate",
      thread_id: "th-1",
      thread_title: "Fix auth token refresh race",
      run_id: "run-1",
      step_id: "step-2",
      role: "review",
      activity_summary: "git commit -m fix",
    },
  ],
  relays: [
    {
      from_agent_id: "ag-1",
      to_agent_id: "ag-2",
      thread_id: "th-1",
      run_id: "run-1",
      step_id: "step-2",
      role: "review",
      status: "running",
    },
  ],
  pending_permissions: [
    {
      thread_title: "Fix auth token refresh race",
      agent_name: "缅因猫",
      request: {
        id: "perm-1",
        thread_id: "th-1",
        workspace_id: "ws-1",
        run_id: "run-1",
        step_id: "step-2",
        attempt_id: "att-1",
        member_id: "ag-2",
        member_name: "缅因猫",
        tool_call_id: "call-1",
        origin: "host",
        action: "execute",
        tool_name: "run_command",
        normalized_resource_scope: "/tmp/repo",
        arguments_digest: "abc",
        reason: "commit fix",
        operation_summary: "git commit -m fix",
        permission_epoch: 1,
        status: "pending",
        request_id: "req-1",
        revision: 1,
        created_at: "2026-11-22T12:00:00Z",
      },
    },
  ],
  artifacts: [
    {
      id: "art-1",
      version: 2,
      thread_id: "th-1",
      thread_title: "Fix auth token refresh race",
      run_id: "run-1",
      type: "report",
      title: "Auth Audit Report",
      created_at: "2026-11-22T12:00:00Z",
    },
  ],
};

const workspaces: Workspace[] = [
  {
    id: "ws-1",
    name: "猫猫研究室",
    description: "",
    root: "/tmp/repo",
    skill_ids: [],
    revision: 1,
    created_at: "2026-11-22T10:00:00Z",
  },
];

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("StudioPage", () => {
  it("renders workspace cats, inline gate approval, gem showcase and dispatches commands", async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/v1/workspaces/ws-1/studio") {
        return Response.json(snapshot);
      }
      if (url === "/api/v1/permission-requests/perm-1/decisions" && init?.method === "POST") {
        return Response.json({ ...snapshot.pending_permissions[0].request, status: "allowed_once" });
      }
      return Response.json({});
    });
    vi.stubGlobal("fetch", fetchMock);

    const onDispatch = vi.fn(async () => "th-2");
    const onOpenThread = vi.fn();

    render(
      <StudioPage
        workspaces={workspaces}
        workspaceID="ws-1"
        onWorkspace={vi.fn()}
        onOpenThread={onOpenThread}
        onDispatch={onDispatch}
      />,
    );

    expect(await screen.findByText("Auth Audit Report")).toBeInTheDocument();
    expect(screen.getByText("git commit -m fix")).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "仅本次允许" }));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/permission-requests/perm-1/decisions",
        expect.objectContaining({ method: "POST" }),
      ),
    );

    const input = screen.getByLabelText("猫猫研发室指令输入框");
    await user.type(input, "@ragdoll 检查最新提交");
    await user.click(screen.getByRole("button", { name: "发送指令" }));
    await waitFor(() =>
      expect(onDispatch).toHaveBeenCalledWith("@ragdoll 检查最新提交", undefined),
    );
  });
});
