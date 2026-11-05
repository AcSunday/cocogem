import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { ProjectTaskView, Question } from "../api/client";
import { TaskTodoList } from "./TaskTodoList";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

afterEach(() => {
  cleanup();
  request.mockReset();
});

function task(
  id: string,
  status: string,
  goal: string,
  waitingReason = "",
  lastRunStatus = "",
): ProjectTaskView {
  return {
    task: {
      id,
      workspace_id: "ws",
      thread_id: `thread-${id}`,
      current_revision: 1,
      owner_member_id: "owner",
      owner_revision: 1,
      root_task_id: id,
      root_control_epoch: 1,
      budget_account_id: `budget-${id}`,
      collaboration_scope_id: `scope-${id}`,
      status,
      waiting_reason: waitingReason,
      priority: 0,
      evaluation_generation: 1,
      evaluation_status: status === "completed" ? "current" : "incomplete",
      created_at: "2026-11-04T00:00:00Z",
      updated_at: "2026-11-04T00:00:00Z",
    },
    revision: {
      task_id: id,
      revision: 1,
      goal,
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
      used_continuations: 2,
      reserved_runs: 0,
      active_branches: 0,
      active_writers: 0,
      fix_rounds_by_issue: {},
      no_progress_by_branch: {},
      updated_at: "2026-11-04T00:00:00Z",
    },
    completions: [],
    last_run_status: lastRunStatus || undefined,
  } as ProjectTaskView;
}

const question: Question = {
  id: "question",
  task_id: "waiting",
  task_revision: 1,
  blocking_scope: "数据库",
  body: "测试环境尚缺数据库地址",
  options: [],
  status: "open",
  created_at: "2026-11-04T00:00:00Z",
};

it("filters linked tasks, shows attention and pauses from the list", async () => {
  request.mockResolvedValue({});
  const changed = vi.fn();
  const open = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskTodoList
      tasks={[
        task("active", "active", "幂等请求支持"),
        task("waiting", "waiting", "迁移评估", "question"),
        task("done", "completed", "接口方案 v2"),
        task("failed", "ready", "失败任务", "", "failed"),
      ]}
      questions={[question]}
      workspaces={{ ws: "示例项目" }}
      onOpenTask={open}
      onChanged={changed}
    />,
  );
  expect(screen.getByRole("button", { name: /幂等请求支持/ })).toBeVisible();
  expect(screen.getByText("需要你处理")).toBeVisible();
  expect(screen.getByText("接口方案 v2")).toBeVisible();
  expect(screen.getByText("失败任务")).toBeVisible();

  await user.click(screen.getByRole("button", { name: "清空" }));
  await user.click(screen.getByRole("button", { name: /失败/ }));
  expect(screen.getByText("失败任务")).toBeVisible();
  expect(screen.queryByText("幂等请求支持")).not.toBeInTheDocument();
  expect(screen.queryByText("接口方案 v2")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "清空" }));
  await user.click(screen.getByRole("button", { name: /需要我/ }));
  expect(screen.getByText("测试环境尚缺数据库地址")).toBeVisible();
  expect(screen.queryByText("幂等请求支持")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "清空" }));
  await user.click(screen.getByRole("button", { name: /已验收/ }));
  expect(screen.getByText("接口方案 v2")).toBeVisible();
  expect(screen.queryByText("幂等请求支持")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "全选" }));
  await user.click(screen.getByRole("button", { name: "暂停" }));
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/tasks/active/actions",
      "POST",
      expect.objectContaining({ action: "pause" }),
    ),
  );
  expect(changed).toHaveBeenCalled();

  await user.click(screen.getByRole("button", { name: /迁移评估/ }));
  expect(open).toHaveBeenCalledWith("ws", "waiting");
});
