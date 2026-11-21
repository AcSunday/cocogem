import type {
  Agent,
  Attempt,
  Event,
  Message,
  Run,
  Source,
} from "../api/client";
import { isEnglish, t, translateBackendMessage } from "./i18n";

/** The single dynamic collaboration kernel has no preset workflow choices. */
export const workflows = {
  get free() {
    return t("协作会话", "Collaborative conversation");
  },
};
export const errorText = (error: unknown) =>
  error instanceof Error
    ? runErrorText(error.message)
    : t("操作失败", "Operation failed");
/** Member avatars use the first character of the display name. */
export const initial = (name = "") => [...name.trim()][0] ?? "?";

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
  return resolved || requested
    ? t("CLI 默认模型", "CLI default model")
    : "";
}

const labels =
  (zhTable: Record<string, string>, enTable: Record<string, string>) =>
  (key = "") => {
    const table = isEnglish() ? enTable : zhTable;
    return Object.hasOwn(table, key) ? table[key] : key;
  };

export const toolLabel = labels(
  {
    list_files: "列出文件",
    read_file: "读取文件",
    search_content: "搜索内容",
    git_inspect: "仓库检查",
    preview_page: "页面预览",
    web_search: "在线搜索",
    fetch_url: "读取网页",
    read_source: "重读来源",
    list_sources: "列出本步来源",
    publish_artifact: "发布产物",
    write_file: "写入文件",
    patch_file: "局部修改",
    run_test: "运行测试",
    run_command: "任务执行（完全访问）",
    view_image: "查看图片",
    publish_file: "交付文件",
    handoff_task: "交接任务",
    get_task: "查询协作任务",
    read_skill: "读取协作技能",
    Read: "原生读取文件",
    Glob: "原生列出文件",
    Grep: "原生搜索内容",
    WebSearch: "原生联网搜索",
    WebFetch: "原生读取网页",
  },
  {
    list_files: "List files",
    read_file: "Read file",
    search_content: "Search content",
    git_inspect: "Inspect repository",
    preview_page: "Preview page",
    web_search: "Web search",
    fetch_url: "Fetch web page",
    read_source: "Re-read source",
    list_sources: "List step sources",
    publish_artifact: "Publish deliverable",
    write_file: "Write file",
    patch_file: "Patch file",
    run_test: "Run tests",
    run_command: "Run command (Full access)",
    view_image: "View image",
    publish_file: "Deliver file",
    handoff_task: "Hand off task",
    get_task: "Inspect task",
    read_skill: "Load collaboration skill",
    Read: "Native read file",
    Glob: "Native list files",
    Grep: "Native search content",
    WebSearch: "Native web search",
    WebFetch: "Native fetch page",
  },
);

export const artifactTypeLabel = labels(
  {
    note: "讨论纪要",
    research_report: "研究报告",
    code_analysis: "代码分析报告",
    engineering_report: "工程交付报告",
  },
  {
    note: "Discussion Summary",
    research_report: "Research Report",
    code_analysis: "Code Analysis Report",
    engineering_report: "Engineering Delivery Report",
  },
);

export const roleLabel = labels(
  {
    researcher: "研究",
    critic: "质疑",
    coordinator: "汇总",
    engineer: "执行",
    analyst: "分析",
    developer: "开发",
    reviewer: "复核",
    final_reviewer: "最终复核",
    discussant: "讨论",
    synthesizer: "综合草案",
    checker: "原作者复查",
    responder: "回复",
    member: "成员",
  },
  {
    researcher: "Research",
    critic: "Critique",
    coordinator: "Coordinate",
    engineer: "Execute",
    analyst: "Analyze",
    developer: "Develop",
    reviewer: "Review",
    final_reviewer: "Final review",
    discussant: "Discuss",
    synthesizer: "Draft synthesis",
    checker: "Author re-check",
    responder: "Reply",
    member: "Member",
  },
);

/** Persisted run outcomes of the single dynamic kernel. */
export const methodLabel = labels(
  {
    direct: "直接回答",
    delegation: "协作分工",
  },
  {
    direct: "Direct reply",
    delegation: "Team delegation",
  },
);

/**
 * The settled outcome recorded on a run: a member retry, a delegated chain, or a
 * direct answer. Preset workflow labels no longer exist.
 */
export function runMethod(run: Pick<Run, "method" | "retry_of_step_id">) {
  if (run.retry_of_step_id)
    return {
      method: "member_retry",
      label: t("成员重试", "Member retry"),
      reason: t(
        "保留已完成的回复，仅重试失败步骤并继续其尚未执行的后续工作",
        "Keeps completed replies, retries only the failed member step, and continues remaining downstream work",
      ),
    };
  const method = run.method || "";
  const label = method ? methodLabel(method) : workflows.free;
  return { method, label, reason: "" };
}

export const skillLabel = labels(
  {
    "task-orchestration": "任务编排",
    "task-handoff": "任务交接",
    "independent-review": "独立复核",
    "synthesize-results": "结果汇总",
    "file-delivery": "文件交付",
    "writing-plans": "编写方案",
    debugging: "调试",
    tdd: "测试驱动",
    "quality-gate": "交付自检",
    "receive-review": "处理复核",
    _shared: "共享参考",
  },
  {
    "task-orchestration": "Task orchestration",
    "task-handoff": "Task handoff",
    "independent-review": "Independent review",
    "synthesize-results": "Result synthesis",
    "file-delivery": "File delivery",
    "writing-plans": "Plan writing",
    debugging: "Debugging",
    tdd: "Test-driven development",
    "quality-gate": "Quality gate",
    "receive-review": "Review response",
    _shared: "Shared reference",
  },
);

export const findingStatusLabel = labels(
  {
    supported: "有依据",
    confirmed: "已确认",
    disputed: "有争议",
    insufficient_evidence: "证据不足",
  },
  {
    supported: "Supported",
    confirmed: "Confirmed",
    disputed: "Disputed",
    insufficient_evidence: "Insufficient evidence",
  },
);

/** Engine errors are English diagnostics; known ones get plain wording, the rest stay as-is. */
export function runErrorText(error = "", status = "") {
  if (!error) return "";
  if (status === "canceled" || /context canceled/.test(error))
    return t("已按你的要求停止。", "Stopped as requested.");
  if (error.includes("host_input_limit"))
    return t(
      "成员设置的输入上限不足以容纳本次初始输入，任务没有开始执行；请缩短问题、改用来源引用或调高该成员的上下文预算（tokens）。不会自动重试。",
      "The member's input limit is too small for this prompt, so execution did not start. Shorten the prompt, reference files instead, or raise the member's context token limit. Not retried automatically.",
    );
  if (error.includes("context_overflow"))
    return t(
      "CLI 的上下文窗口已超出容量，本次尝试未完成；检查点已保留，需要你确认后继续。不会自动重试。",
      "The CLI context window ran out of capacity before finishing. A checkpoint was saved; review and resume manually. Not retried automatically.",
    );
  if (/failure_kind[=:]\s*interrupted|^interrupted(?:[:：]|$)/.test(error))
    return t(
      "执行被中断，检查点已保留；需要你确认后手动继续。不会自动重试。",
      "Execution was interrupted and a checkpoint was saved. Confirm to resume manually. Not retried automatically.",
    );
  if (error.includes("category=protocol_mismatch"))
    return t(
      "当前 CLI 的接口协议与现有上游不匹配，请检查本机 CLI 的服务地址与供应商映射。",
      "The local CLI's API protocol does not match the configured upstream endpoint. Check your local CLI endpoint and provider settings.",
    );
  if (
    error.includes(
      "Gemini returned an empty, truncated or malformed response",
    )
  )
    return t(
      "Gemini 返回了空响应、截断内容或无效工具调用，本次回复未完成；可以重试该成员。",
      "Gemini returned an empty, truncated, or malformed response. This reply did not finish; you can retry this member.",
    );
  if (error.includes("Gemini model stream ended with an incomplete event"))
    return t(
      "Gemini 的模型响应流不完整，本次回复未完成；可以重试该成员。",
      "The Gemini response stream ended incomplete. This reply did not finish; you can retry this member.",
    );
  if (error.includes("Gemini provider returned a temporary server error"))
    return t(
      "Gemini 上游服务暂时出错，本次回复未完成；可以稍后重试该成员。",
      "The Gemini upstream service returned a temporary server error. You can retry this member shortly.",
    );
  if (error.includes("Gemini provider returned an unclassified stream error"))
    return t(
      "Gemini 上游服务返回了错误，本次回复未完成；可以稍后重试该成员。",
      "The Gemini upstream service returned a stream error. You can retry this member shortly.",
    );
  if (error.includes("Gemini provider reported a quota or rate limit"))
    return t(
      "Gemini 上游服务报告额度或速率限制，本次回复未完成；可以稍后重试该成员。",
      "The Gemini upstream service reported a quota or rate limit. You can retry this member later.",
    );
  if (error.includes("Gemini provider rejected the existing authentication"))
    return t(
      "Gemini 上游服务未接受现有认证，本次回复未完成；请检查本机 CLI 的连接配置。",
      "The Gemini upstream service rejected the current authentication. Check your local CLI connection settings.",
    );
  if (error.includes("Gemini provider denied this request"))
    return t(
      "Gemini 上游服务拒绝了这次请求，本次回复未完成。",
      "The Gemini upstream service denied this request.",
    );
  if (
    error.includes("result=result_error, category=execution") ||
    error.includes(
      "Gemini reported an unsuccessful result without diagnostic details",
    )
  )
    return t(
      "Gemini 未确认回复成功，CLI 没有提供具体失败原因；可以重试该成员。",
      "Gemini did not confirm a successful reply and the CLI provided no specific error details; you can retry this member.",
    );
  const native =
    /(?:^|[\s\]:])([a-z_]+): native CLI did not confirm success \(result=[a-z0-9_.-]+(?:, status=(\d+))?, category=([a-z_]+)\)/.exec(
      error,
    );
  if (native) return nativeFailureText(native[1], native[3], native[2]);
  if (error.includes("connection_changed:")) {
    const detail = error.replace(/^.*?connection_changed:/, "").trim();
    return t(
      `本机 CLI 的解析模型或上游连接已变化，任务与检查点已保留：${detail}`,
      `The local CLI's resolved model or upstream connection changed (checkpoint saved): ${detail}`,
    );
  }
  if (error.includes("connection_blocked:")) {
    const detail = error.replace(/^.*?connection_blocked:/, "").trim();
    return t(
      `本机 CLI 连接配置未通过检查，任务与检查点已保留：${detail}`,
      `The local CLI connection check did not pass (checkpoint saved): ${detail}`,
    );
  }
  if (
    error.includes("capability_blocked:") ||
    error.includes("required_tools_unavailable:")
  ) {
    const detail = error
      .replace(/^.*?(capability_blocked:|required_tools_unavailable:)/, "")
      .trim();
    return t(
      `CLI 能力检查未通过，任务与检查点已保留：${detail}`,
      `The CLI capability check did not pass (checkpoint saved): ${detail}`,
    );
  }
  if (error.includes("checkpoint_blocked:")) {
    const detail = error.replace(/^.*?checkpoint_blocked:/, "").trim();
    return t(
      `恢复检查未通过，任务与检查点已保留：${detail}`,
      `Recovery check did not pass (checkpoint saved): ${detail}`,
    );
  }
  if (error.startsWith("recovery_uncertain_process:"))
    return t(
      "上次执行的进程无法核实归属，已保留检查点且没有自动重跑。确认进程已停止后可继续执行。",
      "Could not verify whether the previous CLI process has stopped. A checkpoint was saved without automatic rerun; confirm the process has stopped before resuming.",
    );
  if (error.startsWith("step_timeout:"))
    return t(
      "这一步超过配置的执行时限，未产生已提交结果。",
      "This step exceeded the configured execution time limit without producing a committed result.",
    );
  if (error.startsWith("first_response_timeout:"))
    return t(
      "CLI 在等待首个有效响应时超过时限。",
      "Timed out waiting for the first response from the CLI.",
    );
  if (error.startsWith("stall_timeout:"))
    return t(
      "CLI 长时间没有产生任何执行进展，已按停滞策略停止。",
      "The CLI made no progress for too long and was stopped by the stall policy.",
    );
  const knownTable: Record<string, string> = isEnglish()
    ? {
        "service restarted before execution settled":
          "The service restarted before this collaboration run finished.",
        "upstream step did not succeed":
          "An upstream step did not finish, so this step was skipped.",
        "run stopped before this step":
          "The collaboration run was stopped before this step started.",
        "step ended without a committed completion":
          "This step ended unexpectedly without saving a final result.",
        "service suspended before step completion; will resume":
          "The service stopped before this step finished; it will resume from the checkpoint on restart.",
        "service restarted; checkpoint preserved":
          "The service restarted and preserved this step's checkpoint.",
      }
    : {
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
      };
  return knownTable[error] ?? translateBackendMessage(error);
}

/**
 * A native CLI reports why its endpoint failed as a machine kind, a category and
 * the HTTP status when there was one. The status is shown so the cause can be
 * checked against the CLI's own endpoint, relay or account.
 */
function nativeFailureText(kind: string, category: string, status = "") {
  if (isEnglish()) {
    const code = status ? ` (HTTP ${status})` : "";
    if (category === "incomplete_response")
      return "The upstream service returned an incomplete or malformed response (often caused by a proxy or gateway dropping the connection). You can retry this member.";
    switch (kind) {
      case "provider_transient":
        return `The upstream model service had a temporary error${code}. You can retry this member shortly.`;
      case "network":
        return `The local CLI could not reach its configured upstream endpoint${code}. Check your network or proxy settings and retry this member.`;
      case "quota":
        return `The upstream service reported a quota or rate limit${code}. Retry later or check the CLI account's quota.`;
      case "authentication":
        return `The upstream service rejected the local CLI's authentication${code}. Check the CLI's own login or API key configuration.`;
      case "permissions":
        return `The upstream service or CLI denied the required permissions for this request${code}.`;
      case "model_unavailable":
        return `The upstream service does not offer the selected model${code}. Check the member's model setting and CLI configuration.`;
      case "tool_unavailable":
        return "The CLI reported that a required tool is unavailable.";
    }
    return status
      ? `The upstream service rejected this request${code} without a specific reason. You can retry this member.`
      : "The CLI did not confirm a successful reply or provide a specific failure reason. You can retry this member.";
  }
  const code = status ? `（HTTP ${status}）` : "";
  if (category === "incomplete_response")
    return "上游返回了不完整或格式异常的响应（常见于中转网关或代理中途断开连接），本次回复未完成；可以重试该成员。";
  switch (kind) {
    case "provider_transient":
      return `上游模型服务暂时出错${code}，本次回复未完成；可以稍后重试该成员。`;
    case "network":
      return `本机 CLI 无法连接它配置的上游端点${code}，本次回复未完成；请检查本机网络与代理后重试该成员。`;
    case "quota":
      return `上游报告额度或速率限制${code}，本次回复未完成；可以稍后重试该成员，或检查该 CLI 账户的现有额度。`;
    case "authentication":
      return `上游没有接受本机 CLI 的现有认证${code}，本次回复未完成；请检查该 CLI 自身的连接配置。`;
    case "permissions":
      return `上游或 CLI 拒绝了这次请求所需的权限${code}，本次回复未完成。`;
    case "model_unavailable":
      return `上游不提供所选模型${code}，本次回复未完成；请检查成员的模型设置与该 CLI 的模型映射。`;
    case "tool_unavailable":
      return "CLI 报告所需工具不可用，本次回复未完成。";
  }
  return status
    ? `上游拒绝了这次请求${code}，CLI 没有给出可归类的原因，本次回复未完成；可以重试该成员。`
    : "CLI 没有确认回复成功，也没有给出可归类的失败原因，本次回复未完成；可以重试该成员。";
}

/** Machine failure kinds need a clear reason without promising an automatic retry. */
export const failureKindLabel = labels(
  {
    host_input_limit:
      "成员设置的输入上限不足以容纳本次初始输入，任务没有开始执行；请缩短问题、改用来源引用或调高该成员的上下文预算（tokens）。不会自动重试。",
    context_overflow:
      "CLI 上下文窗口已超出容量，本次尝试未完成；检查点已保留，需要你确认后继续。不会自动重试。",
    process_uncertain:
      "旧 CLI 进程状态尚未确认；必须先核实该进程已停止，才能继续。不会自动续跑。",
    interrupted:
      "执行被中断，检查点已保留；需要你确认后手动继续。不会自动重试。",
    first_response_timeout: "CLI 在等待首个有效响应时超过时限。",
    stall_timeout: "CLI 长时间没有产生任何执行进展，已按停滞策略停止。",
    permissions: "CLI 拒绝了这次执行所需的权限，任务没有继续。",
    quota: "CLI 账户报告额度或速率限制，任务没有继续。",
    authentication: "CLI 的现有认证配置未通过检查，任务没有继续。",
    provider_transient: "上游模型服务暂时出错，本次尝试未完成。",
    network: "本机 CLI 无法连接它配置的上游端点，或上游响应中途断开。",
  },
  {
    host_input_limit:
      "The member's input limit is too small for this prompt, so execution did not start. Shorten the prompt, reference files instead, or raise the member's context token limit. Not retried automatically.",
    context_overflow:
      "The CLI context window ran out of capacity before finishing. A checkpoint was saved; confirm to resume manually. Not retried automatically.",
    process_uncertain:
      "The previous CLI process state is unconfirmed; verify it has stopped before resuming. Not resumed automatically.",
    interrupted:
      "Execution was interrupted and a checkpoint was saved. Confirm to resume manually. Not retried automatically.",
    first_response_timeout:
      "Timed out waiting for the first response from the CLI.",
    stall_timeout:
      "The CLI made no progress for too long and was stopped by the stall policy.",
    permissions:
      "The CLI denied the permissions required for this step.",
    quota:
      "The CLI account reported a quota or rate limit.",
    authentication:
      "The CLI's authentication configuration did not pass verification.",
    provider_transient:
      "The upstream model service encountered a temporary error.",
    network:
      "The local CLI could not reach its configured upstream endpoint, or the connection dropped.",
  },
);

export const attemptReasonLabel = labels(
  {
    initial: "首次执行",
    retry_after_interruption: "重启后续跑",
    service_restart: "服务重启恢复",
    manual_resume: "手动继续",
    owned_process_reclaimed: "已核实并停止旧进程，创建新尝试",
    new_attempt_from_context: "使用已保存上下文创建新尝试",
    manual: "需要人工核实",
  },
  {
    initial: "Initial run",
    retry_after_interruption: "Resumed after restart",
    service_restart: "Recovered after service restart",
    manual_resume: "Resumed manually",
    owned_process_reclaimed: "Verified and stopped old process, started new attempt",
    new_attempt_from_context: "Started new attempt from saved context",
    manual: "Requires manual check",
  },
);

export const attemptStatusLabel = labels(
  {
    running: "执行中",
    succeeded: "已完成",
    failed: "失败",
    canceled: "已停止",
    interrupted: "已中断",
    uncertain: "结果待核实",
    blocked: "能力不足",
  },
  {
    running: "Running",
    succeeded: "Completed",
    failed: "Failed",
    canceled: "Stopped",
    interrupted: "Interrupted",
    uncertain: "Needs verification",
    blocked: "Capability unavailable",
  },
);

export const sessionModeLabel = labels(
  {
    fresh: "独立新会话",
    resumed: "原生会话续跑",
    context_continuation: "上下文续跑",
  },
  {
    fresh: "Fresh session",
    resumed: "Resumed native session",
    context_continuation: "Continued with saved context",
  },
);

export const livenessLabel = labels(
  {
    waiting_first_response: "等待首个响应",
    stalled: "长时间无进展",
    waiting_tool: "等待工具",
    waiting_member: "等待其他成员",
  },
  {
    waiting_first_response: "Waiting for first response",
    stalled: "No progress for a while",
    waiting_tool: "Waiting for tool",
    waiting_member: "Waiting for another member",
  },
);

// Native notices stay in the event log for diagnostics; only capability changes
// reach the user. Literal matching keeps prototype keys out of the render path.
export const noticeLabel = (kind = "") =>
  kind === "native_file_degraded"
    ? t(
        "原生文件工具已停用，本步改用平台文件工具",
        "Native file tools disabled; using built-in workspace file tools for this step",
      )
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
  if (event.type === "method_recorded") {
    const method = typeof data.method === "string" ? data.method : "";
    return method
      ? t(
          `本轮方法：${methodLabel(method)}`,
          `Approach: ${methodLabel(method)}`,
        )
      : "";
  }
  return "";
}

/**
 * Only confirmed activity clears a warning. An explicit false wins even when a
 * snapshot carries old content, and snapshots without the flag prove nothing.
 */
function activityEvent(event: Event) {
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
  if (!known.length) return t("用量未知", "Usage unknown");
  const label = `${total("input_tokens")} / ${total("output_tokens")} tokens`;
  return known.length === usage.length
    ? label
    : t(
        `部分用量：${label}（其余未知）`,
        `Partial usage: ${label} (others unknown)`,
      );
}

export const severityLabel = labels(
  { high: "高", medium: "中", low: "低" },
  { high: "High", medium: "Medium", low: "Low" },
);

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
    return t(
      `已知部分 ${usage.input} / ${usage.output} tokens（总用量未知）`,
      `Known part: ${usage.input} / ${usage.output} tokens (total unknown)`,
    );
  return t("用量未知", "Usage unknown");
}

/**
 * Run totals use persisted attempts, not the bounded live event window. Known
 * parts of a partial attempt still count; gaps never become zero.
 */
export function runUsageTokens(_run: Run, attempts: Attempt[]) {
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
  if (!reported && !incomplete) return "";
  // A partial zero is still a reported number; only missing usage is unknown.
  if (!reported) return t("用量未知", "Usage unknown");
  const label = `${input} / ${output} tokens`;
  return incomplete
    ? t(
        `部分用量：${label}（${incomplete} 处未知）`,
        `Partial usage: ${label} (${incomplete} unknown)`,
      )
    : label;
}

export function runUsageSummary(run: Run, attempts: Attempt[]) {
  const tokens = runUsageTokens(run, attempts);
  if (
    !tokens ||
    tokens === "用量未知" ||
    tokens === "Usage unknown" ||
    tokens.startsWith("部分用量：") ||
    tokens.startsWith("Partial usage:")
  )
    return tokens;
  return t(`用量：${tokens}`, `Usage: ${tokens}`);
}

/** The terminal status glyph shared by the session list and run footer. */
export function statusIconName(status: string) {
  switch (status) {
    case "succeeded":
    case "completed":
      return "check" as const;
    case "failed":
      return "close" as const;
    case "canceled":
      return "stop" as const;
    case "interrupted":
      return "refresh" as const;
  }
  return undefined;
}
const disabledLimitLabelZh: Record<string, string> = {
  run_seconds: "不设协作总期限",
  step_seconds: "不设单步执行期限",
  first_response_seconds: "关闭首个响应超时检测",
  stall_seconds: "关闭长时间无进展检测",
};
const disabledLimitLabelEn: Record<string, string> = {
  run_seconds: "No overall collaboration time limit",
  step_seconds: "No per-step time limit",
  first_response_seconds: "First-response timeout check disabled",
  stall_seconds: "No-progress stall detection disabled",
};
export const limitLabel = (key: string, value: number) => {
  if (key === "tool_calls") return "";
  const disabledTable = isEnglish()
    ? disabledLimitLabelEn
    : disabledLimitLabelZh;
  if (value === 0 && disabledTable[key]) return disabledTable[key];
  if (isEnglish()) {
    return (
      {
        members: `Up to ${value} members per conversation`,
        queued_runs: `Up to ${value} queued runs`,
        parallel_runs: `Up to ${value} concurrent conversations`,
        run_seconds: `Up to ${Math.round(value / 60)} min per collaboration run`,
        step_seconds: `Up to ${Math.round(value / 60)} min per step`,
        first_response_seconds: `Up to ${Math.round(value / 60)} min for first response`,
        stall_seconds: `No-progress alert threshold: ${Math.round(value / 60)} min`,
        handoffs: `Up to ${value} task handoffs per run`,
        steps: `Up to ${value} steps per run`,
        handoff_depth: `Up to ${value} handoff depth levels`,
      }[key] ?? `${key}: ${value}`
    );
  }
  return (
    {
      members: `每个会话最多 ${value} 位成员`,
      queued_runs: `最多排队 ${value} 次协作`,
      parallel_runs: `最多同时执行 ${value} 个会话`,
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
  if (isEnglish()) {
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.floor(seconds / 60);
    const rest = seconds % 60;
    return rest ? `${minutes}m ${rest}s` : `${minutes} min`;
  }
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
  if (isEnglish()) {
    return [
      {
        label: "Overall time limit",
        value:
          policy.run_timeout_ns > 0
            ? policyDuration(policy.run_timeout_ns)
            : "No hard time limit",
      },
      {
        label: "Per-step time limit",
        value:
          policy.step_timeout_ns > 0
            ? policyDuration(policy.step_timeout_ns)
            : "No hard time limit",
      },
      {
        label: "First response",
        value:
          policy.first_response_ns > 0
            ? `Wait up to ${policyDuration(policy.first_response_ns)}`
            : "First-response check disabled",
      },
      {
        label: "No progress",
        value:
          policy.stall_ns > 0
            ? `${
                policy.stall_action === "stop" ? "Stop" : "Warn only"
              } after ${policyDuration(policy.stall_ns)}`
            : "Stall check disabled",
      },
    ];
  }
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
export const getPolicyNote = () =>
  t(
    policyNote,
    "These execution limits were locked when this run started and do not change if system defaults are updated later.",
  );
export const getLegacyPolicyNote = () =>
  t(
    legacyPolicyNote,
    "Older records did not store an execution policy; current defaults are not retroactively applied.",
  );

/** Byte totals stay in bytes so a preview is never presented as the full text. */
export const bytesLabel = (value: number) =>
  t(
    `${Math.max(0, Math.round(value))} 字节`,
    `${Math.max(0, Math.round(value))} bytes`,
  );
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
    ? t("文件", "File")
    : source.kind === "native_file"
      ? t("文件 · 原生读取", "File · Native read")
      : source.kind === "diff"
        ? t("写文件 diff", "File write diff")
        : source.kind === "test"
          ? t("测试记录", "Test log")
          : source.kind === "search_snippet"
            ? t("搜索摘要 · 未读原文", "Search snippet · Full page unread")
            : source.kind === "native_search"
              ? t("原生搜索线索 · 未读原文", "Native search lead · Full page unread")
              : source.kind === "native_web"
                ? legacySearchResultURL(source.url)
                  ? t("原生观察 · 搜索页线索", "Native observation · Search page lead")
                  : t("原生网页观察 · 可能为摘要", "Native web observation · May be summary")
                : source.kind === "search_results" ||
                    legacySearchResultURL(source.url)
                  ? t("搜索结果页 · 未读原文", "Search results · Full page unread")
                  : t("网页 · 已读原文", "Web page · Full text read");

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
/** Compact task-list timestamps: minutes, hours, yesterday, days, then a date. */
export function formatRelativeTime(iso?: string, now = new Date()) {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const elapsed = Math.max(0, now.getTime() - date.getTime());
  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (elapsed < minute) return t("刚刚", "Just now");
  if (elapsed < hour) {
    const mins = Math.floor(elapsed / minute);
    return t(`${mins} 分钟前`, `${mins}m ago`);
  }
  if (elapsed < day) {
    const hours = Math.floor(elapsed / hour);
    return t(`${hours} 小时前`, `${hours}h ago`);
  }
  if (elapsed < 2 * day) return t("昨天", "Yesterday");
  if (elapsed < 7 * day) {
    const days = Math.floor(elapsed / day);
    return t(`${days} 天前`, `${days}d ago`);
  }
  return formatDateTime(iso).slice(0, 10);
}
/** Today shows only the time; older messages carry their date. */
export function formatMessageTime(iso: string, now = new Date()) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  if (date.toDateString() === now.toDateString()) return clock(date);
  if (isEnglish()) {
    const day = `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${clock(date)}`;
    return date.getFullYear() === now.getFullYear()
      ? day
      : `${date.getFullYear()}-${day}`;
  }
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
  if (isEnglish()) {
    return seconds < 60
      ? `${seconds}s`
      : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  }
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
      ?.trim() || t("新会话", "New conversation");
  const chars = [...sentence];
  return chars.length > 28 ? `${chars.slice(0, 28).join("")}…` : sentence;
}

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
