import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  isConflict,
  terminal,
  threadPermissionGrants,
  threadPermissionRequests,
  type ConversationGrant,
  type Run,
  type Settings,
  type ToolPermissionRequest,
} from "../api/client";
import type { PermissionPrompt } from "../lib/toolActivity";
import { errorText } from "../lib/presentation";
import styles from "../styles/permission-decision.module.css";

export type PermissionDecisionKind =
  "allow_once" | "allow_conversation" | "deny";

const activityLabels: Record<string, string> = {
  read: "读取",
  write: "写入",
  run_test: "运行测试",
  network: "联网",
  publish_artifact: "发布产物",
  publish: "发布",
  deploy: "部署",
  install: "安装软件",
  native_read: "原生读取",
  native_write: "原生写入",
  native_execute: "原生执行",
};

/** The request identity the pending card can resolve. */
export type PendingPermission = {
  /** `ToolPermissionRequest.id` — the decision path segment. */
  requestID: string;
  prompt?: PermissionPrompt;
  request?: ToolPermissionRequest;
};

const decisionText: Record<PermissionDecisionKind, string> = {
  allow_once: "已允许本次操作",
  allow_conversation: "已允许本会话中同动作、同范围的操作",
  deny: "已拒绝该操作",
};

/** The text for a persisted status, or "" while it is still pending. */
function decisionTextOf(status: string): string {
  switch (status) {
    case "allowed_once":
      return decisionText.allow_once;
    case "allowed_conversation":
      return decisionText.allow_conversation;
    case "denied":
      return decisionText.deny;
    case "expired":
      return "该授权请求已失效，需要重新申请";
    case "canceled":
      return "该操作已取消，不再等待授权";
    default:
      return "";
  }
}

type SharedDecision = {
  busy: PermissionDecisionKind | "";
  decided: PermissionDecisionKind | "";
  /** Server/prop revision seen when the local decision settled. */
  settledRevision: number;
};

/**
 * Request-wide decision state. Every request id has ONE record, so two rendered
 * instances of the same request share the busy state and settle together.
 */
const decisionStates = new Map<string, SharedDecision>();
const decisionListeners = new Set<() => void>();
/** In-flight decisions keyed by request id (one at most per request). */
const decisionPromises = new Map<string, Promise<void>>();

function emitDecision(requestID: string, next: SharedDecision) {
  decisionStates.set(requestID, next);
  for (const listener of decisionListeners) listener();
}

/**
 * A same-decision request while a POST is in flight joins that promise instead
 * of sending a second one; a different decision waits for it and then no-ops
 * because the request is already settled.
 */
export function decideShared(
  requestID: string,
  decision: PermissionDecisionKind,
  submit: () => Promise<void>,
): Promise<void> {
  const running = decisionPromises.get(requestID);
  if (running) return running;
  const current = decisionStates.get(requestID);
  if (current?.decided || current?.busy) return Promise.resolve();
  emitDecision(requestID, { busy: decision, decided: "", settledRevision: -1 });
  const run = Promise.resolve()
    .then(submit)
    .then(() => {
      emitDecision(requestID, {
        busy: "",
        decided: decision,
        settledRevision: revisionOf(requestID),
      });
    })
    .catch((failure) => {
      // A failed decision becomes clickable again; the real error is surfaced
      // by the instance that owns the promise.
      emitDecision(requestID, { busy: "", decided: "", settledRevision: -1 });
      throw failure;
    })
    .finally(() => {
      decisionPromises.delete(requestID);
    });
  decisionPromises.set(requestID, run);
  return run;
}

/**
 * The highest revision each request has been rendered with. A local decision is
 * only "settled" while the caller still shows the same request revision as
 * pending; once a newer server status lands, the persisted value wins over any
 * stale local flag.
 */
const observedRevisions = new Map<string, number>();
function revisionOf(requestID: string) {
  return observedRevisions.get(requestID) ?? 0;
}

/** The shared record as seen for one request revision, without mutation. */
function decisionFor(
  requestID: string,
  status: string,
  revision: number,
): SharedDecision {
  const record = decisionStates.get(requestID);
  if (!record) return { busy: "", decided: "", settledRevision: -1 };
  // A pending prop at the same revision as when the local decision settled
  // knows nothing new; anything newer (or a settled status) is authoritative.
  if (status === "pending" && record.decided && record.settledRevision >= revision)
    return record;
  if (status !== "pending")
    return { busy: record.busy, decided: "", settledRevision: -1 };
  return record;
}

/** Re-renders on any decision change; returns this request's shared record. */
function usePermissionDecisionState(
  requestID: string,
  status: string,
  revision: number,
): SharedDecision {
  const [, force] = useState(0);
  useEffect(() => {
    const listener = () => force((value) => value + 1);
    decisionListeners.add(listener);
    return () => {
      decisionListeners.delete(listener);
    };
  }, []);
  // Record the revision this instance is displaying for the settle marker.
  if ((observedRevisions.get(requestID) ?? -1) < revision)
    observedRevisions.set(requestID, revision);
  return decisionFor(requestID, status, revision);
}

/** Test seam: forget every shared record and in-flight promise. */
export function resetDecisionState() {
  decisionStates.clear();
  decisionPromises.clear();
  observedRevisions.clear();
}

/**
 * A pending tool authorization. The three actions are fixed; the scope always
 * comes from the original request and is never editable here. Deciding only
 * advances this blocked call: it never submits the Composer draft and never
 * restarts the root task.
 */
export function PermissionDecision({
  prompt,
  request,
  onDecide,
  compact,
}: {
  prompt: PermissionPrompt;
  request?: ToolPermissionRequest;
  onDecide: (decision: PermissionDecisionKind) => Promise<void> | void;
  compact?: boolean;
}) {
  // The same request can be rendered twice at once (the member's inline activity
  // and the Composer queue). Busy/decided state is therefore shared per request
  // id, not per component instance, and the persisted status is authoritative:
  // a decided or expired request never shows clickable actions again.
  const persisted = request?.status ?? "";
  const shared = usePermissionDecisionState(
    prompt.requestID,
    persisted,
    request?.revision ?? 0,
  );
  const [error, setError] = useState("");
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const settled = decisionTextOf(persisted) || (shared.decided ? decisionText[shared.decided] : "");
  const busy = shared.busy;
  const readonly = !!settled;
  const decide = async (kind: PermissionDecisionKind) => {
    if (readonly || busy) return;
    setError("");
    // `decideShared` deduplicates across instances and across repeat clicks:
    // a same-decision request shares the in-flight promise, a different one is
    // blocked while it settles.
    try {
      await decideShared(prompt.requestID, kind, async () => {
        await onDecide(kind);
      });
    } catch (failure) {
      if (mounted.current) setError(errorText(failure));
    }
  };
  const member = prompt.memberName || request?.member_name || "";
  const action = request?.action ?? "";
  // The live activity event and the persisted request are two views of the same
  // call; prefer whichever knows the operation details so an approval card never
  // shows "未提供范围" for a request that does state its scope.
  const scope = prompt.scope || request?.normalized_resource_scope || "";
  const rawReason = prompt.reason || request?.reason || "";
  const operation = request?.operation_summary?.trim() ?? "";
  // The host's reason usually restates the same operation and path ("操作 X
  // 需要授权：<scope>"). Show it only when it carries information the operation
  // and scope rows do not already show, so the card never says the same thing
  // twice.
  const duplicatesOperation =
    !!rawReason &&
    (rawReason === operation ||
      (!!scope && rawReason.includes(scope)) ||
      (!!operation && rawReason.includes(operation)));
  const reason = duplicatesOperation ? "" : rawReason;
  const details = [
    member ? `成员 ${member}` : "",
    action ? (activityLabels[action] ?? action) : "",
    scope,
    reason && reason !== operation ? reason : "",
  ].filter(Boolean);
  return (
    <div
      className={compact ? styles.decisionCompact : styles.decision}
      role="group"
      aria-label={`待决定授权：${prompt.label}`}
      data-permission-decision={prompt.requestID}
    >
      <div className={styles.decisionHead}>
        <strong>{prompt.label}</strong>
        {/* Show the activity's own target only when the persisted request has no
            `operation_summary`; otherwise the exact operation would appear twice
            (once here, once in the details list below). */}
        {prompt.target && !operation && (
          <span className={styles.target}>{prompt.target}</span>
        )}
      </div>
      {!!details.length && (
        <dl className={styles.details}>
          {member && (
            <div>
              <dt>成员</dt>
              <dd>{member}</dd>
            </div>
          )}
          {action && (
            <div>
              <dt>动作</dt>
              <dd>{activityLabels[action] ?? action}</dd>
            </div>
          )}
          {operation && (
            <div>
              <dt>操作</dt>
              {/* The host's own bounded, secret-free description. */}
              <dd>{operation}</dd>
            </div>
          )}
          <div>
            <dt>范围</dt>
            <dd>{scope || "服务器未提供范围"}</dd>
          </div>
          {reason && (
            <div>
              <dt>原因</dt>
              <dd>{reason}</dd>
            </div>
          )}
        </dl>
      )}
      {readonly ? (
        <p className={styles.decided} role="status">
          {settled}
        </p>
      ) : (
        <div className={styles.actions}>
          <button
            type="button"
            className={styles.primary}
            disabled={!!busy}
            onClick={() => void decide("allow_once")}
          >
            {busy === "allow_once" ? "正在允许…" : "仅本次允许"}
          </button>
          <button
            type="button"
            className={styles.secondary}
            disabled={!!busy}
            onClick={() => void decide("allow_conversation")}
            title="本会话内所有成员可对同一工具与范围使用此授权"
          >
            {busy === "allow_conversation" ? "正在允许…" : "本会话持续允许"}
          </button>
          <button
            type="button"
            className={styles.danger}
            disabled={!!busy}
            onClick={() => void decide("deny")}
          >
            {busy === "deny" ? "正在拒绝…" : "拒绝"}
          </button>
        </div>
      )}
      <p className={styles.note}>
        本会话持续允许对同会话所有成员共享，但只限本次申请的工具、动作与范围。
      </p>
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

/**
 * Reads the thread's permission requests, keeps the oldest still-pending one
 * as the visible card (a later request never replaces it) and exposes the
 * ordered queue. Closing the card is not a decision: it stays pending.
 */
export function usePermissionRequests(threadID: string, activeRun?: Run) {
  const [items, setItems] = useState<ToolPermissionRequest[]>([]);
  const [error, setError] = useState("");
  const [dismissed, setDismissed] = useState<Set<string>>(() => new Set());
  const generation = useRef(0);
  // The thread that produced the current `items`. A response for another thread
  // (or for no thread) never reaches the queue, even if it resolves later.
  const loadedThread = useRef(threadID);
  const request = useCallback(async () => {
    const target = threadID;
    if (!target) {
      generation.current++;
      setItems([]);
      return;
    }
    const mine = ++generation.current;
    try {
      const next = await threadPermissionRequests(target);
      if (mine !== generation.current || loadedThread.current !== target) return;
      setItems(next);
      setError("");
    } catch (failure) {
      if (mine === generation.current) setError(errorText(failure));
    }
  }, [threadID]);
  // A thread change synchronously invalidates any in-flight read and clears the
  // old queue before the new one loads, so the previous conversation's pending
  // requests are never shown or decided in the new one.
  useEffect(() => {
    // Invalidate before clearing, then let this effect's own read take the next
    // generation number.
    generation.current++;
    loadedThread.current = threadID;
    setItems([]);
    setDismissed(new Set());
    setError("");
    const mine = ++generation.current;
    const target = threadID;
    if (!target) return;
    void threadPermissionRequests(target)
      .then((next) => {
        if (mine !== generation.current || loadedThread.current !== target)
          return;
        setItems(next);
        setError("");
      })
      .catch((failure) => {
        if (mine === generation.current) setError(errorText(failure));
      });
  }, [threadID]);
  // An in-flight run can create a request at any moment; a bounded poll keeps
  // the card reachable without holding a long-lived connection.
  useEffect(() => {
    if (!threadID) return;
    const running = !activeRun || !terminal(activeRun.status);
    const timer = window.setInterval(
      () => {
        if (document.visibilityState === "visible") void request();
      },
      running ? 1500 : 5000,
    );
    const onVisibility = () => {
      if (document.visibilityState === "visible") void request();
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [threadID, activeRun?.status, request]);
  const pending = useMemo(
    () => items.filter((item) => item.status === "pending"),
    [items],
  );
  // The OLDEST pending request owns the card ("当前请求不得被后到请求覆盖").
  // Closing it is a view-only dismissal that leaves it pending: later requests
  // never slide into its slot, so dismissing one of several collapses the whole
  // queue to the reopen bar instead of silently promoting request #2.
  const oldest = pending[0];
  const visible = oldest && !dismissed.has(oldest.id) ? oldest : undefined;
  return {
    items,
    pending,
    visible,
    error,
    request,
    dismiss: (id: string) =>
      setDismissed((current) => {
        const next = new Set(current);
        next.add(id);
        return next;
      }),
    restore: () =>
      setDismissed((current) => {
        if (!current.size) return current;
        return new Set();
      }),
  };
}

/**
 * The decision idempotency key. `ToolPermissionRequest.request_id` is the key
 * the server stored at creation time and it is **empty** for host/native
 * requests (`internal/workflow/permission.go` and `native_permission.go` create
 * them without a RequestID). The decision body requires 8–100 characters and the
 * server overwrites the stored `request_id` with it, so the key must be minted
 * here per (permission request, chosen decision) and reused on a transport retry.
 *
 * Keys stay for the component's lifetime; a different decision is a different
 * key, so a conflicting choice can never be replayed as the earlier one.
 */
export function decisionKey(requestID: string, decision: PermissionDecisionKind) {
  return "decision:" + requestID + ":" + decision;
}

const inFlightDecisions = new Map<string, string>();

/**
 * Applies a decision through the HTTP contract. The scope always comes from the
 * original request; only the decision and our own idempotency key travel.
 *
 * Semantics (see `internal/project/permissions.go:decideInTx`):
 * - identical replay (same key, same decision) returns the stored decision;
 * - a different choice after a decision is rejected as a conflict;
 * - a stale revision or an earlier permission epoch is a 409 the caller must
 *   resolve by re-reading, never a silent success.
 */
export async function submitPermissionDecision(
  request: ToolPermissionRequest,
  decision: PermissionDecisionKind,
): Promise<ToolPermissionRequest> {
  const key = decisionKey(request.id, decision);
  // One deterministic key per request+decision. It is retained across an
  // uncertain network failure so a retry is the same idempotent decision and a
  // duplicated POST can never authorize a second operation.
  const requestID = inFlightDecisions.get(key) ?? key;
  inFlightDecisions.set(key, requestID);
  try {
    const saved = await api<ToolPermissionRequest>(
      `/api/v1/permission-requests/${encodeURIComponent(request.id)}/decisions`,
      "POST",
      { decision, request_id: requestID },
      { "If-Match": String(request.revision) },
    );
    inFlightDecisions.delete(key);
    return saved;
  } catch (failure) {
    if (isConflict(failure)) {
      // A stale revision, an earlier epoch or a conflicting prior choice: the
      // request must be re-evaluated instead of silently accepted.
      inFlightDecisions.delete(key);
      throw new Error(
        "该授权请求已过期、已决定或与先前的选择冲突；请刷新待处理授权后再决定。",
        { cause: failure },
      );
    }
    throw failure;
  }
}

/** Test seam: collapse the retained keys after a suite. */
export function resetDecisionKeys() {
  inFlightDecisions.clear();
}

/**
 * Settings → 权限: the persistent conversation grants of one thread, each with
 * an idempotent revoke. This is a compact list on the permission page, not a
 * workspace or member permission editor.
 */
export function ConversationGrants({
  threadID,
  settings,
  threadTitle,
}: {
  threadID: string;
  settings?: Settings;
  threadTitle?: string;
}) {
  const [grants, setGrants] = useState<ConversationGrant[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  // Every thread change invalidates any read still in flight: a slow response
  // for conversation A must never replace the list shown under conversation B
  // (its rows could then be revoked by accident).
  const generation = useRef(0);
  const loadedThread = useRef(threadID);
  const load = useCallback(async () => {
    const target = threadID;
    const mine = ++generation.current;
    if (!target) {
      setGrants([]);
      setLoading(false);
      setError("");
      return;
    }
    setLoading(true);
    try {
      const next = await threadPermissionGrants(target);
      if (mine !== generation.current || loadedThread.current !== target) return;
      setGrants(next);
      setError("");
    } catch (failure) {
      if (mine !== generation.current) return;
      setError(errorText(failure));
    } finally {
      if (mine === generation.current) setLoading(false);
    }
  }, [threadID]);
  useEffect(() => {
    // Clear synchronously before reading the new thread so the old rows are
    // never visible (or revocable) under the new title.
    generation.current++;
    loadedThread.current = threadID;
    setGrants([]);
    setBusy("");
    setError("");
    void load();
  }, [threadID, load]);
  const revoke = async (grant: ConversationGrant) => {
    if (busy) return;
    // Pin the revoke to the thread whose list is displayed. A grant row that
    // arrived from another thread's late response is rejected instead of
    // revoking a grant of the wrong conversation.
    if (grant.thread_id !== threadID) {
      setError("授权记录与当前会话不一致，请刷新后重试。");
      return;
    }
    setBusy(grant.id);
    setError("");
    const owner = threadID;
    try {
      const updated = await api<ConversationGrant>(
        `/api/v1/permission-grants/${encodeURIComponent(grant.id)}/revoke`,
        "POST",
        {},
      );
      // A response for a thread we already left must not patch the new list.
      if (loadedThread.current !== owner) return;
      setGrants((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
    } catch (failure) {
      if (loadedThread.current === owner) setError(errorText(failure));
    } finally {
      if (loadedThread.current === owner) setBusy("");
    }
  };
  if (!threadID) {
    return (
      <p className={styles.muted}>
        打开一个会话后，可在此查看并撤销该会话的持续授权。
      </p>
    );
  }
  return (
    <div className={styles.grants}>
      <div className={styles.grantsHead}>
        <span>{threadTitle ? `会话「${threadTitle}」` : "当前会话"}</span>
        <button
          type="button"
          className={styles.link}
          disabled={loading}
          onClick={() => void load()}
        >
          {loading ? "正在读取…" : "刷新"}
        </button>
      </div>
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
      {!loading && !grants.length && (
        <p className={styles.muted}>该会话没有持续授权。</p>
      )}
      {!!grants.length && (
        <ul className={styles.grantList} aria-label="会话持续授权">
          {grants.map((grant) => {
            const current =
              !settings ||
              settings.permission_epoch === undefined ||
              grant.permission_epoch === settings.permission_epoch;
            return (
              <li key={grant.id} className={styles.grantRow}>
                <div className={styles.grantMain}>
                  <strong>{grant.tool_matcher || grant.action}</strong>
                  <small>
                    {grant.normalized_resource_scope || "未提供范围"} ·{" "}
                    {grant.status === "revoked"
                      ? "已撤销"
                      : current
                        ? "有效"
                        : "已随权限切换失效"}
                  </small>
                </div>
                {grant.status !== "revoked" && (
                  <button
                    type="button"
                    className={styles.danger}
                    disabled={!!busy}
                    onClick={() => void revoke(grant)}
                  >
                    {busy === grant.id ? "正在撤销…" : "撤销"}
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
