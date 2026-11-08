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
      onResume={() => {}}
    />,
  );
  expect(screen.getByText("研究协作")).toBeVisible();
  expect(screen.getByText("需要外部资料核对")).toBeVisible();
  expect(screen.queryByText("自由协作")).not.toBeInTheDocument();
});

it("leaves per-member tool activity to the reply instead of one global latest line", () => {
  render(
    <RunDivider
      run={{ ...run, status: "running" }}
      index={1}
      steps={[{ id: "step", agent_name: "研究成员", status: "running" } as Step]}
      latest
      busy={false}
      onSnapshot={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.queryByTestId("latest-tool")).not.toBeInTheDocument();
  expect(screen.queryByText(/最近工具/)).not.toBeInTheDocument();
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
      onResume={() => {}}
    />,
  );
  expect(
    screen.queryByText("长时间无进展 · 已 900 秒"),
  ).not.toBeInTheDocument();
  expect(screen.getByText("等待工具 · 已 5 秒")).toBeVisible();
});

it("does not place a member retry beside the collaboration process", () => {
  render(
    <RunDivider
      run={{
        ...run,
        status: "failed",
        error: "provider returned an invalid result",
      }}
      index={2}
      latest={false}
      busy={false}
      steps={[
        { id: "failed", agent_id: "member", agent_name: "橘猫", status: "failed" } as Step,
        { id: "succeeded", agent_id: "peer", agent_name: "布偶猫", status: "succeeded" } as Step,
      ]}
      onSnapshot={() => {}}
      onResume={() => {}}
    />,
  );
  expect(screen.queryByRole("button", { name: "重试此轮" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "重试 橘猫" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "重试 布偶猫" })).not.toBeInTheDocument();
});

it("does not offer a new run for interrupted checkpoints", () => {
  render(
    <RunDivider
      run={{ ...run, status: "interrupted", error: "service restarted" }}
      index={1}
      latest
      busy={false}
      onSnapshot={() => {}}
      onResume={() => {}}
    />,
  );
  expect(
    screen.queryByRole("button", { name: "重新运行" }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "继续执行" })).toBeVisible();
});
