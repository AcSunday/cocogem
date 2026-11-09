import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Settings } from "../api/client";
import { ConversationGrants } from "./PermissionDecision";
import { PermissionSettings } from "./PermissionSettings";

/**
 * The grant list is loaded by the real `threadPermissionGrants` client, so the
 * test stubs `fetch` (the layer below `api`) instead of mocking `api`.
 */
const fetch = vi.fn<typeof globalThis.fetch>(
  async () => Response.json({ items: [] }) as Response,
);
vi.stubGlobal("fetch", fetch);

afterEach(() => {
  cleanup();
  fetch.mockReset();
  fetch.mockImplementation(async () => Response.json({ items: [] }) as Response);
});

const settings = {
  revision: 4,
  default_coordinator_id: "",
  assignments: {},
  retention_days: 7,
  collaboration_budget: {
    auto_continuations: 20,
    parallel_branches: 4,
    writer_branches: 2,
    fix_rounds: 3,
    no_progress_failures: 2,
  },
  execution_mode: "project_autonomous",
  permission_epoch: 9,
} satisfies Settings;

it("describes a permission change in plain language, never internal epochs", () => {
  const { container } = render(
    <PermissionSettings
      settings={settings}
      onExecutionMode={vi.fn(async () => {})}
      threadID="thread"
    />,
  );
  const text = container.textContent ?? "";
  // No runtime internals reach the page.
  for (const internal of ["epoch", "代次", "permission_epoch", "Grant", "授权代次"])
    expect(text).not.toContain(internal);
  // The plain consequence is stated instead.
  expect(text).toContain("之前额外允许的操作和本会话持续授权都会失效");
  // The three user-facing mode names remain.
  expect(screen.getByText("受控")).toBeVisible();
  expect(screen.getByText("项目内自主")).toBeVisible();
  expect(screen.getByText("完全访问")).toBeVisible();
});

it("labels a grant from an earlier boundary as expired by the switch", async () => {
  fetch.mockImplementation(async () =>
    Response.json({
      items: [
        {
          id: "grant-old",
          thread_id: "thread",
          action: "write",
          tool_matcher: "write_file",
          normalized_resource_scope: "/project/a.md",
          // An earlier boundary than the current settings record.
          permission_epoch: 3,
          source_request_id: "r1",
          status: "active",
          created_at: "2026-11-08T00:00:00Z",
        },
      ],
    }),
  );
  const { container } = render(
    <PermissionSettings
      settings={settings}
      onExecutionMode={vi.fn(async () => {})}
      threadID="thread"
    />,
  );
  expect(await screen.findByText(/已随权限切换失效/, { exact: false })).toBeVisible();
  const text = container.textContent ?? "";
  expect(text).not.toContain("epoch");
  expect(text).not.toContain("代次");
});

const conversationGrant = (thread: string) => ({
  id: `grant-${thread}`,
  thread_id: thread,
  action: "write",
  tool_matcher: `fixture-${thread}`,
  normalized_resource_scope: `/fixture/${thread}`,
  permission_epoch: 1,
  source_request_id: `request-${thread}`,
  status: "active",
  created_at: "2026-11-08T00:00:00Z",
});

it("a late previous-thread grant response never replaces the current thread", async () => {
  const releaseA: { fn?: () => void } = {};
  const heldA = new Promise<void>((resolve) => (releaseA.fn = resolve));
  fetch.mockImplementation(async (input) => {
    const path = String(input);
    if (path.includes("thread-a")) {
      await heldA;
      return Response.json({ items: [conversationGrant("a")] });
    }
    // Thread B answers immediately, before A is released.
    return Response.json({ items: [conversationGrant("b")] });
  });
  const { rerender } = render(
    <ConversationGrants threadID="thread-a" threadTitle="Conversation A" />,
  );
  rerender(<ConversationGrants threadID="thread-b" threadTitle="Conversation B" />);
  expect(await screen.findByText("fixture-b")).toBeVisible();
  // The held A response resolves after B is on screen.
  await act(async () => {
    releaseA.fn?.();
    await Promise.resolve();
    await Promise.resolve();
  });
  // The list stays B's; A's grant is not shown (and so cannot be revoked).
  expect(screen.getByText("fixture-b")).toBeVisible();
  expect(screen.queryByText("fixture-a")).not.toBeInTheDocument();
  expect(screen.getByText("会话「Conversation B」")).toBeVisible();
  const revokePaths = fetch.mock.calls
    .map((call) => String(call[0]))
    .filter((path) => path.includes("/revoke"));
  expect(revokePaths).toEqual([]);
});

it("refuses to revoke a grant that belongs to another conversation", async () => {
  // Defensive: even if a foreign row reached the list, the revoke is pinned to
  // the displayed thread instead of silently revoking the wrong conversation.
  fetch.mockImplementation(async () =>
    Response.json({ items: [conversationGrant("thread-a")] }),
  );
  render(<ConversationGrants threadID="thread-b" threadTitle="Conversation B" />);
  const revoke = await screen.findByRole("button", { name: "撤销" });
  await userEvent.click(revoke);
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "授权记录与当前会话不一致",
  );
  expect(
    fetch.mock.calls.map((call) => String(call[0])).some((path) =>
      path.includes("/revoke"),
    ),
  ).toBe(false);
});
