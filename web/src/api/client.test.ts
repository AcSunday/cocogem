import { afterEach, describe, expect, it, vi } from "vitest";
import { loadAllThreadPages, mergeEvents, terminal, type Event } from "./client";
afterEach(() => vi.unstubAllGlobals());
describe("event delivery", () => {
  it("deduplicates replayed events and preserves sequence order", () => {
    const event: Event = {
      schema_version: 1,
      run_id: "run",
      seq: 2,
      type: "message_delta",
      occurred_at: "2026-10-16T00:00:00Z",
      data: { text: "hello" },
    };
    expect(mergeEvents([event], event)).toHaveLength(1);
    expect(
      mergeEvents([event], { ...event, seq: 1 }).map((event) => event.seq),
    ).toEqual([1, 2]);
  });
  it("does not treat cancellation intent as terminal", () => {
    expect(terminal("canceling")).toBe(false);
    expect(terminal("canceled")).toBe(true);
  });
});

it("reads later message pages so long conversations include their newest output", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          thread: { id: "thread" },
          messages: {
            items: Array.from({ length: 100 }, (_, i) => ({ id: String(i) })),
            next_cursor: "next",
          },
          runs: [],
          sources: [],
        }),
      ),
    )
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          thread: { id: "thread" },
          messages: { items: [{ id: "latest" }], next_cursor: "" },
          runs: [],
          sources: [],
        }),
      ),
    );
  vi.stubGlobal("fetch", fetcher);
  const result = await loadAllThreadPages("thread");
  expect(result.messages.items).toHaveLength(101);
  expect(result.messages.items.at(-1)?.id).toBe("latest");
  expect(fetcher.mock.calls[1][0]).toContain("cursor=next");
});
