import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Agent, Event } from "../api/client";
import {
  MemberToolActivity,
  PendingToolAuthorization,
  ToolActivityBlock,
} from "./MemberToolActivity";
import { projectToolActivity } from "../lib/toolActivity";

afterEach(() => {
  cleanup();
  localStorage.clear();
});

const event = (
  seq: number,
  type: string,
  data: Record<string, unknown>,
  extra: Partial<Event> = {},
): Event => ({
  schema_version: 1,
  run_id: "run",
  seq,
  type,
  occurred_at: "",
  agent_id: "member",
  step_id: "step",
  data,
  ...extra,
});

const groupOf = (events: Event[], options = {}) =>
  projectToolActivity(events, options)[0];

const agent: Agent = {
  id: "member",
  workspace_id: "ws",
  display_name: "研究成员",
  handle: "member",
  aliases: [],
  role_description: "",
  model: "default",
  tools: [],
  disabled: false,
  revision: 1,
};

it("starts collapsed and expands to the real tool receipt", async () => {
  const user = userEvent.setup();
  const group = groupOf([
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "run_test",
      input: { path: "." },
    }),
    event(2, "tool_finished", {
      tool_call_id: "call",
      name: "run_test",
      status: "ok",
      duration_ms: 42,
      exit_code: 0,
      output: "ok  internal/tools",
    }),
  ]);
  render(<ToolActivityBlock group={group} />);
  const toggle = screen.getByRole("button", { name: /执行测试/ });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByText("ok  internal/tools")).not.toBeInTheDocument();

  await user.click(toggle);
  expect(toggle).toHaveAttribute("aria-expanded", "true");
  const activity = screen.getByTestId("tool-activity");
  expect(within(activity).getByText(/运行测试 · \.$/)).toBeVisible();
  expect(within(activity).getByText("完成")).toBeVisible();
  expect(within(activity).getAllByText("42 ms")[0]).toBeVisible();
  expect(within(activity).getByText("exit 0")).toBeVisible();
  expect(within(activity).getByText(/ok\s+internal\/tools/)).toBeVisible();
});

it("supports keyboard toggling and exposes the expanded state", async () => {
  const user = userEvent.setup();
  const group = groupOf([
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "read_file",
      input: { path: "a.go" },
    }),
  ]);
  render(<ToolActivityBlock group={group} />);
  const toggle = screen.getByRole("button", { name: /读取 1 个文件/ });
  toggle.focus();
  expect(toggle).toHaveFocus();
  await user.keyboard("{Enter}");
  expect(toggle).toHaveAttribute("aria-expanded", "true");
  await user.keyboard(" ");
  expect(toggle).toHaveAttribute("aria-expanded", "false");
});

it("keeps the reader's expansion while events stream in and after a remount", async () => {
  const user = userEvent.setup();
  const started = event(1, "tool_started", {
    tool_call_id: "call",
    name: "read_file",
    input: { path: "a.go" },
  });
  const finished = event(2, "tool_finished", {
    tool_call_id: "call",
    name: "read_file",
    status: "ok",
    output: "package a",
  });
  const view = (events: Event[]) => (
    <ToolActivityBlock group={groupOf(events)} />
  );
  const first = render(view([started]));
  const toggle = screen.getByRole("button", { name: /读取 1 个文件/ });
  await user.click(toggle);
  expect(toggle).toHaveAttribute("aria-expanded", "true");

  // A finish arrives while the reader is looking at the open block.
  first.rerender(view([started, finished]));
  expect(
    screen.getByRole("button", { name: /读取 1 个文件/ }),
  ).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByText("package a")).toBeVisible();

  first.unmount();
  render(view([started, finished]));
  expect(
    screen.getByRole("button", { name: /读取 1 个文件/ }),
  ).toHaveAttribute("aria-expanded", "true");

  await user.click(screen.getByRole("button", { name: /读取 1 个文件/ }));
  cleanup();
  render(view([started, finished]));
  expect(
    screen.getByRole("button", { name: /读取 1 个文件/ }),
  ).toHaveAttribute("aria-expanded", "false");
});

it("renders pending authorization in its own layer with explicit actions", () => {
  const group = groupOf(
    [
      event(1, "tool_started", {
        tool_call_id: "native-1",
        name: "Bash",
        origin: "native",
        status: "awaiting_permission",
        permission_request_id: "request-1",
        normalized_resource_scope: "command: npm publish",
        reason: "发布到公共 registry",
        input: { command: "npm publish" },
      }),
    ],
    { runStatus: "running" },
  );
  const permissionSlot = vi.fn((prompt) => (
    <button type="button">仅本次允许 {prompt.requestID}</button>
  ));
  render(
    <>
      <PendingToolAuthorization
        groups={[group]}
        agents={[agent]}
        permissionSlot={permissionSlot}
      />
      <ToolActivityBlock group={group} />
    </>,
  );
  const layer = screen.getByLabelText("待处理授权");
  const notice = within(layer).getByRole("status");
  expect(notice).toHaveTextContent("等待授权 · Bash（npm publish）");
  expect(notice).toHaveTextContent("成员 研究成员");
  expect(notice).toHaveTextContent("command: npm publish");
  expect(notice).toHaveTextContent("发布到公共 registry");
  expect(notice).toHaveAttribute("data-permission-request-id", "request-1");
  expect(
    within(layer).getByRole("button", { name: "仅本次允许 request-1" }),
  ).toBeVisible();
  expect(permissionSlot).toHaveBeenCalledWith(
    expect.objectContaining({
      requestID: "request-1",
      memberName: "研究成员",
      toolName: "Bash",
      target: "npm publish",
      scope: "command: npm publish",
    }),
  );
  // The activity block still folds its log, but holds no decision control.
  const toggle = screen.getByRole("button", { name: /等待授权/ });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(
    within(screen.getByTestId("tool-activity")).queryByRole("button", {
      name: "仅本次允许 request-1",
    }),
  ).toBeNull();
});

it("explains the approval boundary when no permission slot is wired yet", () => {
  const group = groupOf(
    [
      event(1, "tool_started", {
        tool_call_id: "native-1",
        name: "Bash",
        status: "awaiting_permission",
        input: { command: "rm -rf build" },
      }),
    ],
    { runStatus: "running" },
  );
  render(<PendingToolAuthorization groups={[group]} />);
  expect(screen.getByText("批准后才会执行")).toBeVisible();
});

it("never turns a start-only native call into a success", async () => {
  const user = userEvent.setup();
  const group = groupOf(
    [
      event(1, "tool_started", {
        tool_call_id: "native-1",
        name: "Read",
        origin: "native",
        input: { file_path: "a.go" },
      }),
    ],
    { runStatus: "succeeded" },
  );
  render(<ToolActivityBlock group={group} />);
  const toggle = screen.getByRole("button", { name: /读取 1 个文件/ });
  expect(toggle).not.toHaveTextContent("完成");
  expect(toggle).toHaveTextContent("结果未知");
  // Unknown work is reported, not silently omitted, once the block is read.
  await user.click(toggle);
  expect(screen.getByText(/未收到完成记录/)).toBeVisible();
  expect(screen.queryByText("完成")).not.toBeInTheDocument();
});

it("offers on-demand history for an unfinished call from an ended attempt", async () => {
  const user = userEvent.setup();
  const group = groupOf(
    [
      event(7, "attempt_started", { id: "attempt-a", number: 1 }),
      event(8, "tool_started", {
        tool_call_id: "call",
        name: "read_file",
        input: {},
      }),
      event(9, "attempt_finished", {
        id: "attempt-a",
        number: 1,
        status: "interrupted",
      }),
    ],
    { runStatus: "running" },
  );
  const onHistory = vi.fn();
  render(<ToolActivityBlock group={group} onHistory={onHistory} />);
  // The unfinished call is visible in the collapsed summary and the history
  // link is reachable without expanding the block.
  expect(
    screen.getByRole("button", { name: /读取 1 个文件/ }),
  ).toHaveTextContent("结果未知");
  await user.click(screen.getByRole("button", { name: "查看历史" }));
  expect(onHistory).toHaveBeenCalledWith(
    expect.objectContaining({ seq: 8, toolCallID: "call" }),
  );
});

it("merges the awaiting update into the one executed activity", async () => {
  const group = groupOf(
    [
      event(1, "tool_started", {
        tool_call_id: "native-1",
        name: "Bash",
        status: "running",
        input: { command: "npm publish" },
      }),
      event(2, "tool_finished", {
        tool_call_id: "native-1",
        name: "Bash",
        status: "awaiting_permission",
        permission_request_id: "request-1",
      }),
      event(3, "tool_finished", {
        tool_call_id: "native-1",
        name: "Bash",
        status: "succeeded",
        output: "published",
      }),
    ],
    { runStatus: "running" },
  );
  render(
    <>
      <PendingToolAuthorization
        groups={[group]}
        permissionSlot={() => <button type="button">允许</button>}
      />
      <ToolActivityBlock group={group} />
    </>,
  );
  expect(screen.queryByLabelText("待处理授权")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "允许" }),
  ).not.toBeInTheDocument();
  const toggle = screen.getByRole("button", { name: /执行 1 条命令/ });
  expect(toggle).toBeVisible();
  await userEvent.click(toggle);
  expect(screen.getAllByRole("listitem")).toHaveLength(1);
  expect(screen.getByText("完成")).toBeVisible();
  expect(screen.queryByText("等待授权")).not.toBeInTheDocument();
  expect(screen.getByText("published")).toBeVisible();
});

it("renders parallel members as separate activity blocks", () => {
  const events = [
    event(
      1,
      "tool_started",
      { tool_call_id: "a", name: "read_file", input: {} },
      { agent_id: "one", step_id: "step-one" },
    ),
    event(
      2,
      "tool_finished",
      { tool_call_id: "a", name: "read_file", status: "ok" },
      { agent_id: "one", step_id: "step-one" },
    ),
    event(
      3,
      "tool_started",
      { tool_call_id: "b", name: "web_search", input: { query: "x" } },
      { agent_id: "two", step_id: "step-two" },
    ),
    event(
      4,
      "tool_finished",
      {
        tool_call_id: "b",
        name: "web_search",
        status: "error",
        output: "blocked",
      },
      { agent_id: "two", step_id: "step-two" },
    ),
  ];
  const groups = projectToolActivity(events);
  render(
    <MemberToolActivity
      groups={groups}
      agents={[
        { ...agent, id: "one", display_name: "研究成员", handle: "one" },
        {
          ...agent,
          id: "two",
          display_name: "检索成员",
          handle: "searcher",
        },
      ]}
    />,
  );
  expect(
    screen.getByRole("region", { name: "研究成员的工具活动" }),
  ).toHaveTextContent("读取 1 个文件");
  expect(
    screen.getByRole("region", { name: "检索成员的工具活动" }),
  ).toHaveTextContent("联网搜索 1 次 · 1 个失败");
  expect(screen.queryByTestId("latest-tool")).not.toBeInTheDocument();
});
