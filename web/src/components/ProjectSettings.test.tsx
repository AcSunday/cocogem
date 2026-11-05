import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Workspace } from "../api/client";
import { ProjectSettings } from "./ProjectSettings";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()), api: request,
}));
const workspace = {
  id: "ws", name: "项目", description: "", root: "", revision: 1, created_at: "", skill_ids: [],
  execution_mode: "controlled", budget_override: { auto_continuations: 8 },
} as Workspace;

afterEach(() => { cleanup(); request.mockReset(); });

it("saves field-level budget inheritance and revokes a durable grant", async () => {
  request.mockImplementation(async (path: string) => {
    if (path === "/api/v1/workspaces/ws/grants") return { items: [{ id: "grant", workspace_id: "ws", subject_member_id: "member", actions: ["write"], resources: ["project"], status: "active", epoch: 2 }] };
    return { items: [] };
  });
  const changed = vi.fn();
  const user = userEvent.setup();
  render(<ProjectSettings workspace={workspace} onWorkspaceChange={changed} settings={{ revision: 1, default_coordinator_id: "", assignments: {}, collaboration_budget: { auto_continuations: 20, parallel_branches: 4, writer_branches: 2, fix_rounds: 3, no_progress_failures: 2 } }} />);
  const input = await screen.findByLabelText("自动续办次数");
  expect(input).toHaveValue(8);
  await user.clear(input);
  await user.click(screen.getByRole("button", { name: "保存项目覆盖" }));
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/workspaces/ws/budget-policy", "PATCH", { override: {} }, { "If-Match": "1" }));
  await user.click(screen.getByRole("button", { name: "全部恢复继承" }));
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/workspaces/ws/budget-policy", "PATCH", { override: null }, { "If-Match": "1" }));
  await user.click(await screen.findByRole("button", { name: "撤销此成员授权" }));
  await waitFor(() => expect(request).toHaveBeenCalledWith("/api/v1/workspaces/ws/grants/member/revoke", "POST", expect.objectContaining({ source_message_id: expect.any(String) })));
});
