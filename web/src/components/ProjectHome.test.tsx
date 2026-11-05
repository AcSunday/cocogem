import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { ProjectOverview, Workspace } from "../api/client";
import { ProjectHome } from "./ProjectHome";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

afterEach(() => {
  cleanup();
  request.mockReset();
});

const workspace = {
  id: "project",
  name: "订单系统",
  description: "兼容已有客户端",
  root: "",
  revision: 1,
  created_at: "2026-11-04T00:00:00Z",
  skill_ids: [],
} as Workspace;

it("keeps the main welcome surface and adds a small linked summary", async () => {
  request.mockResolvedValue({
    workspace,
    records: [],
    tasks: [
      { task: { status: "active" } },
      { task: { status: "waiting" } },
      { task: { status: "completed" } },
    ],
    needs_attention: [
      { id: "question", task_id: "waiting", task_revision: 1, status: "open" },
    ],
  } as unknown as ProjectOverview);
  const openTasks = vi.fn();
  const edit = vi.fn();
  const user = userEvent.setup();
  render(
    <ProjectHome
      workspace={workspace}
      members={3}
      onCreate={() => {}}
      onPick={() => {}}
      onOpenTasks={openTasks}
      onEditWorkspace={edit}
    />,
  );
  expect(
    await screen.findByRole("heading", { name: "把问题交给一个团队。" }),
  ).toBeVisible();
  expect(screen.getByRole("button", { name: /做一次研究协作/ })).toBeVisible();
  expect(screen.getByText("2")).toBeVisible();
  expect(screen.getByText(/需要你/)).toBeVisible();
  await waitFor(() => expect(request).toHaveBeenCalled());
  await user.click(screen.getByRole("button", { name: "打开任务清单" }));
  expect(openTasks).toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "编辑项目" }));
  expect(edit).toHaveBeenCalled();
});
