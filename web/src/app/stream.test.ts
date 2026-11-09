import { describe, expect, it } from "vitest";
import type { Event } from "../api/client";
import { collectRunWindow } from "./stream";

const event = (
  seq: number,
  type: string,
  data: unknown,
  runID = "run-1",
): Event => ({
  run_id: runID,
  seq,
  type,
  data,
  occurred_at: "2026-01-01T00:00:00Z",
  schema_version: 1,
});

const message = {
  id: "msg-1",
  thread_id: "thread-1",
  run_id: "run-1",
  step_id: "step-1",
  agent_id: "agent-1",
  agent_name: "Researcher",
  role: "assistant",
  status: "processing",
  content: "hello",
  created_at: "2026-01-01T00:00:00Z",
};

describe("collectRunWindow", () => {
  it("projects only the requested run while keeping every event", () => {
    const window = collectRunWindow(
      [
        event(1, "run_started", { id: "run-1", status: "running" }),
        event(2, "message_started", message),
        event(3, "step_started", {
          id: "step-1",
          run_id: "run-1",
          status: "running",
        }),
        event(4, "attempt_started", {
          id: "attempt-1",
          run_id: "run-1",
          status: "running",
        }),
        event(5, "run_finished", { id: "run-1", status: "succeeded" }),
        event(6, "message_started", { ...message, id: "msg-2" }, "run-2"),
      ],
      "run-1",
    );

    expect(window.events).toHaveLength(6);
    expect(window.newest).toBe(6);
    expect(window.messages.map((item) => item.message.id)).toEqual(["msg-1"]);
    expect(window.steps).toHaveLength(1);
    expect(window.attempts).toHaveLength(1);
    expect(window.run?.status).toBe("succeeded");
  });

  it("deduplicates replayed frames and keeps terminal state", () => {
    const window = collectRunWindow(
      [
        event(1, "step_started", {
          id: "step-1",
          run_id: "run-1",
          status: "running",
        }),
        event(1, "step_started", {
          id: "step-1",
          run_id: "run-1",
          status: "running",
        }),
        event(2, "step_finished", {
          id: "step-1",
          run_id: "run-1",
          status: "succeeded",
        }),
        event(3, "step_started", {
          id: "step-1",
          run_id: "run-1",
          status: "running",
        }),
      ],
      "run-1",
    );

    expect(window.events.map((item) => item.seq)).toEqual([1, 2, 3]);
    expect(window.steps).toHaveLength(1);
    expect(window.steps[0].status).toBe("succeeded");
    expect(window.newest).toBe(3);
  });
});
