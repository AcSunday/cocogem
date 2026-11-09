import { afterEach, expect, it } from "vitest";
import type { Event } from "../api/client";
import {
  activityDecisionKey,
  missingFinishLabel,
  projectToolActivity,
  readActivityDecision,
  saveActivityDecision,
  summarizeActivities,
  toolStatusLabel,
  toolTarget,
  unresolvedPermissionActivities,
  type ToolActivity,
} from "./toolActivity";

afterEach(() => localStorage.clear());

const event = (
  seq: number,
  type: string,
  data: Record<string, unknown>,
  extra: Partial<Event> = {},
): Event => ({
  schema_version: 1,
  run_id: "run",
  seq,
  type,
  occurred_at: "2026-11-08T10:00:00Z",
  agent_id: "member",
  step_id: "step",
  data,
  ...extra,
});

const only = (events: Event[], options = {}): ToolActivity[] =>
  projectToolActivity(events, options).flatMap((group) => group.activities);

it("pairs a start with the first finish of its own attempt and call id", () => {
  const items = only([
    event(1, "tool_started", {
      tool_call_id: "call-1",
      name: "read_file",
      input: { path: "internal/a.go" },
    }),
    event(2, "tool_finished", {
      tool_call_id: "call-1",
      name: "read_file",
      status: "ok",
      duration_ms: 12,
      exit_code: 0,
      output: "package a",
    }),
  ]);
  expect(items).toHaveLength(1);
  expect(items[0]).toMatchObject({
    toolCallID: "call-1",
    status: "succeeded",
    target: "internal/a.go",
    durationMs: 12,
    exitCode: 0,
    missingStart: false,
    missingFinish: false,
  });
  expect(items[0].output).toBe("package a");
});

it("does not pair a retried native-1 finish with the previous attempt's start", () => {
  const items = only([
    event(1, "attempt_started", { id: "attempt-a", number: 1 }),
    event(2, "tool_started", {
      tool_call_id: "native-1",
      name: "Read",
      origin: "native",
      input: { file_path: "first.txt" },
    }),
    event(3, "attempt_finished", {
      id: "attempt-a",
      number: 1,
      status: "interrupted",
    }),
    event(4, "attempt_started", { id: "attempt-b", number: 2 }),
    event(5, "tool_finished", {
      tool_call_id: "native-1",
      name: "Read",
      origin: "native",
      status: "ok",
      output: "second attempt output",
    }),
  ]);
  expect(items.map((item) => item.status)).toEqual(["unknown", "succeeded"]);
  expect(items[0].output).toBe("");
  expect(items[0].unknownReason).toBe("attempt_ended");
  expect(items[0].target).toBe("first.txt");
  // The finish has no verifiable start in its attempt: its own receipt stays,
  // but it is marked as a standalone record.
  expect(items[1]).toMatchObject({
    status: "succeeded",
    missingStart: true,
    output: "second attempt output",
  });
});

it("pairs a finish inside a fresh attempt even when the id was reused", () => {
  const items = only([
    event(1, "attempt_started", { id: "attempt-a", number: 1 }),
    event(2, "tool_started", {
      tool_call_id: "native-1",
      name: "Read",
      input: { file_path: "first.txt" },
    }),
    event(3, "attempt_finished", {
      id: "attempt-a",
      number: 1,
      status: "interrupted",
    }),
    event(4, "attempt_started", { id: "attempt-b", number: 2 }),
    event(5, "tool_started", {
      tool_call_id: "native-1",
      name: "Read",
      input: { file_path: "second.txt" },
    }),
    event(6, "tool_finished", {
      tool_call_id: "native-1",
      name: "Read",
      status: "ok",
      output: "second",
    }),
  ]);
  expect(items).toHaveLength(2);
  expect(items[0].status).toBe("unknown");
  expect(items[0].target).toBe("first.txt");
  expect(items[1].status).toBe("succeeded");
  expect(items[1].target).toBe("second.txt");
  expect(items[1].output).toBe("second");
});

it("keeps a standalone finish with its real receipt from a later page", () => {
  const [item] = only([
    event(9, "tool_finished", {
      tool_call_id: "call-late",
      name: "fetch_url",
      status: "ok",
      duration_ms: 7,
      output: "page body",
    }),
  ], { mode: "history" });
  expect(item).toMatchObject({
    status: "succeeded",
    missingStart: true,
    durationMs: 7,
  });
  expect(item.output).toBe("page body");
});

it("never presents a start-only call as success when the run ended", () => {
  const [item] = only(
    [
      event(1, "tool_started", {
        tool_call_id: "native-1",
        name: "Bash",
        origin: "native",
        input: { command: "go test ./..." },
      }),
    ],
    { runStatus: "succeeded" },
  );
  expect(item.status).toBe("unknown");
  expect(item.status).not.toBe("succeeded");
  expect(item.missingFinish).toBe(true);
  expect(item.unknownReason).toBe("run_ended");
  expect(missingFinishLabel(item)).toContain("运行已结束");
  expect(toolStatusLabel(item.status)).toBe("结果未知");
});

it("never leaves a start of an interrupted attempt looking like it still runs", () => {
  const [item] = only([
    event(1, "attempt_started", { id: "attempt-a", number: 1 }),
    event(2, "tool_started", {
      tool_call_id: "call",
      name: "run_test",
      input: {},
    }),
    event(3, "attempt_finished", {
      id: "attempt-a",
      number: 1,
      status: "interrupted",
    }),
  ]);
  expect(item.status).toBe("unknown");
  expect(item.unknownReason).toBe("attempt_ended");
});

it("keeps a retained start running while the live window is full", () => {
  const events = [
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "read_file",
      input: {},
    }),
  ];
  for (let seq = 2; seq <= 500; seq++)
    events.push(event(seq, "native_notice", { kind: "status" }));
  const [item] = only(events, { runStatus: "running" });
  // A FIFO window drops an older start before its later finish, so a retained
  // start during an active run is genuinely running, not a lost receipt.
  expect(item.status).toBe("running");
  expect(item.unknownReason).toBeUndefined();
  expect(item.missingFinish).toBe(true);
});

it("still marks a start unknown when a history page cannot verify its finish", () => {
  const [item] = only(
    [
      event(1, "tool_started", {
        tool_call_id: "call",
        name: "read_file",
        input: {},
      }),
    ],
    { mode: "history" },
  );
  expect(item.status).toBe("unknown");
  expect(item.unknownReason).toBe("window_evicted");
  expect(missingFinishLabel(item)).toBe("完成记录不在当前页");
});

it("closes an awaiting start when the run was canceled", () => {
  const [item] = only(
    [
      event(1, "tool_started", {
        tool_call_id: "call",
        name: "Bash",
        status: "awaiting_permission",
        permission_request_id: "request-1",
        input: { command: "npm publish" },
      }),
    ],
    { runStatus: "canceled" },
  );
  expect(item.status).toBe("canceled");
  expect(item.status).not.toBe("awaiting_permission");
});

it("closes a pending request when its attempt finished", () => {
  const events = [
    event(1, "attempt_started", { id: "attempt-a", number: 1 }),
    event(2, "tool_started", {
      tool_call_id: "call",
      name: "Bash",
      status: "awaiting_permission",
      permission_request_id: "request-1",
      input: { command: "npm publish" },
    }),
    event(3, "attempt_finished", {
      id: "attempt-a",
      number: 1,
      status: "interrupted",
    }),
  ];
  const [item] = only(events, { runStatus: "running" });
  expect(item.status).toBe("unknown");
  expect(item.unknownReason).toBe("attempt_ended");
  expect(unresolvedPermissionActivities([item])).toEqual([]);
});

it("expires a queued start with its terminal run", () => {
  const [item] = only(
    [
      event(1, "tool_started", {
        tool_call_id: "call",
        name: "fetch_url",
        status: "queued",
        input: { url: "https://example.test/" },
      }),
    ],
    { runStatus: "failed" },
  );
  expect(item.status).toBe("unknown");
  expect(item.unknownReason).toBe("run_ended");
});

it("does not keep an awaiting finish pending after the run ended", () => {
  const [group] = projectToolActivity(
    [
      event(1, "tool_started", {
        tool_call_id: "call",
        name: "Bash",
        input: { command: "npm publish" },
      }),
      event(2, "tool_finished", {
        tool_call_id: "call",
        name: "Bash",
        status: "awaiting_permission",
        permission_request_id: "request-1",
        output: "",
      }),
    ],
    { runStatus: "failed" },
  );
  expect(group.activities[0].status).toBe("unknown");
  expect(group.summary).not.toContain("等待授权");
});

it("treats a finish without a status as unknown instead of success", () => {
  const [item] = only([
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "read_file",
      input: {},
    }),
    event(2, "tool_finished", {
      tool_call_id: "call",
      name: "read_file",
      output: "body",
    }),
  ]);
  expect(item.status).toBe("unknown");
  expect(item.output).toBe("body");
  expect(item.finishSeq).toBe(2);
});

it("normalizes legacy ok/error and denial statuses for the projection", () => {
  const statuses = [
    ["ok", "succeeded"],
    ["error", "failed"],
    ["denied", "denied"],
    ["canceled", "canceled"],
    ["awaiting_permission", "awaiting_permission"],
  ] as const;
  for (const [legacy, expected] of statuses) {
    const [item] = only([
      event(1, "tool_started", {
        tool_call_id: "call",
        name: "run_command",
        input: {},
      }),
      event(2, "tool_finished", {
        tool_call_id: "call",
        name: "run_command",
        status: legacy,
        output: "",
      }),
    ]);
    expect(item.status).toBe(expected);
  }
});

it("keeps a denied call denied even without a finish", () => {
  const [item] = only([
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "write_file",
      status: "denied",
      input: { path: "outside.txt" },
    }),
  ]);
  expect(item.status).toBe("denied");
  // No finish event exists, but a denial is a real terminal decision, not an
  // unverifiable outcome: the UI must not hint at a missing completion record.
  expect(item.missingFinish).toBe(true);
});

it("summarizes consecutive calls by what they did", () => {
  const reads = ["a", "b", "c"].flatMap((path, index) => [
    event(index * 2 + 1, "tool_started", {
      tool_call_id: `read-${index}`,
      name: "read_file",
      input: { path },
    }),
    event(index * 2 + 2, "tool_finished", {
      tool_call_id: `read-${index}`,
      name: "read_file",
      status: "ok",
    }),
  ]);
  const [group] = projectToolActivity(reads);
  expect(group.summary).toBe("读取 3 个文件");

  const [test] = projectToolActivity([
    event(1, "tool_started", {
      tool_call_id: "test",
      name: "run_test",
      input: {},
    }),
    event(2, "tool_finished", {
      tool_call_id: "test",
      name: "run_test",
      status: "ok",
    }),
  ]);
  expect(test.summary).toBe("执行测试");

  const waiting = projectToolActivity([
    event(1, "tool_started", {
      tool_call_id: "native-1",
      name: "Bash",
      status: "awaiting_permission",
      input: { command: "npm publish" },
    }),
  ]);
  expect(waiting[0].summary).toBe("等待授权");
});

const makeActivity = (overrides: Partial<ToolActivity>): ToolActivity => ({
  key: "a",
  runID: "run",
  seq: 1,
  stepID: "step",
  attemptID: "",
  memberID: "member",
  toolCallID: "1",
  origin: "host",
  name: "read_file",
  label: "读取文件",
  status: "succeeded",
  target: "",
  inputPreview: "",
  input: {},
  output: "",
  outputTruncated: false,
  permissionRequestID: "",
  permissionScope: "",
  reason: "",
  occurredAt: "",
  missingStart: false,
  missingFinish: false,
  ...overrides,
});

it("adds failure and unknown suffixes without replacing the action", () => {
  const items: ToolActivity[] = [
    makeActivity({ key: "a", seq: 1, status: "failed" }),
    makeActivity({ key: "b", seq: 2, status: "unknown" }),
  ];
  expect(summarizeActivities([items[1]])).toBe("读取 1 个文件 · 1 个结果未知");
  expect(summarizeActivities(items)).toBe("读取 2 个文件 · 1 个失败");
});

it("keeps parallel members in separate groups in first-seen order", () => {
  const groups = projectToolActivity([
    event(1, "tool_started", {
      tool_call_id: "a",
      name: "read_file",
      input: {},
    }, { agent_id: "one", step_id: "step-one" }),
    event(2, "tool_started", {
      tool_call_id: "b",
      name: "web_search",
      input: { query: "docs" },
    }, { agent_id: "two", step_id: "step-two" }),
    event(3, "tool_finished", {
      tool_call_id: "a",
      name: "read_file",
      status: "ok",
    }, { agent_id: "one", step_id: "step-one" }),
    event(4, "tool_finished", {
      tool_call_id: "b",
      name: "web_search",
      status: "error",
      output: "blocked",
    }, { agent_id: "two", step_id: "step-two" }),
  ]);
  expect(groups.map((group) => group.memberID)).toEqual(["one", "two"]);
  expect(groups.map((group) => group.summary)).toEqual([
    "读取 1 个文件",
    "联网搜索 1 次 · 1 个失败",
  ]);
});

it("keeps attempt identity per run when two runs reuse the same seq and call id", () => {
  const events = [
    event(1, "attempt_started", { id: "attempt-a", number: 1 }, { run_id: "run-a" }),
    event(1, "attempt_started", { id: "attempt-b", number: 1 }, { run_id: "run-b" }),
    event(
      2,
      "tool_started",
      {
        tool_call_id: "native-1",
        name: "read_file",
        input: { path: "a.txt" },
      },
      { run_id: "run-a" },
    ),
    event(
      2,
      "tool_started",
      {
        tool_call_id: "native-1",
        name: "read_file",
        input: { path: "b.txt" },
      },
      { run_id: "run-b" },
    ),
    event(
      3,
      "tool_finished",
      {
        tool_call_id: "native-1",
        name: "read_file",
        status: "ok",
        output: "A",
      },
      { run_id: "run-a" },
    ),
    event(
      3,
      "tool_finished",
      {
        tool_call_id: "native-1",
        name: "read_file",
        status: "ok",
        output: "B",
      },
      { run_id: "run-b" },
    ),
    event(
      4,
      "attempt_finished",
      { id: "attempt-a", number: 1, status: "succeeded" },
      { run_id: "run-a" },
    ),
    event(
      4,
      "attempt_finished",
      { id: "attempt-b", number: 1, status: "succeeded" },
      { run_id: "run-b" },
    ),
  ];
  const groups = projectToolActivity(events);
  expect(groups).toHaveLength(2);
  const byRun = new Map(groups.map((group) => [group.runID, group]));
  expect(byRun.get("run-a")?.activities[0]).toMatchObject({
    status: "succeeded",
    target: "a.txt",
    output: "A",
    attemptID: "attempt-a",
  });
  expect(byRun.get("run-b")?.activities[0]).toMatchObject({
    status: "succeeded",
    target: "b.txt",
    output: "B",
    attemptID: "attempt-b",
  });
});

it("folds a host awaiting receipt into the running invocation", () => {
  const events = [
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "run_command",
      input: { command: "go test ./..." },
    }),
    event(2, "tool_finished", {
      tool_call_id: "call",
      name: "run_command",
      status: "awaiting_permission",
      permission_request_id: "request-1",
    }),
  ];
  const [group] = projectToolActivity(events, { runStatus: "running" });
  expect(group.activities).toHaveLength(1);
  expect(group.activities[0]).toMatchObject({
    status: "awaiting_permission",
    target: "go test ./...",
    permissionRequestID: "request-1",
    missingStart: false,
    missingFinish: true,
  });
  expect(group.summary).toBe("等待授权");
});

it("keeps one host activity when the real receipt follows the awaiting update", () => {
  const events = [
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "run_command",
      input: { command: "go test ./..." },
    }),
    event(2, "tool_finished", {
      tool_call_id: "call",
      name: "run_command",
      status: "awaiting_permission",
      permission_request_id: "request-1",
    }),
    event(3, "tool_finished", {
      tool_call_id: "call",
      name: "run_command",
      status: "ok",
      output: "ok  internal/workflow",
      duration_ms: 12,
      exit_code: 0,
    }),
  ];
  for (const runStatus of ["running", "succeeded"]) {
    const [group] = projectToolActivity(events, { runStatus });
    expect(group.activities).toHaveLength(1);
    expect(group.activities[0]).toMatchObject({
      status: "succeeded",
      target: "go test ./...",
      output: "ok  internal/workflow",
      durationMs: 12,
      exitCode: 0,
      permissionRequestID: "request-1",
      missingStart: false,
      missingFinish: false,
    });
    expect(group.summary).toBe("执行 1 条命令");
    expect(group.summary).not.toContain("结果未知");
  }
});

it("folds a native awaiting receipt that arrives before the start", () => {
  const events = [
    event(1, "tool_finished", {
      tool_call_id: "native-1",
      name: "Bash",
      origin: "native",
      status: "awaiting_permission",
      permission_request_id: "request-1",
    }),
    event(2, "tool_started", {
      tool_call_id: "native-1",
      name: "Bash",
      origin: "native",
      input: { command: "npm publish" },
    }),
  ];
  const [group] = projectToolActivity(events, { runStatus: "running" });
  expect(group.activities).toHaveLength(1);
  expect(group.activities[0]).toMatchObject({
    status: "awaiting_permission",
    target: "npm publish",
    permissionRequestID: "request-1",
    missingStart: false,
    missingFinish: true,
  });
  expect(group.summary).toBe("等待授权");
});

it("keeps one native activity when the start and real receipt follow the awaiting update", () => {
  const events = [
    event(1, "tool_finished", {
      tool_call_id: "native-1",
      name: "Bash",
      origin: "native",
      status: "awaiting_permission",
      permission_request_id: "request-1",
    }),
    event(2, "tool_started", {
      tool_call_id: "native-1",
      name: "Bash",
      origin: "native",
      input: { command: "npm publish" },
    }),
    event(3, "tool_finished", {
      tool_call_id: "native-1",
      name: "Bash",
      origin: "native",
      status: "ok",
      output: "published",
      duration_ms: 40,
      exit_code: 0,
    }),
  ];
  for (const runStatus of ["running", "succeeded"]) {
    const [group] = projectToolActivity(events, { runStatus });
    expect(group.activities).toHaveLength(1);
    expect(group.activities[0]).toMatchObject({
      status: "succeeded",
      target: "npm publish",
      output: "published",
      durationMs: 40,
      permissionRequestID: "request-1",
      missingStart: false,
      missingFinish: false,
    });
    expect(group.summary).toBe("执行 1 条命令");
    expect(group.summary).not.toContain("结果未知");
  }
});

it("keeps genuine repeated call ids as separate invocations after a terminal receipt", () => {
  const events = [
    event(1, "tool_started", {
      tool_call_id: "native-1",
      name: "Read",
      input: { file_path: "a.txt" },
    }),
    event(2, "tool_finished", {
      tool_call_id: "native-1",
      name: "Read",
      status: "ok",
      output: "A",
    }),
    event(3, "tool_started", {
      tool_call_id: "native-1",
      name: "Read",
      input: { file_path: "b.txt" },
    }),
    event(4, "tool_finished", {
      tool_call_id: "native-1",
      name: "Read",
      status: "ok",
      output: "B",
    }),
  ];
  const [group] = projectToolActivity(events, { runStatus: "running" });
  expect(group.activities).toHaveLength(2);
  expect(group.activities.map((item) => item.target)).toEqual([
    "a.txt",
    "b.txt",
  ]);
  expect(group.activities.map((item) => item.output)).toEqual(["A", "B"]);
  expect(group.summary).toBe("读取 2 个文件");
});

it("deduplicates redelivered events by run and sequence", () => {  const started = event(1, "tool_started", {
    tool_call_id: "call",
    name: "read_file",
    input: {},
  });
  const finished = event(2, "tool_finished", {
    tool_call_id: "call",
    name: "read_file",
    status: "ok",
    output: "once",
  });
  const [group] = projectToolActivity([started, finished, started, finished]);
  expect(group.activities).toHaveLength(1);
  expect(group.activities[0].output).toBe("once");
});

it("exposes the permission request identity and normalized scope", () => {
  const [group] = projectToolActivity([
    event(1, "tool_started", {
      tool_call_id: "native-1",
      name: "Bash",
      origin: "native",
      status: "awaiting_permission",
      permission_request_id: "request-1",
      normalized_resource_scope: "command: npm publish",
      reason: "发布到公共 registry",
      input: { command: "npm publish" },
    }),
  ]);
  const [item] = group.activities;
  expect(item).toMatchObject({
    permissionRequestID: "request-1",
    permissionScope: "command: npm publish",
    reason: "发布到公共 registry",
    origin: "native",
  });
});

it("treats an awaiting request as resolved once the same call later executed", () => {
  const [group] = projectToolActivity([
    event(1, "tool_started", {
      tool_call_id: "native-1",
      name: "Bash",
      status: "running",
      input: { command: "npm publish" },
    }),
    event(2, "tool_finished", {
      tool_call_id: "native-1",
      name: "Bash",
      status: "awaiting_permission",
      permission_request_id: "request-1",
    }),
    event(3, "tool_finished", {
      tool_call_id: "native-1",
      name: "Bash",
      status: "succeeded",
      output: "published",
    }),
  ]);
  expect(group.activities).toHaveLength(1);
  expect(group.activities[0].status).toBe("succeeded");
  expect(unresolvedPermissionActivities(group.activities)).toEqual([]);
  expect(group.summary).not.toContain("等待授权");
  expect(group.summary).toBe("执行 1 条命令");
});

it("only shows known argument fields as the target", () => {
  expect(toolTarget({ path: "docs/a.md" })).toBe("docs/a.md");
  expect(toolTarget({ command: "go test ./..." })).toBe("go test ./...");
  expect(toolTarget({ token: "secret" })).toBe("");
  expect(toolTarget({ api_key: "secret" })).toBe("");
});

it("remembers the reader's expand decision per run and step", () => {
  const [group] = projectToolActivity([
    event(1, "tool_started", {
      tool_call_id: "call",
      name: "read_file",
      input: {},
    }),
  ]);
  const key = activityDecisionKey(group);
  expect(readActivityDecision(key)).toBeUndefined();
  saveActivityDecision(key, true);
  expect(readActivityDecision(key)).toBe(true);
  saveActivityDecision(key, false);
  expect(readActivityDecision(key)).toBe(false);
});
