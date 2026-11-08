import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
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
  vi.unstubAllGlobals();
});

function stubViewport(initiallyCompact: boolean) {
  let matches = initiallyCompact;
  const listeners = new Set<() => void>();
  const media = {
    get matches() {
      return matches;
    },
    media: "(max-width: 700px)",
    onchange: null,
    addEventListener: (_: string, listener: () => void) => {
      listeners.add(listener);
    },
    removeEventListener: (_: string, listener: () => void) => {
      listeners.delete(listener);
    },
    addListener: (listener: () => void) => {
      listeners.add(listener);
    },
    removeListener: (listener: () => void) => {
      listeners.delete(listener);
    },
    dispatchEvent: () => true,
  };
  vi.stubGlobal("matchMedia", () => media);
  return {
    set(next: boolean) {
      matches = next;
      act(() => listeners.forEach((listener) => listener()));
    },
  };
}

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

it("renders no summary card or placeholder on mobile and restores it when widened", async () => {
  const viewport = stubViewport(true);
  request.mockResolvedValue({
    workspace,
    records: [],
    tasks: [{ task: { status: "active" } }],
    needs_attention: [],
  } as unknown as ProjectOverview);
  render(
    <ProjectHome
      workspace={workspace}
      members={3}
      onCreate={() => {}}
      onPick={() => {}}
      onOpenTasks={() => {}}
      onEditWorkspace={() => {}}
    />,
  );
  expect(
    await screen.findByRole("heading", { name: "把问题交给一个团队。" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("region", { name: "项目摘要" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("项目摘要")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "打开任务清单" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "编辑项目" }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: /做一次研究协作/ })).toBeVisible();
  expect(request).not.toHaveBeenCalled();

  viewport.set(false);
  const summary = await screen.findByRole("region", { name: "项目摘要" });
  expect(summary).toBeVisible();
  expect(
    screen.getByRole("button", { name: "打开任务清单" }),
  ).toBeVisible();
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/workspaces/project/overview",
    ),
  );
});
