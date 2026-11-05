import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { ProjectTaskView } from "../api/client";
import { TaskDetail } from "./TaskDetail";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

afterEach(() => {
  cleanup();
  request.mockReset();
});

const view = {
  task: {
    id: "task", workspace_id: "ws", thread_id: "thread", current_revision: 1,
    owner_member_id: "owner", owner_revision: 1, root_task_id: "task", root_control_epoch: 1,
    budget_account_id: "budget", collaboration_scope_id: "scope", status: "active", priority: 0,
    evaluation_generation: 1, evaluation_status: "incomplete", waiting_reason: "", created_at: "2026-11-04T00:00:00Z", updated_at: "2026-11-04T00:00:00Z",
  },
  revision: {
    task_id: "task", revision: 1, goal: "完成可追溯研究", scope: [], non_goals: [], constraint_refs: [],
    acceptance: [{ criterion_id: "outcome", statement: "结论有来源", required: true, kind: "outcome", evidence_requirements: ["exact_candidate"], evaluation_policy: "all_required" }],
    input_refs: [], allowed_actions: ["read"], source_message_id: "message", accepted_by: "user", acceptance_evidence: "message", created_at: "2026-11-04T00:00:00Z", digest: "goal-digest",
  },
  budget: {
    id: "budget", root_task_id: "task", policy: { auto_continuations: 20, parallel_branches: 4, writer_branches: 2, fix_rounds: 3, no_progress_failures: 2 },
    sources: { auto_continuations: "global", parallel_branches: "global", writer_branches: "global", fix_rounds: "global", no_progress_failures: "global" },
    revision: 3, used_continuations: 2, reserved_runs: 1, active_branches: 0, active_writers: 0, fix_rounds_by_issue: {}, no_progress_by_branch: {}, updated_at: "2026-11-04T00:00:00Z",
  },
  completions: [],
} satisfies ProjectTaskView;

const artifact = {
  id: "report", version: 2, revision: 1, thread_id: "thread", run_id: "run", step_id: "step", type: "report", title: "研究报告", body: "正文", findings: [], source_ids: [], limitations: [], feedback: "", feedback_at: "", created_at: "2026-11-04T00:00:00Z",
};

function load(current: ProjectTaskView = view) {
  request.mockImplementation(async (path: string) => {
    if (path === "/api/v1/tasks/task") return structuredClone(current);
    if (path === "/api/v1/threads/thread") return { thread: { id: "thread", task_id: "task" }, messages: { items: [] }, runs: [], sources: [] };
    if (path === "/api/v1/threads/thread/artifacts") return { items: [artifact] };
    if (path === "/api/v1/tasks/task/questions") return { items: [{ id: "question", task_id: "task", task_revision: 1, blocking_scope: "范围", body: "是否包含反证？", options: ["包含"], recommended: "包含", status: "open", created_at: "2026-11-04T00:00:00Z" }] };
    return { items: [] };
  });
}

it("answers revision-bound questions and saves durable steering", async () => {
  load();
  const user = userEvent.setup();
  render(<TaskDetail taskID="task" agents={[]} onOpenThread={() => {}} onOpenArtifact={() => {}} onOpenSource={() => {}} />);
  expect(await screen.findByRole("heading", { name: "完成可追溯研究" })).toBeVisible();
  await user.click(screen.getByRole("button", { name: "提交回答" }));
  expect(request).toHaveBeenCalledWith("/api/v1/tasks/task/questions/question/answers", "POST", expect.objectContaining({ answer: "包含" }), { "If-Match": "1" });
  await user.type(screen.getByLabelText("补充内容"), "不能新增 Redis");
  await user.click(screen.getByRole("button", { name: "保存补充" }));
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/tasks/task/inbox", "POST", expect.objectContaining({ body: "不能新增 Redis", expected_revision: 1, application_policy: "next_boundary" })));
});

it("binds manual acceptance to an exact artifact and evidence", async () => {
  load();
  const user = userEvent.setup();
  render(<TaskDetail taskID="task" agents={[]} onOpenThread={() => {}} onOpenArtifact={() => {}} onOpenSource={() => {}} />);
  const criterion = await screen.findByRole("group", { name: "结论有来源（必需）" });
  await user.selectOptions(await screen.findByLabelText("判断"), "passed");
  await user.click(await screen.findByRole("checkbox", { name: "研究报告 v2" }));
  expect(criterion).toBeVisible();
  await user.click(screen.getByRole("button", { name: "记录人工验收" }));
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/tasks/task/completions", "POST", expect.objectContaining({
    candidate_refs: ["artifact:report@v2"], judge_member_id: "user", evidence_producer: "human_confirmed",
    results: [{ criterion_id: "outcome", result: "passed", evidence: ["artifact:report@v2"], limitations: [] }],
  })));
});

it("offers an explicit budget increase and continuation without resetting the ledger", async () => {
  const waiting = structuredClone(view);
  waiting.task.status = "waiting";
  waiting.task.waiting_reason = "budget";
  waiting.budget.policy.auto_continuations = 2;
  waiting.budget.used_continuations = 2;
  waiting.budget.reserved_runs = 0;
  load(waiting);
  const user = userEvent.setup();
  render(<TaskDetail taskID="task" agents={[]} onOpenThread={() => {}} onOpenArtifact={() => {}} onOpenSource={() => {}} />);
  expect(await screen.findByText(/已用 2 次 · 预留 0 次 · 剩余 0 次/)).toBeVisible();
  await user.clear(screen.getByLabelText(/^自动续办/));
  await user.type(screen.getByLabelText(/^自动续办/), "5");
  await user.click(screen.getByRole("button", { name: "调整预算并继续" }));
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/tasks/task/budget", "PATCH", { policy: expect.objectContaining({ auto_continuations: 5 }) }, { "If-Match": "3" }));
  await user.click(screen.getByRole("button", { name: "保持暂停" }));
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/tasks/task/actions", "POST", expect.objectContaining({ action: "pause" })));
});
