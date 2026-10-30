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

function sidebar(threadID = "a") {
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
      threads={[thread("a", "会话 A"), thread("b", "会话 B")]}
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
