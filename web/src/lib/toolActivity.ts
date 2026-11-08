import type { Event } from "../api/client";
import { toolLabel } from "./presentation";

/**
 * Normalized project of the persisted tool events. The conversation body uses
 * it so every member's step owns its own activity block, and so a retried
 * attempt can never lend a `native-1` finish to an older start.
 */

export type ToolActivityStatus =
  | "queued"
  | "running"
  | "awaiting_permission"
  | "succeeded"
  | "failed"
  | "denied"
  | "canceled"
  | "unknown";

export type ToolActivityOrigin = "host" | "native";

/** Why a start has no verifiable finish; never presented as success. */
export type ToolActivityUnknownReason =
  | "window_evicted"
  | "attempt_ended"
  | "run_ended";

export type ToolActivity = {
  /** Stable within a run: start seq keeps reused native ids apart. */
  key: string;
  runID: string;
  seq: number;
  stepID: string;
  attemptID: string;
  memberID: string;
  toolCallID: string;
  origin: ToolActivityOrigin;
  name: string;
  label: string;
  status: ToolActivityStatus;
  /** Short human target: path, URL, query or command. */
  target: string;
  /** Bounded single-line arguments when no known target key exists. */
  inputPreview: string;
  input: unknown;
  output: string;
  outputTruncated: boolean;
  durationMs?: number;
  exitCode?: number;
  /** Sequence of the paired finish, when one was verified. */
  finishSeq?: number;
  permissionRequestID: string;
  permissionScope: string;
  reason: string;
  occurredAt: string;
  /** A finish whose start is on an earlier page. */
  missingStart: boolean;
  /** A start whose finish cannot be verified from these events. */
  missingFinish: boolean;
  unknownReason?: ToolActivityUnknownReason;
};

export type ToolActivityGroup = {
  /** Step identity inside a run; the expand preference is keyed on it. */
  key: string;
  runID: string;
  stepID: string;
  memberID: string;
  activities: ToolActivity[];
  /** Compact collapsed label, e.g. "读取 3 个文件" or "等待授权". */
  summary: string;
};

/** The approval card needs only this; the decisions themselves stay server-owned. */
export type PermissionPrompt = {
  requestID: string;
  activityKey: string;
  toolCallID: string;
  memberID: string;
  memberName: string;
  toolName: string;
  label: string;
  target: string;
  scope: string;
  reason: string;
};

export type ProjectToolActivityOptions = {
  mode?: "live" | "history";
  runStatus?: string;
  /** Explicit terminal truth; beats runStatus when both are given. */
  runTerminal?: boolean;
  /** True when the live window dropped the beginning of this run. */
  liveWindowEvicted?: boolean;
};

function data(event: Event): Record<string, unknown> {
  return event.data && typeof event.data === "object"
    ? (event.data as Record<string, unknown>)
    : {};
}

const stepKey = (event: Event) => event.step_id || event.agent_id || "";

const attemptKey = (runID: string, stepID: string, attemptID: string) =>
  `${runID}\u0000${stepID}\u0000${attemptID}`;

const text = (value: unknown) =>
  typeof value === "string" ? value.trim() : "";

const number = (value: unknown) =>
  typeof value === "number" && Number.isFinite(value) ? value : undefined;

const TERMINAL_RUNS = new Set([
  "succeeded",
  "failed",
  "canceled",
  "interrupted",
]);

const STATUS: Record<string, ToolActivityStatus> = {
  ok: "succeeded",
  succeeded: "succeeded",
  success: "succeeded",
  completed: "succeeded",
  error: "failed",
  failed: "failed",
  failure: "failed",
  denied: "denied",
  rejected: "denied",
  canceled: "canceled",
  cancelled: "canceled",
  stopped: "canceled",
  awaiting_permission: "awaiting_permission",
  permission_required: "awaiting_permission",
  pending_permission: "awaiting_permission",
  queued: "queued",
  pending: "queued",
  running: "running",
  started: "running",
  in_progress: "running",
  unknown: "unknown",
  interrupted: "unknown",
  uncertain: "unknown",
};

export function normalizeToolStatus(value: unknown): ToolActivityStatus | "" {
  return STATUS[String(value ?? "").trim().toLowerCase()] ?? "";
}

const STATUS_LABEL: Record<ToolActivityStatus, string> = {
  queued: "排队中",
  running: "执行中",
  awaiting_permission: "等待授权",
  succeeded: "完成",
  failed: "失败",
  denied: "已拒绝",
  canceled: "已停止",
  unknown: "结果未知",
};

export const toolStatusLabel = (status: ToolActivityStatus) =>
  STATUS_LABEL[status];

export function normalizeOrigin(value: unknown): ToolActivityOrigin {
  return String(value ?? "").trim().toLowerCase() === "native"
    ? "native"
    : "host";
}

const TARGET_KEYS = [
  "path",
  "file_path",
  "filePath",
  "url",
  "query",
  "command",
  "pattern",
  "source_id",
  "sourceId",
  "task_id",
  "skill",
];

const TARGET_LIMIT = 120;

function bounded(value: string, limit: number) {
  const clean = value.replace(/\s+/g, " ").trim();
  return clean.length > limit ? `${clean.slice(0, limit)}…` : clean;
}

/** Only known safe argument fields become the visible target. */
export function toolTarget(input: unknown): string {
  if (!input || typeof input !== "object") return "";
  const record = input as Record<string, unknown>;
  for (const key of TARGET_KEYS) {
    const value = record[key];
    if (typeof value === "string" && value.trim())
      return bounded(value, TARGET_LIMIT);
  }
  return "";
}

const INPUT_LIMIT = 600;

/** Bounded single-line arguments for tools without a known target field. */
export function toolInputPreview(input: unknown): string {
  if (input === undefined || input === null) return "";
  let text: string;
  try {
    text = typeof input === "string" ? input : JSON.stringify(input);
  } catch {
    return "";
  }
  if (!text || text === "{}" || text === "null") return "";
  return bounded(text, INPUT_LIMIT);
}

const OUTPUT_LIMIT = 8_000;

function boundedOutput(value: unknown): string {
  if (value === undefined || value === null) return "";
  const output = String(value);
  return output.length > OUTPUT_LIMIT
    ? `${output.slice(0, OUTPUT_LIMIT)}\n…（输出已截断）`
    : output;
}

const CATEGORY: Record<string, string> = {
  read_file: "read",
  read: "read",
  list_files: "list",
  glob: "list",
  ls: "list",
  search_content: "search",
  grep: "search",
  search: "search",
  web_search: "web_search",
  websearch: "web_search",
  fetch_url: "web_fetch",
  webfetch: "web_fetch",
  fetch: "web_fetch",
  run_test: "test",
  test: "test",
  run_command: "command",
  bash: "command",
  shell: "command",
  exec: "command",
  command: "command",
  write_file: "write",
  write: "write",
  patch_file: "patch",
  edit: "patch",
  patch: "patch",
  publish_artifact: "artifact",
  publish_file: "deliver",
  view_image: "image",
  handoff_task: "handoff",
  get_task: "task",
  read_skill: "skill",
  read_source: "source",
  list_sources: "sources",
};

const PHRASE: Record<string, (count: number) => string> = {
  read: (count) => `读取 ${count} 个文件`,
  list: (count) => `列出 ${count} 个目录`,
  search: (count) => `搜索 ${count} 次内容`,
  web_search: (count) => `联网搜索 ${count} 次`,
  web_fetch: (count) => `读取 ${count} 个网页`,
  test: () => "执行测试",
  command: (count) => `执行 ${count} 条命令`,
  write: (count) => `写入 ${count} 个文件`,
  patch: (count) => `修改 ${count} 个文件`,
  artifact: (count) => `发布 ${count} 个产物`,
  deliver: (count) => `交付 ${count} 个文件`,
  image: (count) => `查看 ${count} 张图片`,
  handoff: (count) => `交接 ${count} 次`,
  task: (count) => `查询任务 ${count} 次`,
  skill: (count) => `读取 ${count} 个技能`,
  source: (count) => `重读 ${count} 个来源`,
  sources: (count) => `列出 ${count} 次来源`,
};

function toolKey(name: string) {
  return name.trim().toLowerCase().replace(/[\s-]+/g, "_");
}

function category(name: string) {
  const key = toolKey(name);
  return CATEGORY[key] ?? `tool:${key}`;
}

export function activityPhrase(
  name: string,
  label: string,
  count: number,
): string {
  const phrase = PHRASE[category(name)];
  if (phrase) return phrase(count);
  const shown = label || toolLabel(name) || "工具调用";
  return count > 1 ? `${shown} ×${count}` : shown;
}

/** Awaiting calls whose request was not superseded by a later attempt. */
export function unresolvedPermissionActivities(
  items: ToolActivity[],
): ToolActivity[] {
  return items.filter((item, index) => {
    if (item.status !== "awaiting_permission") return false;
    return !items
      .slice(index + 1)
      .some((later) => later.toolCallID === item.toolCallID);
  });
}

/** Collapsed summary: short, honest about blocked and unfinished work. */
export function summarizeActivities(items: ToolActivity[]): string {
  if (!items.length) return "";
  const pending = unresolvedPermissionActivities(items).length;
  if (pending) return pending > 1 ? `等待授权 · ${pending} 项` : "等待授权";
  const counts = new Map<string, { count: number; sample: ToolActivity }>();
  for (const item of items) {
    const key = category(item.name);
    const entry = counts.get(key);
    if (entry) entry.count++;
    else counts.set(key, { count: 1, sample: item });
  }
  let phrase: string;
  if (counts.size === 1) {
    const [entry] = counts.values();
    phrase = activityPhrase(
      entry.sample.name,
      entry.sample.label,
      entry.count,
    );
  } else {
    phrase = `${items.length} 个工具调用`;
  }
  const failed = items.filter((item) => item.status === "failed").length;
  const denied = items.filter((item) => item.status === "denied").length;
  const running = items.filter(
    (item) => item.status === "running" || item.status === "queued",
  ).length;
  const unknown = items.filter((item) => item.status === "unknown").length;
  const suffix = failed
    ? ` · ${failed} 个失败`
    : denied
      ? ` · ${denied} 个已拒绝`
      : running
        ? " · 执行中"
        : unknown
          ? ` · ${unknown} 个结果未知`
          : "";
  return `${phrase}${suffix}`;
}

function eventAttemptID(event: Event): string {
  const direct = (event as Event & { attempt_id?: unknown }).attempt_id;
  if (typeof direct === "string" && direct) return direct;
  const nested = data(event).attempt_id;
  return typeof nested === "string" && nested ? nested : "";
}

type Pairing = {
  runID: string;
  stepID: string;
  attemptID: string;
  start?: Event;
  /** Intermediate permission/progress update for this same invocation. */
  awaiting?: Event;
  /** Terminal receipt for this invocation. */
  finish?: Event;
  firstSeq: number;
  memberID: string;
};

function buildActivity(
  pairing: Pairing,
  state: {
    runTerminal: boolean;
    runStatus?: string;
    evicted: boolean;
    endedAttempts: Set<string>;
  },
): ToolActivity {
  const { start, awaiting, finish } = pairing;
  const source = (start ?? finish ?? awaiting)!;
  const startData = start ? data(start) : {};
  const awaitingData = awaiting ? data(awaiting) : {};
  const finishData = finish ? data(finish) : {};
  const runID = pairing.runID;
  const stepID = pairing.stepID;
  const attemptID = pairing.attemptID;
  const memberID = pairing.memberID || source.agent_id || "";
  const name =
    text(startData.name) || text(awaitingData.name) || text(finishData.name);
  const label = toolLabel(name) || "工具调用";
  const toolCallID =
    text(startData.tool_call_id) ||
    text(awaitingData.tool_call_id) ||
    text(finishData.tool_call_id);
  const rawInput = start
    ? startData.input
    : finishData.input ?? finishData.arguments ?? awaitingData.input;
  const permissionRequestID =
    text(awaitingData.permission_request_id) ||
    text(finishData.permission_request_id) ||
    text(startData.permission_request_id);
  const permissionScope =
    text(awaitingData.normalized_resource_scope) ||
    text(finishData.normalized_resource_scope) ||
    text(startData.normalized_resource_scope) ||
    text(awaitingData.scope) ||
    text(finishData.scope) ||
    text(startData.scope);
  const reason =
    text(awaitingData.reason) ||
    text(finishData.reason) ||
    text(startData.reason);
  const attemptOver = state.endedAttempts.has(
    attemptKey(runID, stepID, attemptID),
  );
  const closed = attemptOver || state.runTerminal;
  // A pending request cannot outlive its attempt or run. The permission
  // service expires it; the UI must not keep offering a decision for work that
  // can no longer execute.
  const closePending = (): {
    status: ToolActivityStatus;
    unknownReason?: ToolActivityUnknownReason;
  } => {
    if (state.runStatus === "canceled") return { status: "canceled" };
    if (attemptOver)
      return { status: "unknown", unknownReason: "attempt_ended" };
    return { status: "unknown", unknownReason: "run_ended" };
  };
  let status: ToolActivityStatus;
  let unknownReason: ToolActivityUnknownReason | undefined;
  if (finish) {
    // A finish without an explicit status is not proof of success.
    status = normalizeToolStatus(finishData.status) || "unknown";
  } else if (awaiting || start) {
    const pendingStatus =
      normalizeToolStatus(awaitingData.status) ||
      normalizeToolStatus(startData.status);
    if (
      pendingStatus === "denied" ||
      pendingStatus === "canceled" ||
      pendingStatus === "failed"
    ) {
      status = pendingStatus;
    } else if (
      pendingStatus === "awaiting_permission" ||
      pendingStatus === "queued" ||
      pendingStatus === "running"
    ) {
      if (closed) ({ status, unknownReason } = closePending());
      else status = pendingStatus;
    } else if (state.evicted) {
      status = "unknown";
      unknownReason = "window_evicted";
    } else if (attemptOver) {
      status = "unknown";
      unknownReason = "attempt_ended";
    } else if (state.runTerminal) {
      status = "unknown";
      unknownReason = "run_ended";
    } else if (awaiting) {
      // A permission request with no synchronous start is still blocked.
      status = "awaiting_permission";
    } else {
      // A retained start is not evidence that its finish was evicted: within a
      // FIFO window the start would be dropped before its later finish. While
      // the attempt/run is active this is a genuinely running call.
      status = "running";
    }
  } else {
    status = "unknown";
  }
  return {
    key: [runID, stepID, attemptID, toolCallID, pairing.firstSeq].join(
      "\u0000",
    ),
    runID,
    seq: pairing.firstSeq,
    stepID,
    attemptID,
    memberID,
    toolCallID,
    origin: normalizeOrigin(
      (start && startData.origin) ??
        (awaiting && awaitingData.origin) ??
        (finish && finishData.origin),
    ),
    name,
    label,
    status,
    target: toolTarget(rawInput),
    inputPreview: toolTarget(rawInput) ? "" : toolInputPreview(rawInput),
    input: rawInput,
    output: finish ? boundedOutput(finishData.output) : "",
    outputTruncated:
      finishData.output_truncated === true ||
      startData.output_truncated === true,
    durationMs: finish ? number(finishData.duration_ms) : undefined,
    exitCode: finish ? number(finishData.exit_code) : undefined,
    finishSeq: finish?.seq,
    permissionRequestID,
    permissionScope,
    reason,
    occurredAt: source.occurred_at,
    missingStart: !start,
    missingFinish: !finish,
    unknownReason,
  };
}

export function projectToolActivity(
  events: Event[],
  options: ProjectToolActivityOptions = {},
): ToolActivityGroup[] {
  // A reconnect can redeliver an event; identity is run + seq.
  const seen = new Set<string>();
  const ordered = [...events]
    .filter((event) => {
      const key = `${event.run_id}:${event.seq}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })
    .sort((left, right) => left.seq - right.seq);
  const runTerminal =
    options.runTerminal ??
    (options.mode === "history" ||
      (options.runStatus ? TERMINAL_RUNS.has(options.runStatus) : false));
  // Array length alone proves nothing: a FIFO window drops a start before its
  // later finish, so a retained start while the attempt is active is running.
  // History pages and callers that know the window lost events say so.
  const evicted =
    options.liveWindowEvicted === true || options.mode === "history";

  // Attempt boundaries: a finish can only pair with a start from the same
  // attempt. native-1 restarts for every attempt, so crossing this boundary
  // would show one attempt's output under another attempt's call.
  const currentAttempt = new Map<string, string>();
  const attemptAt = new Map<string, string>();
  const endedAttempts = new Set<string>();
  for (const event of ordered) {
    const scope = `${event.run_id}\u0000${stepKey(event)}`;
    const seqKey = `${event.run_id}:${event.seq}`;
    const step = stepKey(event);
    if (event.type === "attempt_started") {
      currentAttempt.set(
        scope,
        text(data(event).id) || `seq:${event.seq}`,
      );
    } else if (event.type === "attempt_finished") {
      const id =
        currentAttempt.get(scope) ||
        text(data(event).id) ||
        `seq:${event.seq}`;
      endedAttempts.add(attemptKey(event.run_id, step, id));
      currentAttempt.delete(scope);
    }
    attemptAt.set(
      seqKey,
      eventAttemptID(event) || currentAttempt.get(scope) || "",
    );
  }

  // Build exactly one invocation per run + step + attempt + call id.
  // `awaiting_permission`/`queued` receipts are intermediate updates on that
  // invocation, not terminal finishes: host tools emit them between the start
  // and the real receipt, native CLIs emit them before the start. A terminal
  // receipt closes the invocation, so a genuinely repeated call id afterwards
  // opens a new one instead of being globally deduplicated by id.
  const invocations: Pairing[] = [];
  const waitingStart = new Map<string, Pairing>();
  const running = new Map<string, Pairing[]>();
  const newPairing = (
    event: Event,
    identity: { runID: string; stepID: string; attemptID: string },
    firstSeq: number,
  ): Pairing => ({
    ...identity,
    firstSeq,
    memberID: event.agent_id || "",
  });
  for (const event of ordered) {
    if (event.type !== "tool_started" && event.type !== "tool_finished")
      continue;
    const info = data(event);
    const stepID = stepKey(event);
    const attemptID =
      attemptAt.get(`${event.run_id}:${event.seq}`) || "";
    const toolCallID = text(info.tool_call_id);
    const pairKey = [event.run_id, stepID, attemptID, toolCallID].join(
      "\u0000",
    );
    const identity = {
      runID: event.run_id,
      stepID,
      attemptID,
    };
    if (event.type === "tool_started") {
      const preflight = waitingStart.get(pairKey);
      if (preflight) {
        waitingStart.delete(pairKey);
        if (!preflight.memberID) preflight.memberID = event.agent_id || "";
        preflight.start = event;
        const queue = running.get(pairKey) ?? [];
        queue.push(preflight);
        running.set(pairKey, queue);
      } else {
        const invocation = newPairing(event, identity, event.seq);
        invocation.start = event;
        invocations.push(invocation);
        const queue = running.get(pairKey) ?? [];
        queue.push(invocation);
        running.set(pairKey, queue);
      }
      continue;
    }
    const finishStatus = normalizeToolStatus(info.status);
    const intermediate =
      finishStatus === "awaiting_permission" ||
      finishStatus === "queued" ||
      finishStatus === "running";
    if (intermediate) {
      const open = running.get(pairKey)?.at(-1);
      if (open && !open.finish) {
        open.awaiting = event;
        continue;
      }
      const preflight = waitingStart.get(pairKey);
      if (preflight) {
        preflight.awaiting = event;
        continue;
      }
      const invocation = newPairing(event, identity, event.seq);
      invocation.awaiting = event;
      waitingStart.set(pairKey, invocation);
      invocations.push(invocation);
      continue;
    }
    const queue = running.get(pairKey);
    const open = queue?.shift();
    if (open) {
      open.finish = event;
      if (!queue?.length) running.delete(pairKey);
      continue;
    }
    const preflight = waitingStart.get(pairKey);
    if (preflight) {
      waitingStart.delete(pairKey);
      preflight.finish = event;
      continue;
    }
    const standalone = newPairing(event, identity, event.seq);
    standalone.finish = event;
    invocations.push(standalone);
  }

  const state = {
    runTerminal,
    runStatus: options.runStatus,
    evicted,
    endedAttempts,
  };
  const items = invocations.map((pairing) => buildActivity(pairing, state));
  items.sort((left, right) => left.seq - right.seq);

  const groups: ToolActivityGroup[] = [];
  const byKey = new Map<string, ToolActivityGroup>();
  for (const item of items) {
    const key = [item.runID, item.stepID || item.memberID || "run"].join(
      "\u0000",
    );
    let group = byKey.get(key);
    if (!group) {
      group = {
        key,
        runID: item.runID,
        stepID: item.stepID,
        memberID: item.memberID,
        activities: [],
        summary: "",
      };
      byKey.set(key, group);
      groups.push(group);
    }
    group.activities.push(item);
    if (!group.memberID && item.memberID) group.memberID = item.memberID;
  }
  for (const group of groups)
    group.summary = summarizeActivities(group.activities);
  return groups;
}

/** Why a start-only activity is not running any more. */
export function missingFinishLabel(activity: ToolActivity): string {
  switch (activity.unknownReason) {
    case "window_evicted":
      return "完成记录不在当前页";
    case "attempt_ended":
      return "未收到完成记录 · 尝试已结束";
    case "run_ended":
      return "未收到完成记录 · 运行已结束";
    default:
      return "未收到完成记录";
  }
}

const DECISION_KEY = "cocogem.tool-activity";
const DECISION_LIMIT = 400;

export function activityDecisionKey(group: {
  runID: string;
  key: string;
}): string {
  return `${group.runID}/${group.key}`;
}

/** The reader's explicit expand/collapse wins over the collapsed default. */
export function readActivityDecision(key: string): boolean | undefined {
  try {
    const stored = JSON.parse(localStorage.getItem(DECISION_KEY) ?? "{}");
    const decision = stored?.[key];
    return typeof decision === "boolean" ? decision : undefined;
  } catch {
    return undefined;
  }
}

export function saveActivityDecision(key: string, open: boolean): void {
  try {
    const stored = JSON.parse(localStorage.getItem(DECISION_KEY) ?? "{}");
    const decisions =
      stored && typeof stored === "object" && !Array.isArray(stored)
        ? (stored as Record<string, boolean>)
        : {};
    decisions[key] = open;
    const keys = Object.keys(decisions);
    for (const stale of keys.slice(
      0,
      Math.max(0, keys.length - DECISION_LIMIT),
    ))
      delete decisions[stale];
    localStorage.setItem(DECISION_KEY, JSON.stringify(decisions));
  } catch {
    // A disabled storage only costs the preference, never the conversation.
  }
}
