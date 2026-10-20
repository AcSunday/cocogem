import type { Agent, Run, Source } from "../api/client";

export const workflows = {
  free: "自由协作",
  discussion: "协作讨论",
  research: "Research",
  code_analysis: "代码分析",
};
export const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "操作失败";
/** Member avatars use the first character of the display name. */
export const initial = (name = "") => [...name.trim()][0] ?? "?";
export const memberLabel = (agent: Agent) =>
  `${agent.display_name} (@${agent.handle})`;

const labels =
  (table: Record<string, string>) =>
  (key = "") =>
    table[key] ?? key;
export const toolLabel = labels({
  list_files: "列出文件",
  read_file: "读取文件",
  search_content: "搜索内容",
  web_search: "在线搜索",
  fetch_url: "读取网页",
  read_source: "重读来源",
  publish_artifact: "发布产物",
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
  responder: "回复",
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
  return (
    {
      "service restarted before execution settled":
        "服务重启时这次协作还没结束，已中断。",
      "upstream step did not succeed": "上游步骤没有完成，这一步已跳过。",
      "run stopped before this step": "协作已停止，这一步没有开始。",
      "step ended without a committed completion":
        "这一步没有正常结束，结果未保存。",
    }[error] ?? error
  );
}
export const severityLabel = labels({ high: "高", medium: "中", low: "低" });
export const limitLabel = (key: string, value: number) =>
  ({
    members: `每个会话最多 ${value} 位成员`,
    queued_runs: `最多排队 ${value} 次协作`,
    step_seconds: `单个步骤最长 ${value} 秒`,
    tool_calls: `单个步骤最多调用 ${value} 次工具`,
  })[key] ?? `${key}: ${value}`;
export const sourceKindLabel = (source: Source) =>
  source.kind === "file"
    ? "文件"
    : source.kind === "search_snippet"
      ? "搜索摘要 · 未读原文"
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

export function defaultAssignments(agents: Agent[], coordinatorID = "") {
  const enabled = agents.filter((agent) => !agent.disabled);
  return {
    researcher: enabled[0]?.id ?? "",
    critic: enabled[1]?.id ?? "",
    coordinator:
      enabled.find((agent) => agent.id === coordinatorID)?.id ??
      enabled[2]?.id ??
      enabled[0]?.id ??
      "",
    analyst: enabled[0]?.id ?? "",
    reviewer: enabled[1]?.id ?? "",
  };
}
