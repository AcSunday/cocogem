import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { Settings, Workspace } from "../api/client";
import { ProjectSettings } from "./ProjectSettings";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

const workspace = {
  id: "ws",
  name: "项目",
  description: "",
  root: "",
  revision: 1,
  created_at: "",
  skill_ids: [],
  // Inert audit data: a leftover workspace mode/budget must never be shown or
  // used as an effective default again.
  execution_mode: "full_access",
  budget_override: { auto_continuations: 8 },
} as Workspace;

const settings = {
  revision: 3,
  default_coordinator_id: "",
  assignments: {},
  execution_mode: "project_autonomous",
  permission_epoch: 2,
  collaboration_budget: {
    auto_continuations: 20,
    parallel_branches: 4,
    writer_branches: 2,
    fix_rounds: 3,
    no_progress_failures: 2,
  },
} satisfies Settings;

afterEach(() => {
  cleanup();
  request.mockReset();
});

it("shows the inherited global permission instead of a workspace override", async () => {
  render(
    <ProjectSettings
      workspace={workspace}
      settings={settings}
      agents={[]}
      onWorkspaceChange={() => {}}
    />,
  );
  // The global value wins over the leftover workspace full_access.
  expect(screen.getByText("项目内自主")).toBeVisible();
  expect(screen.queryByText("完全访问")).not.toBeInTheDocument();
  // No workspace permission/budget editor survives anywhere in the panel.
  expect(screen.queryByRole("radiogroup", { name: "执行权限模式" }))
    .not.toBeInTheDocument();
  expect(screen.queryByLabelText("自动续办次数")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "保存项目覆盖" }))
    .not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "全部恢复继承" }))
    .not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "保存权限模式" }))
    .not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "为这些成员启用任务执行" }))
    .not.toBeInTheDocument();
  // The global budget defaults are read-only context, not an editable override.
  expect(
    screen.getByText(/续办 20 · 并行 4 · 写分支 2 · 修复 3 · 无进展 2/),
  ).toBeVisible();
  // Nothing was requested to load a removed grants endpoint.
  expect(request).not.toHaveBeenCalled();
});

it("does not call the retired workspace grant or budget routes", async () => {
  request.mockImplementation(async () => ({ items: [] }));
  render(<ProjectSettings workspace={workspace} settings={settings} />);
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "保存项目覆盖" }))
      .not.toBeInTheDocument(),
  );
  const retired = request.mock.calls
    .map((call) => String(call[0]))
    .filter((path) =>
      /\/execution-policy$|\/budget-policy$|\/file-execution$|\/grants/.test(
        path,
      ),
    );
  expect(retired).toEqual([]);
});

it("explains an unavailable global record without inventing a workspace value", () => {
  render(<ProjectSettings workspace={workspace} />);
  expect(screen.getByText("项目内自主")).toBeVisible();
  expect(screen.getByText("尚未读取全局预算默认值。")).toBeVisible();
});
