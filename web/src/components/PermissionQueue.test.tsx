import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { APIError, type ToolPermissionRequest } from "../api/client";
import { PermissionQueue } from "./PermissionQueue";
import {
  PermissionDecision,
  resetDecisionState,
  submitPermissionDecision,
} from "./PermissionDecision";
import type { PermissionPrompt } from "../lib/toolActivity";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

afterEach(() => {
  cleanup();
  request.mockReset();
  // The decision state is request-wide (shared by both rendered locations), so
  // it must start empty for each test.
  resetDecisionState();
});

const permission = (
  overrides: Partial<ToolPermissionRequest> = {},
): ToolPermissionRequest => ({
  id: "request-1",
  thread_id: "thread",
  workspace_id: "ws",
  run_id: "run",
  step_id: "step",
  attempt_id: "attempt",
  member_id: "member",
  member_name: "布偶猫",
  tool_call_id: "call-1",
  origin: "host",
  action: "write",
  tool_name: "write_file",
  normalized_resource_scope: "/project/reports/out.md",
  arguments_digest: "digest",
  reason: "项目外写入需要确认",
  permission_epoch: 3,
  status: "pending",
  request_id: "req-key-1",
  revision: 2,
  created_at: "2026-11-08T00:00:00Z",
  ...overrides,
});

const prompt = (overrides: Partial<PermissionPrompt> = {}): PermissionPrompt => ({
  requestID: "request-1",
  activityKey: "run|step|attempt|call-1",
  toolCallID: "call-1",
  memberID: "member",
  memberName: "布偶猫",
  toolName: "write_file",
  label: "写入文件",
  target: "reports/out.md",
  scope: "/project/reports/out.md",
  reason: "项目外写入需要确认",
  ...overrides,
});

it("offers exactly the three decisions and shows member, action, scope and reason", async () => {
  const onDecide = vi.fn(async () => {});
  render(<PermissionDecision prompt={prompt()} request={permission()} onDecide={onDecide} />);
  const card = screen.getByRole("group", { name: /待决定授权/ });
  expect(within(card).getByText(/布偶猫/)).toBeVisible();
  expect(within(card).getByText("写入")).toBeVisible();
  expect(within(card).getByText("/project/reports/out.md")).toBeVisible();
  expect(within(card).getByText("项目外写入需要确认")).toBeVisible();
  expect(within(card).getByText(/同会话所有成员共享/)).toBeVisible();
  const names = within(card)
    .getAllByRole("button")
    .map((button) => button.textContent);
  expect(names).toEqual(["仅本次允许", "本会话持续允许", "拒绝"]);
});

it("sends the chosen decision once even if it is clicked again", async () => {
  let finish!: () => void;
  const onDecide = vi.fn(
    () => new Promise<void>((resolve) => (finish = resolve)),
  );
  render(<PermissionDecision prompt={prompt()} request={permission()} onDecide={onDecide} />);
  const user = userEvent.setup();
  const allow = screen.getByRole("button", { name: "仅本次允许" });
  await user.click(allow);
  await user.click(allow);
  await user.click(screen.getByRole("button", { name: "拒绝" }));
  expect(onDecide).toHaveBeenCalledTimes(1);
  expect(onDecide).toHaveBeenCalledWith("allow_once");
  await act(async () => finish());
  expect(await screen.findByRole("status")).toHaveTextContent("已允许本次操作");
  // Turns are final: no decision button remains after the answer.
  expect(screen.queryByRole("button", { name: "拒绝" })).not.toBeInTheDocument();
});

it("keeps the card usable and shows the real error when a decision fails", async () => {
  const onDecide = vi.fn(async () => {
    throw new Error("该授权请求已过期或已被更新；请刷新待处理授权后再决定。");
  });
  render(<PermissionDecision prompt={prompt()} request={permission()} onDecide={onDecide} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "本会话持续允许" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "该授权请求已过期或已被更新",
  );
  expect(screen.getByRole("button", { name: "拒绝" })).toBeEnabled();
});

it("shows the oldest pending request and counts the rest", async () => {
  const first = permission();
  const second = permission({ id: "request-2", request_id: "req-key-2" });
  const onDecide = vi.fn(async () => {});
  render(
    <PermissionQueue
      items={[first, second]}
      visible={first}
      onDecide={onDecide}
      onDismiss={() => {}}
      onRestore={() => {}}
    />,
  );
  const layer = screen.getByRole("region", { name: "待处理授权" });
  expect(layer).toHaveTextContent("等待授权（1/2）");
  expect(layer).toHaveTextContent("还有 1 个待处理请求");
  expect(
    within(layer).getByRole("group", { name: /待决定授权/ }),
  ).toHaveAttribute("data-permission-decision", "request-1");
  const user = userEvent.setup();
  await user.click(within(layer).getByRole("button", { name: "仅本次允许" }));
  expect(onDecide).toHaveBeenCalledWith("request-1", "allow_once");
});

it("dismissing is not a decision and offers to reopen", async () => {
  const onDecide = vi.fn(async () => {});
  const onDismiss = vi.fn();
  const { rerender } = render(
    <PermissionQueue
      items={[permission()]}
      visible={permission()}
      onDecide={onDecide}
      onDismiss={onDismiss}
      onRestore={() => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "稍后处理" }));
  expect(onDismiss).toHaveBeenCalledOnce();
  // No decision was posted; the request is still pending.
  expect(onDecide).not.toHaveBeenCalled();
  const restore = vi.fn();
  rerender(
    <PermissionQueue
      items={[permission()]}
      visible={undefined}
      onDecide={onDecide}
      onDismiss={() => {}}
      onRestore={restore}
    />,
  );
  // A single dismissed-but-pending request shows only the reopen bar; it is NOT
  // silently re-activated as the visible card.
  expect(
    screen.getByText("1 个操作等待授权，已折叠但未决定。"),
  ).toBeVisible();
  expect(
    screen.queryByRole("group", { name: /待决定授权/ }),
  ).not.toBeInTheDocument();
  expect(onDecide).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "重新打开" }));
  expect(restore).toHaveBeenCalledOnce();
});

it("decides through If-Match and a client-owned idempotency key", async () => {
  request.mockResolvedValue({ ...permission(), status: "allowed_conversation" });
  const saved = await submitPermissionDecision(
    permission(),
    "allow_conversation",
  );
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/permission-requests/request-1/decisions",
      "POST",
      // Host/native requests leave request_id empty; the UI must mint its own
      // 8–100 char key instead of forwarding the stored (empty) creation id.
      { decision: "allow_conversation", request_id: "decision:request-1:allow_conversation" },
      { "If-Match": "2" },
    ),
  );
  expect(saved.status).toBe("allowed_conversation");
});

it("sends a valid decision key even when the host request stored no request_id", async () => {
  request.mockResolvedValue({ ...permission(), request_id: "" });
  // Realistic host/native request: the creation id was never set.
  await submitPermissionDecision(permission({ request_id: "" }), "allow_once");
  const body = request.mock.calls[0][2] as { request_id: string };
  expect(body.request_id.length).toBeGreaterThanOrEqual(8);
  expect(body.request_id.length).toBeLessThanOrEqual(100);
  expect(body.request_id).toBe("decision:request-1:allow_once");
});

it("reuses the same key on a transient retry and mints a new one per decision", async () => {
  request.mockRejectedValueOnce(new APIError("读取本地服务响应失败", 0));
  await expect(
    submitPermissionDecision(permission({ request_id: "" }), "allow_once"),
  ).rejects.toThrow();
  request.mockResolvedValue(permission());
  await submitPermissionDecision(permission({ request_id: "" }), "allow_once");
  const [, , firstBody] = request.mock.calls[0];
  const [, , retryBody] = request.mock.calls[1];
  expect((retryBody as { request_id: string }).request_id).toBe(
    (firstBody as { request_id: string }).request_id,
  );
  await submitPermissionDecision(permission({ request_id: "" }), "deny");
  const [, , otherBody] = request.mock.calls[2];
  expect((otherBody as { request_id: string }).request_id).not.toBe(
    (retryBody as { request_id: string }).request_id,
  );
});

it("turns a 409 into an explicit re-evaluation message", async () => {
  request.mockRejectedValue(new APIError("revision_conflict", 409));
  await expect(
    submitPermissionDecision(permission(), "allow_once"),
  ).rejects.toThrow(/已过期、已决定或与先前的选择冲突/);
});

it("two rendered instances of one request share one in-flight decision", async () => {
  let finish!: () => void;
  const inline = vi.fn(
    () => new Promise<void>((resolve) => (finish = resolve)),
  );
  // The same request is visible in the member's inline activity AND the queue.
  render(
    <>
      <PermissionDecision prompt={prompt()} request={permission()} onDecide={inline} />
      <PermissionDecision prompt={prompt()} request={permission()} onDecide={inline} />
    </>,
  );
  const cards = screen.getAllByRole("group", { name: /待决定授权/ });
  expect(cards).toHaveLength(2);
  const user = userEvent.setup();
  // Approve from the queue copy...
  await user.click(within(cards[0]).getByRole("button", { name: "仅本次允许" }));
  // ...while it is in flight the inline copy is also busy and non-clickable.
  for (const card of screen.getAllByRole("group", { name: /待决定授权/ })) {
    expect(within(card).getByRole("button", { name: "正在允许…" })).toBeDisabled();
    expect(within(card).getByRole("button", { name: "拒绝" })).toBeDisabled();
  }
  // A programmatic submit on the other copy (the disabled attribute is only a
  // hint; keyboard/assistive paths can still dispatch) joins the promise and
  // never starts a second POST.
  await act(async () => {
    const button = within(cards[1]).getByRole("button", { name: "本会话持续允许" });
    button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await Promise.resolve();
  });
  expect(inline).toHaveBeenCalledTimes(1);
  expect(inline).toHaveBeenCalledWith("allow_once");
  await act(async () => finish());
  // Both copies settle together and show the decision read-only.
  await waitFor(() => {
    for (const card of screen.getAllByRole("group", { name: /待决定授权/ }))
      expect(within(card).getByRole("status")).toHaveTextContent(
        "已允许本次操作",
      );
  });
  for (const card of screen.getAllByRole("group", { name: /待决定授权/ }))
    expect(within(card).queryByRole("button")).not.toBeInTheDocument();
});

it("a persisted decision renders read-only without new actions", () => {
  // The queue approved the request but the host has not emitted a new running
  // event yet: the old card must not look pending again.
  for (const state of ["allowed_once", "allowed_conversation", "denied", "expired", "canceled"]) {
    const { container } = render(
      <PermissionDecision
        prompt={prompt()}
        request={permission({ status: state })}
        onDecide={vi.fn(async () => {})}
      />,
    );
    expect(within(container).queryByRole("button")).not.toBeInTheDocument();
    expect(within(container).getByRole("status")).toBeVisible();
    cleanup();
  }
});

it("renders the host's bounded operation summary instead of inventing detail", async () => {
  const onDecide = vi.fn(async () => {});
  render(
    <PermissionQueue
      items={[
        permission({
          tool_name: "read_file",
          normalized_resource_scope: "/tmp/outside.txt",
          operation_summary: "read_file /tmp/outside.txt (scope /tmp/outside.txt)",
        }),
      ]}
      visible={permission({
        tool_name: "read_file",
        normalized_resource_scope: "/tmp/outside.txt",
        operation_summary: "read_file /tmp/outside.txt (scope /tmp/outside.txt)",
      })}
      onDecide={onDecide}
      onDismiss={() => {}}
      onRestore={() => {}}
    />,
  );
  const card = screen.getByRole("group", { name: /待决定授权/ });
  // A localized label plus the server's own description; never "read_file read_file".
  expect(within(card).getByText("读取文件")).toBeVisible();
  expect(
    within(card).getByText(
      "read_file /tmp/outside.txt (scope /tmp/outside.txt)",
    ),
  ).toBeVisible();
  expect(card.textContent).not.toMatch(/read_file\s+read_file/);
  // The real scope is shown, not a "missing scope" placeholder.
  expect(within(card).getByText("/tmp/outside.txt")).toBeVisible();
  expect(card.textContent).not.toContain("未提供范围");
});

it("falls back to the persisted scope when the live event has none", () => {
  render(
    <PermissionDecision
      // The activity event path may not carry the normalized scope yet.
      prompt={prompt({ scope: "", reason: "" })}
      request={permission({
        normalized_resource_scope: "/tmp/outside.txt",
        reason: "操作 read_file 需要授权",
      })}
      onDecide={vi.fn(async () => {})}
    />,
  );
  const card = screen.getByRole("group", { name: /待决定授权/ });
  expect(within(card).getByText("/tmp/outside.txt")).toBeVisible();
  expect(within(card).getByText("操作 read_file 需要授权")).toBeVisible();
  expect(card.textContent).not.toContain("未提供范围");
});
