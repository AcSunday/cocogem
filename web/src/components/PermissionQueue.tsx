import type { ToolPermissionRequest } from "../api/client";
import type { PermissionPrompt } from "../lib/toolActivity";
import { PermissionDecision, type PermissionDecisionKind } from "./PermissionDecision";
import styles from "../styles/permission-decision.module.css";

const toolLabels: Record<string, string> = {
  read_file: "读取文件",
  write_file: "写入文件",
  patch_file: "修改文件",
  list_files: "列出目录",
  search_content: "搜索内容",
  run_command: "执行命令",
  run_test: "运行测试",
  fetch_url: "读取网页",
  web_search: "联网搜索",
  request_tool_access: "申请工具权限",
  external_publish: "外部发布",
};

/** A stable short label; the exact operation comes from `operation_summary`. */
function operationLabel(toolName: string) {
  return toolLabels[toolName] ?? toolName;
}

/**
 * The Composer-facing pending queue. It sits directly above the Composer and
 * outside every run/process disclosure, so a blocked call stays visible even
 * after collapsing the log. A later request never replaces the current one:
 * the oldest still-pending request is the card, and the rest are a count that
 * can be moved through in order.
 *
 * Closing the notice is not a decision. It only hides the card from this spot;
 * the request stays pending and can be reopened from the same notice.
 */
export function PermissionQueue({
  items,
  visible,
  onDecide,
  onDismiss,
  onRestore,
}: {
  items: ToolPermissionRequest[];
  visible?: ToolPermissionRequest;
  onDecide: (
    requestID: string,
    decision: PermissionDecisionKind,
  ) => Promise<void>;
  onDismiss: () => void;
  onRestore: () => void;
}) {
  // The active card is ONLY the explicit `visible` request. When every pending
  // request has been closed (view-only), the queue collapses to a reopen bar and
  // no request silently becomes active again.
  const active = visible;
  if (!active) {
    const hidden = items.length;
    if (!hidden) return null;
    return (
      <div className={styles.queueBar}>
        <span>{hidden} 个操作等待授权，已折叠但未决定。</span>
        <button type="button" className={styles.link} onClick={onRestore}>
          重新打开
        </button>
      </div>
    );
  }
  // The queue only has the persisted request, so it renders the host's own
  // bounded `operation_summary` (command/path + effect) instead of inventing a
  // label. `target` is that summary when the server provided one; otherwise it
  // stays empty rather than duplicating the tool name as fake detail.
  const summary = active.operation_summary?.trim() ?? "";
  const prompt: PermissionPrompt = {
    requestID: active.id,
    activityKey: active.tool_call_id,
    toolCallID: active.tool_call_id,
    memberID: active.member_id,
    memberName: active.member_name ?? "",
    toolName: active.tool_name,
    label: operationLabel(active.tool_name),
    target: summary,
    scope: active.normalized_resource_scope,
    reason: active.reason,
  };
  const position = items.findIndex((item) => item.id === active.id) + 1;
  return (
    <section className={styles.queue} aria-label="待处理授权">
      <div className={styles.queueHead}>
        <span>
          等待授权
          {items.length > 1 ? `（${position}/${items.length}）` : ""}
        </span>
        <button
          type="button"
          className={styles.quiet}
          aria-label="稍后处理"
          title="关闭不会拒绝或允许；请求保持待处理"
          onClick={onDismiss}
        >
          稍后处理
        </button>
      </div>
      <PermissionDecision
        key={active.id}
        prompt={prompt}
        request={active}
        onDecide={(decision) => onDecide(active.id, decision)}
      />
      {items.length > 1 && (
        <p className={styles.muted}>
          还有 {items.length - 1} 个待处理请求，按申请顺序逐个决定。
        </p>
      )}
    </section>
  );
}
