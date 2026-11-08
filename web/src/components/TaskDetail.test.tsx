import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import type { ProjectTaskView } from "../api/client";
import { TaskDetail } from "./TaskDetail";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
});

afterEach(() => {
  cleanup();
  request.mockReset();
});

const baseView: ProjectTaskView = {
  task: {
    id: "task",
    workspace_id: "ws",
    thread_id: "thread",
    current_revision: 1,
    owner_member_id: "owner",
    owner_revision: 1,
    root_task_id: "task",
    root_control_epoch: 1,
    budget_account_id: "budget",
    collaboration_scope_id: "scope",
    status: "active",
    priority: 0,
    evaluation_generation: 1,
    evaluation_status: "incomplete",
    waiting_reason: "",
    created_at: "2026-11-04T00:00:00Z",
    updated_at: "2026-11-04T00:00:00Z",
  },
  revision: {
    task_id: "task",
    revision: 1,
    goal: "完成可追溯研究",
    scope: [],
    non_goals: [],
    constraint_refs: [],
    acceptance: [
      {
        criterion_id: "outcome",
        statement: "结论有来源",
        required: true,
        kind: "outcome",
        evidence_requirements: ["exact_candidate"],
        evaluation_policy: "all_required",
      },
    ],
    input_refs: [],
    allowed_actions: ["read"],
    source_message_id: "message",
    accepted_by: "user",
    acceptance_evidence: "message",
    created_at: "2026-11-04T00:00:00Z",
    digest: "goal-digest",
  },
  budget: {
    id: "budget",
    root_task_id: "task",
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
    revision: 3,
    used_continuations: 2,
    reserved_runs: 1,
    active_branches: 0,
    active_writers: 0,
    fix_rounds_by_issue: {},
    no_progress_by_branch: {},
    updated_at: "2026-11-04T00:00:00Z",
  },
  completions: [],
};

const artifact = {
  id: "report",
  version: 2,
  revision: 1,
  thread_id: "thread",
  run_id: "run",
  step_id: "step",
  type: "report",
  title: "研究报告",
  body: "正文",
  findings: [],
  source_ids: [],
  limitations: [],
  feedback: "",
  feedback_at: "",
  created_at: "2026-11-04T00:00:00Z",
  files: [
    {
      media_type: "text/plain",
      name: "report.txt",
      sha256: "sha",
      size: 2048,
      source_id: "file-source",
    },
  ],
};

const deliveredMessage = {
  id: "reply",
  thread_id: "thread",
  run_id: "run",
  step_id: "step",
  agent_id: "owner",
  agent_name: "研究员",
  role: "assistant",
  content: "研究结论与来源说明",
  status: "completed",
  created_at: "2026-11-04T00:00:00Z",
};

const succeededRun = {
  id: "run",
  task_id: "task",
  thread_id: "thread",
  status: "succeeded",
  coordinator_id: "owner",
  agents: [],
  error: "",
  created_at: "2026-11-04T00:00:00Z",
  started_at: "2026-11-04T00:00:00Z",
  finished_at: "2026-11-04T00:01:00Z",
  delivery: {
    message_ids: ["reply"],
    artifact_refs: [{ id: "report", version: 2 }],
  },
};

function makeView(change: (view: ProjectTaskView) => void = () => {}) {
  const view = structuredClone(baseView);
  change(view);
  return view;
}

function deliveries({
  runs = [],
  artifacts = [],
  messages = [],
}: {
  runs?: unknown[];
  artifacts?: unknown[];
  messages?: unknown[];
} = {}) {
  return {
    task_id: "task",
    runs,
    artifacts,
    messages,
  };
}

function mockRoutes({
  view = baseView,
  delivery = deliveries(),
  questions = [],
  tasks = [],
  childQuestions = {},
  runDetail,
}: {
  view?: ProjectTaskView;
  delivery?: ReturnType<typeof deliveries>;
  questions?: unknown[];
  tasks?: ProjectTaskView[];
  childQuestions?: Record<string, unknown[]>;
  runDetail?: unknown;
} = {}) {
  request.mockImplementation(async (path: string, method?: string) => {
    if (path === "/api/v1/tasks/task" && !method) return structuredClone(view);
    if (path === "/api/v1/tasks/task/deliveries")
      return structuredClone(delivery);
    if (path === "/api/v1/tasks/task/questions") return { items: questions };
    if (path === "/api/v1/workspaces/ws/tasks") return { items: tasks };
    if (path === "/api/v1/runs/run") return runDetail;
    if (path === "/api/v1/tasks/task/start")
      return { id: "run", thread_id: "thread" };
    const child = path.match(/^\/api\/v1\/tasks\/([^/]+)\/questions$/);
    if (child) return { items: childQuestions[child[1]] ?? [] };
    return {};
  });
}

function requestedPaths() {
  return request.mock.calls.map(([path]) => String(path));
}

it("answers revision-bound questions without task settings or source requests", async () => {
  mockRoutes({
    questions: [
      {
        id: "question",
        task_id: "task",
        task_revision: 1,
        blocking_scope: "范围",
        body: "是否包含反证？",
        options: ["包含"],
        recommended: "包含",
        status: "open",
        created_at: "2026-11-04T00:00:00Z",
      },
    ],
  });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  expect(
    await screen.findByRole("heading", { name: "完成可追溯研究" }),
  ).toBeVisible();
  await user.click(screen.getByRole("button", { name: "提交回答" }));
  expect(request).toHaveBeenCalledWith(
    "/api/v1/tasks/task/questions/question/answers",
    "POST",
    expect.objectContaining({ answer: "包含" }),
    { "If-Match": "1" },
  );

  expect(screen.queryByText("任务设置")).not.toBeInTheDocument();
  expect(screen.queryByText("补充与纠偏")).not.toBeInTheDocument();
  expect(screen.queryByText("目标版本")).not.toBeInTheDocument();
  expect(screen.queryByText("本任务协作预算")).not.toBeInTheDocument();
  expect(screen.queryByText("暂停任务")).not.toBeInTheDocument();
  expect(screen.queryByText("取消任务")).not.toBeInTheDocument();

  const paths = requestedPaths();
  expect(paths.some((path) => path.includes("/inbox"))).toBe(false);
  expect(paths.some((path) => path.includes("/revisions"))).toBe(false);
  expect(paths.some((path) => path.includes("/budget"))).toBe(false);
  expect(paths.some((path) => path.includes("/actions"))).toBe(false);
  expect(paths.some((path) => path.startsWith("/api/v1/threads/"))).toBe(false);
});

it("opens a dedicated acceptance dialog and records an exact artifact version", async () => {
  const view = makeView((current) => {
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  mockRoutes({
    view,
    delivery: deliveries({ runs: [succeededRun], artifacts: [artifact] }),
  });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  await user.click(screen.getAllByRole("button", { name: "去验收" })[0]);

  expect(screen.getByRole("dialog", { name: "人工验收" })).toBeVisible();
  expect(screen.getByRole("radio", { name: "研究报告 · v2" })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: "研究报告 · v2" })).toBeChecked();
  await user.selectOptions(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  await user.click(screen.getByRole("button", { name: "提交验收" }));

  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/tasks/task/completions",
      "POST",
      expect.objectContaining({
        evaluation_generation: 1,
        input_snapshot_digest: "goal-digest",
        candidate_refs: ["artifact:report@v2"],
        judge_member_id: "user",
        evidence_producer: "human_confirmed",
        results: [
          {
            criterion_id: "outcome",
            result: "passed",
            evidence: ["artifact:report@v2"],
            limitations: [],
          },
        ],
      }),
    ),
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("dialog", { name: "人工验收" }),
    ).not.toBeInTheDocument(),
  );
});

it("cancels acceptance without saving a decision", async () => {
  const view = makeView((current) => {
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  mockRoutes({
    view,
    delivery: deliveries({ runs: [succeededRun], artifacts: [artifact] }),
  });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  await user.click(screen.getAllByRole("button", { name: "去验收" })[0]);
  await user.click(screen.getByRole("button", { name: "取消" }));

  expect(
    screen.queryByRole("dialog", { name: "人工验收" }),
  ).not.toBeInTheDocument();
  expect(request).not.toHaveBeenCalledWith(
    "/api/v1/tasks/task/completions",
    "POST",
    expect.anything(),
  );
});

it("keeps one save in flight despite dismiss and reopen attempts", async () => {
  const view = makeView((current) => {
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  let finish: () => void = () => {};
  request.mockImplementation(async (path: string, method?: string) => {
    if (path === "/api/v1/tasks/task" && !method) return structuredClone(view);
    if (path === "/api/v1/tasks/task/deliveries")
      return deliveries({ runs: [succeededRun], artifacts: [artifact] });
    if (path === "/api/v1/tasks/task/questions") return { items: [] };
    if (path === "/api/v1/workspaces/ws/tasks") return { items: [] };
    if (path === "/api/v1/tasks/task/completions" && method === "POST")
      return new Promise<void>((resolve) => {
        finish = resolve;
      });
    return {};
  });
  const completionCalls = () =>
    request.mock.calls.filter(
      ([path, method]) =>
        path === "/api/v1/tasks/task/completions" && method === "POST",
    ).length;
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  await user.click(screen.getAllByRole("button", { name: "去验收" })[0]);
  const dialog = screen.getByRole("dialog", { name: "人工验收" });
  await user.selectOptions(
    within(dialog).getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  await user.click(within(dialog).getByRole("button", { name: "提交验收" }));
  expect(completionCalls()).toBe(1);

  await user.click(screen.getByRole("button", { name: "关闭窗口" }));
  fireEvent(dialog, new Event("cancel", { cancelable: true }));
  fireEvent.pointerDown(dialog);
  fireEvent.click(dialog);
  fireEvent.click(screen.getAllByRole("button", { name: "去验收" })[0]);

  expect(screen.getByRole("dialog", { name: "人工验收" })).toBeVisible();
  expect(completionCalls()).toBe(1);

  finish();
  await waitFor(() =>
    expect(
      screen.queryByRole("dialog", { name: "人工验收" }),
    ).not.toBeInTheDocument(),
  );
  expect(completionCalls()).toBe(1);
});

it("freezes the acceptance basis when a poll changes the task revision", async () => {
  const initialView = makeView((current) => {
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  const updatedView = structuredClone(initialView);
  updatedView.task.current_revision = 2;
  updatedView.task.evaluation_generation = 2;
  updatedView.revision.revision = 2;
  updatedView.revision.goal = "完成新一轮研究";
  updatedView.revision.acceptance = [
    {
      criterion_id: "outcome",
      statement: "新结论必须复核",
      required: true,
      kind: "outcome",
      evidence_requirements: ["exact_candidate"],
      evaluation_policy: "all_required",
    },
  ];
  updatedView.revision.digest = "new-digest";
  const updatedArtifact = { ...artifact, version: 3 };
  const updatedRun = {
    ...succeededRun,
    delivery: {
      ...succeededRun.delivery,
      artifact_refs: [{ id: "report", version: 3 }],
    },
  };
  let taskCalls = 0;
  let deliveryCalls = 0;
  request.mockImplementation(async (path: string) => {
    if (path === "/api/v1/tasks/task")
      return structuredClone(++taskCalls === 1 ? initialView : updatedView);
    if (path === "/api/v1/tasks/task/deliveries")
      return structuredClone(
        ++deliveryCalls === 1
          ? deliveries({ runs: [succeededRun], artifacts: [artifact] })
          : deliveries({ runs: [updatedRun], artifacts: [updatedArtifact] }),
      );
    if (path === "/api/v1/tasks/task/questions") return { items: [] };
    if (path === "/api/v1/workspaces/ws/tasks") return { items: [] };
    return {};
  });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  await user.click(screen.getAllByRole("button", { name: "去验收" })[0]);
  const opened = screen.getByRole("dialog", { name: "人工验收" });
  await user.selectOptions(
    within(opened).getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  expect(
    within(opened).getByRole("radio", { name: "研究报告 · v2" }),
  ).toBeChecked();

  // The page polls every 3 seconds; the goal and exact candidate set change
  // while the dialog is still open.
  await new Promise((resolve) => setTimeout(resolve, 3300));
  await waitFor(
    () =>
      expect(
        within(screen.getByRole("dialog", { name: "人工验收" })).getByText(
          "验收依据已更新",
        ),
      ).toBeVisible(),
    { timeout: 3000 },
  );

  const stale = screen.getByRole("dialog", { name: "人工验收" });
  expect(within(stale).getByText(/结论有来源/)).toBeVisible();
  expect(within(stale).queryByText(/新结论必须复核/)).not.toBeInTheDocument();
  expect(
    within(stale).getByRole("button", { name: "验收依据已变化" }),
  ).toBeDisabled();
  fireEvent.submit(within(stale).getByRole("form", { name: "人工验收表单" }));
  expect(request).not.toHaveBeenCalledWith(
    "/api/v1/tasks/task/completions",
    "POST",
    expect.anything(),
  );

  await user.click(within(stale).getByRole("button", { name: "重新核对" }));
  const refreshed = screen.getByRole("dialog", { name: "人工验收" });
  expect(within(refreshed).getByText(/新结论必须复核/)).toBeVisible();
  expect(
    within(refreshed).getByRole("radio", { name: "研究报告 · v3" }),
  ).toBeChecked();
  expect(
    within(refreshed).getByRole("combobox", {
      name: "判断：新结论必须复核",
    }),
  ).toHaveValue("inconclusive");
}, 15000);

it("uses an exact successful Delivery reply as the acceptance candidate", async () => {
  const view = makeView((current) => {
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  mockRoutes({
    view,
    delivery: deliveries({
      runs: [succeededRun],
      messages: [deliveredMessage],
    }),
  });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  expect(screen.getByText(/研究结论与来源说明/)).toBeVisible();
  await user.click(screen.getAllByRole("button", { name: "去验收" })[0]);
  await user.selectOptions(
    screen.getByRole("combobox", { name: "判断：结论有来源" }),
    "passed",
  );
  await user.click(screen.getByRole("button", { name: "提交验收" }));

  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/tasks/task/completions",
      "POST",
      expect.objectContaining({
        candidate_refs: ["message:reply"],
        results: [
          expect.objectContaining({
            evidence: ["message:reply"],
            result: "passed",
          }),
        ],
      }),
    ),
  );
});

it("does not offer acceptance when this task has no exact delivery", async () => {
  const view = makeView((current) => {
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  mockRoutes({
    view,
    delivery: deliveries({
      runs: [{ ...succeededRun, delivery: undefined }],
    }),
  });
  const openThread = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={openThread}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  expect(
    screen.queryByRole("button", { name: "去验收" }),
  ).not.toBeInTheDocument();
  expect(screen.getByText(/没有本任务成功交付的确切成果或回复/)).toBeVisible();
  await user.click(screen.getAllByRole("button", { name: "打开会话" })[0]);
  expect(openThread).toHaveBeenCalledWith("thread");
});

it("routes an exhausted budget to the conversation instead of a manual editor", async () => {
  const view = makeView((current) => {
    current.task.status = "waiting";
    current.task.waiting_reason = "budget";
    current.last_run_status = "canceled";
    current.last_run_id = "run";
    current.revision.acceptance = [];
  });
  mockRoutes({ view });
  const openThread = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={openThread}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  expect(screen.queryByText("调整预算")).not.toBeInTheDocument();
  expect(screen.queryByText("调整预算并继续")).not.toBeInTheDocument();
  expect(screen.queryByText("本任务协作预算")).not.toBeInTheDocument();
  expect(screen.queryByText("保存预算")).not.toBeInTheDocument();
  await user.click(screen.getAllByRole("button", { name: "打开会话" })[0]);
  expect(openThread).toHaveBeenCalledWith("thread");
  expect(requestedPaths().some((path) => path.includes("/budget"))).toBe(false);
});

it("shows one 已停止 fact and retries from the exact stopped step", async () => {
  const view = makeView((current) => {
    current.task.status = "ready";
    current.task.waiting_reason = "";
    current.last_run_status = "canceled";
    current.last_run_id = "run";
    current.revision.acceptance = [];
  });
  const stoppedRun = {
    ...succeededRun,
    status: "canceled",
    delivery: undefined,
  };
  mockRoutes({
    view,
    delivery: deliveries({ runs: [stoppedRun] }),
    runDetail: {
      run: stoppedRun,
      steps: [{ id: "step", status: "canceled" }],
      attempts: [],
      cursor: 0,
    },
  });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  expect(screen.getAllByText("已停止").length).toBeGreaterThan(0);
  expect(screen.queryByText("执行已停止")).not.toBeInTheDocument();
  expect(screen.getByText("上次执行在完成前停止")).toBeVisible();

  await user.click(screen.getAllByRole("button", { name: "从停止处继续" })[0]);
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/runs/run/retry",
      "POST",
      { step_id: "step" },
      { "Idempotency-Key": expect.any(String) },
    ),
  );
  expect(
    requestedPaths().some((path) => path.startsWith("/api/v1/threads/")),
  ).toBe(false);
});

it("ignores drafts, failed-run artifacts and other versions during acceptance", async () => {
  const view = makeView((current) => {
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  const draftArtifact = {
    ...artifact,
    id: "draft",
    version: 1,
    title: "失败草稿",
  };
  const failedRun = {
    ...succeededRun,
    id: "failed-run",
    status: "failed",
    delivery: undefined,
    error: "failed",
  };
  mockRoutes({
    view,
    delivery: deliveries({
      runs: [succeededRun, failedRun],
      artifacts: [artifact, draftArtifact],
      messages: [
        deliveredMessage,
        {
          ...deliveredMessage,
          id: "draft-reply",
          content: "未完成的草稿",
          status: "draft",
        },
      ],
    }),
  });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  expect(screen.getByText(/研究报告 · v2/)).toBeVisible();
  expect(screen.queryByText(/失败草稿/)).not.toBeInTheDocument();
  expect(screen.queryByText(/未完成的草稿/)).not.toBeInTheDocument();

  await user.click(screen.getAllByRole("button", { name: "去验收" })[0]);
  expect(screen.getByRole("radio", { name: "研究报告 · v2" })).toBeChecked();
  expect(
    screen.queryByRole("radio", { name: /失败草稿/ }),
  ).not.toBeInTheDocument();
  expect(screen.queryByRole("radio", { name: /会话交付/ })).toBeInTheDocument();
});

it("expands the full original goal without restoring goal editing", async () => {
  const goal = "完成可追溯研究。\n第一段保留全部约束。\n第二段说明原始目标。";
  const view = makeView((current) => {
    current.revision.goal = goal;
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  mockRoutes({
    view,
    delivery: deliveries({ runs: [succeededRun], artifacts: [artifact] }),
  });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: /完成可追溯研究/ });
  const toggle = screen.getByText("展开完整要求");
  const details = toggle.closest("details");
  expect(details).not.toBeNull();
  const full = within(details as HTMLElement).getByText(/第二段说明原始目标/);
  expect(full).not.toBeVisible();
  await user.click(toggle);
  expect(full).toBeVisible();
  expect(screen.queryByText("编辑目标")).not.toBeInTheDocument();
  expect(screen.queryByText(/生成 v2/)).not.toBeInTheDocument();
});

it("opens the real delivered artifact and its file identity", async () => {
  const view = makeView((current) => {
    current.last_run_status = "succeeded";
    current.last_run_id = "run";
  });
  mockRoutes({
    view,
    delivery: deliveries({ runs: [succeededRun], artifacts: [artifact] }),
  });
  const openArtifact = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={openArtifact}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  expect(screen.getByText("report.txt")).toBeVisible();
  expect(screen.getByText("2 KB")).toBeVisible();
  await user.click(screen.getByRole("button", { name: /研究报告 · v2/ }));
  expect(openArtifact).toHaveBeenCalledWith(
    expect.objectContaining({ id: "report", version: 2 }),
  );
});

it("starts a task that has a conversation but no execution yet", async () => {
  const view = makeView((current) => {
    current.task.status = "ready";
    current.revision.acceptance = [];
  });
  mockRoutes({ view });
  const openThread = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={openThread}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  await user.click(screen.getAllByRole("button", { name: "开始执行" })[0]);
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/tasks/task/start",
      "POST",
      {},
    ),
  );
  expect(openThread).toHaveBeenCalledWith("thread");
});

it("keeps current and historical acceptance records readable", async () => {
  const view = makeView((current) => {
    current.task.status = "completed";
    current.task.evaluation_status = "complete";
    current.completions = [
      {
        id: "current",
        task_id: "task",
        task_revision: 1,
        evaluation_generation: 1,
        input_snapshot_digest: "goal-digest",
        candidate_refs: ["artifact:report@v2"],
        results: [
          {
            criterion_id: "outcome",
            result: "passed",
            evidence: ["artifact:report@v2"],
            limitations: [],
          },
        ],
        unresolved: [],
        evidence_producer: "human_confirmed",
        judge_member_id: "user",
        subject_digest: "subject",
        validity: "current",
        created_at: "2026-11-04T01:00:00Z",
      },
      {
        id: "history",
        task_id: "task",
        task_revision: 0,
        evaluation_generation: 1,
        input_snapshot_digest: "old-digest",
        candidate_refs: [],
        results: [],
        unresolved: [],
        evidence_producer: "member_reported",
        judge_member_id: "owner",
        subject_digest: "old-subject",
        validity: "superseded",
        created_at: "2026-11-03T01:00:00Z",
      },
    ];
  });
  mockRoutes({ view });
  const user = userEvent.setup();
  render(
    <TaskDetail
      taskID="task"
      agents={[]}
      onOpenThread={() => {}}
      onOpenArtifact={() => {}}
    />,
  );
  await screen.findByRole("heading", { name: "完成可追溯研究" });
  expect(screen.getByText("2 条验收记录")).toBeVisible();
  await user.click(screen.getByText("2 条验收记录"));
  expect(screen.getByText("当前版本验收记录")).toBeVisible();
  expect(screen.getByText("历史验收 · 目标 v0")).toBeVisible();
});
