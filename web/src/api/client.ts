import type { components } from "./schema";

export type Workspace = components["schemas"]["Workspace"];
export type Agent = components["schemas"]["Agent"];
export type WorkspaceMember = components["schemas"]["WorkspaceMember"];
export type Settings = components["schemas"]["Settings"];
export type SettingsInput = components["schemas"]["SettingsInput"];
export type Thread = components["schemas"]["Thread"];
export type Message = components["schemas"]["Message"];
export type Run = components["schemas"]["Run"];
export type RunInput = components["schemas"]["RunInput"];
export type Step = components["schemas"]["Step"];
export type Attempt = components["schemas"]["Attempt"];
export type Source = components["schemas"]["Source"];
export type SourceImport = components["schemas"]["SourceImport"];
export type Artifact = components["schemas"]["Artifact"];
export type Event = components["schemas"]["Event"];
export type System = components["schemas"]["SystemInfo"];
export type ModelOptions = components["schemas"]["ModelOptions"];
export type Routing = components["schemas"]["Routing"];
export type ThreadView = components["schemas"]["ThreadView"];
export type RunView = components["schemas"]["RunView"];
export type MessageContentPage = components["schemas"]["MessageContentPage"];
export type EventPage = components["schemas"]["EventPage"];
export type DirectoryListing = components["schemas"]["DirectoryListing"];
export type ProjectOverview = components["schemas"]["Overview"];
export type ProjectTaskView = components["schemas"]["TaskView"];
export type TaskBoard = components["schemas"]["TaskBoard"];
export type TaskDeletion = components["schemas"]["TaskDeletion"];
export type ProjectRecord = components["schemas"]["ProjectRecord"];
export type BudgetPolicy = components["schemas"]["BudgetPolicy"];
export type BudgetAccount = components["schemas"]["BudgetAccount"];
export type BudgetPolicyOverride =
  components["schemas"]["BudgetPolicyOverride"];
export type BootstrapInput = components["schemas"]["BootstrapInput"];
export type BootstrapReply = components["schemas"]["BootstrapReply"];
export type InboxItem = components["schemas"]["InboxItem"];
export type ProjectTask = components["schemas"]["Task"];
export type TaskRevision = components["schemas"]["TaskRevision"];
export type Question = components["schemas"]["Question"];
export type CompletionRecord = components["schemas"]["CompletionRecord"];
export type CriterionResult = components["schemas"]["CriterionResult"];
export type Grant = components["schemas"]["Grant"];
export type Claim = components["schemas"]["Claim"];
export type DatasetManifest = components["schemas"]["DatasetManifest"];
export type CollaborationMessage =
  components["schemas"]["CollaborationMessage"];
export type Wakeup = components["schemas"]["Wakeup"];
export type TaskDeliveries = components["schemas"]["TaskDeliveries"];
export type ToolPermissionRequest =
  components["schemas"]["ToolPermissionRequest"];
export type PermissionRequestList =
  components["schemas"]["PermissionRequestList"];
export type ConversationGrant = components["schemas"]["ConversationGrant"];
export type ConversationGrantList =
  components["schemas"]["ConversationGrantList"];

/** The browser keeps a bounded activity window; older history is paged on demand. */
export const EVENT_WINDOW = 500;
/** Mirrors the server's message preview bound so SSE cannot grow the page without limit. */
export const MAX_MESSAGE_PREVIEW_BYTES = 64 << 10;
const CONTENT_CHUNKS_PER_PAGE = 8;
const encoder = new TextEncoder();

const bytes = (text: string) => encoder.encode(text).length;

/** Cuts on a UTF-8 boundary, so a preview never ends with half a character. */
export function truncateUTF8(text: string, maxBytes: number) {
  const total = bytes(text);
  if (total <= maxBytes) return { text, bytes: total, truncated: false };
  let used = 0;
  let end = 0;
  for (const character of text) {
    const size = bytes(character);
    if (used + size > maxBytes) break;
    used += size;
    end += character.length;
  }
  return { text: text.slice(0, end), bytes: used, truncated: true };
}

/**
 * The timeline only ever holds a preview. Legacy messages can still arrive with
 * complete inline text, so bound them here as well as in the delta reducer.
 */
export function boundedMessage(message: Message): Message {
  const content = message.content ?? "";
  const preview = truncateUTF8(content, MAX_MESSAGE_PREVIEW_BYTES);
  const reported =
    typeof message.content_bytes === "number" && message.content_bytes >= 0
      ? message.content_bytes
      : 0;
  const contentBytes = Math.max(reported, bytes(content));
  return {
    ...message,
    content: preview.text,
    content_bytes: contentBytes,
    content_truncated: message.content_truncated === true || preview.truncated,
  };
}

const messageTerminalStatus = new Set([
  "completed",
  "interrupted",
  "canceled",
  "failed",
]);

const messageTerminal = (status: string) => messageTerminalStatus.has(status);

const messageBytes = (message: Message) =>
  Math.max(
    typeof message.content_bytes === "number" && message.content_bytes >= 0
      ? message.content_bytes
      : 0,
    bytes(message.content ?? ""),
  );

/**
 * Thread snapshots and replayed deltas can arrive after newer live state.
 * Keep the longer body and never turn a finished message back into one that is
 * still generating.
 */
export function mergeMessage(current: Message, incoming: Message): Message {
  const body =
    messageBytes(incoming) >= messageBytes(current) ? incoming : current;
  // A retry reuses the message with a new attempt id and may legitimately
  // return from a terminal failure to processing; stale snapshots keep the old id.
  const retried =
    !!incoming.attempt_id && incoming.attempt_id !== current.attempt_id;
  return {
    ...current,
    ...incoming,
    content: body.content ?? "",
    content_bytes: messageBytes(body),
    content_truncated: body.content_truncated === true,
    status:
      messageTerminal(current.status) &&
      !messageTerminal(incoming.status) &&
      !retried
        ? current.status
        : incoming.status,
  };
}

/** Extracts a bounded message projection from a persisted message event. */
export function eventMessage(event: Event): Message | undefined {
  if (event.type !== "message_delta") {
    if (event.type !== "message_started" && event.type !== "message_finished")
      return undefined;
    const data =
      event.data && typeof event.data === "object"
        ? (event.data as Partial<Message>)
        : undefined;
    return data && typeof data.id === "string"
      ? boundedMessage(data as Message)
      : undefined;
  }
  const data =
    event.data && typeof event.data === "object"
      ? (event.data as Record<string, unknown>)
      : {};
  if (data.mode !== "snapshot") return undefined;
  const snapshot =
    data.message && typeof data.message === "object"
      ? (data.message as Partial<Message>)
      : undefined;
  return snapshot && typeof snapshot.id === "string"
    ? boundedMessage(snapshot as Message)
    : undefined;
}

/**
 * Applies one message_delta to the loaded timeline. The backend is moving to
 * snapshot deltas; append remains accepted for older runs. A bounded preview is
 * all the client keeps, but byte metadata keeps counting after truncation.
 */
export function applyMessageDelta(
  messages: Message[],
  event: Event,
): Message[] {
  if (event.type !== "message_delta") return messages;
  const data =
    event.data && typeof event.data === "object"
      ? (event.data as Record<string, unknown>)
      : {};
  const id = typeof data.message_id === "string" ? data.message_id : "";
  const index = messages.findIndex((message) => message.id === id);
  if (index < 0) return messages;
  const current = messages[index];
  const snapshot = data.mode === "snapshot" ? data.message : undefined;
  let next: Message;
  if (snapshot && typeof snapshot === "object") {
    next = mergeMessage(
      current,
      boundedMessage({
        ...current,
        ...(snapshot as Partial<Message>),
      }),
    );
  } else if (typeof data.text === "string") {
    const appended = data.text;
    const currentBytes = current.content_bytes ?? bytes(current.content);
    // The server's cumulative byte count wins, but a replayed lower value
    // must never shrink the total.
    const total =
      typeof data.content_bytes === "number" && data.content_bytes >= 0
        ? Math.max(currentBytes, data.content_bytes)
        : currentBytes + bytes(appended);
    if (current.content_truncated) {
      // The preview is full; keep the byte total accurate without appending.
      next = {
        ...current,
        content_bytes: total,
        content_truncated: true,
      };
    } else {
      const preview = truncateUTF8(
        current.content + appended,
        MAX_MESSAGE_PREVIEW_BYTES,
      );
      next = {
        ...current,
        content: preview.text,
        content_bytes: Math.max(total, preview.bytes),
        content_truncated: data.content_truncated === true || preview.truncated,
      };
    }
  } else {
    return messages;
  }
  const copy = messages.slice();
  copy[index] = next;
  return copy;
}

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
export async function uploadSource(
  threadID: string,
  file: File,
): Promise<SourceImport> {
  const form = new FormData();
  form.append("file", file);
  let response: Response;
  try {
    response = await fetch(
      `/api/v1/threads/${encodeURIComponent(threadID)}/sources`,
      {
        method: "POST",
        credentials: "same-origin",
        headers: { "X-CSRF-Token": csrf },
        body: form,
      },
    );
  } catch {
    throw new APIError("无法连接本地服务；请确认服务正在运行。", 0);
  }
  let text: string;
  try {
    text = await response.text();
  } catch {
    throw new APIError("读取本地服务响应失败；请确认服务正在运行。", 0);
  }
  let result: { error?: { message?: string } } | undefined;
  if (text.trim()) {
    try {
      result = JSON.parse(text) as { error?: { message?: string } };
    } catch {
      result = undefined;
    }
  }
  if (!response.ok) {
    if (response.status === 401)
      window.dispatchEvent(new Event("cocogem:unauthorized"));
    throw new APIError(
      result?.error?.message ?? `资料上传失败（HTTP ${response.status}）`,
      response.status,
    );
  }
  if (result === undefined)
    throw new APIError("本地服务返回了空响应；请确认服务正在运行。", 0);
  return result as SourceImport;
}

/** True for a local-service conflict that the caller can resolve by re-reading. */
export const isConflict = (error: unknown) =>
  error instanceof APIError && error.status === 409;

export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  headers: Record<string, string> = {},
): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        ...(method !== "GET" ? { "X-CSRF-Token": csrf } : {}),
        ...headers,
      },
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
  } catch (error) {
    // A stopped local service or proxy failure rejects before any HTTP status.
    // Keep a caller-supplied message, but localize the browser's network text.
    const raw = error instanceof Error ? error.message : "";
    const unavailable =
      !raw ||
      /fetch failed|failed to fetch|networkerror|load failed/i.test(raw);
    throw new APIError(
      unavailable ? "无法连接本地服务；请确认服务正在运行。" : raw,
      0,
    );
  }
  // A proxy or an aborted upstream can answer with an empty or non-JSON body.
  // Parsing it blindly would surface a raw browser SyntaxError instead of a
  // retryable service error.
  let text: string;
  try {
    text = await response.text();
  } catch {
    throw new APIError("读取本地服务响应失败；请确认服务正在运行。", 0);
  }
  let result: { error?: { message?: string } } | undefined;
  if (text.trim()) {
    try {
      result = JSON.parse(text) as { error?: { message?: string } };
    } catch {
      result = undefined;
    }
  }
  if (!response.ok) {
    if (response.status === 401 && path.startsWith("/api/"))
      window.dispatchEvent(new Event("cocogem:unauthorized"));
    const fallback =
      response.status >= 500
        ? "本地服务暂时不可用；请确认服务正在运行。"
        : `HTTP ${response.status}`;
    throw new APIError(result?.error?.message ?? fallback, response.status);
  }
  if (result === undefined) {
    throw new APIError("本地服务返回了空响应；请确认服务正在运行。", 0);
  }
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
  uncertain: "结果待核实",
  blocked: "能力不足",
  configured: "已配置",
  not_configured: "未配置",
  verified: "已验证",
  error: "异常",
};

/**
 * Deduplicates by (run_id, seq), keeps per-run sequence order and caps the
 * window. Events of different runs never collide just because their seq matches.
 */
export function mergeEvents(
  current: Event[],
  incoming: Event,
  limit = EVENT_WINDOW,
): Event[] {
  if (
    current.some(
      (event) => event.run_id === incoming.run_id && event.seq === incoming.seq,
    )
  )
    return current;
  const next = [...current, incoming];
  const runOrder = new Map<string, number>();
  for (const event of next)
    if (!runOrder.has(event.run_id)) runOrder.set(event.run_id, runOrder.size);
  next.sort(
    (a, b) =>
      (runOrder.get(a.run_id) ?? 0) - (runOrder.get(b.run_id) ?? 0) ||
      a.seq - b.seq,
  );
  return next.length > limit ? next.slice(next.length - limit) : next;
}

/** Loads exactly one conversation page; earlier history is an explicit action. */
export async function loadThreadPage(
  id: string,
  cursor = "",
): Promise<ThreadView> {
  const query = new URLSearchParams({ limit: "100" });
  if (cursor) query.set("cursor", cursor);
  const view = await api<ThreadView>(
    `/api/v1/threads/${encodeURIComponent(id)}?${query}`,
  );
  return {
    ...view,
    messages: {
      ...view.messages,
      items: (view.messages.items ?? []).map(boundedMessage),
    },
  };
}

export function messageContent(
  runID: string,
  messageID: string,
  cursor = "",
): Promise<MessageContentPage> {
  const query = new URLSearchParams({ limit: String(CONTENT_CHUNKS_PER_PAGE) });
  if (cursor) query.set("cursor", cursor);
  return api<MessageContentPage>(
    `/api/v1/runs/${encodeURIComponent(runID)}/messages/${encodeURIComponent(messageID)}/content?${query}`,
  );
}

export function eventHistory(
  runID: string,
  after = "",
  limit = 100,
): Promise<EventPage> {
  const query = new URLSearchParams({ limit: String(limit) });
  if (after) query.set("after", after);
  return api<EventPage>(
    `/api/v1/runs/${encodeURIComponent(runID)}/event-history?${query}`,
  );
}

/** Pending-first, bounded permission history for one conversation. */
export async function threadPermissionRequests(
  threadID: string,
  limit = 100,
): Promise<ToolPermissionRequest[]> {
  const query = new URLSearchParams({ limit: String(limit) });
  const page = await api<PermissionRequestList>(
    `/api/v1/threads/${encodeURIComponent(threadID)}/permission-requests?${query}`,
  );
  return page.items ?? [];
}

/**
 * The three-way decision. The scope always comes from the original request; a
 * retried identical submission returns the stored decision, so callers can pass
 * the request id as the idempotency key.
 */
export async function decidePermissionRequest(
  request: ToolPermissionRequest,
  decision: "allow_once" | "allow_conversation" | "deny",
): Promise<ToolPermissionRequest> {
  return api<ToolPermissionRequest>(
    `/api/v1/permission-requests/${encodeURIComponent(request.id)}/decisions`,
    "POST",
    { decision, request_id: request.request_id },
    { "If-Match": String(request.revision) },
  );
}

export async function threadPermissionGrants(
  threadID: string,
): Promise<ConversationGrant[]> {
  const page = await api<ConversationGrantList>(
    `/api/v1/threads/${encodeURIComponent(threadID)}/permission-grants`,
  );
  return page.items ?? [];
}

export async function revokePermissionGrant(
  grantID: string,
): Promise<ConversationGrant> {
  return api<ConversationGrant>(
    `/api/v1/permission-grants/${encodeURIComponent(grantID)}/revoke`,
    "POST",
    {},
  );
}

/** Narrow delivery projection used by TaskDetail; never loads sources. */
export function taskDeliveries(taskID: string): Promise<TaskDeliveries> {
  return api<TaskDeliveries>(
    `/api/v1/tasks/${encodeURIComponent(taskID)}/deliveries`,
  );
}
