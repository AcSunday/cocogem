import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import userEvent from "@testing-library/user-event";
import { RunDivider } from "./RunDivider";
import type { Attempt, Event, Run, Step } from "../api/client";

afterEach(() => {
  cleanup();
  localStorage.clear();
});

const run = {
  id: "run",
  status: "succeeded",
  created_at: "2026-10-20T00:00:00Z",
  finished_at: "2026-10-20T00:01:00Z",
  error: "",
  input: { workflow: "free" },
  agents: [],
  routing: {
    usage_known: false,
    model_call: false,
    input_tokens: 0,
    output_tokens: 0,
  },
} as unknown as Run;

const attempt = (extra: Partial<Attempt>): Attempt => ({
  id: "attempt",
  run_id: "run",
  step_id: "step",
  number: 1,
  status: "succeeded",
  started_at: "",
  ...extra,
});

const toolEvent = (seq: number, type: string, data: unknown): Event => ({
  schema_version: 1,
  run_id: "run",
  seq,
  type,
  step_id: "step",
  agent_id: "agent",
  occurred_at: "",
  data,
});

it("shows the process while running and keeps a manual collapse after success", async () => {
  const step = {
    id: "step",
    agent_name: "研究成员",
    status: "running",
    role: "researcher",
  } as Step;
  const props = {
    index: 1,
    steps: [step],
    latest: true,
    busy: false,
    onSnapshot: () => {},
    onRetry: () => {},
    onResume: () => {},
  };
  const { rerender } = render(
    <RunDivider run={{ ...run, status: "running" }} {...props} />,
  );
  const summary = screen.getByText(/^协作过程/, { selector: "summary" });
  expect(summary.closest("details")).toHaveAttribute("open");
  expect(screen.getByRole("list", { name: "协作任务" })).toBeVisible();
  expect(screen.getByText("研究成员正在处理")).toBeVisible();
  expect(screen.getByTestId("run-status")).toBeVisible();
  await userEvent.click(summary);
  expect(summary.closest("details")).not.toHaveAttribute("open");
  rerender(
    <RunDivider
      run={{
        ...run,
        delivery: { message_ids: ["final"], artifact_refs: [] },
      }}
      {...props}
      steps={[{ ...step, status: "succeeded" }]}
    />,
  );
  expect(summary.closest("details")).not.toHaveAttribute("open");
  expect(screen.queryByText("研究成员正在处理")).not.toBeInTheDocument();
});

it("folds a delivered success once and lets the reader expand it again", async () => {
  const step = {
    id: "step",
    agent_name: "研究成员",
    status: "succeeded",
  } as Step;
  const delivered = {
    ...run,
    delivery: { message_ids: ["final"], artifact_refs: [] },
  };
  const props = {
    index: 1,
    steps: [step],
    latest: true,
    busy: false,
    onSnapshot: () => {},
    onRetry: () => {},
    onResume: () => {},
  };
  const { rerender } = render(
    <RunDivider run={{ ...run, status: "running" }} {...props} />,
  );
  const details = screen
    .getByText(/^协作过程/, { selector: "summary" })
    .closest("details")!;
  expect(details).toHaveAttribute("open");
  rerender(<RunDivider run={delivered} {...props} />);
  expect(details).not.toHaveAttribute("open");
  expect(details.querySelector("summary")).toHaveTextContent(
    "协作已完成 · 1 条过程",
  );
  await userEvent.click(details.querySelector("summary")!);
  expect(details).toHaveAttribute("open");
  expect(details.querySelector("summary")).toHaveTextContent(
    "协作过程 · 1 条过程",
  );
});

it("shows a selected workflow and its reason instead of the free label", () => {
  render(
    <RunDivider
      run={{
        ...run,
        status: "running",
        selected_workflow: {
          workflow: "research",
          targets: null,
          assignments: null,
          reason: "需要外部资料核对",
          step_id: "step",
          selected_at: "2026-10-20T00:00:00Z",
        },
      }}
      index={1}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.getByText("Research")).toBeVisible();
  expect(screen.getByText("需要外部资料核对")).toBeVisible();
  expect(screen.queryByText("自由协作")).not.toBeInTheDocument();
});

it("shows the latest tool activity and its error while a run is active", () => {
  const events = [
    toolEvent(1, "tool_started", {
      tool_call_id: "native-1",
      name: "web_search",
      input: { query: "DeBank" },
    }),
    toolEvent(2, "tool_finished", {
      tool_call_id: "native-1",
      name: "web_search",
      status: "ok",
      output: "{}",
      duration_ms: 650,
    }),
    toolEvent(3, "tool_started", {
      tool_call_id: "native-2",
      name: "fetch_url",
      input: { url: "https://debank.com/" },
    }),
    toolEvent(4, "tool_finished", {
      tool_call_id: "native-2",
      name: "fetch_url",
      status: "error",
      output:
        "Tool error: source returned no readable text (the page may require JavaScript or block automated access); try a different URL",
      duration_ms: 120,
    }),
  ];
  const props = {
    index: 1,
    events,
    latest: true,
    busy: false,
    onSnapshot: () => {},
    onRetry: () => {},
    onResume: () => {},
  };
  const { rerender } = render(
    <RunDivider run={{ ...run, status: "running" }} {...props} />,
  );
  expect(
    screen.getByText(
      /最近工具：读取网页（https:\/\/debank.com\/） · 失败 · Tool error: source returned no readable text/,
    ),
  ).toBeVisible();
  rerender(<RunDivider run={{ ...run, status: "canceled" }} {...props} />);
  expect(screen.queryByTestId("latest-tool")).not.toBeInTheDocument();
});

it("pairs a reused native call id with the start from the same step", () => {
  const events = [
    {
      ...toolEvent(1, "tool_started", {
        tool_call_id: "native-1",
        name: "web_search",
        input: { query: "DeBank 风险" },
      }),
      step_id: "a",
      agent_id: "a",
    },
    {
      ...toolEvent(2, "tool_started", {
        tool_call_id: "native-1",
        name: "fetch_url",
        input: { url: "https://b.example/" },
      }),
      step_id: "b",
      agent_id: "b",
    },
    {
      ...toolEvent(3, "tool_finished", {
        tool_call_id: "native-1",
        name: "web_search",
        status: "ok",
        output: "{}",
        duration_ms: 5,
      }),
      step_id: "a",
      agent_id: "a",
    },
  ];
  render(
    <RunDivider
      run={{ ...run, status: "running" }}
      index={1}
      events={events}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.getByTestId("latest-tool")).toHaveTextContent(
    "最近工具：在线搜索（DeBank 风险） · 完成",
  );
  expect(screen.queryByText(/b\.example/)).not.toBeInTheDocument();
});

it("omits the target when the restored attempt's start is not on the page", () => {
  const events = [
    toolEvent(1, "attempt_started", {
      id: "attempt-a",
      number: 1,
      status: "running",
    }),
    toolEvent(2, "tool_started", {
      tool_call_id: "native-1",
      name: "web_search",
      input: { query: "旧尝试" },
    }),
    toolEvent(3, "tool_finished", {
      tool_call_id: "native-1",
      name: "web_search",
      status: "ok",
      output: "{}",
      duration_ms: 5,
    }),
    toolEvent(4, "attempt_finished", {
      id: "attempt-a",
      number: 1,
      status: "interrupted",
    }),
    toolEvent(5, "attempt_started", {
      id: "attempt-b",
      number: 2,
      status: "running",
    }),
    toolEvent(6, "tool_finished", {
      tool_call_id: "native-1",
      name: "web_search",
      status: "ok",
      output: "{}",
      duration_ms: 5,
    }),
  ];
  render(
    <RunDivider
      run={{ ...run, status: "running" }}
      index={1}
      events={events}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.getByTestId("latest-tool")).toHaveTextContent(
    "最近工具：在线搜索 · 完成",
  );
  expect(screen.queryByText(/旧尝试/)).not.toBeInTheDocument();
});

it("does not pair a finish across an attempt end without a new start", () => {
  const events = [
    toolEvent(1, "tool_started", {
      tool_call_id: "native-1",
      name: "web_search",
      input: { query: "旧尝试" },
    }),
    toolEvent(2, "attempt_finished", {
      id: "attempt-a",
      number: 1,
      status: "interrupted",
    }),
    toolEvent(3, "tool_finished", {
      tool_call_id: "native-1",
      name: "web_search",
      status: "ok",
      output: "{}",
      duration_ms: 5,
    }),
  ];
  render(
    <RunDivider
      run={{ ...run, status: "running" }}
      index={1}
      events={events}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.getByTestId("latest-tool")).toHaveTextContent(
    "最近工具：在线搜索 · 完成",
  );
  expect(screen.queryByText(/旧尝试/)).not.toBeInTheDocument();
});

it("shows persisted attempt usage even when no live usage event remains", () => {
  render(
    <RunDivider
      run={run}
      index={1}
      attempts={[
        attempt({ usage_known: true, input_tokens: 12, output_tokens: 5 }),
        attempt({ usage_known: false }),
      ]}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.getByText("部分用量：12 / 5 tokens（1 处未知）")).toBeVisible();
});

it("keeps explicit zero usage distinguishable from unknown usage", () => {
  const { rerender } = render(
    <RunDivider
      run={run}
      index={1}
      attempts={[
        attempt({ usage_known: true, input_tokens: 0, output_tokens: 0 }),
      ]}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.getByText("用量：0 / 0 tokens")).toBeVisible();
  rerender(
    <RunDivider
      run={run}
      index={1}
      attempts={[attempt({ usage_known: false })]}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.getByText("用量未知")).toBeVisible();
  expect(screen.queryByText("用量：0 / 0 tokens")).not.toBeInTheDocument();
});

it("keeps each running step's warning and counts partial known usage", () => {
  const warning = (
    seq: number,
    step: string,
    state: string,
    silence: number,
  ): Event => ({
    schema_version: 1,
    run_id: "run",
    seq,
    type: "liveness_warning",
    step_id: step,
    occurred_at: "",
    data: { state, silence_seconds: silence },
  });
  const { rerender } = render(
    <RunDivider
      run={{ ...run, status: "running" }}
      index={1}
      attempts={[
        attempt({
          id: "a",
          step_id: "a",
          status: "running",
          usage_known: false,
          usage_completeness: "partial",
          input_tokens: 7,
          output_tokens: 0,
        }),
        attempt({
          id: "b",
          step_id: "b",
          status: "running",
          usage_known: true,
          input_tokens: 10,
          output_tokens: 2,
        }),
      ]}
      liveness={[
        warning(1, "a", "stalled", 900),
        warning(2, "b", "waiting_tool", 5),
      ]}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.getByText("长时间无进展 · 已 900 秒")).toBeVisible();
  expect(screen.getByText("等待工具 · 已 5 秒")).toBeVisible();
  expect(screen.getByText("部分用量：17 / 2 tokens（1 处未知）")).toBeVisible();
  rerender(
    <RunDivider
      run={{ ...run, status: "running" }}
      index={1}
      attempts={[attempt({ id: "b", step_id: "b", status: "running" })]}
      liveness={[warning(2, "b", "waiting_tool", 5)]}
      latest
      busy={false}
      onSnapshot={() => {}}
      onRetry={() => {}}
      onResume={() => {}}
    />,
  );
  expect(
    screen.queryByText("长时间无进展 · 已 900 秒"),
  ).not.toBeInTheDocument();
  expect(screen.getByText("等待工具 · 已 5 秒")).toBeVisible();
});
