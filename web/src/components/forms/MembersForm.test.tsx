import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { MembersForm } from "./MembersForm";
import type { Agent, System } from "../../api/client";

HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute("open", "");
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute("open");
};
afterEach(cleanup);
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
    executors: [],
    roots: [],
    model_status: "verified",
    search_status: "verified",
    verified: {},
    csrf_token: "test",
    limits: {},
  };
  const save = vi.fn(async (value: Agent) => ({
    ...value,
    revision: value.revision + 1,
  }));
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
  await user.type(screen.getByLabelText("职责"), "与复核");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      id: "stable",
      revision: 4,
      role_description: "研究与复核",
    }),
  );
});

it("keeps the created identity and configures a CLI without granting tools", async () => {
  const system: System = {
    demo: false,
    models: ["default"],
    executors: [
      {
        id: "claude",
        installed: true,
        version: "1",
        supported: true,
        reason: "",
      },
      {
        id: "codex",
        installed: true,
        version: "1",
        supported: true,
        reason: "只读工具范围由 Cocogem 管理",
      },
      {
        id: "gemini",
        installed: true,
        version: "old",
        supported: false,
        reason: "缺少安全工具模式",
      },
    ],
    roots: [],
    model_status: "configured",
    search_status: "not_configured",
    verified: {},
    csrf_token: "test",
    limits: {},
  };
  const save = vi.fn(async (value: Agent) => ({
    ...value,
    id: "created",
    revision: value.id ? value.revision + 1 : 1,
  }));
  render(
    <MembersForm
      agents={[]}
      system={system}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("显示名"), "布偶猫");
  await user.type(screen.getByLabelText("Handle"), "ragdoll");
  await user.click(screen.getByLabelText("read_file"));
  await user.selectOptions(screen.getByLabelText("执行 CLI"), "codex");
  expect(screen.getByRole("status")).toHaveTextContent(
    "使用限制：只读工具范围由 Cocogem 管理",
  );
  expect(screen.getByLabelText("模型（留空使用 CLI 默认）")).not.toBeRequired();
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      executor: "codex",
      model: "default",
      tools: expect.not.arrayContaining(["read_file"]),
    }),
  );
  await user.type(
    screen.getByLabelText("模型（留空使用 CLI 默认）"),
    "chosen-model",
  );
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      id: "created",
      revision: 1,
      executor: "codex",
      model: "chosen-model",
      tools: expect.not.arrayContaining(["read_file"]),
    }),
  );
  await user.selectOptions(screen.getByLabelText("执行 CLI"), "gemini");
  expect(screen.getByRole("status")).toHaveTextContent("缺少安全工具模式");
});

it("requires an explicit CLI choice when editing an old API member", async () => {
  const agent: Agent = {
    id: "old",
    workspace_id: "ws",
    display_name: "旧成员",
    handle: "legacy",
    aliases: [],
    role_description: "",
    model: "old-anthropic-model",
    tools: ["read_file"],
    disabled: false,
    revision: 2,
  };
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[agent]}
      system={{
        demo: false,
        models: ["default"],
        roots: [],
        model_status: "installed",
        search_status: "not_configured",
        verified: {},
        csrf_token: "test",
        limits: {},
        executors: [],
      }}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  expect(screen.getByLabelText("执行 CLI")).toHaveValue("");
  expect(screen.getByLabelText("模型（留空使用 CLI 默认）")).toHaveValue("");
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).not.toHaveBeenCalled();
  await user.selectOptions(screen.getByLabelText("执行 CLI"), "claude");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({
      id: "old",
      executor: "claude",
      model: "default",
      tools: ["read_file"],
    }),
  );
});
