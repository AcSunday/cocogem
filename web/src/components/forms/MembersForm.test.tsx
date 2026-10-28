import { chooseOption } from "../../test/select";
import { cleanup, render, screen, within } from "@testing-library/react";
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
    model: "default",
    executor: "claude",
    tools: ["read_file"],
    disabled: false,
    revision: 3,
  };
  const system: System = {
    models: ["default"],
    executors: [
      {
        id: "claude",
        installed: true,
        version: "1",
        supported: true,
        reason: "",
        adapter: "claude-stream-json",
        isolation: "enforced",
        protocol: "runtime",
      },
    ],
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
    models: ["default"],
    executors: [
      {
        id: "claude",
        installed: true,
        version: "1",
        supported: true,
        reason: "",
        adapter: "claude-stream-json",
        isolation: "enforced",
        protocol: "runtime",
      },
      {
        id: "codex",
        installed: true,
        version: "1",
        supported: true,
        reason: "只读工具范围由 Cocogem 管理",
        adapter: "codex-exec-json",
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
  await chooseOption(user, screen.getByLabelText("执行 CLI"), "codex");
  expect(screen.getByRole("status")).toHaveTextContent(
    "说明：只读工具范围由 Cocogem 管理",
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
  await chooseOption(user, screen.getByLabelText("执行 CLI"), "gemini");
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
  await chooseOption(user, screen.getByLabelText("执行 CLI"), "claude");
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

it("edits the global profile separately from workspace permissions", async () => {
  const agent: Agent = {
    id: "shared",
    workspace_id: "",
    display_name: "共享成员",
    handle: "shared",
    aliases: [],
    role_description: "研究",
    capabilities: ["资料分析"],
    executor: "codex",
    model: "default",
    tools: ["read_file", "web_search"],
    disabled: false,
    revision: 4,
  };
  const membership = {
    workspace_id: "ws",
    agent_id: agent.id,
    enabled: true,
    tools: ["read_file"],
    revision: 2,
  };
  const save = vi.fn(async (value: Agent) => ({ ...value, revision: 5 }));
  const saveMembership = vi.fn(async () => {});
  const unlink = vi.fn(async () => {});
  render(
    <MembersForm
      agents={[
        { ...agent, workspace_id: "ws", tools: ["read_file"], membership },
      ]}
      catalog={[agent]}
      system={{
        models: ["default"],
        roots: [],
        model_status: "configured",
        search_status: "configured",
        verified: {},
        csrf_token: "test",
        limits: {},
      }}
      workspaceID="ws"
      close={() => {}}
      save={save}
      saveMembership={saveMembership}
      unlink={unlink}
    />,
  );
  const user = userEvent.setup();
  await user.clear(screen.getByLabelText("显示名"));
  await user.type(screen.getByLabelText("显示名"), "统一新名称");
  await user.type(screen.getByLabelText("能力标签（用逗号分隔）"), ",技术复核");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({
      id: "shared",
      revision: 4,
      display_name: "统一新名称",
      tools: ["read_file", "web_search"],
      capabilities: ["资料分析", "技术复核"],
    }),
  );
  const grants = within(screen.getByRole("group", { name: "本工作空间权限" }));
  expect(grants.getByLabelText("web_search")).not.toBeChecked();
  await user.click(grants.getByLabelText("在本工作空间启用"));
  await user.click(screen.getByRole("button", { name: "保存本空间权限" }));
  expect(saveMembership).toHaveBeenCalledWith(
    expect.objectContaining({ id: "shared", revision: 5 }),
    membership,
    { enabled: false, tools: ["read_file"] },
  );
  await user.click(screen.getByRole("button", { name: "移出本工作空间" }));
  expect(unlink).toHaveBeenCalledWith(membership);
});

it("saves a built-in avatar for a global member without touching workspace grants", async () => {
  const agent: Agent = {
    id: "global",
    workspace_id: "",
    display_name: "全局成员",
    handle: "global",
    aliases: [],
    role_description: "",
    executor: "claude",
    model: "default",
    tools: ["read_file"],
    disabled: false,
    revision: 2,
  };
  const save = vi.fn(async (value: Agent) => ({ ...value, revision: 3 }));
  render(
    <MembersForm
      agents={[]}
      catalog={[agent]}
      global
      system={{
        models: ["default"],
        roots: [],
        model_status: "configured",
        search_status: "not_configured",
        verified: {},
        csrf_token: "test",
        limits: {},
        executors: [],
      }}
      workspaceID=""
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  expect(
    screen.queryByRole("group", { name: "本工作空间权限" }),
  ).not.toBeInTheDocument();
  await chooseOption(user, screen.getByLabelText("头像"), "🦊");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({ id: "global", revision: 2, avatar: "🦊" }),
  );
});

it("reuses a global member identity without creating a new profile", async () => {
  const agent: Agent = {
    id: "existing",
    workspace_id: "",
    display_name: "已有成员",
    handle: "existing",
    aliases: [],
    role_description: "复核",
    executor: "claude",
    model: "default",
    tools: ["read_file"],
    disabled: false,
    revision: 7,
  };
  const save = vi.fn(async (value: Agent) => value);
  const saveMembership = vi.fn(async () => {});
  render(
    <MembersForm
      agents={[]}
      catalog={[agent]}
      system={{
        models: ["default"],
        roots: [],
        model_status: "configured",
        search_status: "configured",
        verified: {},
        csrf_token: "test",
        limits: {},
      }}
      workspaceID="second"
      close={() => {}}
      save={save}
      saveMembership={saveMembership}
      unlink={async () => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "加入本工作空间" }));
  expect(saveMembership).toHaveBeenCalledWith(agent, undefined, {
    enabled: true,
    tools: ["read_file"],
  });
  expect(save).not.toHaveBeenCalled();
});
