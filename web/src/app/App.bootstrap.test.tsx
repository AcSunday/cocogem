import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { ProjectTaskView } from "../api/client";
import { App } from "./App";

const system = {
  models: ["default"], roots: [], model_status: "configured", search_status: "not_configured",
  verified: {}, csrf_token: "test", limits: {}, executors: [],
};
const agent = {
  id: "owner", workspace_id: "ws", display_name: "协作成员", handle: "owner", aliases: [],
  role_description: "", model: "default", executor: "claude", tools: [], disabled: false, revision: 1,
};
const workspace = { id: "ws", name: "未命名项目", description: "", root: "", revision: 1, created_at: "2026-11-05T00:00:00Z", skill_ids: [] };
const thread = { id: "thread", workspace_id: "ws", title: "第一次提问", agent_ids: ["owner"], coordinator_id: "owner", workflow: "free", revision: 1, updated_at: "" };
const taskView = {
  task: {
    id: "task", workspace_id: "ws", thread_id: "thread", current_revision: 1, owner_member_id: "owner",
    owner_revision: 1, root_task_id: "task", root_control_epoch: 1, budget_account_id: "budget",
    collaboration_scope_id: "scope", status: "active", priority: 0, evaluation_generation: 1,
    evaluation_status: "incomplete", created_at: "2026-11-05T00:00:00Z", updated_at: "2026-11-05T00:00:00Z",
  },
  revision: {
    task_id: "task", revision: 1, goal: "第一次提问", scope: [], non_goals: [], constraint_refs: [], acceptance: [],
    input_refs: [], allowed_actions: [], source_message_id: "message", accepted_by: "user", acceptance_evidence: "message",
    created_at: "2026-11-05T00:00:00Z", digest: "digest",
  },
  budget: {
    id: "budget", root_task_id: "task", policy: { auto_continuations: 20, parallel_branches: 4, writer_branches: 2, fix_rounds: 3, no_progress_failures: 2 },
    sources: { auto_continuations: "global", parallel_branches: "global", writer_branches: "global", fix_rounds: "global", no_progress_failures: "global" }, revision: 1, used_continuations: 0, reserved_runs: 0, active_branches: 0, active_writers: 0,
    fix_rounds_by_issue: {}, no_progress_by_branch: {}, updated_at: "2026-11-05T00:00:00Z",
  },
  completions: [],
} satisfies ProjectTaskView;

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  history.replaceState({}, "", "/");
});

it("bootstraps into the conversation with a stable key and restores a task deep link", async () => {
  localStorage.setItem("cocogem-unlocked", "true");
  const keys: string[] = [];
  let attempts = 0;
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/session") return Response.json({ unlocked: true });
    if (path === "/api/v1/workspaces" && !options?.method) return Response.json({ items: attempts ? [workspace] : [] });
    if (path === "/api/v1/agents") return Response.json({ items: attempts ? [agent] : [] });
    if (path === "/api/v1/bootstrap") {
      attempts++;
      keys.push(new Headers(options?.headers).get("Idempotency-Key") ?? "");
      if (attempts === 1) return Response.json({ error: { message: "暂时不可用" } }, { status: 503 });
      return Response.json({ workspace, thread, agents: [agent] });
    }
    if (path === "/api/v1/workspaces/ws/agents") return Response.json({ items: [agent] });
    if (path.startsWith("/api/v1/workspaces/ws/threads")) return Response.json({ items: [thread], next_cursor: "" });
    if (path === "/api/v1/threads/thread/routing-preview") return Response.json({ proposal_id: "proposal", routing: { decision: "respond", targets: [] } });
    if (path === "/api/v1/threads/thread/runs") return Response.json({ id: "run", task_id: "task", input: { message: "第一次提问", workflow: "free", targets: [], assignments: {}, source_urls: [], artifact_refs: [], research_scope: "auto", proposal_id: "proposal", retry_of_run_id: "" }, agents: [agent], status: "running", created_at: "2026-11-05T00:00:00Z" });
    if (path === "/api/v1/tasks/task") return Response.json(taskView);
    if (path === "/api/v1/threads/thread") return Response.json({ thread, messages: { items: [], next_cursor: "" }, runs: [], sources: [] });
    if (path.startsWith("/api/v1/threads/thread?")) return Response.json({ thread, messages: { items: [], next_cursor: "" }, runs: [], sources: [] });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  const first = render(<App />);
  const user = userEvent.setup();
  const composer = await screen.findByLabelText("输入问题");
  expect(composer).toBeEnabled();
  await user.type(composer, "第一次提问");
  await user.click(screen.getByRole("button", { name: "发送" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("暂时不可用");
  expect(composer.closest("form")).toContainElement(screen.getByRole("alert"));
  await user.click(screen.getByRole("button", { name: "关闭错误提示" }));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(composer).toHaveValue("第一次提问");
  await user.click(screen.getByRole("button", { name: "发送" }));
  expect(await screen.findByRole("heading", { name: "第一次提问" })).toBeVisible();
  expect(location.pathname).toBe("/w/ws/t/thread");
  expect(keys).toHaveLength(2);
  expect(keys[0]).toHaveLength(36);
  expect(keys[1]).toBe(keys[0]);

  // A task URL is durable: remounting reads the same task, not a new composer.
  history.replaceState({}, "", "/w/ws/tasks/task");
  first.unmount();
  render(<App />);
  await screen.findByRole("heading", { name: "第一次提问" });
  // D10/A17: 任务设置 is gone with its budget/goal/pause controls and is not
  // reproduced under any other menu.
  expect(screen.queryByText("任务设置")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("heading", { name: "本任务协作预算" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "暂停任务" })).toBeNull();
  // No dedicated request survives for the removed settings area: no budget
  // PATCH, inbox read, revision list or manual action POST.
  const taskPaths = fetcher.mock.calls
    .map((call) => `${String(call[1]?.method ?? "GET")} ${String(call[0])}`)
    .filter((entry) =>
      /\/api\/v1\/tasks\/task(\/|\?|$)/.test(entry.split(" ")[1]),
    );
  expect(taskPaths.every((entry) => entry.startsWith("GET "))).toBe(true);
  expect(taskPaths).toContain("GET /api/v1/tasks/task");
  // The task surface still renders and keeps its deep link.
  expect(location.pathname).toBe("/w/ws/tasks/task");

  history.replaceState({}, "", "/w/ws/t/thread");
  await act(async () => {
    dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(await screen.findByLabelText("输入问题")).toBeVisible();
});
