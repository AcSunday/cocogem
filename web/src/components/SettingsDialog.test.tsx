import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type {
  Agent,
  Run,
  Settings,
  SettingsInput,
  System,
} from "../api/client";
import { SettingsDialog, SnapshotDialog } from "./SettingsDialog";
import { chooseOption } from "../test/select";

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
  expect(screen.getByText(/以上是本次协作创建时冻结的执行策略/)).toBeVisible();
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

it("clears an unavailable member instead of saving the stale id", async () => {
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
  const agents: Agent[] = [
    {
      id: "one",
      workspace_id: "",
      display_name: "布偶猫",
      handle: "ragdoll",
      aliases: [],
      role_description: "",
      model: "default",
      tools: [],
      disabled: false,
      revision: 1,
    },
  ];
  const settings: Settings = {
    revision: 1,
    default_coordinator_id: "gone",
    assignments: {
      research: { researcher: "gone", critic: "one" },
    },
  };
  const saveSettings = vi.fn(async (value: SettingsInput) => ({
    revision: 2,
    ...value,
  }));
  render(
    <SettingsDialog
      system={system}
      setSystem={() => {}}
      close={() => {}}
      lock={async () => {}}
      settings={settings}
      saveSettings={saveSettings}
      agents={agents}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("tab", { name: "协作" }));
  expect(
    screen.getAllByText("原成员已停用或不存在，保存时清除").length,
  ).toBeGreaterThan(0);
  await user.click(screen.getByRole("button", { name: "保存协作默认" }));
  await waitFor(() => expect(saveSettings).toHaveBeenCalledOnce());
  expect(saveSettings).toHaveBeenCalledWith({
    default_coordinator_id: "",
    assignments: { research: { critic: "one" } },
  });
});

it("configures engineering analyst, developer and reviewer defaults", async () => {
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
  const agents: Agent[] = ["analyst", "developer", "reviewer"].map((id) => ({
    id,
    workspace_id: "",
    display_name: id,
    handle: id,
    aliases: [],
    role_description: "",
    model: "default",
    tools: [],
    disabled: false,
    revision: 1,
  }));
  const saveSettings = vi.fn(async (value: SettingsInput) => ({
    revision: 2,
    ...value,
  }));
  render(
    <SettingsDialog
      system={system}
      setSystem={() => {}}
      close={() => {}}
      lock={async () => {}}
      settings={{ revision: 1, default_coordinator_id: "", assignments: {} }}
      saveSettings={saveSettings}
      agents={agents}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("tab", { name: "协作" }));
  const engineering = screen
    .getByText("工程协作")
    .closest("fieldset") as HTMLElement;
  expect(within(engineering).getByLabelText("分析")).toBeInTheDocument();
  expect(within(engineering).getByLabelText("复核")).toBeInTheDocument();
  await chooseOption(
    user,
    within(engineering).getByLabelText("开发"),
    "developer",
  );
  await user.click(screen.getByRole("button", { name: "保存协作默认" }));
  await waitFor(() => expect(saveSettings).toHaveBeenCalledOnce());
  expect(
    saveSettings.mock.calls[0][0].assignments?.engineering?.developer,
  ).toBe("developer");
});

it("shows the persisted method and selection reason without raw payloads", () => {
  const selected = {
    id: "selected-run",
    input: { workflow: "free" },
    created_at: "2026-10-24T00:00:00Z",
    selected_workflow: {
      workflow: "research",
      targets: ["member-uuid"],
      assignments: { researcher: "member-uuid", future_role: "other-uuid" },
      reason: "  需要外部资料核对  ",
      step_id: "step-uuid",
      selected_at: "2026-10-24T00:00:00Z",
    },
    agents: [],
  } as unknown as Run;
  const { rerender } = render(
    <SnapshotDialog run={selected} close={() => {}} />,
  );
  expect(screen.getByText("本轮方法：Research")).toBeVisible();
  expect(screen.getByText("选择原因：需要外部资料核对")).toBeVisible();
  // Unknown assignment roles and raw IDs never reach the visible snapshot.
  expect(
    screen.queryByText(/member-uuid|other-uuid|future_role|step-uuid/),
  ).toBeNull();

  rerender(
    <SnapshotDialog
      run={{ ...selected, selected_workflow: null, method: "direct" }}
      close={() => {}}
    />,
  );
  expect(screen.getByText("本轮方法：直接回答")).toBeVisible();
  expect(screen.queryByText(/选择原因：/)).toBeNull();
});
