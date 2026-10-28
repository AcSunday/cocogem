import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { System } from "../../api/client";
import { WorkspaceForm } from "./WorkspaceForm";

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

const system: System = {
  models: ["default"],
  executors: [],
  roots: [],
  model_status: "not_configured",
  search_status: "not_configured",
  verified: {},
  csrf_token: "test",
  limits: {},
};

it("saves a workspace without an extra data-sharing confirmation", async () => {
  const save = vi.fn().mockResolvedValue(undefined);
  render(<WorkspaceForm system={system} close={() => {}} save={save} />);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("名称"), "我的研究室");
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({ name: "我的研究室" }),
  );
  expect(save.mock.calls[0][0]).not.toHaveProperty("consent_version");
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});

it("defaults to the shared skills and saves an explicit empty selection", async () => {
  const save = vi.fn().mockResolvedValue(undefined);
  render(
    <WorkspaceForm
      system={{
        ...system,
        skills: [
          {
            id: "task-handoff",
            name: "task-handoff",
            description: "选择符合条件的成员并交接任务。",
            version: "version-one",
          },
          {
            id: "independent-review",
            name: "independent-review",
            description: "由非作者独立复核证据。",
            version: "version-two",
          },
        ],
      }}
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("名称"), "技能配置");
  const handoff = screen.getByRole("checkbox", { name: /任务交接/ });
  const review = screen.getByRole("checkbox", { name: /独立复核/ });
  expect(handoff).toBeChecked();
  expect(review).toBeChecked();
  await user.click(handoff);
  await user.click(review);
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({ skill_ids: [] }),
  );
  await user.click(review);
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({ skill_ids: ["independent-review"] }),
  );
});

it("preserves a saved disabled skill selection when editing workspace details", async () => {
  const save = vi.fn().mockResolvedValue(undefined);
  render(
    <WorkspaceForm
      current={{
        id: "workspace",
        name: "禁用技能",
        description: "",
        root: "",
        revision: 1,
        created_at: "",
        skill_ids: [],
      }}
      system={{
        ...system,
        skills: [
          {
            id: "task-handoff",
            name: "task-handoff",
            description: "按任务交接",
            version: "version-one",
          },
        ],
      }}
      close={() => {}}
      save={save}
    />,
  );
  expect(screen.getByRole("checkbox", { name: /任务交接/ })).not.toBeChecked();
  await userEvent.click(screen.getByRole("button", { name: "保存工作空间" }));
  expect(save).toHaveBeenCalledWith(expect.objectContaining({ skill_ids: [] }));
});

it("accepts a local directory path even when no startup directories are configured", async () => {
  const save = vi.fn().mockResolvedValue(undefined);
  render(<WorkspaceForm system={system} close={() => {}} save={save} />);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("名称"), "本地项目");
  await user.click(screen.getByRole("combobox", { name: "本地资料目录" }));
  await user.click(screen.getByRole("option", { name: "输入目录路径…" }));
  await user.type(
    screen.getByLabelText("目录路径"),
    "/Users/example/中文 项目",
  );
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({ root: "/Users/example/中文 项目" }),
  );
  await user.click(screen.getByRole("combobox", { name: "本地资料目录" }));
  await user.click(screen.getByRole("option", { name: "暂不绑定" }));
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  expect(save).toHaveBeenLastCalledWith(expect.objectContaining({ root: "" }));
});

it("retains a previously saved custom path and keeps it after a failed save", async () => {
  const save = vi.fn().mockRejectedValue(new Error("目录不存在或无法访问"));
  render(
    <WorkspaceForm
      current={{
        id: "ws",
        name: "现有项目",
        description: "",
        root: "/existing/project",
        revision: 2,
        created_at: "",
        skill_ids: null,
      }}
      system={system}
      close={() => {}}
      save={save}
    />,
  );
  const user = userEvent.setup();
  expect(screen.getByLabelText("目录路径")).toHaveValue("/existing/project");
  await user.clear(screen.getByLabelText("目录路径"));
  await user.type(screen.getByLabelText("目录路径"), "/missing/project");
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "目录不存在或无法访问",
  );
  expect(screen.getByLabelText("目录路径")).toHaveValue("/missing/project");
  expect(screen.getByLabelText("名称")).toHaveValue("现有项目");
});

it("browses without saving, keeps the original binding on cancel and saves the chosen folder", async () => {
  const save = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json({
        path: "/public/selected",
        parent: "/public",
        items: [],
        truncated: false,
      }),
    ),
  );
  render(<WorkspaceForm system={system} close={() => {}} save={save} />);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("名称"), "图形选择目录");
  await user.click(screen.getByRole("button", { name: "浏览目录" }));
  await screen.findByText("没有可选的子目录");
  expect(screen.getByRole("button", { name: "保存工作空间" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "取消选择" }));
  expect(screen.getByRole("combobox", { name: "本地资料目录" })).toHaveValue(
    "",
  );
  expect(screen.getByRole("button", { name: "浏览目录" })).toHaveFocus();
  expect(save).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "浏览目录" }));
  await screen.findByText("没有可选的子目录");
  await user.click(screen.getByRole("button", { name: "选择此目录" }));
  expect(
    screen.queryByRole("region", { name: "选择本机目录" }),
  ).not.toBeInTheDocument();
  expect(screen.getByLabelText("目录路径")).toHaveValue("/public/selected");
  expect(save).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({ root: "/public/selected" }),
  );
});
