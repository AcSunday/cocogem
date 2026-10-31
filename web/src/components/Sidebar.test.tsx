import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Thread } from "../api/client";
import { Sidebar } from "./Sidebar";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const thread = (id: string, title: string): Thread => ({
  id,
  workspace_id: "ws",
  title,
  agent_ids: ["one"],
  coordinator_id: "one",
  workflow: "free",
  revision: 3,
  updated_at: "2026-10-28T00:00:00Z",
});

const onEditThread = vi.fn();
const onRenameThread = vi.fn();
const onDeleteThread = vi.fn();

function sidebar(
  threadID = "a",
  threadList = [thread("a", "会话 A"), thread("b", "会话 B")],
) {
  return (
    <Sidebar
      open
      close={() => {}}
      workspaces={[
        {
          id: "ws",
          name: "空间",
          description: "",
          root: "",
          skill_ids: null,
          revision: 1,
          created_at: "",
        },
      ]}
      workspaceID="ws"
      workspace={{
        id: "ws",
        name: "空间",
        description: "",
        root: "",
        skill_ids: null,
        revision: 1,
        created_at: "",
      }}
      threads={threadList}
      threadID={threadID}
      onWorkspace={() => {}}
      onHome={() => {}}
      onThread={() => {}}
      onNewThread={() => {}}
      onEditThread={onEditThread}
      onRenameThread={onRenameThread}
      onDeleteThread={onDeleteThread}
      onCreateWorkspace={() => {}}
      onEditWorkspace={() => {}}
      onSettings={() => {}}
      appearance="system"
      onAppearance={() => {}}
    />
  );
}

it("opens a per-thread menu, focuses its items and returns focus on Escape", async () => {
  const user = userEvent.setup();
  render(sidebar());
  const trigger = screen.getByRole("button", {
    name: "更多会话操作：会话 A",
  });
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  await user.click(trigger);
  const edit = screen.getByRole("menuitem", { name: "编辑" });
  expect(edit).toHaveFocus();
  expect(screen.getByRole("menuitem", { name: "重命名" })).toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: "删除" })).toBeInTheDocument();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menuitem", { name: "重命名" })).toHaveFocus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menuitem", { name: "删除" })).toHaveFocus();
  await user.keyboard("{ArrowDown}");
  expect(edit).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();
});

it("closes on an outside press and when another conversation is selected", async () => {
  const user = userEvent.setup();
  const view = render(sidebar());
  await user.click(
    screen.getByRole("button", { name: "更多会话操作：会话 A" }),
  );
  expect(screen.getByRole("menu")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "新建会话" }));
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();

  // A menu opened on a row that is not the current one must also close.
  await user.click(
    screen.getByRole("button", { name: "更多会话操作：会话 B" }),
  );
  expect(screen.getByRole("menu")).toBeInTheDocument();
  view.rerender(sidebar("b"));
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(onDeleteThread).not.toHaveBeenCalled();
});

it("hands rename and confirmed delete actions to the app", async () => {
  const user = userEvent.setup();
  render(sidebar());
  const trigger = screen.getByRole("button", {
    name: "更多会话操作：会话 A",
  });
  await user.click(trigger);
  await user.click(screen.getByRole("menuitem", { name: "重命名" }));
  expect(onRenameThread).toHaveBeenCalledWith(thread("a", "会话 A"));
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(trigger).toHaveFocus();

  await user.click(trigger);
  await user.click(screen.getByRole("menuitem", { name: "删除" }));
  expect(onDeleteThread).toHaveBeenCalledWith(thread("a", "会话 A"));
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
});

it("shows accessible active and terminal run states without inventing completion", () => {
  const running = {
    ...thread("a", "运行中"),
    active_run_id: "run-a",
    active_run_status: "running",
  };
  const { rerender } = render(sidebar("a", [running]));
  expect(
    screen.getByRole("status", { name: "运行状态：执行中" }),
  ).toHaveTextContent("");
  expect(screen.queryByRole("status", { name: "运行状态：已完成" })).toBeNull();

  rerender(
    sidebar("a", [
      { ...running, active_run_id: undefined, last_run_status: "succeeded" },
    ]),
  );
  expect(
    screen.getByRole("status", { name: "运行状态：已完成" }),
  ).toBeVisible();
  expect(screen.queryByRole("status", { name: "运行状态：执行中" })).toBeNull();

  rerender(
    sidebar("a", [
      { ...running, active_run_id: undefined, last_run_status: "failed" },
    ]),
  );
  expect(screen.getByRole("status", { name: "运行状态：失败" })).toBeVisible();
  rerender(
    sidebar("a", [
      { ...running, active_run_id: undefined, last_run_status: "canceled" },
    ]),
  );
  expect(
    screen.getByRole("status", { name: "运行状态：已停止" }),
  ).toBeVisible();
  rerender(
    sidebar("a", [
      { ...running, active_run_id: undefined, last_run_status: "interrupted" },
    ]),
  );
  expect(
    screen.getByRole("status", { name: "运行状态：运行已中断" }),
  ).toBeVisible();
});

it("shows queued, running and canceling states for an active run", () => {
  const base = thread("a", "排队");
  const { rerender } = render(
    sidebar("a", [
      { ...base, active_run_id: "run", active_run_status: "queued" },
    ]),
  );
  expect(
    screen.getByRole("status", { name: "运行状态：排队中" }),
  ).toBeVisible();
  rerender(
    sidebar("a", [
      { ...base, active_run_id: "run", active_run_status: "canceling" },
    ]),
  );
  expect(
    screen.getByRole("status", { name: "运行状态：正在停止" }),
  ).toBeVisible();
});

it("shows the updated time in a right-side tooltip on hover", async () => {
  const user = userEvent.setup();
  render(sidebar());
  expect(screen.queryByRole("tooltip")).toBeNull();
  await user.hover(screen.getByRole("button", { name: "会话 A" }));
  expect(await screen.findByRole("tooltip")).toHaveTextContent(
    "更新于 2026-10-28",
  );
  await user.unhover(screen.getByRole("button", { name: "会话 A" }));
  expect(screen.queryByRole("tooltip")).toBeNull();
});

it("clears the search after choosing a conversation", async () => {
  const user = userEvent.setup();
  render(sidebar());
  const search = screen.getByLabelText("搜索会话");
  await user.type(search, "会话 B");
  expect(screen.queryByRole("button", { name: "会话 A" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "会话 B" }));
  expect(search).toHaveValue("");
  expect(screen.getByRole("button", { name: "会话 A" })).toBeInTheDocument();
});

it("leaves a new empty conversation without a status badge", () => {
  render(sidebar("a", [thread("empty", "新会话")]));
  expect(screen.queryByRole("status")).toBeNull();
  expect(screen.getByRole("button", { name: "新建会话" })).toBeInTheDocument();
});
