import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { TaskBoard } from "../api/client";
import { TaskBoardPage } from "./TaskBoardPage";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

afterEach(() => {
  cleanup();
  request.mockReset();
});

const task = (id: string, workspaceID: string, status: string) =>
  ({
    task: {
      id,
      workspace_id: workspaceID,
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
      evaluation_status: "incomplete",
      created_at: "2026-11-04T00:00:00Z",
      updated_at: "2026-11-04T00:00:00Z",
    },
    revision: {
      task_id: id,
      revision: 1,
      goal: id,
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
  }) as unknown as NonNullable<TaskBoard["tasks"]>[number];

it("shows tasks from every workspace and opens the owning project", async () => {
  request.mockResolvedValue({
    tasks: [task("订单幂等", "orders", "active"), task("研究缓存", "research", "waiting")],
    workspaces: { orders: "订单系统", research: "缓存研究" },
    needs_attention: [],
  } satisfies TaskBoard);
  const open = vi.fn();
  const user = userEvent.setup();
  render(<TaskBoardPage onOpenTask={open} />);
  expect(await screen.findByRole("heading", { name: "任务清单" })).toBeVisible();
  expect(screen.getByText("订单系统")).toBeVisible();
  expect(screen.getByText("缓存研究")).toBeVisible();
  await user.click(screen.getByRole("button", { name: /研究缓存/ }));
  expect(open).toHaveBeenCalledWith("research", "研究缓存");
});
