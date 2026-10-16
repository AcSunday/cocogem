import { describe, expect, it } from "vitest";
import { mergeEvents, terminal, type Event } from "./api";
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
