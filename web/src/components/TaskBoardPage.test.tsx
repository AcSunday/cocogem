import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { TaskBoard } from "../api/client";
import { TaskBoardPage } from "./TaskBoardPage";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
};
HTMLDialogElement.prototype.close = function () {
  this.open = false;
};

afterEach(() => {
  cleanup();
  request.mockReset();
  sessionStorage.clear();
});

const task = (
  id: string,
  workspaceID: string,
  status: string,
  lastRunStatus?: string,
) =>
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
    last_run_status: lastRunStatus,
  }) as unknown as NonNullable<TaskBoard["tasks"]>[number];

it("shows tasks from every workspace and opens the owning project", async () => {
  request.mockResolvedValue({
    tasks: [
      task("订单幂等", "orders", "active", "running"),
      task("研究缓存", "research", "waiting"),
    ],
    workspaces: { orders: "订单系统", research: "缓存研究" },
    needs_attention: [],
  } satisfies TaskBoard);
  const open = vi.fn();
  const user = userEvent.setup();
  render(<TaskBoardPage onOpenTask={open} />);
  expect(await screen.findByRole("heading", { name: "任务清单" })).toBeVisible();
  expect(
    screen.getAllByText("订单系统").find((element) => element.tagName !== "OPTION"),
  ).toBeVisible();
  expect(
    screen.getAllByText("缓存研究").find((element) => element.tagName !== "OPTION"),
  ).toBeVisible();
  await user.click(screen.getByRole("button", { name: /研究缓存/ }));
  expect(open).toHaveBeenCalledWith("research", "研究缓存");
});

it("filters by project and status, and keeps the view and filters on return", async () => {
  const delivered = task("等待验收", "orders", "ready", "succeeded");
  const accepted = task("已经验收", "orders", "completed", "succeeded");
  request.mockResolvedValue({
    tasks: [delivered, accepted],
    workspaces: { orders: "订单系统", research: "缓存研究" },
    needs_attention: [],
  } satisfies TaskBoard);
  const user = userEvent.setup();
  const first = render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  const review = screen.getByRole("region", { name: "待验收任务" });
  expect(review).toHaveTextContent("等待验收");
  expect(
    screen.queryByRole("button", { name: /已经验收/ }),
  ).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: /筛选项目/ }));
  await user.click(screen.getByRole("option", { name: "订单系统" }));
  expect(screen.getByRole("button", { name: /筛选项目/ })).toHaveTextContent(
    "订单系统",
  );
  expect(
    screen.queryByRole("region", { name: "已验收任务" }),
  ).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: /查看已验收任务/ }),
  ).toBeVisible();

  await user.click(screen.getByRole("button", { name: "看板" }));
  expect(screen.getByRole("button", { name: "清单" })).toBeVisible();
  expect(screen.getByRole("region", { name: "待验收任务" })).toHaveTextContent(
    "等待验收",
  );

  await user.click(screen.getByRole("button", { name: "清单" }));
  await user.click(screen.getByRole("button", { name: /筛选状态/ }));
  await user.click(screen.getByRole("option", { name: "已验收" }));
  const acceptedGroup = screen.getByRole("region", { name: "已验收任务" });
  expect(acceptedGroup).toHaveTextContent("已经验收");
  expect(
    screen.queryByRole("region", { name: "待验收任务" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: /查看已验收任务/ }),
  ).not.toBeInTheDocument();

  first.unmount();
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  expect(screen.getByRole("button", { name: /筛选状态/ })).toHaveTextContent(
    "已验收",
  );
  expect(screen.getByRole("button", { name: /筛选项目/ })).toHaveTextContent(
    "订单系统",
  );
  expect(screen.getByRole("button", { name: "看板" })).toBeVisible();
});

it("keeps a running task read-only without a manual pause control", async () => {
  const running = task("运行中的任务", "orders", "active", "running");
  request.mockResolvedValue({
    tasks: [running],
    workspaces: { orders: "订单系统" },
    needs_attention: [],
  } satisfies TaskBoard);
  const openThread = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskBoardPage onOpenTask={() => {}} onOpenThread={openThread} />,
  );
  await screen.findByRole("heading", { name: "任务清单" });
  expect(screen.queryByRole("button", { name: "暂停" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "查看" }));
  expect(openThread).toHaveBeenCalledWith("orders", "thread-运行中的任务");
  expect(request).not.toHaveBeenCalledWith(
    expect.stringContaining("/actions"),
    "POST",
    expect.anything(),
  );
});

it("permanently deletes selected tasks only after confirmation", async () => {
  const first = task("任务甲", "orders", "waiting");
  const second = task("任务乙", "orders", "waiting");
  let board: TaskBoard = {
    tasks: [first, second],
    workspaces: { orders: "订单系统" },
    needs_attention: [],
  };
  request.mockImplementation(
    async (path: string, method?: string, body?: unknown) => {
      if (path === "/api/v1/tasks/delete") {
        expect(method).toBe("POST");
        expect(body).toEqual({
          task_ids: expect.arrayContaining(["任务甲", "任务乙"]),
        });
        board = { ...board, tasks: [] };
        return { deleted: ["任务甲", "任务乙"], count: 2 };
      }
      if (path === "/api/v1/tasks") return board;
      return { items: [] };
    },
  );
  const user = userEvent.setup();
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  await user.click(screen.getByRole("button", { name: "选择" }));
  await user.click(screen.getByRole("button", { name: "全选当前" }));
  expect(screen.getByText("已选 2 项")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "永久删除所选" }));
  const dialog = screen.getByRole("dialog", { name: "确认永久删除任务" });
  expect(dialog).toHaveTextContent("无法恢复");
  await user.click(within(dialog).getByRole("button", { name: "永久删除" }));
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/tasks/delete",
      "POST",
      { task_ids: expect.arrayContaining(["任务甲", "任务乙"]) },
    ),
  );
  expect(await screen.findByRole("status")).toHaveTextContent(
    "已永久删除 2 个任务",
  );
  expect(
    screen.queryByRole("button", { name: /任务甲/ }),
  ).not.toBeInTheDocument();
});

it("puts a stopped run in the attention stream with a real next action", async () => {
  const stopped = task("停止的任务", "orders", "ready", "canceled");
  request.mockResolvedValue({
    tasks: [stopped],
    workspaces: { orders: "订单系统" },
    needs_attention: [],
  } satisfies TaskBoard);
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  const attention = screen.getByRole("region", { name: "需要你任务" });
  expect(attention).toHaveTextContent("执行已停止");
  expect(attention).toHaveTextContent("上次执行在完成前停止");
  expect(attention).toHaveTextContent("处理");
  expect(screen.queryByText(/满足条件后自动继续/)).not.toBeInTheDocument();
});

it("shows a completed task that needs revalidation as review work", async () => {
  const revalidating = task("需要复验", "orders", "completed", "succeeded");
  revalidating.task.evaluation_status = "needs_revalidation";
  request.mockResolvedValue({
    tasks: [revalidating],
    workspaces: { orders: "订单系统" },
    needs_attention: [],
  } satisfies TaskBoard);
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  const review = screen.getByRole("region", { name: "待验收任务" });
  expect(review).toHaveTextContent("需要复验");
  expect(review).toHaveTextContent("待复验");
  expect(
    screen.queryByRole("region", { name: "已验收任务" }),
  ).not.toBeInTheDocument();
});

it("keeps the standalone task header controls aligned and switches views", async () => {
  request.mockResolvedValue({
    tasks: [task("看板任务", "orders", "active", "succeeded")],
    workspaces: { orders: "订单系统" },
    needs_attention: [],
  } satisfies TaskBoard);
  const user = userEvent.setup();
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  const select = screen.getByRole("button", { name: "选择" });
  const list = screen.getByRole("button", { name: "清单" });
  const board = screen.getByRole("button", { name: "看板" });
  const create = screen.getByRole("button", { name: /新建任务/ });
  for (const control of [select, list, board, create]) {
    expect(control.className).toMatch(/_task-head-control_/);
  }
  expect(list).toHaveAttribute("aria-pressed", "true");
  expect(board).toHaveAttribute("aria-pressed", "false");
  expect(
    screen.getByRole("group", { name: "任务视图" }),
  ).toContainElement(board);
  await user.click(board);
  expect(board).toHaveAttribute("aria-pressed", "true");
  expect(list).toHaveAttribute("aria-pressed", "false");
  expect(document.querySelector('[class*="task-board-surface"]')).not.toBeNull();
});

it("renders the view switch in the supplied header and restores it after selection", async () => {
  request.mockResolvedValue({
    tasks: [task("看板任务", "orders", "waiting")],
    workspaces: { orders: "订单系统" },
    needs_attention: [],
  } satisfies TaskBoard);
  const target = document.createElement("div");
  document.body.append(target);
  const user = userEvent.setup();
  const page = render(
    <TaskBoardPage onOpenTask={() => {}} viewSwitchTarget={target} />,
  );
  await screen.findByRole("heading", { name: "任务清单" });

  const switchGroup = within(target).getByRole("group", { name: "任务视图" });
  await user.click(within(switchGroup).getByRole("button", { name: "看板" }));
  expect(within(switchGroup).getByRole("button", { name: "看板" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await user.click(within(switchGroup).getByRole("button", { name: "清单" }));
  await user.click(screen.getByRole("button", { name: "选择" }));
  expect(within(target).queryByRole("group", { name: "任务视图" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "退出选择" }));
  expect(within(target).getByRole("group", { name: "任务视图" })).toBeVisible();

  page.unmount();
  expect(target).toBeEmptyDOMElement();
  target.remove();
});

it("renders board cards with only title, status, owner and one action", async () => {
  const review = task("看板任务", "orders", "active", "succeeded");
  review.task.title = "看板任务";
  review.revision.goal = "一段不应出现在卡片里的完整目标描述";
  const unowned = task("无主任务", "orders", "waiting");
  unowned.task.owner_member_id = "missing";
  request.mockResolvedValue({
    tasks: [review, unowned],
    workspaces: { orders: "订单系统" },
    members: { owner: "橘猫" },
    needs_attention: [],
  } satisfies TaskBoard);
  const user = userEvent.setup();
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  await user.click(screen.getByRole("button", { name: "看板" }));

  const card = screen
    .getByRole("button", { name: "打开任务：看板任务" })
    .closest("article")!;
  expect(card).toHaveTextContent("看板任务");
  expect(card).toHaveTextContent("待验收");
  expect(card).toHaveTextContent("橘猫");
  expect(within(card).queryByText("订单系统")).not.toBeInTheDocument();
  expect(within(card).queryByText(/不应出现在卡片里/)).not.toBeInTheDocument();
  expect(within(card).queryByText("当前")).not.toBeInTheDocument();
  expect(within(card).queryByText("下一步")).not.toBeInTheDocument();
  expect(within(card).queryByText(/验收 \d+\/\d+/)).not.toBeInTheDocument();
  const buttons = within(card).getAllByRole("button");
  expect(buttons).toHaveLength(2);
  for (const button of buttons) {
    expect(button.querySelector("button")).toBeNull();
  }
  expect(within(card).getByRole("button", { name: "验收" })).toBeVisible();

  const noOwner = screen
    .getByRole("button", { name: "打开任务：无主任务" })
    .closest("article")!;
  expect(noOwner).toHaveTextContent("未分配");
  const empty = screen.getByRole("region", { name: "待办任务" });
  expect(empty).toHaveTextContent("暂无任务");
  expect(screen.getByRole("region", { name: "待验收任务" }).className).toMatch(
    /_task-column-review_/,
  );
  expect(screen.getByRole("region", { name: "等待处理任务" }).className).toMatch(
    /_task-column-waiting_/,
  );
  expect(screen.getByRole("region", { name: "进行中任务" }).className).toMatch(
    /_task-column-active_/,
  );
});

it("keeps every one of 20 same-column cards in the board DOM for vertical scrolling", async () => {
  const tasks = Array.from({ length: 20 }, (_, index) =>
    task(`同列任务 ${index + 1}`, "orders", "active", "succeeded"),
  );
  request.mockResolvedValue({
    tasks,
    workspaces: { orders: "订单系统" },
    members: { owner: "橘猫" },
    needs_attention: [],
  } satisfies TaskBoard);
  const user = userEvent.setup();
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  await user.click(screen.getByRole("button", { name: "看板" }));
  const review = screen.getByRole("region", { name: "待验收任务" });
  const cards = within(review).getAllByRole("article");
  expect(cards).toHaveLength(20);
  for (const card of cards) {
    expect(within(card).getByRole("button", { name: /打开任务/ })).toBeVisible();
    expect(within(card).getByRole("button", { name: "验收" })).toBeVisible();
  }
});

it("opens details from the title but executes a card action alone", async () => {
  const running = task("运行中的任务", "orders", "active", "running");
  request.mockResolvedValue({
    tasks: [running],
    workspaces: { orders: "订单系统" },
    members: { owner: "橘猫" },
    needs_attention: [],
  } satisfies TaskBoard);
  const openTask = vi.fn();
  const openThread = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskBoardPage onOpenTask={openTask} onOpenThread={openThread} />,
  );
  await screen.findByRole("heading", { name: "任务清单" });
  await user.click(screen.getByRole("button", { name: "看板" }));
  await user.click(screen.getByRole("button", { name: "查看" }));
  expect(openThread).toHaveBeenCalledWith("orders", "thread-运行中的任务");
  expect(openTask).not.toHaveBeenCalled();

  await user.click(
    screen.getByRole("button", { name: "打开任务：运行中的任务" }),
  );
  expect(openTask).toHaveBeenCalledWith("orders", "运行中的任务");
  expect(openThread).toHaveBeenCalledOnce();
});

it("labels an exhausted-budget task without a conversation as 查看任务", async () => {
  const waiting = task("预算耗尽无会话", "orders", "active");
  waiting.task.waiting_reason = "budget";
  waiting.task.thread_id = "";
  request.mockResolvedValue({
    tasks: [waiting],
    workspaces: { orders: "订单系统" },
    members: { owner: "橘猫" },
    needs_attention: [],
  } satisfies TaskBoard);
  const openTask = vi.fn();
  const openThread = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskBoardPage onOpenTask={openTask} onOpenThread={openThread} />,
  );
  await screen.findByRole("heading", { name: "任务清单" });
  await user.click(screen.getByRole("button", { name: "看板" }));
  expect(screen.queryByText("调整预算")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "查看任务" }));
  expect(openTask).toHaveBeenCalledWith("orders", "预算耗尽无会话");
  expect(openThread).not.toHaveBeenCalled();
});

it("leaves multi-select from the header without deleting anything", async () => {
  request.mockResolvedValue({
    tasks: [task("任务甲", "orders", "waiting")],
    workspaces: { orders: "订单系统" },
    needs_attention: [],
  } satisfies TaskBoard);
  const user = userEvent.setup();
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  await user.click(screen.getByRole("button", { name: "选择" }));
  expect(screen.getByRole("group", { name: "任务选择" })).toBeVisible();
  await user.click(screen.getByRole("button", { name: "退出选择" }));
  expect(
    screen.queryByRole("group", { name: "任务选择" }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "选择" })).toBeVisible();
  expect(
    screen.queryByRole("checkbox", { name: /选择任务/ }),
  ).not.toBeInTheDocument();
});

it("sends an exhausted-budget task to its conversation instead of a budget form", async () => {
  const waiting = task("预算耗尽", "orders", "active");
  waiting.task.waiting_reason = "budget";
  request.mockResolvedValue({
    tasks: [waiting],
    workspaces: { orders: "订单系统" },
    members: { owner: "橘猫" },
    needs_attention: [],
  } satisfies TaskBoard);
  const openThread = vi.fn();
  const openTask = vi.fn();
  const user = userEvent.setup();
  render(
    <TaskBoardPage onOpenTask={openTask} onOpenThread={openThread} />,
  );
  await screen.findByRole("heading", { name: "任务清单" });
  expect(screen.queryByText("调整预算")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "打开会话" })).toBeVisible();
  await user.click(screen.getByRole("button", { name: "打开会话" }));
  expect(openThread).toHaveBeenCalledWith("orders", "thread-预算耗尽");
  expect(openTask).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "看板" }));
  expect(screen.queryByText("调整预算")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "打开会话" })).toBeVisible();
});

it("keeps a long card title complete and available to assistive tech", async () => {
  const title =
    "一个非常长的任务标题会占满看板卡片的三行空间但仍然保留完整的可访问名称";
  request.mockResolvedValue({
    tasks: [task(title, "orders", "active", "running")],
    workspaces: { orders: "订单系统" },
    members: { owner: "橘猫" },
    needs_attention: [],
  } satisfies TaskBoard);
  const user = userEvent.setup();
  render(<TaskBoardPage onOpenTask={() => {}} />);
  await screen.findByRole("heading", { name: "任务清单" });
  await user.click(screen.getByRole("button", { name: "看板" }));
  const open = screen.getByRole("button", { name: `打开任务：${title}` });
  expect(open).toHaveTextContent(title);
  expect(open).toHaveAttribute("title", title);
});
