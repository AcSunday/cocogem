import type { components } from "./schema";

export type Workspace = components["schemas"]["Workspace"];
export type Agent = components["schemas"]["Agent"];
export type Thread = components["schemas"]["Thread"];
export type Message = components["schemas"]["Message"];
export type Run = components["schemas"]["Run"];
export type RunInput = components["schemas"]["RunInput"];
export type Step = components["schemas"]["Step"];
export type Source = components["schemas"]["Source"];
export type Artifact = components["schemas"]["Artifact"];
export type Event = components["schemas"]["Event"];
export type System = components["schemas"]["SystemInfo"];
export type Routing = components["schemas"]["Routing"];
export type ThreadView = components["schemas"]["ThreadView"];

let csrf = "";
export class APIError extends Error {
  constructor(
    message: string,
    public readonly status: number,
  ) {
    super(message);
  }
}
export function setCSRF(value: string) {
  csrf = value;
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  headers: Record<string, string> = {},
): Promise<T> {
  const response = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      ...(method !== "GET" ? { "X-CSRF-Token": csrf } : {}),
      ...headers,
    },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const result = await response.json();
  if (!response.ok)
    throw new APIError(
      result.error?.message ?? `HTTP ${response.status}`,
      response.status,
    );
  return result as T;
}
export const terminal = (status: string) =>
  ["succeeded", "failed", "canceled", "interrupted"].includes(status);
export const statusText: Record<string, string> = {
  queued: "排队中",
  running: "执行中",
  canceling: "正在停止",
  succeeded: "已完成",
  failed: "失败",
  canceled: "已停止",
  interrupted: "运行已中断",
  completed: "已完成",
  processing: "生成中",
  pending: "等待上游",
  skipped: "已跳过",
  configured: "已配置",
  not_configured: "未配置",
  verified: "已验证",
  demo: "离线演示",
  error: "异常",
};

export function mergeEvents(current: Event[], incoming: Event): Event[] {
  if (
    current.some(
      (event) => event.run_id === incoming.run_id && event.seq === incoming.seq,
    )
  )
    return current;
  return [...current, incoming].sort((a, b) => a.seq - b.seq);
}

// Read every page so a long conversation does not hide its latest messages.
export async function loadAllThreadPages(id: string): Promise<ThreadView> {
  const first = await api<ThreadView>(`/api/v1/threads/${id}?limit=100`);
  let cursor = first.messages.next_cursor;
  while (cursor) {
    const page = await api<ThreadView>(
      `/api/v1/threads/${id}?limit=100&cursor=${cursor}`,
    );
    first.messages.items.push(...page.messages.items);
    cursor = page.messages.next_cursor;
  }
  return first;
}
