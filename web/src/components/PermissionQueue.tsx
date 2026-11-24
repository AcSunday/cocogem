import type { ToolPermissionRequest } from "../api/client";
import { isEnglish, t, useLanguage } from "../lib/i18n";
import type { PermissionPrompt } from "../lib/toolActivity";
import {
  PermissionDecision,
  type PermissionDecisionKind,
  type PermissionGrantScope,
} from "./PermissionDecision";
import styles from "../styles/permission-decision.module.css";

const toolLabelsZh: Record<string, string> = {
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

const toolLabelsEn: Record<string, string> = {
  read_file: "Read file",
  write_file: "Write file",
  patch_file: "Modify file",
  list_files: "List directory",
  search_content: "Search content",
  run_command: "Run command",
  run_test: "Run tests",
  fetch_url: "Fetch web page",
  web_search: "Web search",
  request_tool_access: "Request tool access",
  external_publish: "External publish",
};

/** A stable short label; the exact operation comes from `operation_summary`. */
function operationLabel(toolName: string) {
  const dict = isEnglish() ? toolLabelsEn : toolLabelsZh;
  return dict[toolName] ?? toolName;
}

export function permissionPrompt(request: ToolPermissionRequest): PermissionPrompt {
  return {
    requestID: request.id,
    activityKey: request.tool_call_id,
    toolCallID: request.tool_call_id,
    memberID: request.member_id,
    memberName: request.member_name ?? "",
    toolName: request.tool_name,
    label: operationLabel(request.tool_name),
    target: request.operation_summary?.trim() ?? "",
    scope: request.normalized_resource_scope,
    reason: request.reason,
  };
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
    grantScope?: PermissionGrantScope,
  ) => Promise<void>;
  onDismiss: () => void;
  onRestore: () => void;
}) {
  useLanguage();
  // The active card is ONLY the explicit `visible` request. When every pending
  // request has been closed (view-only), the queue collapses to a reopen bar and
  // no request silently becomes active again.
  const active = visible;
  if (!active) {
    const hidden = items.length;
    if (!hidden) return null;
    return (
      <div className={styles.queueBar}>
        <span>
          {t(
            `${hidden} 个操作等待授权，已折叠但未决定。`,
            `${hidden} action(s) awaiting approval (collapsed, not yet decided).`,
          )}
        </span>
        <button type="button" className={styles.link} onClick={onRestore}>
          {t("重新打开", "Reopen")}
        </button>
      </div>
    );
  }
  // The queue only has the persisted request, so it renders the host's own
  // bounded `operation_summary` (command/path + effect) instead of inventing a
  // label. `target` is that summary when the server provided one; otherwise it
  // stays empty rather than duplicating the tool name as fake detail.
  const position = items.findIndex((item) => item.id === active.id) + 1;
  return (
    <section
      className={styles.queue}
      aria-label={t("待处理授权", "Pending approvals")}
    >
      <div className={styles.queueHead}>
        <span>
          {t("等待授权", "Awaiting approval")}
          {items.length > 1
            ? t(
                `（${position}/${items.length}）`,
                ` (${position}/${items.length})`,
              )
            : ""}
        </span>
        <button
          type="button"
          className={styles.quiet}
          aria-label={t("稍后处理", "Decide later")}
          title={t(
            "关闭不会拒绝或允许；请求保持待处理",
            "Closing does not approve or deny; the request remains pending",
          )}
          onClick={onDismiss}
        >
          {t("稍后处理", "Decide later")}
        </button>
      </div>
      <PermissionDecision
        key={active.id}
        prompt={permissionPrompt(active)}
        request={active}
        onDecide={(decision, grantScope) =>
          grantScope
            ? onDecide(active.id, decision, grantScope)
            : onDecide(active.id, decision)
        }
      />
      {items.length > 1 && (
        <p className={styles.muted}>
          {t(
            `还有 ${items.length - 1} 个待处理请求，按申请顺序逐个决定。`,
            `${items.length - 1} more pending request(s) will be shown in order.`,
          )}
        </p>
      )}
    </section>
  );
}
