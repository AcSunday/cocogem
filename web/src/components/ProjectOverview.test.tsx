import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { ProjectOverview as Overview, Workspace } from "../api/client";
import { ProjectOverview } from "./ProjectOverview";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

afterEach(() => {
  cleanup();
  request.mockReset();
});

const workspace = {
  id: "project",
  name: "订单系统",
  description: "兼容已有客户端",
  root: "",
  revision: 1,
  created_at: "2026-11-04T00:00:00Z",
  skill_ids: [],
} as Workspace;

function task(id: string, status: string) {
  return {
    task: {
      id,
      workspace_id: workspace.id,
      thread_id: `thread-${id}`,
      current_revision: 1,
      owner_member_id: "owner",
      owner_revision: 1,
      root_task_id: id,
      root_control_epoch: 1,
      budget_account_id: `budget-${id}`,
      collaboration_scope_id: `scope-${id}`,
      status,
      priority: 0,
      evaluation_generation: 1,
      evaluation_status: status === "completed" ? "current" : "incomplete",
      created_at: "2026-11-04T00:00:00Z",
      updated_at: "2026-11-04T00:00:00Z",
    },
    revision: {
      task_id: id,
      revision: 1,
      goal: id === "active" ? "幂等请求支持" : "接口方案 v2",
      scope: [],
      non_goals: [],
      constraint_refs: [],
      acceptance: [],
      input_refs: [],
      allowed_actions: [],
      source_message_id: "message",
      accepted_by: "user",
      acceptance_evidence: "message",
      created_at: "2026-11-04T00:00:00Z",
      digest: id,
    },
    budget: {
      id: `budget-${id}`,
      root_task_id: id,
      policy: {
        auto_continuations: 20,
        parallel_branches: 4,
        writer_branches: 2,
        fix_rounds: 3,
        no_progress_failures: 2,
      },
      sources: {
        auto_continuations: "global",
        parallel_branches: "global",
        writer_branches: "global",
        fix_rounds: "global",
        no_progress_failures: "global",
      },
      revision: 1,
      used_continuations: 0,
      reserved_runs: 0,
      active_branches: 0,
      active_writers: 0,
      fix_rounds_by_issue: {},
      no_progress_by_branch: {},
      updated_at: "2026-11-04T00:00:00Z",
    },
    completions: [],
  } as NonNullable<Overview["tasks"]>[number];
}

it("renders durable goals, outcomes and opens the exact task thread", async () => {
  request.mockResolvedValue({
    workspace,
    records: [],
    tasks: [task("active", "active"), task("done", "completed")],
    needs_attention: [],
  } satisfies Overview);
  const open = vi.fn();
  render(
    <ProjectOverview
      workspace={workspace}
      workspaceID={workspace.id}
      onOpenTask={open}
      onWorkspaceChange={() => {}}
      onNewTask={() => {}}
    />,
  );
  expect(await screen.findByRole("heading", { name: "订单系统" })).toBeVisible();
  expect(screen.getAllByText("幂等请求支持").length).toBeGreaterThan(0);
  expect(screen.getAllByText("接口方案 v2").length).toBeGreaterThan(0);
  expect(screen.queryByRole("heading", { name: "需要你处理" })).not.toBeInTheDocument();
  await userEvent.click(screen.getAllByRole("button", { name: /幂等请求支持/ })[0]);
  expect(open).toHaveBeenCalledWith("active");
});

it("shows blocking input and retries a failed project read", async () => {
  request
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValueOnce({
      workspace,
      records: [],
      tasks: [task("active", "waiting")],
      needs_attention: [
        {
          id: "question",
          task_id: "active",
          task_revision: 1,
          blocking_scope: "测试环境",
          body: "尚缺数据库地址",
          options: [],
          status: "open",
          created_at: "2026-11-04T00:00:00Z",
        },
      ],
    } satisfies Overview);
  render(
    <ProjectOverview
      workspace={workspace}
      workspaceID={workspace.id}
      onOpenTask={() => {}}
      onWorkspaceChange={() => {}}
      onNewTask={() => {}}
    />,
  );
  expect(await screen.findByRole("alert")).toHaveTextContent("offline");
  await userEvent.click(screen.getByRole("button", { name: "重新加载项目" }));
  expect(await screen.findByRole("heading", { name: "需要你处理" })).toBeVisible();
  expect(screen.getByText("尚缺数据库地址")).toBeVisible();
});
