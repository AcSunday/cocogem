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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const savedSettings = (value: SettingsInput): Settings => ({
  revision: 2,
  default_coordinator_id: value.default_coordinator_id,
  assignments: value.assignments,
  retention_days: value.retention_days ?? undefined,
});

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
  const saveSettings = vi.fn(async (value: SettingsInput) =>
    savedSettings(value),
  );
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
    collaboration_budget: {
      auto_continuations: 20,
      parallel_branches: 4,
      writer_branches: 2,
      fix_rounds: 3,
      no_progress_failures: 2,
    },
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
  const saveSettings = vi.fn(async (value: SettingsInput) =>
    savedSettings(value),
  );
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
  expect(screen.getByText("本轮方法：研究协作")).toBeVisible();
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

const serviceSystem = () =>
  ({
    csrf_token: "",
    limits: {},
    roots: [],
    model_status: "configured",
    search_status: "not_configured",
    models: [],
    verified: {},
    executors: [
      {
        id: "claude",
        installed: true,
        version: "2.1",
        supported: true,
        reason: "",
        adapter: "claude-stream-json",
        isolation: "enforced",
        protocol: "runtime",
      },
      {
        id: "gemini",
        installed: true,
        version: "old",
        supported: false,
        reason: "缺少安全工具模式",
        adapter: "gemini-stream-json",
        isolation: "unavailable",
        protocol: "runtime",
      },
    ],
  }) as unknown as System;

const trashItem = (
  id: string,
  title: string,
  revision: number,
  workspace = "研究室",
) => ({
  thread: {
    id,
    workspace_id: "ws",
    title,
    agent_ids: ["one"],
    coordinator_id: "one",
    workflow: "free",
    revision,
    updated_at: "2026-10-01T00:00:00Z",
    deleted_at: "2026-10-01T09:30:00Z",
    purge_at: "2026-10-31T09:30:00Z",
  },
  workspace_name: workspace,
});

it("uses real tabs with roving focus and arrow-key navigation", async () => {
  render(
    <SettingsDialog
      system={serviceSystem()}
      setSystem={() => {}}
      close={() => {}}
      lock={async () => {}}
      settings={{ revision: 1, default_coordinator_id: "", assignments: {} }}
      saveSettings={async (value) => savedSettings(value)}
    />,
  );
  const user = userEvent.setup();
  expect(screen.getByRole("tablist", { name: "设置分类" })).toBeVisible();
  const collaboration = screen.getByRole("tab", { name: "协作" });
  collaboration.focus();
  expect(screen.getByRole("tabpanel")).toHaveAttribute(
    "aria-labelledby",
    "settings-tab-collaboration",
  );
  await user.keyboard("{ArrowDown}");
  const service = screen.getByRole("tab", { name: "服务" });
  expect(service).toHaveFocus();
  expect(service).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{End}");
  expect(screen.getByRole("tab", { name: "回收站" })).toHaveFocus();
});

it("shows install, execution and connection state independently", async () => {
  render(
    <SettingsDialog
      system={serviceSystem()}
      setSystem={() => {}}
      close={() => {}}
      lock={async () => {}}
    />,
  );
  expect(screen.getByText(/已安装 · 2.1/)).toBeInTheDocument();
  expect(screen.getByText("可执行")).toBeInTheDocument();
  expect(screen.getAllByText(/连接未验证/).length).toBeGreaterThan(0);
  expect(screen.getByText(/不支持：缺少安全工具模式/)).toBeInTheDocument();
  expect(screen.getByText(/已安装不代表连接或模型可用/)).toBeVisible();
  expect(screen.getByText(/搜索不可用时仍可读取公开 URL/)).toBeVisible();
  expect(
    screen.getByRole("button", { name: "锁定工作空间" }),
  ).toBeInTheDocument();
});

it("restores or purges trash entries only after explicit confirmation", async () => {
  const items = [
    trashItem("old", "旧会话", 7),
    trashItem("gone", "已清理会话", 8, "别的空间"),
  ];
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/trash") return Response.json({ items });
    return Response.json({ id: path.split("/")[4] });
  });
  vi.stubGlobal("fetch", fetcher);
  const onRestoreThread = vi.fn(async () => {});
  const onTrashChanged = vi.fn(async () => {});
  render(
    <SettingsDialog
      system={serviceSystem()}
      setSystem={() => {}}
      close={() => {}}
      lock={async () => {}}
      settings={{
        revision: 4,
        default_coordinator_id: "one",
        assignments: { research: { researcher: "one" } },
        retention_days: 30,
      }}
      saveSettings={async (value) => savedSettings(value)}
      onRestoreThread={onRestoreThread}
      onTrashChanged={onTrashChanged}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("tab", { name: "回收站" }));
  expect(await screen.findByText("旧会话")).toBeVisible();
  expect(screen.getAllByText("研究室").length).toBeGreaterThan(0);
  expect(screen.getAllByText(/删除于 2026-10-01/).length).toBeGreaterThan(0);
  expect(
    screen.getAllByText(/计划于 2026-10-31.*自动清理/).length,
  ).toBeGreaterThan(0);

  await user.click(screen.getByRole("button", { name: "恢复会话：旧会话" }));
  expect(
    screen.getByRole("alertdialog", { name: "确认恢复 旧会话" }),
  ).toBeVisible();
  expect(fetcher).not.toHaveBeenCalledWith(
    "/api/v1/threads/old/restore",
    expect.anything(),
  );
  await user.click(screen.getByRole("button", { name: "确认恢复" }));
  await waitFor(() =>
    expect(onRestoreThread).toHaveBeenCalledWith(
      expect.objectContaining({ id: "old", workspace_id: "ws" }),
    ),
  );
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/threads/old/restore",
    expect.objectContaining({
      method: "POST",
      headers: expect.objectContaining({ "If-Match": "7" }),
    }),
  );

  await user.click(
    screen.getByRole("button", { name: "永久删除会话：已清理会话" }),
  );
  expect(
    screen.getByRole("alertdialog", { name: "确认永久删除 已清理会话" }),
  ).toHaveTextContent("无法恢复");
  await user.click(screen.getByRole("button", { name: "确认永久删除" }));
  await waitFor(() => expect(onTrashChanged).toHaveBeenCalledOnce());
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/threads/gone/permanent",
    expect.objectContaining({
      method: "DELETE",
      headers: expect.objectContaining({ "If-Match": "8" }),
    }),
  );
});

it("saves retention with the existing collaboration defaults intact", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) =>
      Response.json(path === "/api/v1/trash" ? { items: [] } : {}),
    ),
  );
  const settings: Settings = {
    revision: 5,
    default_coordinator_id: "one",
    assignments: { research: { researcher: "one", critic: "two" } },
    retention_days: 30,
  };
  const saveSettings = vi.fn(async (value: SettingsInput) => ({
    ...savedSettings(value),
    revision: 6,
  }));
  render(
    <SettingsDialog
      system={serviceSystem()}
      setSystem={() => {}}
      close={() => {}}
      lock={async () => {}}
      settings={settings}
      saveSettings={saveSettings}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("tab", { name: "回收站" }));
  expect(await screen.findByText("回收站为空。")).toBeVisible();
  const days = screen.getByLabelText("回收站保留天数");
  await user.clear(days);
  await user.type(days, "45");
  await user.click(screen.getByRole("button", { name: "保存保留周期" }));
  await waitFor(() => expect(saveSettings).toHaveBeenCalledOnce());
  expect(saveSettings).toHaveBeenCalledWith({
    default_coordinator_id: "one",
    assignments: { research: { researcher: "one", critic: "two" } },
    retention_days: 45,
  });
  expect(
    await screen.findByText("保留周期已保存，回收站中的清理时间已同步更新。"),
  ).toBeVisible();
  expect(
    screen.getByText(/保存后会同步重算回收站中会话的清理时间/),
  ).toBeVisible();
});

it("shows a trash read failure with a working retry", async () => {
  let fail = true;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path !== "/api/v1/trash") return Response.json({});
      if (fail) throw new Error("trash unavailable");
      return Response.json({ items: [] });
    }),
  );
  render(
    <SettingsDialog
      system={serviceSystem()}
      setSystem={() => {}}
      close={() => {}}
      lock={async () => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("tab", { name: "回收站" }));
  expect(await screen.findByRole("alert")).toBeVisible();
  fail = false;
  await user.click(screen.getByRole("button", { name: "重试" }));
  expect(await screen.findByText("回收站为空。")).toBeVisible();
});
