import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Agent, System } from "../api/client";
import { App } from "./App";

const system: System = {
  demo: false,
  models: ["default"],
  roots: [],
  model_status: "configured",
  search_status: "not_configured",
  verified: {},
  csrf_token: "test",
  limits: {},
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
};
HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute("open", "");
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute("open");
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  history.replaceState({}, "", "/");
});

it("verifies the selected local CLI and exact model, with unsupported versions disabled", async () => {
  const info = structuredClone(system);
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/v1/system/verify") {
      const input = JSON.parse(String(options?.body));
      info.verified[`${input.executor}:${input.model}`] =
        "2026-10-19T00:00:00Z";
      return Response.json({ ...input, verified_at: "2026-10-19T00:00:00Z" });
    }
    return Response.json(path === "/api/v1/system" ? info : { items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "服务与设置" }));
  expect(screen.getByText(/已安装不代表连接或模型可用/)).toBeInTheDocument();
  await user.selectOptions(screen.getByLabelText("验证 CLI"), "codex");
  expect(
    screen.getByText(/使用限制：只读工具范围由 Cocogem 管理/),
  ).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "验证模型连接（可能产生用量）" }),
  ).toBeEnabled();
  expect(
    screen.getByLabelText("验证模型（留空使用 CLI 默认）"),
  ).not.toBeRequired();
  await user.click(
    screen.getByRole("button", { name: "验证模型连接（可能产生用量）" }),
  );
  await waitFor(() =>
    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/system/verify",
      expect.objectContaining({
        body: JSON.stringify({ executor: "codex", model: "default" }),
      }),
    ),
  );
  await screen.findByText(/该 CLI 与模型上次已验证/);
  await user.type(
    screen.getByLabelText("验证模型（留空使用 CLI 默认）"),
    "chosen-model",
  );
  await user.click(
    screen.getByRole("button", { name: "验证模型连接（可能产生用量）" }),
  );
  await waitFor(() =>
    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/system/verify",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ executor: "codex", model: "chosen-model" }),
      }),
    ),
  );
  expect(
    await screen.findByText(/该 CLI 与模型上次已验证/),
  ).toBeInTheDocument();
  await user.selectOptions(screen.getByLabelText("验证 CLI"), "gemini");
  expect(
    screen.getByRole("button", { name: "验证模型连接（可能产生用量）" }),
  ).toBeDisabled();
  expect(screen.getByText(/不支持：缺少安全工具模式/)).toBeInTheDocument();
});

it("uses the saved conversation workflow and enabled conversation roles without adding outsiders", async () => {
  history.replaceState({}, "", "/w/ws/t/two");
  const agents: Agent[] = ["disabled", "one", "two", "outside"].map((id) => ({
    id,
    workspace_id: "ws",
    display_name: id,
    handle: id,
    aliases: [],
    role_description: "",
    model: "default",
    executor: "codex",
    tools: [],
    disabled: id === "disabled",
    revision: 1,
  }));
  const thread = {
    id: "two",
    workspace_id: "ws",
    title: "双成员研究",
    agent_ids: ["one", "two"],
    coordinator_id: "two",
    workflow: "research",
    revision: 1,
    updated_at: "",
  };
  let previewInput:
    { targets: string[]; assignments: Record<string, string> } | undefined;
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [{ id: "ws", name: "工作空间" }] });
    if (path === "/api/v1/workspaces/ws/agents")
      return Response.json({ items: agents });
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [thread], next_cursor: "" });
    if (path.startsWith("/api/v1/threads/two?"))
      return Response.json({
        thread,
        messages: { items: [], next_cursor: "" },
        runs: [],
        sources: [],
      });
    if (path === "/api/v1/threads/two/routing-preview") {
      previewInput = JSON.parse(String(options?.body));
      return Response.json({ routing: { clarification: "保留问题等待确认" } });
    }
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "双成员研究" });
  await waitFor(() => expect(screen.getByLabelText("研究")).toHaveValue("one"));
  expect(screen.getByLabelText("工作方式")).toHaveValue("research");
  expect(screen.getByLabelText("质疑")).toHaveValue("two");
  expect(screen.getByLabelText("汇总")).toHaveValue("two");
  await user.selectOptions(screen.getByLabelText("工作方式"), "discussion");
  await user.type(screen.getByLabelText("输入问题"), "比较协作的取舍");
  await user.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() =>
    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/threads/two/routing-preview",
      expect.objectContaining({ body: expect.any(String) }),
    ),
  );
  expect(previewInput?.targets).toEqual([]);
  expect(previewInput?.assignments).toMatchObject({
    coordinator: "two",
    researcher: "one",
    critic: "two",
  });
  expect(Object.values(previewInput?.assignments ?? {})).not.toContain(
    "disabled",
  );
  expect(Object.values(previewInput?.assignments ?? {})).not.toContain(
    "outside",
  );
});
