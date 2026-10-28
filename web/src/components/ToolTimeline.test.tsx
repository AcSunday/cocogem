import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { ToolTimeline } from "./ToolTimeline";
import type { Agent, Event, Source } from "../api/client";

afterEach(cleanup);it("shows the committed upstream replies, exact artifact versions and captured sources", async () => {
  const agent: Agent = {
    id: "critic",
    workspace_id: "ws",
    display_name: "旧缅因猫",
    handle: "maine",
    aliases: [],
    role_description: "",
    model: "default",
    tools: [],
    disabled: false,
    revision: 1,
  };
  const event: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 1,
    type: "handoff_prepared",
    occurred_at: "",
    agent_id: "critic",
    data: {
      instruction: "核对上游结论",
      original_task: "比较两种协作方案",
      from_steps: ["internal-step-id"],
      messages: [
        {
          id: "message",
          agent_name: "布偶猫",
          content: "方案一节省时间，但证据仍有不足。",
        },
      ],
      artifacts: [{ id: "artifact", version: 2, title: "方案分析" }],
      source_ids: ["source"],
    },
  };
  const source = { id: "source", title: "公开研究资料" } as Source;
  const onSource = vi.fn();
  render(
    <ToolTimeline
      agents={[agent]}
      events={[event]}
      sources={[source]}
      onSource={onSource}
    />,
  );
  expect(screen.getByText("旧缅因猫 (@maine)")).toBeVisible();
  await userEvent.click(screen.getByText("收到工作包"));
  expect(screen.getByText("方案一节省时间，但证据仍有不足。")).toBeVisible();
  expect(screen.getByText("方案分析 · v2")).toBeVisible();
  expect(screen.queryByText(/internal-step-id/)).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "公开研究资料" }));
  expect(onSource).toHaveBeenCalledWith("source");
});

const member: Agent = {
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

const event = (seq: number, type: string, data: unknown): Event => ({
  schema_version: 1,
  run_id: "run",
  seq,
  type,
  occurred_at: "",
  agent_id: "member",
  step_id: "step",
  data,
});

it("reports known, partial and unknown usage without counting gaps as zero", () => {
  const usage = event(1, "usage_updated", {
    known: true,
    input_tokens: 12,
    output_tokens: 3,
  });
  const { rerender } = render(
    <ToolTimeline agents={[member]} events={[usage]} />,
  );
  expect(screen.getByText("12 / 3 tokens")).toBeVisible();
  rerender(
    <ToolTimeline
      agents={[member]}
      events={[usage, event(2, "usage_updated", { known: false })]}
    />,
  );
  expect(screen.getByText("部分用量：12 / 3 tokens（其余未知）")).toBeVisible();
  rerender(
    <ToolTimeline
      agents={[member]}
      events={[event(3, "usage_updated", { known: false })]}
    />,
  );
  expect(screen.getByText("用量未知")).toBeVisible();
});

it("offers history instead of claiming an unfinished tool still runs", async () => {
  const onHistory = vi.fn();
  const started = event(5, "tool_started", {
    tool_call_id: "call",
    name: "read_file",
    input: {},
  });
  const { rerender } = render(
    <ToolTimeline
      agents={[member]}
      events={[started]}
      runStatus="running"
      onHistory={onHistory}
    />,
  );
  expect(screen.getByText("执行中")).toBeVisible();
  rerender(
    <ToolTimeline
      agents={[member]}
      events={[started]}
      runStatus="succeeded"
      onHistory={onHistory}
    />,
  );
  const history = screen.getByRole("button", {
    name: "查看这次工具调用的历史记录",
  });
  expect(screen.queryByText("执行中")).not.toBeInTheDocument();
  await userEvent.click(history);
  expect(onHistory).toHaveBeenCalledWith(4);
});

it("matches a reused call id to the first finish after its own start", () => {
  const startedBefore = event(1, "tool_started", {
    tool_call_id: "call",
    name: "read_file",
    input: {},
  });
  const finishedBefore = event(2, "tool_finished", {
    tool_call_id: "call",
    name: "read_file",
    status: "ok",
    duration_ms: 3,
    output: "旧尝试",
  });
  const startedNow = event(3, "tool_started", {
    tool_call_id: "call",
    name: "read_file",
    input: {},
  });
  render(
    <ToolTimeline
      agents={[member]}
      events={[startedBefore, finishedBefore, startedNow]}
      runStatus="running"
    />,
  );
  expect(screen.getAllByText("完成")).toHaveLength(1);
  expect(screen.getByText("执行中")).toBeVisible();
});

it("does not pair a finish with an earlier attempt that reused the same call id", () => {
  const events = [
    event(1, "attempt_started", {
      id: "attempt-a",
      number: 1,
      status: "running",
    }),
    event(2, "tool_started", {
      tool_call_id: "native-1",
      name: "read_file",
      input: { path: "first" },
    }),
    event(3, "attempt_finished", {
      id: "attempt-a",
      number: 1,
      status: "interrupted",
    }),
    event(4, "attempt_started", {
      id: "attempt-b",
      number: 2,
      status: "running",
    }),
    event(5, "tool_started", {
      tool_call_id: "native-1",
      name: "read_file",
      input: { path: "second" },
    }),
    event(6, "tool_finished", {
      tool_call_id: "native-1",
      name: "read_file",
      status: "ok",
      duration_ms: 5,
      output: "第二次结果",
    }),
  ];
  render(
    <ToolTimeline agents={[member]} events={events} runStatus="running" />,
  );
  const records = screen
    .getAllByText("读取文件")
    .map((label) => label.closest("details")!);
  expect(records).toHaveLength(2);
  expect(records[0]).not.toHaveTextContent("第二次结果");
  expect(records[0]).toHaveTextContent("完成记录不在当前页");
  expect(records[1]).toHaveTextContent("第二次结果");
  expect(screen.getAllByText("完成")).toHaveLength(1);
});

it("shows a standalone finish on a later history page", async () => {
  const started = event(5, "tool_started", {
    tool_call_id: "first",
    name: "read_file",
    input: {},
  });
  const finish = event(9, "tool_finished", {
    tool_call_id: "second",
    name: "fetch_url",
    status: "ok",
    duration_ms: 7,
    output: "后续页面结果",
  });
  render(
    <ToolTimeline
      agents={[member]}
      events={[started, finish]}
      mode="history"
      runStatus="running"
    />,
  );
  expect(screen.getByText("完成记录不在当前页")).toBeVisible();
  await userEvent.click(screen.getByText("读取网页"));
  expect(screen.getByText("调用开始记录不在当前页。")).toBeVisible();
  expect(screen.getByText("后续页面结果")).toBeVisible();
});

it("shows a paired native finish without inventing a duration", () => {
  const events = [
    event(1, "tool_started", {
      tool_call_id: "cli:read-1",
      name: "Read",
      input: { file_path: "/workspace/README.md" },
      origin: "native",
    }),
    event(2, "tool_finished", {
      tool_call_id: "cli:read-1",
      name: "Read",
      status: "ok",
      output: "1\tPUBLIC_FIXTURE",
      origin: "native",
    }),
  ];
  render(
    <ToolTimeline agents={[member]} events={events} runStatus="running" />,
  );
  expect(screen.getByText("原生读取文件")).toBeVisible();
  expect(screen.getByText("完成")).toBeVisible();
  expect(document.body.textContent).not.toContain("undefined");
  expect(screen.queryByText(/^\d+ ms$/)).not.toBeInTheDocument();
});

it("hides routine CLI notices from live and history lists but keeps capability degradation", () => {
  const notices = [
    ...Array.from({ length: 12 }, (_, index) =>
      event(index + 1, "native_notice", { kind: "system.thinking_tokens" }),
    ),
    event(13, "native_notice", { kind: "status", detail: "ignored" }),
    event(14, "native_notice", { kind: "mcp.notifications/progress" }),
    event(15, "native_notice", { kind: "repeated_tool_input" }),
  ];
  const degraded = event(16, "native_notice", {
    kind: "native_file_degraded",
    detail:
      "native file tools are disabled for this step; platform MCP file tools remain available",
  });
  for (const mode of ["live", "history"] as const) {
    const view = render(
      <ToolTimeline
        mode={mode}
        agents={[member]}
        events={[...notices, degraded]}
      />,
    );
    // Routine notices stay in the event log for diagnostics only.
    expect(screen.queryByText(/忽略非关键 CLI 通知/)).not.toBeInTheDocument();
    expect(screen.queryByText(/thinking_tokens/)).not.toBeInTheDocument();
    expect(screen.queryByText(/repeated_tool_input/)).not.toBeInTheDocument();
    expect(
      screen.getByText("原生文件工具已停用，本步改用平台文件工具"),
    ).toBeVisible();
    view.unmount();
  }
});

it("leaves no member header for steps that only carry routine notices", () => {
  const routine = [
    event(1, "native_notice", { kind: "system.thinking_tokens" }),
    event(2, "native_notice", { kind: "constructor" }),
    event(3, "native_notice", { kind: "__proto__" }),
    event(4, "native_notice", { kind: "toString" }),
  ];
  const view = render(<ToolTimeline agents={[member]} events={routine} />);
  expect(view.container.textContent).toBe("");
});

it("keeps member groups for degradation notices and failures", () => {
  const events = [
    event(1, "native_notice", { kind: "native_file_degraded" }),
    event(2, "tool_finished", {
      tool_call_id: "call",
      name: "read_file",
      status: "failed",
      output: "boom",
    }),
  ];
  render(<ToolTimeline agents={[member]} events={events} runStatus="running" />);
  expect(screen.getByText("研究成员 (@member)")).toBeVisible();
  expect(
    screen.getByText("原生文件工具已停用，本步改用平台文件工具"),
  ).toBeVisible();
  expect(screen.getByText("失败")).toBeVisible();
});

it("does not fake a running tool once the live window has evicted its finish", async () => {
  const onHistory = vi.fn();
  const events = [
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "read_file",
      input: {},
    }),
  ];
  for (let seq = 2; seq <= 500; seq++)
    events.push(event(seq, "native_notice", { kind: "status" }));
  render(
    <ToolTimeline
      agents={[member]}
      events={events}
      runStatus="running"
      onHistory={onHistory}
    />,
  );
  expect(screen.queryByText("执行中")).not.toBeInTheDocument();
  expect(screen.getByText("完成记录不在当前页")).toBeVisible();
  await userEvent.click(
    screen.getByRole("button", { name: "查看这次工具调用的历史记录" }),
  );
  expect(onHistory).toHaveBeenCalledWith(0);
});
