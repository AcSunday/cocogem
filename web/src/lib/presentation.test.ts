import { expect, it } from "vitest";
import type { Agent, Attempt, Event, Run, Source } from "../api/client";
import {
  attemptUsageLabel,
  conversationCoordinator,
  defaultAssignments,
  errorText,
  executionPolicyRows,
  failureKindLabel,
  formatMessageTime,
  latestSources,
  limitLabel,
  livenessFromAttempts,
  mergeAttemptLiveness,
  nextLiveness,
  noticeLabel,
  runErrorText,
  runDuration,
  runUsageSummary,
  seedLiveness,
  sourceKindLabel,
  threadTitle,
  usageSummary,
} from "./presentation";
import { applyMention, mentionAt, mentionMatches } from "./mention";

const agent = (id: string, extra: Partial<Agent> = {}): Agent => ({
  id,
  workspace_id: "ws",
  display_name: id,
  handle: id,
  aliases: [],
  role_description: "",
  model: "default",
  executor: "claude",
  tools: [],
  disabled: false,
  revision: 1,
  ...extra,
});

it("explains native protocol mismatches without proposing new credentials", () => {
  const error = new Error(
    "native CLI did not confirm success (result=result_error, category=protocol_mismatch); the configured upstream does not accept this native CLI API protocol",
  );
  const message =
    "当前 CLI 的接口协议与现有上游不匹配，请检查本机 CLI 的服务地址与供应商映射。";
  expect(errorText(error)).toBe(message);
  expect(runErrorText(error.message)).toBe(message);
});

it("defaults roles to enabled conversation members and its coordinator", () => {
  const agents = ["disabled", "one", "two"].map((id) =>
    agent(id, {
      disabled: id === "disabled",
      tools: ["publish_artifact"],
    }),
  );
  expect(defaultAssignments(agents, "two")).toEqual({
    researcher: "one",
    critic: "two",
    coordinator: "two",
    analyst: "one",
    reviewer: "two",
  });
  expect(defaultAssignments([], "missing")).toEqual({
    researcher: "",
    critic: "",
    coordinator: "",
    analyst: "",
    reviewer: "",
  });
});

it("uses global role defaults and falls back to available conversation members", () => {
  const agents = ["one", "two"].map((id) =>
    agent(id, { tools: ["publish_artifact"] }),
  );
  expect(
    defaultAssignments(
      agents,
      "",
      { research: { researcher: "two", critic: "one", coordinator: "two" } },
      "one",
      "research",
    ),
  ).toEqual({
    researcher: "two",
    critic: "one",
    analyst: "one",
    reviewer: "two",
    coordinator: "two",
  });
  // Defaults outside this conversation fall back unless they are unavailable.
  expect(
    defaultAssignments(
      agents,
      "",
      { research: { researcher: "missing", critic: "two", coordinator: "missing" } },
      "missing",
      "research",
    ),
  ).toEqual({
    researcher: "one",
    critic: "two",
    analyst: "one",
    reviewer: "two",
    coordinator: "one",
  });
});

it("never reuses an exclusively configured role's member", () => {
  const agents = ["one", "two"].map((id) =>
    agent(id, { tools: ["publish_artifact"] }),
  );
  expect(
    defaultAssignments(
      agents,
      "",
      { research: { critic: "one" } },
      "",
      "research",
    ),
  ).toMatchObject({ researcher: "two", critic: "one" });
  expect(
    defaultAssignments(
      agents,
      "",
      { code_analysis: { reviewer: "one" } },
      "",
      "code_analysis",
    ),
  ).toMatchObject({ analyst: "two", reviewer: "one" });
});

it("does not default a publishing role to a member without the grant", () => {
  const agents = [
    agent("one"),
    agent("two", { tools: ["publish_artifact"] }),
  ];
  expect(
    defaultAssignments(
      agents,
      "",
      {
        research: { coordinator: "one" },
        code_analysis: { reviewer: "one" },
      },
      "",
      "code_analysis",
    ),
  ).toEqual({
    researcher: "one",
    critic: "two",
    analyst: "one",
    reviewer: "two",
    coordinator: "two",
  });
});

it("keeps a feasible reviewer for the only publisher", () => {
  const agents = [
    agent("publisher", { tools: ["publish_artifact"] }),
    agent("reader"),
  ];
  // Both members fit: the reader analyzes and the only publisher reviews.
  expect(
    defaultAssignments(agents, "", {}, "", "code_analysis"),
  ).toMatchObject({ analyst: "reader", reviewer: "publisher" });
  // A global analyst preference yields the only publisher to the reviewer.
  expect(
    defaultAssignments(
      agents,
      "",
      { code_analysis: { analyst: "publisher" } },
      "",
      "code_analysis",
    ),
  ).toMatchObject({ analyst: "reader", reviewer: "publisher" });
});

it("previews the coordinator a new conversation will actually use", () => {
  const agents = ["one", "two", "three", "four"].map((id) => agent(id));
  expect(conversationCoordinator(agents, "four")).toBe("four");
  expect(conversationCoordinator(agents, "missing")).toBe("three");
  expect(conversationCoordinator(agents.slice(0, 2), "")).toBe("two");
  expect(
    conversationCoordinator(
      [agent("one", { disabled: true }), agent("two")],
      "one",
    ),
  ).toBe("two");
  expect(conversationCoordinator([], "missing")).toBe("");
});

it("completes a mention typed anywhere in the draft", () => {
  const text = "请 @ma 复核";
  const at = mentionAt(text, 5);
  expect(at).toEqual({ start: 2, query: "ma" });
  expect(mentionAt("mail@ma", 7)).toBeNull();
  const agents = [
    agent("maine", { display_name: "缅因猫" }),
    agent("ragdoll", { display_name: "布偶猫", aliases: ["布布"] }),
  ];
  expect(mentionMatches(agents, "ma").map((a) => a.id)).toEqual(["maine"]);
  expect(mentionMatches(agents, "布").map((a) => a.id)).toEqual(["ragdoll"]);
  expect(applyMention(text, at!.start, 5, "maine")).toEqual({
    text: "请 @maine  复核",
    caret: 9,
  });
});

it("shortens titles, dates older messages and keeps one capture per source", () => {
  expect(threadTitle("研究多 Agent 协作的适用场景，说明证据和限制。补充")).toBe(
    "研究多 Agent 协作的适用场景，说明证据和限制",
  );
  expect([...threadTitle("一".repeat(40))]).toHaveLength(29);
  const now = new Date(2026, 9, 20, 18, 0);
  expect(
    formatMessageTime(new Date(2026, 9, 20, 9, 5).toISOString(), now),
  ).toBe("09:05");
  expect(
    formatMessageTime(new Date(2026, 9, 18, 9, 5).toISOString(), now),
  ).toBe("10月18日 09:05");
  expect(
    runDuration({
      created_at: "2026-10-20T01:00:00Z",
      finished_at: "2026-10-20T01:01:05Z",
    }),
  ).toBe("1 分 5 秒");
  const source = (id: string, url: string) =>
    ({ id, kind: "web", url, path: "" }) as Source;
  expect(
    latestSources([
      source("a1", "https://a"),
      source("b1", "https://b"),
      source("a2", "https://a"),
    ]).map((s) => s.id),
  ).toEqual(["a2", "b1"]);
  expect(runErrorText("context canceled", "canceled")).toBe(
    "已按你的要求停止。",
  );
  expect(runErrorText("upstream step did not succeed")).toBe(
    "上游步骤没有完成，这一步已跳过。",
  );
  expect(runErrorText("CLI 退出码 2")).toBe("CLI 退出码 2");
});

it("explains recovery and capability checkpoints without hiding the reason", () => {
  expect(
    runErrorText(
      "capability_blocked: claude: CLI 缺少受限执行参数 --restricted",
    ),
  ).toContain("claude: CLI 缺少受限执行参数 --restricted");
  expect(
    runErrorText(
      "recovery_uncertain_process: owned process identity no longer matches",
    ),
  ).toContain("无法核实归属");
  expect(runErrorText("step_timeout: 1h0m0s elapsed")).toContain(
    "超过配置的执行时限",
  );
  expect(runErrorText("first_response_timeout: no response")).toContain(
    "等待首个有效响应",
  );
  expect(runErrorText("stall_timeout: no progress")).toContain("没有产生任何执行进展");
  expect(
    runErrorText("checkpoint_blocked: source directory authorization changed"),
  ).toContain("恢复检查未通过");
  expect(
    runErrorText("connection_blocked: claude: 连接配置不可用"),
  ).toContain("连接配置未通过检查");
  expect(
    runErrorText(
      "connection_changed: resolved model/upstream changed from a to b",
    ),
  ).toContain("解析模型或上游连接已变化");
  expect(limitLabel("run_seconds", 21600)).toContain("360 分钟");
  expect(limitLabel("stall_seconds", 900)).toContain("15 分钟");
});

it("describes disabled limits and hides the removed tool-call counter", () => {
  expect(limitLabel("run_seconds", 0)).toBe("不设协作总期限");
  expect(limitLabel("step_seconds", 0)).toBe("不设单步执行期限");
  expect(limitLabel("first_response_seconds", 0)).toBe(
    "关闭首个响应超时检测",
  );
  expect(limitLabel("stall_seconds", 0)).toBe("关闭长时间无进展检测");
  expect(limitLabel("tool_calls", 30)).toBe("");
  expect(limitLabel("handoffs", 12)).toContain("12 个任务");
  expect(limitLabel("queued_runs", 20)).toContain("20 次协作");
  expect(limitLabel("handoff_depth", 4)).toContain("4 层");
});

it("explains host input, context overflow and interrupted kinds without retry promises", () => {
  for (const kind of ["host_input_limit", "context_overflow"]) {
    const text = runErrorText(`${kind}: diagnostic`);
    expect(text).not.toContain(kind);
    expect(text).toContain("不会自动重试");
  }
  expect(runErrorText("host_input_limit: 100000 bytes")).toContain(
    "初始输入超过上限",
  );
  expect(runErrorText("context_overflow: maximum context")).toContain(
    "上下文窗口已超出容量",
  );
  expect(runErrorText("failure_kind=interrupted")).toContain("执行被中断");
  expect(failureKindLabel("host_input_limit")).toContain("不会自动重试");
  expect(failureKindLabel("context_overflow")).toContain("不会自动重试");
  expect(failureKindLabel("interrupted")).toContain("不会自动重试");
});

it("explains an unconfirmed old CLI process without promising automatic resume", () => {
  const text = failureKindLabel("process_uncertain");
  expect(text).not.toBe("process_uncertain");
  expect(text).toContain("旧 CLI 进程");
  expect(text).toContain("核实");
  expect(text).toContain("停止");
  expect(text).toContain("不会自动续跑");
});

it("shows only capability-changing native notices and ignores prototype keys", () => {
  for (const kind of ["constructor", "__proto__", "toString", "hasOwnProperty"])
    expect(noticeLabel(kind)).toBe("");
  expect(noticeLabel()).toBe("");
  expect(noticeLabel("native_file_degraded")).toBe(
    "原生文件工具已停用，本步改用平台文件工具",
  );
});

it("keeps a liveness warning outside progress and clears it on confirmed activity", () => {
  const warning: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 1,
    type: "liveness_warning",
    occurred_at: "",
    data: { state: "waiting_tool", silence_seconds: 30 },
  };
  const notice: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 2,
    type: "native_notice",
    occurred_at: "",
    data: {},
  };
  const progress: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 3,
    type: "tool_started",
    occurred_at: "",
    data: {},
  };
  expect(nextLiveness([], warning)).toEqual([warning]);
  expect(nextLiveness([warning], notice)).toEqual([warning]);
  expect(nextLiveness([warning], progress)).toEqual([]);
});

it("only clears liveness on real text or activity, never a metadata snapshot", () => {
  const warning: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 1,
    type: "liveness_warning",
    occurred_at: "",
    data: { state: "stalled" },
  };
  const event = (seq: number, type: string, data: unknown = {}): Event => ({
    schema_version: 1,
    run_id: "run",
    seq,
    type,
    occurred_at: "",
    data,
  });
  const kept = [warning];
  for (const type of [
    "usage_updated",
    "native_adapter",
    "native_session",
    "native_notice",
    "message_started",
    "message_finished",
    "step_started",
    "run_started",
  ])
    expect(nextLiveness(kept, event(2, type))).toBe(kept);
  // Content alone or an explicit false never counts as new activity.
  for (const snapshot of [
    {
      message_id: "message",
      mode: "snapshot",
      message: { id: "message", content: "", content_bytes: 0 },
    },
    // Old content bytes alone are not proof of new activity.
    {
      message_id: "message",
      mode: "snapshot",
      message: { id: "message", content: "旧正文", content_bytes: 9 },
    },
    {
      message_id: "message",
      mode: "snapshot",
      activity: false,
      message: { id: "message", content: "旧正文", content_bytes: 9 },
    },
    { message_id: "message", text: "新正文", activity: false },
  ])
    expect(nextLiveness(kept, event(2, "message_delta", snapshot))).toBe(kept);
  expect(
    nextLiveness(
      kept,
      event(2, "message_delta", {
        message_id: "message",
        mode: "snapshot",
        activity: true,
      }),
    ),
  ).toEqual([]);
  expect(
    nextLiveness(
      kept,
      event(2, "message_delta", { message_id: "message", text: "新正文" }),
    ),
  ).toEqual([]);
  // Legacy whitespace-only text is not progress.
  expect(
    nextLiveness(
      kept,
      event(2, "message_delta", { message_id: "message", text: "  \n" }),
    ),
  ).toBe(kept);
  for (const kind of ["reasoning", "text_delta"])
    expect(nextLiveness(kept, event(2, "native_activity", { kind }))).toEqual(
      [],
    );
  expect(
    nextLiveness(kept, event(2, "native_activity", { kind: "bogus" })),
  ).toBe(kept);
  expect(nextLiveness(kept, event(2, "run_finished", {}))).toEqual([]);
});

it("clears only the warning of the step that produced activity", () => {
  const warning = (seq: number, step: string): Event => ({
    schema_version: 1,
    run_id: "run",
    seq,
    type: "liveness_warning",
    step_id: step,
    occurred_at: "",
    data: { state: "waiting_tool", silence_seconds: 30 },
  });
  const a = warning(1, "a");
  const b = warning(2, "b");
  const c = warning(3, "c");
  const activity = (seq: number, step?: string): Event => ({
    schema_version: 1,
    run_id: "run",
    seq,
    type: "tool_finished",
    ...(step ? { step_id: step } : {}),
    occurred_at: "",
    data: {},
  });
  expect(nextLiveness([a, b, c], activity(4, "b"))).toEqual([a, c]);
  // An activity without a step cannot prove which parallel attempt moved.
  expect(nextLiveness([a, b, c], activity(5))).toEqual([a, b, c]);
  expect(nextLiveness([a, b, c], activity(5, "a"))).toEqual([b, c]);
});

it("keeps an older warning when the seed window has none", () => {
  const warning: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 1,
    type: "liveness_warning",
    occurred_at: "",
    data: { state: "waiting_tool" },
  };
  const activity: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 2,
    type: "tool_finished",
    occurred_at: "",
    data: {},
  };
  expect(seedLiveness([warning], [])).toEqual([warning]);
  expect(seedLiveness([warning], [activity])).toEqual([]);
  expect(seedLiveness([warning], [warning])).toEqual([warning]);
  expect(seedLiveness([warning], [warning, activity])).toEqual([]);
});

it("restores persistent warnings from running attempt snapshots", () => {
  const snapshot = (
    step: string,
    warning: Attempt["liveness_warning"] = null,
    status = "running",
  ) =>
    ({
      id: `attempt-${step}`,
      run_id: "run",
      step_id: step,
      number: 1,
      status,
      started_at: "",
      liveness_warning: warning,
    }) as Attempt;
  const stalled = {
    state: "stalled",
    silence_seconds: 900,
    last_event_type: "usage",
    process_alive: true,
    tool_active: false,
    observed_at: "2026-10-27T00:00:00Z",
  };
  const legacy: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 1,
    type: "liveness_warning",
    occurred_at: "",
    data: { state: "waiting_tool" },
  };
  const restored = livenessFromAttempts(
    [
      snapshot("step-a", stalled),
      snapshot("step-b", null),
      snapshot("old", stalled, "succeeded"),
    ],
    "run",
  );
  expect(restored).toHaveLength(1);
  expect(restored[0].step_id).toBe("step-a");
  expect(restored[0].data).toMatchObject(stalled);

  // The snapshot is authoritative: a nil warning clears the stale event and
  // another running attempt's warning survives.
  const merged = mergeAttemptLiveness(
    [legacy, { ...legacy, seq: 2, step_id: "step-b" }],
    [snapshot("step-a", stalled), snapshot("step-b", null)],
    "run",
  );
  expect(merged.map((event) => event.step_id)).toEqual(["step-a"]);
  expect(merged[0].data).toMatchObject({ state: "stalled" });
  // An empty list is a legacy/no-snapshot response, so the fallback stays.
  expect(mergeAttemptLiveness([legacy], [], "run")).toEqual([legacy]);
});

it("reports known, partial and unknown usage without turning gaps into zero", () => {
  const usage = (seq: number, data: Record<string, unknown>): Event => ({
    schema_version: 1,
    run_id: "run",
    seq,
    type: "usage_updated",
    occurred_at: "",
    data,
  });
  expect(usageSummary([])).toBe("");
  expect(usageSummary([usage(1, { known: true, input_tokens: 10, output_tokens: 4 })])).toBe(
    "10 / 4 tokens",
  );
  expect(
    usageSummary([
      usage(1, { known: true, input_tokens: 10, output_tokens: 4 }),
      usage(2, { known: false }),
    ]),
  ).toBe("部分用量：10 / 4 tokens（其余未知）");
  expect(usageSummary([usage(1, { known: false })])).toBe("用量未知");
  // Missing known or non-numeric totals stay unknown instead of counting as 0.
  expect(usageSummary([usage(1, { input_tokens: 10, output_tokens: 4 })])).toBe(
    "用量未知",
  );
  expect(usageSummary([usage(1, { known: true })])).toBe("用量未知");
  expect(
    usageSummary([
      usage(1, { known: true, input_tokens: "10", output_tokens: 4 }),
    ]),
  ).toBe("用量未知");
  expect(
    usageSummary([
      usage(1, { known: true, input_tokens: 10, output_tokens: 4 }),
      usage(2, { known: true, input_tokens: 1, output_tokens: 2 }),
    ]),
  ).toBe("11 / 6 tokens");
});

it("shows frozen policy values without inventing defaults", () => {
  expect(executionPolicyRows(undefined)).toEqual([]);
  expect(
    executionPolicyRows({
      run_timeout_ns: 0,
      step_timeout_ns: 0,
      first_response_ns: 0,
      stall_ns: 0,
      stall_action: "warn",
    }),
  ).toEqual([
    { label: "协作总期限", value: "不设绝对期限" },
    { label: "单步期限", value: "不设绝对期限" },
    { label: "首个有效响应", value: "关闭首响应检测" },
    { label: "长时间无进展", value: "关闭静默检测（warn/stop 不触发）" },
  ]);
  expect(
    executionPolicyRows({
      run_timeout_ns: 3_600_000_000_000,
      step_timeout_ns: 90_000_000_000,
      first_response_ns: 600_000_000_000,
      stall_ns: 900_000_000_000,
      stall_action: "stop",
    }),
  ).toEqual([
    { label: "协作总期限", value: "60 分钟" },
    { label: "单步期限", value: "1 分 30 秒" },
    { label: "首个有效响应", value: "最多等待 10 分钟" },
    { label: "长时间无进展", value: "15 分钟后停止（stop）" },
  ]);
});

it("sums run usage from persisted attempts, not the bounded event window", () => {
  const attempt = (extra: Partial<Attempt>): Attempt => ({
    id: "attempt",
    run_id: "run",
    step_id: "step",
    number: 1,
    status: "succeeded",
    started_at: "",
    ...extra,
  });
  const run = (routing: Record<string, unknown> = {}): Run =>
    ({
      routing: {
        usage_known: false,
        model_call: false,
        input_tokens: 0,
        output_tokens: 0,
        ...routing,
      },
    }) as Run;
  const zero = attempt({ usage_known: true, input_tokens: 0, output_tokens: 0 });
  expect(runUsageSummary(run(), [])).toBe("");
  expect(runUsageSummary(run(), [zero])).toBe("用量：0 / 0 tokens");
  expect(
    runUsageSummary(run(), [
      attempt({ usage_known: true, input_tokens: 10, output_tokens: 2 }),
      attempt({ usage_known: false, input_tokens: 99, output_tokens: 99 }),
    ]),
  ).toBe("部分用量：10 / 2 tokens（1 处未知）");
  expect(runUsageSummary(run(), [attempt({ usage_known: false })])).toBe(
    "用量未知",
  );
  expect(
    runUsageSummary(
      run({ usage_known: true, model_call: true, input_tokens: 3, output_tokens: 1 }),
      [attempt({ usage_known: true, input_tokens: 10, output_tokens: 2 })],
    ),
  ).toBe("用量：13 / 3 tokens");
  expect(attemptUsageLabel(zero)).toBe("0 / 0 tokens");
  expect(attemptUsageLabel(attempt({ usage_known: false }))).toBe("用量未知");
  expect(
    attemptUsageLabel(
      attempt({ usage_known: true, input_tokens: 3, output_tokens: 4 }),
    ),
  ).toBe("3 / 4 tokens");
});

it("keeps a partial attempt's known numbers without calling them complete", () => {
  const attempt = (extra: Partial<Attempt>): Attempt => ({
    id: "attempt",
    run_id: "run",
    step_id: "step",
    number: 1,
    status: "succeeded",
    started_at: "",
    ...extra,
  });
  const run = (routing: Record<string, unknown> = {}): Run =>
    ({
      routing: {
        usage_known: false,
        model_call: false,
        input_tokens: 0,
        output_tokens: 0,
        ...routing,
      },
    }) as Run;
  const partial = attempt({
    usage_known: false,
    usage_completeness: "partial",
    input_tokens: 7,
    output_tokens: 0,
  });
  expect(attemptUsageLabel(partial)).toBe(
    "已知部分 7 / 0 tokens（总用量未知）",
  );
  expect(
    attemptUsageLabel(
      attempt({ usage_known: false, usage_completeness: "unknown" }),
    ),
  ).toBe("用量未知");
  expect(
    runUsageSummary(run(), [
      partial,
      attempt({ usage_known: true, input_tokens: 10, output_tokens: 2 }),
    ]),
  ).toBe("部分用量：17 / 2 tokens（1 处未知）");
  // A partial zero is reported, not a complete zero and not unknown.
  expect(
    attemptUsageLabel(
      attempt({
        usage_known: false,
        usage_completeness: "partial",
        input_tokens: 0,
        output_tokens: 0,
      }),
    ),
  ).toBe("已知部分 0 / 0 tokens（总用量未知）");
  expect(
    runUsageSummary(run(), [
      attempt({
        usage_known: false,
        usage_completeness: "partial",
        input_tokens: 0,
        output_tokens: 0,
      }),
    ]),
  ).toBe("部分用量：0 / 0 tokens（1 处未知）");
  expect(
    attemptUsageLabel(
      attempt({
        usage_known: false,
        usage_completeness: "partial",
        input_tokens: 0,
        output_tokens: 4,
      }),
    ),
  ).toContain("0 / 4");
});

it("labels search result pages as unread leads", () => {
  expect(sourceKindLabel({ kind: "search_results", url: "" })).toBe(
    "搜索结果页 · 未读原文",
  );
  expect(sourceKindLabel({ kind: "search_snippet", url: "" })).toBe(
    "搜索摘要 · 未读原文",
  );
  expect(sourceKindLabel({ kind: "native_file", url: "" })).toBe(
    "文件 · 原生读取",
  );
  expect(sourceKindLabel({ kind: "native_search", url: "" })).toBe(
    "原生搜索线索 · 未读原文",
  );
  expect(sourceKindLabel({ kind: "native_web", url: "" })).toBe(
    "原生网页观察 · 可能为摘要",
  );
  expect(
    sourceKindLabel({ kind: "native_web", url: "https://www.so.com/s?q=x" }),
  ).toBe("原生观察 · 搜索页线索");
  expect(
    sourceKindLabel({
      kind: "web",
      url: "https://www.so.com/s?q=DeBank",
    }),
  ).toBe("搜索结果页 · 未读原文");
  expect(
    sourceKindLabel({
      kind: "web",
      url: "https://example.org/article?q=DeBank",
    }),
  ).toBe("网页 · 已读原文");
});
