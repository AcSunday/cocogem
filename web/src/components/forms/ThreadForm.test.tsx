import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { ThreadForm } from "./ThreadForm";
import type { Agent } from "../../api/client";

afterEach(cleanup);
HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute("open", "");
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute("open");
};

it("reassigns a removed coordinator and distinguishes members sharing a name", async () => {
  const agents: Agent[] = ["one", "two", "three"].map((id) => ({
    id,
    workspace_id: "ws",
    display_name: "猫猫",
    handle: id,
    aliases: [],
    role_description: "",
    model: "demo",
    tools: [],
    disabled: false,
    revision: 1,
  }));
  const save = vi.fn().mockResolvedValue(undefined);
  render(<ThreadForm agents={agents} close={() => {}} save={save} />);
  const user = userEvent.setup();
  expect(screen.getByLabelText("协调者")).toHaveValue("three");
  await user.click(screen.getByRole("checkbox", { name: "猫猫 (@three)" }));
  expect(screen.getByLabelText("协调者")).toHaveValue("one");
  await user.click(screen.getByRole("button", { name: "保存会话" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({
      coordinator_id: "one",
      agent_ids: ["one", "two"],
    }),
  );
});

it("replaces a coordinator disabled since the conversation was saved", () => {
  const agents: Agent[] = ["one", "two"].map((id) => ({
    id,
    workspace_id: "ws",
    display_name: id,
    handle: id,
    aliases: [],
    role_description: "",
    model: "demo",
    tools: [],
    disabled: false,
    revision: 1,
  }));
  render(
    <ThreadForm
      agents={agents}
      current={{
        id: "thread",
        workspace_id: "ws",
        title: "已有会话",
        agent_ids: ["one", "two", "disabled"],
        coordinator_id: "disabled",
        workflow: "discussion",
        revision: 2,
        updated_at: "",
      }}
      close={() => {}}
      save={vi.fn()}
    />,
  );
  expect(screen.getByLabelText("协调者")).toHaveValue("two");
  expect(
    screen
      .getAllByRole("checkbox")
      .filter((element) => (element as HTMLInputElement).checked),
  ).toHaveLength(2);
});
