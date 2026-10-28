import { afterEach, describe, expect, it, vi } from "vitest";
import {
  applyMessageDelta,
  boundedMessage,
  EVENT_WINDOW,
  eventHistory,
  eventMessage,
  loadThreadPage,
  MAX_MESSAGE_PREVIEW_BYTES,
  mergeEvents,
  mergeMessage,
  messageContent,
  terminal,
  truncateUTF8,
  type Event,
  type Message,
  type ThreadView,
} from "./client";

afterEach(() => vi.unstubAllGlobals());

const event = (seq: number, extra: Partial<Event> = {}): Event => ({
  schema_version: 1,
  run_id: "run",
  seq,
  type: "native_notice",
  occurred_at: "2026-10-16T00:00:00Z",
  data: {},
  ...extra,
});

const message = (extra: Partial<Message> = {}): Message => ({
  id: "message",
  thread_id: "thread",
  run_id: "run",
  step_id: "step",
  agent_id: "agent",
  agent_name: "成员",
  role: "assistant",
  content: "预览",
  status: "processing",
  created_at: "2026-10-16T00:00:00Z",
  ...extra,
});

describe("event delivery", () => {
  it("deduplicates replayed events and preserves sequence order", () => {
    const first = event(2);
    expect(mergeEvents([first], first)).toHaveLength(1);
    expect(
      mergeEvents([first], event(1)).map((item) => item.seq),
    ).toEqual([1, 2]);
  });

  it("does not treat cancellation intent as terminal", () => {
    expect(terminal("canceling")).toBe(false);
    expect(terminal("canceled")).toBe(true);
  });

  it("keeps a bounded recent window without mixing different runs", () => {
    let events: Event[] = [];
    for (let seq = 1; seq <= 600; seq++) events = mergeEvents(events, event(seq));
    expect(events).toHaveLength(EVENT_WINDOW);
    expect(events[0].seq).toBe(101);
    expect(events.at(-1)?.seq).toBe(600);

    const other = event(1, { run_id: "other" });
    const mixed = mergeEvents(events, other);
    expect(mixed).toHaveLength(EVENT_WINDOW);
    expect(mixed.filter((item) => item.run_id === "other")).toHaveLength(1);
    expect(
      mixed
        .filter((item) => item.run_id === "run")
        .map((item) => item.seq),
    ).toEqual(events.slice(1).map((item) => item.seq));
  });

  it("keeps each run in sequence order even when runs arrive interleaved", () => {
    const merged = mergeEvents(
      mergeEvents([event(2, { run_id: "a" })], event(1, { run_id: "b" })),
      event(1, { run_id: "a" }),
    );
    expect(merged.map((item) => `${item.run_id}:${item.seq}`)).toEqual([
      "a:1",
      "a:2",
      "b:1",
    ]);
  });
});

it("loads one conversation page instead of walking all history", async () => {
  const page: ThreadView = {
    thread: { id: "thread" } as ThreadView["thread"],
    messages: { items: [message()], next_cursor: "older" },
    runs: [],
    sources: [],
  };
  const fetcher = vi
    .fn()
    .mockImplementation(async () => Response.json(page));
  vi.stubGlobal("fetch", fetcher);
  const result = await loadThreadPage("thread", "cursor-1");
  expect(fetcher).toHaveBeenCalledTimes(1);
  expect(fetcher.mock.calls[0][0]).toBe(
    "/api/v1/threads/thread?limit=100&cursor=cursor-1",
  );
  expect(result.messages.next_cursor).toBe("older");
  const first = await loadThreadPage("thread");
  expect(fetcher).toHaveBeenCalledTimes(2);
  expect(fetcher.mock.calls[1][0]).toBe("/api/v1/threads/thread?limit=100");
  expect(first.messages.items).toHaveLength(1);
});

it("bounds a legacy inline message before it reaches the timeline", async () => {
  const long = "头".repeat(300 * 1024);
  const fetcher = vi.fn().mockResolvedValue(
    Response.json({
      thread: { id: "thread" },
      messages: {
        items: [message({ content: long, content_truncated: false })],
        next_cursor: "",
      },
      runs: [],
      sources: [],
    }),
  );
  vi.stubGlobal("fetch", fetcher);
  const result = await loadThreadPage("thread");
  const bounded = result.messages.items[0];
  expect(new TextEncoder().encode(bounded.content).length).toBeLessThanOrEqual(
    MAX_MESSAGE_PREVIEW_BYTES,
  );
  expect(bounded.content_truncated).toBe(true);
  expect(bounded.content_bytes).toBe(
    new TextEncoder().encode(long).length,
  );
});

it("cuts previews on UTF-8 boundaries", () => {
  const text = "a".repeat(3) + "中".repeat(3);
  const cut = truncateUTF8(text, 6);
  expect(cut.truncated).toBe(true);
  expect(cut.text).toBe("aaa中");
  expect(new TextEncoder().encode(cut.text).length).toBe(6);
});

it("keeps byte totals accurate after the preview truncates", () => {
  const text = "a".repeat(256 * 1024);
  const initial = [message({ content: "开头", content_bytes: 6 })];
  const appended = applyMessageDelta(initial, event(1, {
    type: "message_delta",
    data: { message_id: "message", mode: "append", text },
  }));
  const bounded = appended[0];
  expect(bounded.content.startsWith("开头")).toBe(true);
  expect(new TextEncoder().encode(bounded.content).length).toBeLessThanOrEqual(
    MAX_MESSAGE_PREVIEW_BYTES,
  );
  expect(bounded.content_truncated).toBe(true);
  const afterFirst = 6 + new TextEncoder().encode(text).length;
  expect(bounded.content_bytes).toBe(afterFirst);

  const more = "不应出现";
  const counted = applyMessageDelta(appended, event(2, {
    type: "message_delta",
    data: { message_id: "message", mode: "append", text: more },
  }));
  expect(counted).not.toBe(appended);
  expect(counted[0].content).toBe(bounded.content);
  expect(counted[0].content_bytes).toBe(
    afterFirst + new TextEncoder().encode(more).length,
  );
  expect(counted[0].content_truncated).toBe(true);

  // A replayed lower server total must not shrink the byte count.
  const replayed = applyMessageDelta(counted, event(3, {
    type: "message_delta",
    data: { message_id: "message", mode: "append", text: "", content_bytes: 1 },
  }));
  expect(replayed[0].content_bytes).toBe(counted[0].content_bytes);
});

it("applies snapshot deltas with the server's byte metadata", () => {
  const initial = [message({ content: "旧预览" })];
  const snapshot = message({
    content: "新预览",
    content_bytes: 9,
    content_truncated: true,
    content_format: "chunks_v1",
    status: "completed",
  });
  const next = applyMessageDelta(initial, event(3, {
    type: "message_delta",
    data: { message_id: "message", mode: "snapshot", message: snapshot },
  }));
  expect(next[0]).toMatchObject({
    content: "新预览",
    content_bytes: 9,
    content_truncated: true,
    content_format: "chunks_v1",
    status: "completed",
  });
});

it("never regresses a finished message to an earlier snapshot", () => {
  const current = message({
    content: "完成的完整回复",
    content_bytes: 21,
    status: "completed",
  });
  const stale = message({
    content: "较早的预览",
    content_bytes: 15,
    status: "processing",
  });
  expect(mergeMessage(current, stale)).toMatchObject({
    content: "完成的完整回复",
    content_bytes: 21,
    status: "completed",
  });
  const next = applyMessageDelta([current], event(4, {
    type: "message_delta",
    data: { message_id: "message", mode: "snapshot", message: stale },
  }));
  expect(next[0].content).toBe("完成的完整回复");
  expect(next[0].status).toBe("completed");
});

it("extracts persisted message projections but not append deltas", () => {
  expect(
    eventMessage(
      event(1, {
        type: "message_started",
        data: message({ content: "", status: "processing" }),
      }),
    )?.id,
  ).toBe("message");
  expect(
    eventMessage(
      event(2, {
        type: "message_finished",
        data: message({ content: "完成", status: "completed" }),
      }),
    )?.status,
  ).toBe("completed");
  expect(
    eventMessage(
      event(3, {
        type: "message_delta",
        data: { message_id: "message", mode: "snapshot", message: message() },
      }),
    )?.id,
  ).toBe("message");
  expect(
    eventMessage(
      event(4, {
        type: "message_delta",
        data: { message_id: "message", mode: "append", text: "片段" },
      }),
    ),
  ).toBeUndefined();
});

it("bounds a 300KiB preview even without a matching timeline entry", () => {
  const huge = message({ content: "字".repeat(300 * 1024) });
  const bounded = boundedMessage(huge);
  expect(new TextEncoder().encode(bounded.content).length).toBeLessThanOrEqual(
    MAX_MESSAGE_PREVIEW_BYTES,
  );
  expect(bounded.content_truncated).toBe(true);
});

it("requests message content pages and run event history", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(Response.json({ content: "第一页" }))
    .mockResolvedValueOnce(Response.json({ items: [], next_cursor: "" }));
  vi.stubGlobal("fetch", fetcher);
  await messageContent("run", "message", "cursor-2");
  expect(fetcher.mock.calls[0][0]).toBe(
    "/api/v1/runs/run/messages/message/content?limit=8&cursor=cursor-2",
  );
  await eventHistory("run", "42", 100);
  expect(fetcher.mock.calls[1][0]).toBe(
    "/api/v1/runs/run/event-history?limit=100&after=42",
  );
});
