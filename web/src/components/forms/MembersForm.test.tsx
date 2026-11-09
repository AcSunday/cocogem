import { chooseOption } from "../../test/select";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
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
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

const executorInfo = (id: string, reason = "") => ({
  id,
  installed: true,
  version: "1",
  supported: true,
  reason,
  adapter: `${id}-stream-json`,
  isolation: "enforced",
  protocol: "runtime",
});
const system = (executors = ["claude", "codex", "gemini"]): System => ({
  models: ["default"],
  executors: executors.map((id) =>
    id === "codex"
      ? executorInfo(id, "只读工具范围由 Cocogem 管理")
      : id === "gemini"
        ? executorInfo(id, "缺少安全工具模式")
        : executorInfo(id),
  ),
  roots: [],
  model_status: "configured",
  search_status: "not_configured",
  verified: {},
  csrf_token: "test",
  limits: {},
});
const member = (extra: Partial<Agent> = {}): Agent => ({
  id: "stable",
  workspace_id: "ws",
  display_name: "布偶猫",
  handle: "ragdoll",
  aliases: [],
  role_description: "研究",
  model: "default",
  executor: "claude",
  tools: ["read_file"],
  disabled: false,
  revision: 3,
  ...extra,
});
/** A minimal PNG whose IHDR declares the requested dimensions. */
function pngFile(width = 1, height = 1) {
  const bytes = new Uint8Array(33);
  bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  const view = new DataView(bytes.buffer);
  view.setUint32(8, 13);
  bytes.set([0x49, 0x48, 0x44, 0x52], 12);
  view.setUint32(16, width);
  view.setUint32(20, height);
  return new File([bytes], "avatar.png", { type: "image/png" });
}
const open = async (
  user: ReturnType<typeof userEvent.setup>,
  name: RegExp | string,
) => user.click(screen.getByRole("button", { name }));

it("renames a member without replacing its identity or revision", async () => {
  const agent = member({ aliases: ["布布"] });
  const save = vi.fn(async (value: Agent) => ({
    ...value,
    revision: value.revision + 1,
  }));
  render(
    <MembersForm
      agents={[agent]}
      system={system(["claude"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
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
  const save = vi.fn(async (value: Agent) => ({
    ...value,
    id: "created",
    revision: value.id ? value.revision + 1 : 1,
  }));
  render(
    <MembersForm
      agents={[]}
      system={system()}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /新成员/);
  await user.type(screen.getByLabelText("显示名"), "布偶猫");
  await user.type(screen.getByLabelText("Handle"), "ragdoll");
  // Tool grants are gone from every member form: there is no checkbox to toggle.
  expect(
    screen.queryByRole("group", { name: "全局允许工具" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByLabelText("read_file")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("run_command")).not.toBeInTheDocument();
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
    }),
  );
  expect(save.mock.calls.at(-1)?.[0].tools ?? null).toBeNull();
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
    }),
  );
  await chooseOption(user, screen.getByLabelText("执行 CLI"), "gemini");
  expect(screen.getByRole("status")).toHaveTextContent("缺少安全工具模式");
});

it("requires an explicit CLI choice when editing an old API member", async () => {
  const agent = member({
    id: "old",
    display_name: "旧成员",
    handle: "legacy",
    role_description: "",
    executor: undefined,
    model: "old-anthropic-model",
    revision: 2,
  });
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[agent]}
      system={system([])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /旧成员/);
  expect(screen.getByLabelText("执行 CLI")).toHaveValue("");
  expect(screen.getByLabelText("模型（留空使用 CLI 默认）")).toHaveValue("");
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
  const agent = member({
    id: "shared",
    workspace_id: "",
    display_name: "共享成员",
    handle: "shared",
    capabilities: ["资料分析"],
    executor: "codex",
    tools: ["read_file", "web_search"],
    revision: 4,
  });
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
      system={system()}
      workspaceID="ws"
      close={() => {}}
      save={save}
      saveMembership={saveMembership}
      unlink={unlink}
    />,
  );
  const user = userEvent.setup();
  await open(user, /共享成员/);
  await user.clear(screen.getByLabelText("显示名"));
  await user.type(screen.getByLabelText("显示名"), "统一新名称");
  await user.type(screen.getByLabelText("能力标签（用逗号分隔）"), ",技术复核");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({
      id: "shared",
      revision: 4,
      display_name: "统一新名称",
      // The legacy array travels back untouched; it is never re-edited here.
      tools: ["read_file", "web_search"],
      capabilities: ["资料分析", "技术复核"],
    }),
  );
  // D04: neither the global tool checkboxes nor the workspace grants exist.
  expect(
    screen.queryByRole("group", { name: "全局允许工具" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("group", { name: "本工作空间权限" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByLabelText("patch_file")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("web_search")).not.toBeInTheDocument();
  expect(screen.queryByText(/全局未允许/)).not.toBeInTheDocument();
  // Enable/disable is still editable.
  expect(screen.getByLabelText("在所有工作空间停用此成员")).not.toBeChecked();
  expect(screen.getByLabelText("在本工作空间启用")).toBeChecked();
  await user.click(screen.getByLabelText("在本工作空间启用"));
  await user.click(screen.getByRole("button", { name: "保存本空间设置" }));
  expect(saveMembership).toHaveBeenCalledWith(
    expect.objectContaining({ id: "shared", revision: 5 }),
    membership,
    { enabled: false },
  );
  await user.click(screen.getByRole("button", { name: "移出本工作空间" }));
  expect(unlink).toHaveBeenCalledWith(membership);
});

it("switches card editors without leaking unsaved form state", async () => {
  const first = member({
    id: "first",
    display_name: "第一只",
    handle: "first",
  });
  const second = member({
    id: "second",
    display_name: "第二只",
    handle: "second",
    role_description: "复核",
  });
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[first, second]}
      system={system(["claude"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /第一只/);
  await user.type(screen.getByLabelText("职责"), "临时修改");
  await user.click(screen.getByRole("button", { name: "← 返回列表" }));
  await open(user, /第二只/);
  expect(screen.getByLabelText("显示名")).toHaveValue("第二只");
  expect(screen.getByLabelText("职责")).toHaveValue("复核");
  await user.click(screen.getByRole("button", { name: "← 返回列表" }));
  await open(user, /第一只/);
  expect(screen.getByLabelText("职责")).toHaveValue("研究");
});

it("prefers a newer agent list revision over the saved local profile", async () => {
  const agent = member({
    id: "cached",
    display_name: "初次名称",
    handle: "cached",
    revision: 1,
  });
  const save = vi.fn(async (value: Agent) => ({
    ...value,
    revision: value.revision + 1,
  }));
  const props = {
    system: system(["claude"]),
    workspaceID: "ws",
    close: () => {},
    save,
  };
  const { rerender } = render(<MembersForm {...props} agents={[agent]} />);
  const user = userEvent.setup();
  await open(user, /初次名称/);
  await user.clear(screen.getByLabelText("显示名"));
  await user.type(screen.getByLabelText("显示名"), "已保存名称");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(
    await screen.findByText("已保存，可以继续编辑此成员。"),
  ).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "← 返回列表" }));
  rerender(
    <MembersForm
      {...props}
      agents={[{ ...agent, display_name: "外部新版本", revision: 3 }]}
    />,
  );
  expect(
    screen.getByRole("button", { name: /外部新版本/ }),
  ).toBeInTheDocument();
  await open(user, /外部新版本/);
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({ id: "cached", revision: 3 }),
  );
});

it("shows busy, error and success while saving", async () => {
  const agent = member();
  let finish!: (value: Agent) => void;
  const save = vi.fn(() => new Promise<Agent>((resolve) => (finish = resolve)));
  render(
    <MembersForm
      agents={[agent]}
      system={system(["claude"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(screen.getByRole("button", { name: "保存中…" })).toBeDisabled();
  finish({ ...agent, revision: 4 });
  expect(
    await screen.findByText("已保存，可以继续编辑此成员。"),
  ).toBeInTheDocument();
  cleanup();

  const rejected = vi.fn(async () => {
    throw new Error("该 Handle 已被占用");
  });
  render(
    <MembersForm
      agents={[agent]}
      system={system(["claude"])}
      workspaceID="ws"
      close={() => {}}
      save={rejected}
    />,
  );
  await open(user, /布偶猫/);
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "该 Handle 已被占用",
  );
  expect(screen.getByRole("button", { name: "保存成员" })).toBeEnabled();
});

it("uploads, clears and rejects avatars without touching workspace grants", async () => {
  const agent = member({
    id: "global",
    workspace_id: "",
    display_name: "全局成员",
    handle: "global",
    avatar: "🦊",
    revision: 2,
  });
  const save = vi.fn(async (value: Agent) => ({
    ...value,
    revision: value.revision + 1,
  }));
  render(
    <MembersForm
      agents={[]}
      catalog={[agent]}
      global
      system={system(["claude"])}
      workspaceID=""
      close={() => {}}
      save={save}
    />,
  );
  // Bypass userEvent's accept filter so the product's own type check runs.
  const user = userEvent.setup({ applyAccept: false });
  await open(user, /全局成员/);
  expect(
    screen.queryByRole("group", { name: "本工作空间权限" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("group", { name: "全局允许工具" }),
  ).not.toBeInTheDocument();
  expect(screen.getByText("全")).toBeInTheDocument();
  expect(screen.queryByText("🦊")).not.toBeInTheDocument();

  const input = screen.getByLabelText("上传头像");
  await user.upload(
    input,
    new File(["not an image"], "avatar.webp", { type: "image/webp" }),
  );
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "仅支持 PNG 或 JPEG 图片。",
  );
  await user.upload(input, pngFile(3000, 3000));
  await waitFor(() =>
    expect(screen.getByRole("alert")).toHaveTextContent(
      "头像尺寸不能超过 2048×2048 像素。",
    ),
  );
  await user.upload(
    input,
    new File([new Uint8Array(512 * 1024 + 1)], "large.png", {
      type: "image/png",
    }),
  );
  await waitFor(() =>
    expect(screen.getByRole("alert")).toHaveTextContent(
      "头像不能超过 512 KiB。",
    ),
  );
  await user.upload(input, pngFile());
  expect(
    await screen.findByRole("button", { name: "移除头像" }),
  ).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      id: "global",
      revision: 2,
      avatar: expect.stringMatching(/^data:image\/png;base64,/),
    }),
  );
  await user.click(screen.getByRole("button", { name: "移除头像" }));
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({ id: "global", avatar: "" }),
  );
});

it("saves member identity without color controls or color storage", async () => {
  const agent = member({ id: "neutral" });
  const save = vi.fn(async (value: Agent) => value);
  const renderForm = () =>
    render(
      <MembersForm
        agents={[agent]}
        system={system(["claude"])}
        workspaceID="ws"
        close={() => {}}
        save={save}
      />,
    );
  renderForm();
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  expect(screen.queryByLabelText("专属颜色")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("原生颜色选择")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ id: agent.id }));
  expect(localStorage.getItem("cocogem.member-colors")).toBeNull();
  cleanup();

  renderForm();
  await open(user, /布偶猫/);
  expect(screen.queryByLabelText("专属颜色")).not.toBeInTheDocument();
  expect(screen.getByLabelText("显示名")).toHaveValue(agent.display_name);
});

it("verifies the exact current executor, model and optional options", async () => {
  let resolveFetch!: (value: Response) => void;
  const fetcher = vi.fn(
    () => new Promise<Response>((resolve) => (resolveFetch = resolve)),
  );
  vi.stubGlobal("fetch", fetcher);
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[member()]}
      system={system(["claude"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  await user.type(
    screen.getByLabelText("模型（留空使用 CLI 默认）"),
    "chosen-model",
  );
  await user.type(screen.getByLabelText("上下文预算（tokens）"), "5000");
  await chooseOption(
    user,
    screen.getByLabelText("思考等级（留空保持 CLI 默认）"),
    "high",
  );
  await user.click(screen.getByRole("button", { name: "验证模型连接" }));
  expect(screen.getByRole("button", { name: "验证中…" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "验证中…" }));
  expect(fetcher).toHaveBeenCalledTimes(1);
  const [path, options] = fetcher.mock.calls[0] as unknown as [
    string,
    RequestInit,
  ];
  expect(path).toBe("/api/v1/system/verify");
  expect(JSON.parse(String(options.body))).toEqual({
    executor: "claude",
    model: "chosen-model",
    context_size: 5000,
    thinking_level: "high",
  });
  resolveFetch(Response.json({ verified_at: "2026-10-30T00:00:00Z" }));
  expect(
    await screen.findByText(
      "已验证当前执行 CLI、模型、上下文预算（tokens）和思考等级。",
    ),
  ).toBeInTheDocument();
  await user.type(screen.getByLabelText("模型（留空使用 CLI 默认）"), "x");
  expect(
    screen.queryByText("已验证当前执行 CLI、模型、上下文预算（tokens）和思考等级。"),
  ).not.toBeInTheDocument();
});

it("sends no execution overrides when the model, budget and thinking are empty", async () => {
  const fetcher = vi.fn(async () =>
    Response.json({ verified_at: "2026-10-30T00:00:00Z" }),
  );
  vi.stubGlobal("fetch", fetcher);
  const agent = member({ context_size: undefined, thinking_level: undefined });
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[agent]}
      system={system(["claude"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  expect(screen.getByLabelText("上下文预算（tokens）")).toHaveValue(null);
  expect(screen.getByLabelText("思考等级（留空保持 CLI 默认）")).toHaveValue(
    "",
  );
  await user.click(screen.getByRole("button", { name: "验证模型连接" }));
  expect(
    await screen.findByText(
      "已验证当前执行 CLI、模型、上下文预算（tokens）和思考等级。",
    ),
  ).toBeInTheDocument();
  const [, options] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
  expect(JSON.parse(String(options.body))).toEqual({
    executor: "claude",
    model: "default",
  });
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  const payload = save.mock.calls[0][0];
  expect(payload.model).toBe("default");
  expect(payload.context_size).toBeUndefined();
  expect(payload.thinking_level).toBeUndefined();
});

it("accepts an explicit context limit above the former platform ceiling", async () => {
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[member()]}
      system={system(["claude"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  await user.type(screen.getByLabelText("上下文预算（tokens）"), "500000");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({ context_size: 500000 }),
  );
});

it("requires an explicit Gemini model before enabling thinking levels", async () => {
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[member()]}
      system={system(["claude", "gemini"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  await chooseOption(user, screen.getByLabelText("执行 CLI"), "gemini");
  const thinking = screen.getByLabelText("思考等级（留空保持 CLI 默认）");
  expect(
    screen.getByText(
      "Gemini 选择思考等级前必须填写明确模型，不能留空使用默认。",
    ),
  ).toBeInTheDocument();
  expect(thinking).toBeDisabled();
  await user.type(
    screen.getByLabelText("模型（留空使用 CLI 默认）"),
    "gemini-pro",
  );
  expect(thinking).toBeEnabled();
  await chooseOption(user, thinking, "low");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({
      executor: "gemini",
      model: "gemini-pro",
      thinking_level: "low",
    }),
  );
});

it("reuses a global member identity without creating a new profile", async () => {
  const agent = member({
    id: "existing",
    workspace_id: "",
    display_name: "已有成员",
    handle: "existing",
    role_description: "复核",
    revision: 7,
  });
  const save = vi.fn(async (value: Agent) => value);
  const saveMembership = vi.fn(async () => {});
  render(
    <MembersForm
      agents={[]}
      catalog={[agent]}
      system={system(["claude"])}
      workspaceID="second"
      close={() => {}}
      save={save}
      saveMembership={saveMembership}
      unlink={async () => {}}
    />,
  );
  const user = userEvent.setup();
  await open(user, /已有成员/);
  await user.click(screen.getByRole("button", { name: "加入本工作空间" }));
  expect(saveMembership).toHaveBeenCalledWith(
    expect.objectContaining(agent),
    undefined,
    { enabled: true },
  );
  expect(save).not.toHaveBeenCalled();
});

it("preserves OpenCode member identity and its native model variant", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json({
        resolved_model: "fixture/model#high",
        thinking_levels: [],
      }),
    ),
  );
  const save = vi.fn(async (value: Agent) => ({
    ...value,
    revision: value.revision + 1,
  }));
  render(
    <MembersForm
      agents={[member({ executor: "opencode", model: "fixture/model#high" })]}
      system={system(["opencode"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  expect(screen.getByLabelText("执行 CLI")).toHaveValue("opencode");
  expect(screen.getByLabelText("模型（留空使用 CLI 默认）")).toHaveValue(
    "fixture/model#high",
  );
  expect(screen.getByLabelText("思考等级（留空保持 CLI 默认）")).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      id: "stable",
      revision: 3,
      executor: "opencode",
      model: "fixture/model#high",
      tools: ["read_file"],
    }),
  );
});

it("loads model thinking variants and verifies the selected OpenCode effort", async () => {
  const fetcher = vi.fn(async (path: string) =>
    path.includes("model-options")
      ? Response.json({
          resolved_model: "fixture/model",
          thinking_levels: ["low", "high", "custom-effort"],
        })
      : Response.json({ verified_at: "2026-11-02T00:00:00Z" }),
  );
  vi.stubGlobal("fetch", fetcher);
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[member({ executor: "opencode", model: "fixture/model" })]}
      system={system(["opencode"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  const thinking = screen.getByLabelText("思考等级（留空保持 CLI 默认）");
  await waitFor(() => expect(thinking).toBeEnabled());
  await chooseOption(user, thinking, "custom-effort");
  await user.click(screen.getByRole("button", { name: "验证模型连接" }));
  await screen.findByText(
    "已验证当前执行 CLI、模型、上下文预算（tokens）和思考等级。",
  );
  const verification = fetcher.mock.calls.find(
    (call) => call[0] === "/api/v1/system/verify",
  ) as unknown as [string, RequestInit];
  expect(JSON.parse(String(verification[1].body))).toMatchObject({
    executor: "opencode",
    model: "fixture/model",
    thinking_level: "custom-effort",
  });
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      executor: "opencode",
      thinking_level: "custom-effort",
    }),
  );
});

it("clears a thinking override when switching to an OpenCode model without variants", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) =>
      Response.json({
        resolved_model: "fixture/model",
        thinking_levels: path.includes("other") ? [] : ["high"],
      }),
    ),
  );
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[
        member({
          executor: "opencode",
          model: "fixture/model",
          thinking_level: "high",
        }),
      ]}
      system={system(["opencode"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  const thinking = screen.getByLabelText("思考等级（留空保持 CLI 默认）");
  await waitFor(() => expect(thinking).toBeEnabled());
  await user.clear(screen.getByLabelText("模型（留空使用 CLI 默认）"));
  await user.type(
    screen.getByLabelText("模型（留空使用 CLI 默认）"),
    "other/model",
  );
  await screen.findByText("当前模型未声明可选思考变体，沿用本机配置。");
  expect(thinking).toBeDisabled();
  expect(thinking).toHaveValue("");
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      model: "other/model",
      thinking_level: undefined,
    }),
  );
});

it("deletes a member only after an icon-only confirmation", async () => {
  const agent = member({
    id: "global",
    workspace_id: "",
    display_name: "全局成员",
    handle: "global",
  });
  const remove = vi.fn(async () => {});
  const user = userEvent.setup();
  render(
    <MembersForm
      agents={[]}
      catalog={[agent]}
      global
      system={system(["claude"])}
      workspaceID=""
      close={() => {}}
      save={vi.fn(async (value: Agent) => value)}
      remove={remove}
    />,
  );
  const button = screen.getByRole("button", {
    name: "删除成员 全局成员",
  });
  expect(button).toHaveTextContent("");
  expect(button.querySelector("svg")).toBeInTheDocument();
  await user.click(button);
  const dialog = screen.getByRole("dialog", { name: "删除成员" });
  expect(within(dialog).getByText(/全局成员/)).toBeVisible();
  expect(remove).not.toHaveBeenCalled();
  await user.click(within(dialog).getByRole("button", { name: "取消" }));
  expect(remove).not.toHaveBeenCalled();
  await user.click(button);
  await user.click(screen.getByRole("button", { name: "确认删除" }));
  await waitFor(() => expect(remove).toHaveBeenCalledWith(agent));
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "删除成员 全局成员" }),
    ).not.toBeInTheDocument(),
  );
  expect(
    screen.queryByRole("dialog", { name: "删除成员" }),
  ).not.toBeInTheDocument();
});

it("reports OpenCode capability errors without discarding the saved thinking override", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json(
        { error: { message: "model catalog unavailable" } },
        { status: 503 },
      ),
    ),
  );
  const save = vi.fn(async (value: Agent) => value);
  render(
    <MembersForm
      agents={[
        member({
          executor: "opencode",
          model: "fixture/model",
          thinking_level: "high",
        }),
      ]}
      system={system(["opencode"])}
      workspaceID="ws"
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await open(user, /布偶猫/);
  await screen.findByText(/无法读取思考级别：model catalog unavailable/);
  expect(screen.getByLabelText("思考等级（留空保持 CLI 默认）")).toHaveValue(
    "high",
  );
  await user.click(screen.getByRole("button", { name: "保存成员" }));
  expect(save).not.toHaveBeenCalled();
  expect(screen.getByRole("alert")).toHaveTextContent(
    "当前模型不支持此思考级别",
  );
});
