import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import type { Run, System } from "../api/client";
import { SettingsDialog, SnapshotDialog } from "./SettingsDialog";

HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute("open", "");
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute("open");
};

afterEach(cleanup);

it("shows the recorded coordinator and exact shared skill versions", () => {
  const version = "123456789abc0123456789abcdef";
  const run = {
    id: "historical-run",
    input: { workflow: "free" },
    created_at: "2026-10-24T00:00:00Z",
    coordinator_id: "coordinator",
    agents: [
      {
        id: "coordinator",
        display_name: "历史协调成员",
        handle: "coordinator",
        revision: 3,
        executor: "codex",
        model: "default",
        tools: [],
      },
    ],
    skill_versions: { "independent-review": version },
  } as unknown as Run;
  render(<SnapshotDialog run={run} close={() => {}} />);
  expect(screen.getByText("本轮协调者：历史协调成员")).toBeVisible();
  expect(screen.getByText(/独立复核 ·/)).toBeVisible();
  expect(screen.getByTitle(version)).toHaveTextContent("123456789abc");
  // A legacy run without a policy must not be presented with today's defaults.
  expect(screen.getByText(/旧记录没有保存执行策略/)).toBeVisible();
  expect(screen.queryByText("首个有效响应")).toBeNull();
});

it("shows the frozen execution policy and run deadlines", () => {
  const run = {
    id: "policy-run",
    input: { workflow: "free" },
    created_at: "2026-10-24T00:00:00Z",
    started_at: "2026-10-24T00:01:00Z",
    execution_policy: {
      run_timeout_ns: 0,
      step_timeout_ns: 0,
      first_response_ns: 600_000_000_000,
      stall_ns: 900_000_000_000,
      stall_action: "warn",
    },
    agents: [],
  } as unknown as Run;
  render(<SnapshotDialog run={run} close={() => {}} />);
  const row = (label: string) => screen.getByText(label).parentElement;
  expect(row("协作总期限")).toHaveTextContent("不设绝对期限");
  expect(row("单步期限")).toHaveTextContent("不设绝对期限");
  expect(row("首个有效响应")).toHaveTextContent("最多等待 10 分钟");
  expect(row("长时间无进展")).toHaveTextContent("15 分钟后仅提示（warn）");
  expect(screen.getByText(/开始时间：/)).toBeVisible();
  expect(screen.getByText("截止时间：不设绝对期限")).toBeVisible();
  expect(
    screen.getByText(/以上是本次协作创建时冻结的执行策略/),
  ).toBeVisible();
});

it("explains that one collaboration runs at a time", () => {
  const system = {
    csrf_token: "",
    limits: {},
    roots: [],
    model_status: "configured",
    search_status: "not_configured",
    models: [],
    verified: {},
    executors: [],
  } as unknown as System;
  render(
    <SettingsDialog
      system={system}
      setSystem={() => {}}
      close={() => {}}
      lock={async () => {}}
    />,
  );
  expect(
    screen.getByText("同一时间处理一轮协作，其余按顺序排队；可停止当前协作。"),
  ).toBeVisible();
});
