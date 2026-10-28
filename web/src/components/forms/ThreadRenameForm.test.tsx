import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { ThreadRenameForm } from "./ThreadRenameForm";

HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute("open", "");
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute("open");
};
afterEach(cleanup);

const thread: Thread = {
  id: "thread",
  workspace_id: "ws",
  title: "原会话名称",
  agent_ids: ["one", "two"],
  coordinator_id: "two",
  workflow: "research",
  revision: 4,
  updated_at: "2026-10-28T00:00:00Z",
};

it("rejects an empty title", async () => {
  const user = userEvent.setup();
  const save = vi.fn(async () => {});
  render(<ThreadRenameForm thread={thread} close={() => {}} save={save} />);
  const input = screen.getByLabelText("会话标题");
  expect(input).toHaveValue("原会话名称");
  await user.clear(input);
  await user.click(screen.getByRole("button", { name: "保存名称" }));
  expect(save).not.toHaveBeenCalled();
});

it("keeps the failed draft and shows the server error", async () => {
  const user = userEvent.setup();
  const save = vi.fn(async () => {
    throw new Error("版本冲突，请重新打开会话后再试");
  });
  render(<ThreadRenameForm thread={thread} close={() => {}} save={save} />);
  const input = screen.getByLabelText("会话标题");
  await user.clear(input);
  await user.type(input, "  新的名字  ");
  await user.click(screen.getByRole("button", { name: "保存名称" }));
  expect(save).toHaveBeenCalledWith("新的名字");
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "版本冲突，请重新打开会话后再试",
  );
  expect(input).toHaveValue("  新的名字  ");
  expect(screen.getByRole("button", { name: "保存名称" })).toBeEnabled();
});
