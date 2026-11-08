import { useState, type ReactNode } from "react";
import type { Agent } from "../api/client";
import {
  activityDecisionKey,
  missingFinishLabel,
  readActivityDecision,
  saveActivityDecision,
  toolStatusLabel,
  unresolvedPermissionActivities,
  type PermissionPrompt,
  type ToolActivity,
  type ToolActivityGroup,
  type ToolActivityStatus,
} from "../lib/toolActivity";
import styles from "../styles/member-tool-activity.module.css";
import { Icon } from "./Icon";

/**
 * Renders the approval UI for one blocked native/host call. The request and
 * decision API belong to the permission service; this component only needs a
 * stable request identity plus the already-normalized scope shown to the user.
 */
export type PermissionSlot = (prompt: PermissionPrompt) => ReactNode;

export function permissionPromptOf(
  group: ToolActivityGroup,
  activity: ToolActivity,
  memberName = "",
): PermissionPrompt {
  return {
    requestID: activity.permissionRequestID,
    activityKey: activity.key,
    toolCallID: activity.toolCallID,
    memberID: activity.memberID || group.memberID,
    memberName,
    toolName: activity.name,
    label: activity.label,
    target: activity.target,
    scope: activity.permissionScope,
    reason: activity.reason,
  };
}

export function memberDisplayName(
  memberID: string,
  agents: Agent[] = [],
): string {
  return agents.find((agent) => agent.id === memberID)?.display_name ?? "";
}

/**
 * Pending authorization has its own always-visible layer. It is rendered by the
 * conversation outside every run/process disclosure, so collapsing the log can
 * never hide a blocked operation. Actions come only from the explicit slot; the
 * decision API stays owned by the permission service.
 */
export function PendingToolAuthorization({
  groups,
  agents = [],
  permissionSlot,
}: {
  groups: ToolActivityGroup[];
  agents?: Agent[];
  permissionSlot?: PermissionSlot;
}) {
  const entries = groups.flatMap((group) =>
    unresolvedPermissionActivities(group.activities).map((activity) => ({
      group,
      activity,
    })),
  );
  if (!entries.length) return null;
  return (
    <section className={styles.pendingLayer} aria-label="待处理授权">
      {entries.map(({ group, activity }) => {
        const memberName = memberDisplayName(group.memberID, agents);
        const prompt = permissionPromptOf(group, activity, memberName);
        return (
          <div
            key={activity.key}
            className={styles.permission}
            role="status"
            data-permission-request-id={prompt.requestID || undefined}
          >
            <Icon name="info" size={14} />
            <div className={styles.permissionBody}>
              <span className={styles.permissionTitle}>
                等待授权 · {activity.label}
                {activity.target ? `（${activity.target}）` : ""}
              </span>
              <small className={styles.permissionDetail}>
                {[
                  memberName ? `成员 ${memberName}` : "",
                  prompt.scope,
                  prompt.reason,
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </small>
            </div>
            {permissionSlot ? (
              permissionSlot(prompt)
            ) : (
              <span className={styles.permissionHint}>批准后才会执行</span>
            )}
          </div>
        );
      })}
    </section>
  );
}

function StatusText({ status }: { status: ToolActivityStatus }) {
  return (
    <span className={styles[`status-${status}`]}>
      {toolStatusLabel(status)}
    </span>
  );
}

function ActivityItem({ activity }: { activity: ToolActivity }) {
  return (
    <>
      <div className={styles.itemHead}>
        <span className={styles.itemName}>
          {activity.label}
          {activity.target ? ` · ${activity.target}` : ""}
        </span>
        <StatusText status={activity.status} />
        {activity.durationMs !== undefined && (
          <span className={styles.duration}>{activity.durationMs} ms</span>
        )}
        {activity.exitCode !== undefined && (
          <span className={styles.exit}>exit {activity.exitCode}</span>
        )}
      </div>
      {activity.inputPreview && (
        <pre className={styles.input} tabIndex={0}>
          {activity.inputPreview}
        </pre>
      )}
      {activity.status === "unknown" && (
        <p className={styles.hint}>{missingFinishLabel(activity)}</p>
      )}
      {activity.output && (
        <>
          <pre className={styles.output} tabIndex={0}>
            {activity.output}
          </pre>
          {activity.outputTruncated && (
            <p className={styles.hint}>输出已截断。</p>
          )}
        </>
      )}
    </>
  );
}

/**
 * One member step's consecutive tool calls. Collapsed by default, and only the
 * reader toggles it: streaming events never rewrite the expand decision.
 * A blocked call's action UI lives in PendingToolAuthorization instead, so an
 * outer process disclosure can never hide it.
 */
export function ToolActivityBlock({
  group,
  onHistory,
}: {
  group: ToolActivityGroup;
  onHistory?: (activity: ToolActivity) => void;
}) {
  const decision = activityDecisionKey(group);
  const [open, setOpen] = useState(
    () => readActivityDecision(decision) ?? false,
  );
  const listID = `tool-activity-${group.key.replace(/[^a-zA-Z0-9_-]+/g, "-")}`;
  const toggle = () => {
    const next = !open;
    setOpen(next);
    saveActivityDecision(decision, next);
  };
  const duration = group.activities.reduce(
    (sum, activity) => sum + (activity.durationMs ?? 0),
    0,
  );
  const running = group.activities.some(
    (activity) =>
      activity.status === "running" || activity.status === "queued",
  );
  const unknown = group.activities.find(
    (activity) => activity.status === "unknown",
  );
  return (
    <div
      className={styles.activity}
      data-testid="tool-activity"
      data-step-id={group.stepID || undefined}
      data-running={running ? "true" : undefined}
    >
      <div className={styles.head}>
        <button
          type="button"
          className={styles.toggle}
          aria-expanded={open}
          aria-controls={open ? listID : undefined}
          onClick={toggle}
        >
          <span className={styles.chevron} aria-hidden="true">
            <Icon name="chevron" size={12} />
          </span>
          <span className={styles.summary}>{group.summary}</span>
        </button>
        {duration > 0 && (
          <span className={styles.duration}>{duration} ms</span>
        )}
        {unknown && onHistory && (
          <button
            type="button"
            className={styles.history}
            onClick={() => onHistory(unknown)}
          >
            查看历史
          </button>
        )}
      </div>
      {open && (
        <ol className={styles.list} id={listID}>
          {group.activities.map((activity) => (
            <li key={activity.key} className={styles.item}>
              <ActivityItem activity={activity} />
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

/**
 * A per-member activity list. Used for steps that have events but no visible
 * reply yet, so parallel members keep their own block instead of one global
 * "latest tool" line.
 */
export function MemberToolActivity({
  groups,
  agents = [],
  onHistory,
  showMember = true,
}: {
  groups: ToolActivityGroup[];
  agents?: Agent[];
  onHistory?: (activity: ToolActivity) => void;
  showMember?: boolean;
}) {
  if (!groups.length) return null;
  return (
    <div className={styles.timeline} aria-label="工具活动">
      {groups.map((group) => {
        const name = memberDisplayName(group.memberID, agents);
        return (
          <section
            key={group.key}
            className={styles.group}
            aria-label={name ? `${name}的工具活动` : "工具活动"}
          >
            {showMember && name && (
              <small className={styles.member}>{name}</small>
            )}
            <ToolActivityBlock group={group} onHistory={onHistory} />
          </section>
        );
      })}
    </div>
  );
}
