import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Agent, WorkspaceMember } from "../../api/client";
import { WorkspaceMembersForm } from "./WorkspaceMembersForm";

HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute("open", "");
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute("open");
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const profile = (value: Partial<Agent> & Pick<Agent, "id">): Agent => ({
  workspace_id: "",
  display_name: value.id,
  handle: value.id,
  aliases: [],
  role_description: "",
  model: "default",
  executor: "claude",
  tools: ["read_file"],
  disabled: false,
  revision: 1,
  ...value,
});

const member = (agent: Agent, grant: WorkspaceMember) => ({
  ...agent,
  workspace_id: "ws",
  tools: grant.tools,
  membership: grant,
});

it("only shows workspace grants and the effective tool intersection", async () => {
  const shared = profile({
    id: "shared",
    display_name: "共享成员",
    handle: "shared",
    revision: 4,
  });
  const grant: WorkspaceMember = {
    workspace_id: "ws",
    agent_id: "shared",
    enabled: true,
    tools: ["read_file", "web_search"],
    revision: 2,
  };
  const fetcher = vi.fn(async () => Response.json({}));
  vi.stubGlobal("fetch", fetcher);
  const refresh = vi.fn(async () => {});
  render(
    <WorkspaceMembersForm
      agents={[member(shared, grant)]}
      catalog={[shared]}
      workspaceID="ws"
      refresh={refresh}
      close={() => {}}
    />,
  );
  expect(screen.queryByLabelText("显示名")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Handle")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("别名（用逗号分隔）")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("职责")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("能力标签（用逗号分隔）")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("头像")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("执行 CLI")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("模型（留空使用 CLI 默认）")).not.toBeInTheDocument();
  expect(
    screen.queryByText("在所有工作空间停用此成员"),
  ).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "＋ 新成员" })).not.toBeInTheDocument();
  expect(screen.getByText(/全局档案请到设置/)).toBeVisible();
  expect(screen.getByText(/当前实际生效：read_file。/)).toBeVisible();
  const stale = screen.getByLabelText(/^web_search/);
  expect(stale).toBeChecked();
  expect(stale).toBeEnabled();
  expect(screen.getByLabelText(/^publish_artifact/)).toBeDisabled();

  const user = userEvent.setup();
  await user.click(stale);
  await user.click(screen.getByLabelText("在本工作空间启用"));
  expect(screen.getByText(/当前实际生效：无。/)).toBeVisible();
  await user.click(screen.getByRole("button", { name: "保存本空间权限" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/workspaces/ws/members/shared",
    expect.objectContaining({
      method: "PATCH",
      headers: expect.objectContaining({ "If-Match": "2" }),
      body: JSON.stringify({ enabled: false, tools: ["read_file"] }),
    }),
  );
  expect(screen.getByRole("status")).toHaveTextContent(
    "本空间成员配置已保存。",
  );
});

it("joins an existing global profile without a membership revision", async () => {
  const joined = profile({ id: "joined", display_name: "已有成员" });
  const outsider = profile({
    id: "visitor",
    display_name: "访客猫",
    handle: "visitor",
    tools: ["read_file", "web_search"],
    revision: 3,
  });
  const fetcher = vi.fn(async () => Response.json({}));
  vi.stubGlobal("fetch", fetcher);
  const refresh = vi.fn(async () => {});
  render(
    <WorkspaceMembersForm
      agents={[
        member(joined, {
          workspace_id: "ws",
          agent_id: joined.id,
          enabled: true,
          tools: ["read_file"],
          revision: 1,
        }),
      ]}
      catalog={[joined, outsider]}
      workspaceID="ws"
      refresh={refresh}
      close={() => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(
    screen.getByRole("button", { name: "访客猫 (@visitor) · 可加入" }),
  );
  await user.click(screen.getByRole("button", { name: "加入本工作空间" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/workspaces/ws/members/visitor",
    expect.objectContaining({
      method: "PUT",
      headers: expect.not.objectContaining({ "If-Match": expect.anything() }),
      body: JSON.stringify({
        enabled: true,
        tools: ["read_file", "web_search"],
      }),
    }),
  );
  expect(screen.getByRole("status")).toHaveTextContent(
    "本空间成员配置已保存。",
  );
});

it("removes workspace membership with its current revision", async () => {
  const shared = profile({ id: "shared" });
  const fetcher = vi.fn(async () => Response.json({}));
  vi.stubGlobal("fetch", fetcher);
  const refresh = vi.fn(async () => {});
  render(
    <WorkspaceMembersForm
      agents={[
        member(shared, {
          workspace_id: "ws",
          agent_id: shared.id,
          enabled: true,
          tools: ["read_file"],
          revision: 5,
        }),
      ]}
      catalog={[shared]}
      workspaceID="ws"
      refresh={refresh}
      close={() => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "移出本工作空间" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/workspaces/ws/members/shared",
    expect.objectContaining({
      method: "DELETE",
      headers: expect.objectContaining({ "If-Match": "5" }),
    }),
  );
});

it("keeps the backend revision error visible without refreshing", async () => {
  const shared = profile({ id: "shared" });
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json(
        { error: { message: "配置已更新，请刷新后重新编辑" } },
        { status: 412 },
      ),
    ),
  );
  const refresh = vi.fn(async () => {});
  render(
    <WorkspaceMembersForm
      agents={[
        member(shared, {
          workspace_id: "ws",
          agent_id: shared.id,
          enabled: true,
          tools: ["read_file"],
          revision: 2,
        }),
      ]}
      catalog={[shared]}
      workspaceID="ws"
      refresh={refresh}
      close={() => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "保存本空间权限" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "配置已更新，请刷新后重新编辑",
  );
  expect(refresh).not.toHaveBeenCalled();
});
