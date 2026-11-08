import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { TaskCreateForm } from "./TaskCreateForm";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (original) => ({ ...(await original<typeof import("../api/client")>()), api: request }));
afterEach(() => { cleanup(); request.mockReset(); });

it("keeps the saved task when launch fails and retries without creating another", async () => {
  let launches = 0;
  request.mockImplementation(async (path: string) => {
    if (path.endsWith("/agents")) return { items: [] };
    if (path.endsWith("/tasks")) return { task: { id: "task", workspace_id: "ws", thread_id: "" } };
    if (++launches === 1) throw new Error("CLI 暂时不可用");
    return { id: "run", thread_id: "thread" };
  });
  const done = vi.fn();
  const user = userEvent.setup();
  render(<TaskCreateForm workspaces={{ ws: "项目" }} onCreated={done} />);
  await user.type(screen.getByLabelText("任务标题"), "生成报告");
  await user.type(screen.getByLabelText("目标与交付要求"), "对照资料生成报告");
  await user.click(screen.getByRole("button", { name: "创建并开始" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("任务已保存");
  await user.click(screen.getByRole("button", { name: "重试开始" }));
  await waitFor(() => expect(done).toHaveBeenCalledWith(expect.objectContaining({ task: { id: "task", workspace_id: "ws", thread_id: "" } }), { id: "run", thread_id: "thread" }));
  expect(request.mock.calls.filter(([path]) => path === "/api/v1/workspaces/ws/tasks")).toHaveLength(1);
});
