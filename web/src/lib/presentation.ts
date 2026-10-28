import type { Agent, Attempt, Event, Message, Run, Source } from "../api/client";

export const workflows = {
  free: "自由协作",
  discussion: "协作讨论",
  research: "Research",
  code_analysis: "代码分析",
};
export const errorText = (error: unknown) =>
  error instanceof Error ? runErrorText(error.message) : "操作失败";
/** Member avatars use the first character of the display name. */
export const initial = (name = "") => [...name.trim()][0] ?? "?";
export const memberTone = (id: string) =>
  [...id].reduce((tone, char) => (tone + char.charCodeAt(0)) % 4, 0);
export const memberLabel = (agent: Agent) =>
  `${agent.display_name} (@${agent.handle})`;

/**
 * The model recorded by the message's own attempt. Legacy rows may only carry
 * the run's requested model; an unresolved default stays clearly labelled.
 */
export function messageModel(message: Message): string {
  if (message.role === "user") return "";
  const resolved = message.resolved_model?.trim() ?? "";
  const requested = message.model?.trim() ?? "";
  if (resolved && resolved !== "cli-default") return resolved;
  if (requested && requested !== "default") return requested;
  return resolved || requested ? "CLI 默认模型" : "";
}

const labels =
  (table: Record<string, string>) =>
  (key = "") =>
    Object.hasOwn(table, key) ? table[key] : key;
export const toolLabel = labels({
  list_files: "列出文件",
  read_file: "读取文件",
  search_content: "搜索内容",
  web_search: "在线搜索",
  fetch_url: "读取网页",
  read_source: "重读来源",
  list_sources: "列出本步来源",
  publish_artifact: "发布产物",
  handoff_task: "交接任务",
  get_task: "查询协作任务",
  read_skill: "读取协作技能",
  Read: "原生读取文件",
  Glob: "原生列出文件",
  Grep: "原生搜索内容",
  WebSearch: "原生联网搜索",
  WebFetch: "原生读取网页",
});
export const artifactTypeLabel = labels({
  note: "讨论纪要",
  research_report: "研究报告",
  code_analysis: "代码分析报告",
});
export const roleLabel = labels({
  researcher: "研究",
  critic: "质疑",
  coordinator: "汇总",
  analyst: "分析",
  reviewer: "复核",
  discussant: "讨论",
  synthesizer: "综合草案",
  checker: "原作者复查",
  responder: "回复",
  member: "成员",
});
/** Persisted run methods, including the two free-run outcomes. */
export const methodLabel = labels({
  direct: "直接回答",
  delegation: "自由分工",
  discussion: "协作讨论",
  research: "Research",
  code_analysis: "工程分析",
});

/**
 * The method actually recorded on a run: the coordinator's selection first,
 * then an explicit built-in workflow, then the settled free-run outcome. No
 * message or current member setting can fill a missing record.
 */
export function runMethod(
  run: Pick<Run, "input" | "method" | "selected_workflow">,
) {
  const selected = run.selected_workflow;
  const input = run.input?.workflow ?? "";
  const method =
    selected?.workflow ||
    (input && input !== "free" ? input : "") ||
    run.method ||
    "";
  return {
    method,
    label: method ? methodLabel(method) : workflows.free,
    reason: selected?.reason?.trim() ?? "",
  };
}
export const skillLabel = labels({
  "task-orchestration": "任务编排",
  "task-handoff": "任务交接",
  "independent-review": "独立复核",
  "synthesize-results": "结果汇总",
});
export const findingStatusLabel = labels({
  supported: "有依据",
  confirmed: "已确认",
  disputed: "有争议",
  insufficient_evidence: "证据不足",
});
/** Engine errors are English diagnostics; known ones get plain wording, the rest stay as-is. */
export function runErrorText(error = "", status = "") {
  if (!error) return "";
  if (status === "canceled" || /context canceled/.test(error))
    return "已按你的要求停止。";
  if (error.includes("host_input_limit"))
    return "宿主发送给 CLI 的初始输入超过上限，任务没有开始执行；请缩短问题或改用来源引用后新建任务。不会自动重试。";
  if (error.includes("context_overflow"))
    return "CLI 的上下文窗口已超出容量，本次尝试未完成；检查点已保留，需要你确认后继续。不会自动重试。";
  if (/failure_kind[=:]\s*interrupted|^interrupted(?:[:：]|$)/.test(error))
    return "执行被中断，检查点已保留；需要你确认后手动继续。不会自动重试。";
  if (error.includes("category=protocol_mismatch"))
    return "当前 CLI 的接口协议与现有上游不匹配，请检查本机 CLI 的服务地址与供应商映射。";
  if (error.includes("connection_changed:")) {
    return `本机 CLI 的解析模型或上游连接已变化，任务与检查点已保留：${error.replace(/^.*?connection_changed:/, "").trim()}`;
  }
  if (error.includes("connection_blocked:")) {
    return `本机 CLI 连接配置未通过检查，任务与检查点已保留：${error.replace(/^.*?connection_blocked:/, "").trim()}`;
  }
  if (
    error.includes("capability_blocked:") ||
    error.includes("required_tools_unavailable:")
  ) {
    return `CLI 能力检查未通过，任务与检查点已保留：${error.replace(/^.*?(capability_blocked:|required_tools_unavailable:)/, "").trim()}`;
  }
  if (error.includes("checkpoint_blocked:")) {
    return `恢复检查未通过，任务与检查点已保留：${error.replace(/^.*?checkpoint_blocked:/, "").trim()}`;
  }
  if (error.startsWith("recovery_uncertain_process:"))
    return "上次执行的进程无法核实归属，已保留检查点且没有自动重跑。确认进程已停止后可继续执行。";
  if (error.startsWith("step_timeout:"))
    return "这一步超过配置的执行时限，未产生已提交结果。";
  if (error.startsWith("first_response_timeout:"))
    return "CLI 在等待首个有效响应时超过时限。";
  if (error.startsWith("stall_timeout:"))
    return "CLI 长时间没有产生任何执行进展，已按停滞策略停止。";
  return (
    {
      "service restarted before execution settled":
        "服务重启时这次协作还没结束，已中断。",
      "upstream step did not succeed": "上游步骤没有完成，这一步已跳过。",
      "run stopped before this step": "协作已停止，这一步没有开始。",
      "step ended without a committed completion":
        "这一步没有正常结束，结果未保存。",
      "service suspended before step completion; will resume":
        "服务停止时这一步还没结束；重新启动后会从检查点继续。",
      "service restarted; checkpoint preserved":
        "服务重启，保留了这一步的检查点。",
    }[error] ?? error
  );
}

/** Machine failure kinds need a clear reason without promising an automatic retry. */
export const failureKindLabel = labels({
  host_input_limit:
    "宿主初始输入超过上限，任务没有开始执行；请缩短问题或改用来源引用后新建任务。不会自动重试。",
  context_overflow:
    "CLI 上下文窗口已超出容量，本次尝试未完成；检查点已保留，需要你确认后继续。不会自动重试。",
  process_uncertain:
    "旧 CLI 进程状态尚未确认；必须先核实该进程已停止，才能继续。不会自动续跑。",
  interrupted: "执行被中断，检查点已保留；需要你确认后手动继续。不会自动重试。",
  first_response_timeout: "CLI 在等待首个有效响应时超过时限。",
  stall_timeout: "CLI 长时间没有产生任何执行进展，已按停滞策略停止。",
  permissions: "CLI 拒绝了这次执行所需的权限，任务没有继续。",
  quota: "CLI 账户报告额度或速率限制，任务没有继续。",
  authentication: "CLI 的现有认证配置未通过检查，任务没有继续。",
});
export const attemptReasonLabel = labels({
  initial: "首次执行",
  retry_after_interruption: "重启后续跑",
  service_restart: "服务重启恢复",
  manual_resume: "手动继续",
  owned_process_reclaimed: "已核实并停止旧进程，创建新尝试",
  new_attempt_from_context: "使用已保存上下文创建新尝试",
  manual: "需要人工核实",
});
export const attemptStatusLabel = labels({
  running: "执行中",
  succeeded: "已完成",
  failed: "失败",
  canceled: "已停止",
  interrupted: "已中断",
  uncertain: "结果待核实",
  blocked: "能力不足",
});
export const sessionModeLabel = labels({
  fresh: "独立新会话",
  resumed: "原生会话续跑",
  context_continuation: "上下文续跑",
});
export const livenessLabel = labels({
  waiting_first_response: "等待首个响应",
  stalled: "长时间无进展",
  waiting_tool: "等待工具",
  waiting_member: "等待其他成员",
});
// Native notices stay in the event log for diagnostics; only capability changes
// reach the user. Literal matching keeps prototype keys out of the render path.
export const noticeLabel = (kind = "") =>
  kind === "native_file_degraded"
    ? "原生文件工具已停用，本步改用平台文件工具"
    : "";

/** Only confirmed host tool work counts as activity; status/usage/heartbeats do not. */
const progressEvents = new Set([
  "handoff_prepared",
  "task_submitted",
  "tool_started",
  "tool_finished",
  "skill_read",
  "source_captured",
  "artifact_published",
]);

const eventData = (event: Event) =>
  event.data && typeof event.data === "object"
    ? (event.data as Record<string, unknown>)
    : {};

/**
 * Method events read as one short line. The raw payload (member IDs, role maps,
 * step IDs) stays in the event record for debugging and never reaches this text.
 */
export function methodEventLabel(event: Event) {
  const data = eventData(event);
  if (event.type === "workflow_selected") {
    const selection =
      data.selection && typeof data.selection === "object"
        ? (data.selection as Record<string, unknown>)
        : {};
    const workflow =
      typeof selection.workflow === "string" ? selection.workflow : "";
    if (!workflow) return "";
    const reason =
      typeof selection.reason === "string" ? selection.reason.trim() : "";
    return `协调者选择方法：${methodLabel(workflow)}${reason ? ` · ${reason}` : ""}`;
  }
  if (event.type === "method_recorded") {
    const method = typeof data.method === "string" ? data.method : "";
    return method ? `本轮方法：${methodLabel(method)}` : "";
  }
  return "";
}

/**
 * Only confirmed activity clears a warning. An explicit false wins even when a
 * snapshot carries old content, and snapshots without the flag prove nothing.
 */
export function activityEvent(event: Event) {
  const data = eventData(event);
  if (data.activity === false) return false;
  if (progressEvents.has(event.type)) return true;
  if (event.type === "native_activity")
    return data.kind === "reasoning" || data.kind === "text_delta";
  if (event.type !== "message_delta") return false;
  if (data.activity === true) return true;
  if (data.mode === "snapshot") return false;
  // Legacy append frames predate the explicit activity flag.
  return typeof data.text === "string" && data.text.trim() !== "";
}

const warningKey = (event: Event) => event.step_id ?? "";

function withWarning(current: Event[], warning: Event) {
  const key = warningKey(warning);
  const at = current.findIndex((item) => warningKey(item) === key);
  if (at < 0) return [...current, warning];
  const next = current.slice();
  next[at] = warning;
  return next;
}

/**
 * Liveness is reducer state, not a scan over the bounded event window. Warnings
 * are kept per step: activity in one parallel attempt must not clear another,
 * while a finished run clears them all.
 */
export function nextLiveness(current: Event[], event: Event): Event[] {
  if (event.type === "liveness_warning") return withWarning(current, event);
  if (event.type === "run_finished") return current.length ? [] : current;
  if (!activityEvent(event)) return current;
  const step = event.step_id ?? "";
  const next = current.filter((warning) => {
    const warned = warningKey(warning);
    // Legacy warnings have no step and are cleared by any confirmed activity.
    return warned !== "" && warned !== step;
  });
  return next.length === current.length ? current : next;
}

/** A recent window without a warning cannot erase the older reducer state. */
export function seedLiveness(current: Event[], items: Event[]) {
  return items.reduce(nextLiveness, current);
}

/**
 * Run views carry the backend's only current warning snapshot per attempt, so
 * a warning older than the bounded event window is still restored.
 */
export function livenessFromAttempts(attempts: Attempt[], runID: string) {
  const warnings = new Map<string, Event>();
  for (const attempt of attempts) {
    const warning = attempt.liveness_warning;
    if (!warning || attempt.status !== "running") continue;
    warnings.set(attempt.step_id, {
      schema_version: 1,
      run_id: runID,
      seq: 0,
      type: "liveness_warning",
      step_id: attempt.step_id,
      occurred_at: warning.observed_at,
      data: { ...warning },
    });
  }
  return [...warnings.values()];
}

/**
 * Attempt snapshots are authoritative when present: how a step's running
 * attempt currently reports itself replaces the older event state, including a
 * null warning after real progress. An empty list is a legacy/no-snapshot
 * response and keeps the event fallback.
 */
export function mergeAttemptLiveness(
  current: Event[],
  attempts: Attempt[],
  runID: string,
) {
  if (!attempts.length) return current;
  const steps = new Set(attempts.map((attempt) => attempt.step_id));
  const kept = current.filter(
    (warning) => !!warning.step_id && !steps.has(warning.step_id),
  );
  return [...kept, ...livenessFromAttempts(attempts, runID)];
}

/** Usage totals only add attempts the provider actually reported. */
export function usageSummary(events: Event[]) {
  const usage = events.filter((event) => event.type === "usage_updated");
  if (!usage.length) return "";
  const valid = (value: unknown): value is number =>
    typeof value === "number" && Number.isFinite(value) && value >= 0;
  const value = (event: Event, field: string) => {
    const input = eventData(event)[field];
    return valid(input) ? input : 0;
  };
  const known = usage.filter((event) => {
    const data = eventData(event);
    return (
      data.known === true &&
      valid(data.input_tokens) &&
      valid(data.output_tokens)
    );
  });
  const total = (field: string) =>
    known.reduce((sum, event) => sum + value(event, field), 0);
  if (!known.length) return "用量未知";
  const label = `${total("input_tokens")} / ${total("output_tokens")} tokens`;
  return known.length === usage.length
    ? label
    : `部分用量：${label}（其余未知）`;
}

export const severityLabel = labels({ high: "高", medium: "中", low: "低" });

/** An attempt reports numbers only for the part its provider actually supplied. */
function attemptUsage(attempt: Attempt) {
  const number = (value: number | undefined) =>
    typeof value === "number" && Number.isFinite(value) && value >= 0
      ? value
      : 0;
  const input = number(attempt.input_tokens);
  const output = number(attempt.output_tokens);
  // usage_known is the legacy flag; usage_completeness adds partial reporting.
  if (attempt.usage_known === true || attempt.usage_completeness === "known")
    return { completeness: "known" as const, input, output };
  if (attempt.usage_completeness === "partial")
    return { completeness: "partial" as const, input, output };
  return { completeness: "unknown" as const, input, output };
}

/** A partial attempt keeps its known numbers while the total stays unknown. */
export function attemptUsageLabel(attempt: Attempt) {
  const usage = attemptUsage(attempt);
  if (usage.completeness === "known")
    return `${usage.input} / ${usage.output} tokens`;
  if (usage.completeness === "partial")
    return `已知部分 ${usage.input} / ${usage.output} tokens（总用量未知）`;
  return "用量未知";
}

/**
 * Run totals use persisted attempts (plus known routing usage), not the bounded
 * live event window. Known parts of a partial attempt still count; gaps never
 * become zero.
 */
export function runUsageSummary(run: Run, attempts: Attempt[]) {
  let input = 0;
  let output = 0;
  let reported = 0;
  let incomplete = 0;
  for (const attempt of attempts) {
    const usage = attemptUsage(attempt);
    if (usage.completeness === "unknown") {
      incomplete++;
      continue;
    }
    reported++;
    if (usage.completeness === "partial") incomplete++;
    input += usage.input;
    output += usage.output;
  }
  const routing = run.routing;
  if (routing?.usage_known) {
    reported++;
    input += routing.input_tokens ?? 0;
    output += routing.output_tokens ?? 0;
  } else if (routing?.model_call) {
    incomplete++;
  }
  if (!reported && !incomplete) return "";
  // A partial zero is still a reported number; only missing usage is unknown.
  if (!reported) return "用量未知";
  const label = `${input} / ${output} tokens`;
  return incomplete
    ? `部分用量：${label}（${incomplete} 处未知）`
    : `用量：${label}`;
}
const disabledLimitLabel: Record<string, string> = {
  run_seconds: "不设协作总期限",
  step_seconds: "不设单步执行期限",
  first_response_seconds: "关闭首个响应超时检测",
  stall_seconds: "关闭长时间无进展检测",
};
export const limitLabel = (key: string, value: number) => {
  if (key === "tool_calls") return "";
  if (value === 0 && disabledLimitLabel[key]) return disabledLimitLabel[key];
  return (
    {
      members: `每个会话最多 ${value} 位成员`,
      queued_runs: `最多排队 ${value} 次协作`,
      run_seconds: `单次协作最长为 ${Math.round(value / 60)} 分钟`,
      step_seconds: `单个步骤最长为 ${Math.round(value / 60)} 分钟`,
      first_response_seconds: `等待首个有效响应最多 ${Math.round(value / 60)} 分钟`,
      stall_seconds: `无进展告警阈值为 ${Math.round(value / 60)} 分钟`,
      handoffs: `每次协作最多交接 ${value} 个任务`,
      steps: `每次协作最多 ${value} 个步骤`,
      handoff_depth: `任务交接最多 ${value} 层`,
    }[key] ?? `${key}: ${value}`
  );
};

/** Frozen nanosecond policy values read as human durations, never as defaults. */
const policyDuration = (ns: number) => {
  const seconds = Math.max(0, Math.round(ns / 1e9));
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return rest ? `${minutes} 分 ${rest} 秒` : `${minutes} 分钟`;
};

/** Rows for a run's frozen execution policy; null means the record predates it. */
export function executionPolicyRows(
  policy: Run["execution_policy"],
): { label: string; value: string }[] {
  if (!policy) return [];
  return [
    {
      label: "协作总期限",
      value:
        policy.run_timeout_ns > 0
          ? policyDuration(policy.run_timeout_ns)
          : "不设绝对期限",
    },
    {
      label: "单步期限",
      value:
        policy.step_timeout_ns > 0
          ? policyDuration(policy.step_timeout_ns)
          : "不设绝对期限",
    },
    {
      label: "首个有效响应",
      value:
        policy.first_response_ns > 0
          ? `最多等待 ${policyDuration(policy.first_response_ns)}`
          : "关闭首响应检测",
    },
    {
      label: "长时间无进展",
      value:
        policy.stall_ns > 0
          ? `${policyDuration(policy.stall_ns)}后${
              policy.stall_action === "stop" ? "停止（stop）" : "仅提示（warn）"
            }`
          : "关闭静默检测（warn/stop 不触发）",
    },
  ];
}

export const policyNote =
  "以上是本次协作创建时冻结的执行策略，不是当前系统全局值；环境变量变化只影响新协作。";
export const legacyPolicyNote =
  "旧记录没有保存执行策略，无法推断当时限制；不套用当前默认值。";

/** Byte totals stay in bytes so a preview is never presented as the full text. */
export const bytesLabel = (value: number) =>
  `${Math.max(0, Math.round(value))} 字节`;
// Captures made before the search_results kind are still stored as web.
const legacySearchResultURL = (raw: string) => {
  try {
    const url = new URL(raw);
    const host = url.hostname.toLowerCase().replace(/^www\./, "");
    const path = url.pathname.replace(/\/$/, "");
    const query = url.searchParams;
    if (host === "so.com" || host.endsWith(".so.com"))
      return path === "/s" && query.has("q");
    if (host === "bing.com" || host.endsWith(".bing.com"))
      return path === "/search" && query.has("q");
    if (host === "sogou.com" || host.endsWith(".sogou.com"))
      return path === "/web" && query.has("query");
    if (host === "baidu.com" || host.endsWith(".baidu.com"))
      return path === "/s" && query.has("wd");
    if (host === "google.com" || host.endsWith(".google.com"))
      return path === "/search" && query.has("q");
    if (host === "duckduckgo.com" || host.endsWith(".duckduckgo.com"))
      return path.startsWith("/html") && query.has("q");
    if (host === "search.brave.com")
      return path === "/search" && query.has("q");
  } catch {
    return false;
  }
  return false;
};
export const sourceKindLabel = (source: Pick<Source, "kind" | "url">) =>
  source.kind === "file"
    ? "文件"
    : source.kind === "native_file"
      ? "文件 · 原生读取"
      : source.kind === "search_snippet"
        ? "搜索摘要 · 未读原文"
        : source.kind === "native_search"
          ? "原生搜索线索 · 未读原文"
          : source.kind === "native_web"
            ? legacySearchResultURL(source.url)
              ? "原生观察 · 搜索页线索"
              : "原生网页观察 · 可能为摘要"
            : source.kind === "search_results" ||
                legacySearchResultURL(source.url)
              ? "搜索结果页 · 未读原文"
              : "网页 · 已读原文";

/** Repeated runs re-capture the same page; list only the latest capture of each. */
export function latestSources(sources: Source[]) {
  const byKey = new Map<string, Source>();
  for (const source of sources) {
    const key = [
      source.kind,
      source.url || source.path,
      source.start_line,
      source.end_line,
    ].join("|");
    byKey.delete(key);
    byKey.set(key, source);
  }
  return [...byKey.values()].reverse();
}

const pad = (n: number) => String(n).padStart(2, "0");
const clock = (date: Date) =>
  `${pad(date.getHours())}:${pad(date.getMinutes())}`;
export function formatDateTime(iso?: string) {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${clock(date)}`;
}
/** Today shows only the time; older messages carry their date. */
export function formatMessageTime(iso: string, now = new Date()) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  if (date.toDateString() === now.toDateString()) return clock(date);
  const day = `${date.getMonth() + 1}月${date.getDate()}日 ${clock(date)}`;
  return date.getFullYear() === now.getFullYear()
    ? day
    : `${date.getFullYear()}年${day}`;
}
export function runDuration(run: Pick<Run, "created_at" | "finished_at">) {
  if (!run.finished_at) return "";
  const seconds = Math.max(
    0,
    Math.round(
      (new Date(run.finished_at).getTime() -
        new Date(run.created_at).getTime()) /
        1000,
    ),
  );
  if (Number.isNaN(seconds)) return "";
  return seconds < 60
    ? `${seconds} 秒`
    : `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
}
/** A first question becomes a short title: its first sentence, at most 28 characters. */
export function threadTitle(text: string) {
  const sentence =
    text
      .trim()
      .split(/[\n。？！?!]/)[0]
      ?.trim() || "新会话";
  const chars = [...sentence];
  return chars.length > 28 ? `${chars.slice(0, 28).join("")}…` : sentence;
}

/** Roles each built-in workflow needs, in execution order. */
export const workflowRoles: Record<string, string[]> = {
  discussion: ["coordinator"],
  research: ["researcher", "critic", "coordinator"],
  code_analysis: ["analyst", "reviewer"],
};

/**
 * The coordinator a new conversation gets from its prepared roster: the global
 * default when this workspace can use it, else the server's roster fallback
 * (third member, or the first when fewer are available).
 */
export function conversationCoordinator(
  agents: Agent[],
  defaultCoordinatorID = "",
) {
  const enabled = agents.filter((agent) => !agent.disabled);
  if (enabled.some((agent) => agent.id === defaultCoordinatorID))
    return defaultCoordinatorID;
  return enabled[Math.min(2, enabled.length - 1)]?.id ?? "";
}
