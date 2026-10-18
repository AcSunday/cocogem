import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { MembersForm } from "./MembersForm";
import type { Agent, System } from "../../api/client";

HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute("open", "");
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute("open");
};
it("renames a member without replacing its identity or revision", async () => {
  const agent: Agent = {
    id: "stable",
    workspace_id: "ws",
    display_name: "布偶猫",
    handle: "ragdoll",
    aliases: ["布布"],
    role_description: "研究",
    model: "demo",
    tools: ["read_file"],
    disabled: false,
    revision: 3,
  };
  const system: System = {
    demo: true,
    models: ["demo"],
    roots: [],
    model_status: "verified",
    search_status: "verified",
    verified: {},
    csrf_token: "test",
    limits: {},
  };
  const save = vi.fn().mockResolvedValue(undefined);
  render(
    <MembersForm
      agents={[agent]}
      system={system}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await user.clear(screen.getByLabelText("显示名"));
  await user.type(screen.getByLabelText("显示名"), "橘子");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({
      id: "stable",
      revision: 3,
      display_name: "橘子",
      aliases: ["布布"],
    }),
  );
});
