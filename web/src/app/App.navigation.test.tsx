import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  history.replaceState({}, "", "/");
});

it("keeps conversations under home when switching from the task list or browser history", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const thread = { id: "thread", workspace_id: "ws", title: "导航检查", agent_ids: [], workflow: "free", revision: 1 };
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    if (path === "/api/v1/system") return Response.json({ models: [], roots: [], verified: {}, limits: {}, executors: [], csrf_token: "test" });
    if (path === "/api/v1/workspaces") return Response.json({ items: [{ id: "ws", name: "测试空间" }] });
    if (path.startsWith("/api/v1/workspaces/ws/threads")) return Response.json({ items: [thread] });
    if (path.startsWith("/api/v1/threads/thread") && !path.includes("artifacts")) return Response.json({ thread, messages: { items: [] }, runs: [], sources: [] });
    if (path === "/api/v1/tasks") return Response.json({ tasks: [], needs_attention: [], workspaces: { ws: "测试空间" } });
    if (path.endsWith("/overview")) return Response.json({ workspace: { id: "ws", name: "测试空间" }, tasks: [], needs_attention: [], records: [] });
    return Response.json({ items: [] });
  }));
  render(<App />);
  const user = userEvent.setup();
  expect(await screen.findByRole("heading", { name: "导航检查" })).toBeVisible();
  expect(screen.getByRole("button", { name: "成员 0" })).toBeVisible();
  expect(screen.getByRole("button", { name: "打开协作面板" })).toBeVisible();
  const home = screen.getByRole("button", { name: "项目首页" });
  const tasks = screen.getByRole("button", { name: "任务清单" });
  expect(home).toHaveAttribute("aria-current", "page");
  expect(tasks).not.toHaveAttribute("aria-current");
  await user.click(tasks);
  expect(await screen.findByRole("heading", { name: "任务清单" })).toBeVisible();
  expect(tasks).toHaveAttribute("aria-current", "page");
  expect(screen.queryByRole("navigation", { name: "会话列表" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^成员 / })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "打开协作面板" })).not.toBeInTheDocument();
  history.replaceState({}, "", "/w/ws/t/thread");
  window.dispatchEvent(new PopStateEvent("popstate"));
  expect(await screen.findByRole("heading", { name: "导航检查" })).toBeVisible();
  expect(home).toHaveAttribute("aria-current", "page");
  expect(tasks).not.toHaveAttribute("aria-current");
} );
