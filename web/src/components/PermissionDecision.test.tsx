import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { APIError, type ToolPermissionRequest } from "../api/client";
import {
  resetDecisionKeys,
  resetDecisionState,
  submitPermissionDecision,
  usePermissionRequests,
} from "./PermissionDecision";

const { request } = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: request,
}));

afterEach(() => {
  cleanup();
  request.mockReset();
  vi.unstubAllGlobals();
  resetDecisionKeys();
  resetDecisionState();
});

const permission = (
  id: string,
  overrides: Partial<ToolPermissionRequest> = {},
): ToolPermissionRequest => ({
  id,
  thread_id: "thread-a",
  workspace_id: "ws",
  run_id: "run",
  step_id: "step",
  attempt_id: "attempt",
  member_id: "member",
  tool_call_id: `call-${id}`,
  origin: "host",
  action: "write",
  tool_name: "write_file",
  normalized_resource_scope: "/project/a.md",
  arguments_digest: "digest",
  reason: "reason",
  permission_epoch: 1,
  status: "pending",
  request_id: "",
  revision: 1,
  created_at: "2026-11-08T00:00:00Z",
  ...overrides,
});

/**
 * The queue is served by the REAL `threadPermissionRequests` client function, so
 * stub `fetch` (the layer below `api`) to control per-thread responses. A `api`
 * mock would bypass that function entirely.
 */
function stubServer(routes: Record<string, () => Promise<ToolPermissionRequest[]>>) {
  const fetch = vi.fn(async (path: string) => {
    for (const [needle, load] of Object.entries(routes))
      if (path.includes(needle)) return Response.json({ items: await load() });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

it("closing a single request keeps it pending and lets it be reopened", async () => {
  stubServer({ "thread-a": async () => [permission("one")] });
  const { result } = renderHook(() => usePermissionRequests("thread-a"));
  await waitFor(() => expect(result.current.visible?.id).toBe("one"));
  act(() => result.current.dismiss("one"));
  // No active card and no decision; the request is still pending.
  expect(result.current.visible).toBeUndefined();
  expect(result.current.pending.map((item) => item.id)).toEqual(["one"]);
  act(() => result.current.restore());
  expect(result.current.visible?.id).toBe("one");
});

it("closing every one of several requests shows no active card", async () => {
  stubServer({ "thread-a": async () => [permission("one"), permission("two")] });
  const { result } = renderHook(() => usePermissionRequests("thread-a"));
  await waitFor(() => expect(result.current.pending).toHaveLength(2));
  expect(result.current.visible?.id).toBe("one");
  act(() => result.current.dismiss("one"));
  // The second one is not promoted automatically; it stays pending but hidden.
  expect(result.current.visible).toBeUndefined();
  expect(result.current.pending.map((item) => item.id)).toEqual(["one", "two"]);
  act(() => result.current.restore());
  expect(result.current.visible?.id).toBe("one");
});

it("a late response for the previous thread never fills the new queue", async () => {
  let releaseA!: (value: ToolPermissionRequest[]) => void;
  const pendingA = new Promise<ToolPermissionRequest[]>(
    (resolve) => (releaseA = resolve),
  );
  stubServer({
    "thread-a": () => pendingA,
    "thread-b": async () => [permission("b", { thread_id: "thread-b" })],
  });
  const { result, rerender } = renderHook(
    ({ threadID }: { threadID: string }) => usePermissionRequests(threadID),
    { initialProps: { threadID: "thread-a" } },
  );
  // Switch to B while A's read is still in flight.
  rerender({ threadID: "thread-b" });
  // The A queue is cleared synchronously before B loads.
  expect(result.current.items).toEqual([]);
  await waitFor(() => expect(result.current.visible?.id).toBe("b"));
  // Now A resolves late; it must be ignored.
  await act(async () => {
    releaseA([permission("a")]);
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(result.current.pending.map((item) => item.id)).toEqual(["b"]);
  expect(result.current.visible?.id).toBe("b");
});

it("an empty thread switch clears the old queue instead of keeping it", async () => {
  stubServer({ "thread-a": async () => [permission("a")] });
  const { result, rerender } = renderHook(
    ({ threadID }: { threadID: string }) => usePermissionRequests(threadID),
    { initialProps: { threadID: "thread-a" } },
  );
  await waitFor(() => expect(result.current.visible?.id).toBe("a"));
  rerender({ threadID: "" });
  // No thread: nothing from A may remain visible or decidable.
  expect(result.current.items).toEqual([]);
  expect(result.current.pending).toEqual([]);
  expect(result.current.visible).toBeUndefined();
});

it("only pending items reach the queue", async () => {
  stubServer({
    "thread-a": async () => [
      permission("pending"),
      permission("done", { status: "denied" }),
      permission("expired", { status: "expired" }),
    ],
  });
  const { result } = renderHook(() => usePermissionRequests("thread-a"));
  await waitFor(() => expect(result.current.items).toHaveLength(3));
  expect(result.current.pending.map((item) => item.id)).toEqual(["pending"]);
});

it("a decided request cannot be replayed with a different choice", async () => {
  request.mockRejectedValue(
    new APIError("permission request was already decided", 409),
  );
  await expect(
    submitPermissionDecision(permission("decided"), "deny"),
  ).rejects.toThrow(/已过期、已决定或与先前的选择冲突/);
});
