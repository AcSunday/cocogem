import { useState, type ReactNode } from "react";
import { sourceOriginalImageURL, type Agent, type Source } from "../api/client";
import {
  isImageMediaOrPath,
  parseFileRef,
  type FileRef,
  type ImagePreview,
} from "../lib/fileRefs";
import { t, useLanguage } from "../lib/i18n";
import {
  activityDecisionKey,
  missingFinishLabel,
  readActivityDecision,
  saveActivityDecision,
  toolInputPreview,
  toolStatusLabel,
  unresolvedPermissionActivities,
  type PermissionPrompt,
  type ProcessStep,
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
  useLanguage();
  const entries = groups.flatMap((group) =>
    unresolvedPermissionActivities(group.activities).map((activity) => ({
      group,
      activity,
    })),
  );
  if (!entries.length) return null;
  return (
    <section
      className={styles.pendingLayer}
      aria-label={t("待处理授权", "Pending approvals")}
    >
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
                {t("等待授权", "Awaiting approval")} · {activity.label}
                {activity.target
                  ? t(`（${activity.target}）`, ` (${activity.target})`)
                  : ""}
              </span>
              <small className={styles.permissionDetail}>
                {[
                  memberName
                    ? t(`成员 ${memberName}`, `Member ${memberName}`)
                    : "",
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
              <span className={styles.permissionHint}>
                {t("批准后才会执行", "Runs only after approval")}
              </span>
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

function formattedInput(activity: ToolActivity): string {
  if (activity.inputPreview) return activity.inputPreview;
  if (activity.input === undefined || activity.input === null) return "";
  if (typeof activity.input === "string") return activity.input.trim();
  try {
    const raw = JSON.stringify(activity.input, null, 2);
    if (!raw || raw === "{}" || raw === "null") return "";
    return raw.length > 2000 ? `${raw.slice(0, 2000)}…` : raw;
  } catch {
    return toolInputPreview(activity.input);
  }
}

const EDIT_OR_DIFF_TOOLS = new Set([
  "write_file",
  "patch_file",
  "edit_file",
  "Write",
  "Edit",
  "MultiEdit",
  "fileChange",
  "git_inspect",
]);

function toolActivityFileRef(activity: ToolActivity): FileRef | null {
  const input =
    activity.input && typeof activity.input === "object"
      ? (activity.input as Record<string, unknown>)
      : undefined;
  const rawPath =
    (typeof input?.path === "string" && input.path) ||
    (typeof input?.file_path === "string" && input.file_path) ||
    (typeof input?.target_file === "string" && input.target_file) ||
    activity.target;
  if (!rawPath) return null;
  const parsed = parseFileRef(rawPath);
  if (!parsed) return null;
  const startLine =
    typeof input?.start_line === "number" && input.start_line > 0
      ? input.start_line
      : parsed.startLine;
  const endLine =
    typeof input?.end_line === "number" && input.end_line > 0
      ? input.end_line
      : parsed.endLine;
  return {
    path: parsed.path,
    ...(startLine ? { startLine } : {}),
    ...(endLine ? { endLine } : {}),
    tab: EDIT_OR_DIFF_TOOLS.has(activity.name) ? "diff" : "file",
  };
}

function toolActivityImagePreview(
  activity: ToolActivity,
  sources?: Source[],
): ImagePreview | null {
  if (
    activity.name !== "preview_page" &&
    activity.name !== "view_image" &&
    activity.name !== "publish_file" &&
    !isImageMediaOrPath(activity.target)
  ) {
    return null;
  }
  if (sources?.length) {
    const matched = [...sources]
      .reverse()
      .find((src) => {
        if (!isImageMediaOrPath(src.media_type || src.path || src.title, src.kind)) {
          return false;
        }
        if (src.capture_step_id && src.capture_step_id === activity.stepID) {
          return true;
        }
        return Boolean(
          activity.target &&
            (src.path === activity.target ||
              src.title === activity.target ||
              src.url === activity.target),
        );
      });
    if (matched) {
      const url = sourceOriginalImageURL(matched.id);
      return {
        src: url,
        title: matched.title || matched.path || activity.label,
        downloadUrl: url,
        sourceId: matched.id,
        path: matched.path,
      };
    }
  }
  return null;
}

/**
 * Individual tool call row inside the chronological execution trace. Collapsed
 * by default so active thinking and step progression stay scannable; clicking
 * reveals full arguments and tool output.
 */
function ToolStepItem({
  activity,
  sources,
  onOpenFile,
  onOpenImage,
}: {
  activity: ToolActivity;
  sources?: Source[];
  onOpenFile?: (ref: FileRef) => void;
  onOpenImage?: (preview: ImagePreview) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const inputText = expanded ? formattedInput(activity) : "";
  const fileRef = onOpenFile ? toolActivityFileRef(activity) : null;
  const imagePreview = onOpenImage
    ? toolActivityImagePreview(activity, sources)
    : null;
  const hasDetails =
    Boolean(inputText) ||
    Boolean(activity.output) ||
    activity.status === "unknown";
  return (
    <div className={styles.toolStep} data-tool-status={activity.status}>
      <div className={styles.toolRow}>
        <button
          type="button"
          className={styles.toolToggle}
          aria-expanded={expanded}
          onClick={() => setExpanded((current) => !current)}
        >
          <span className={styles.toolChevron} aria-hidden="true">
            <Icon name="chevron" size={11} />
          </span>
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
        </button>
        {imagePreview && onOpenImage && (
          <button
            type="button"
            className={styles.toolAction}
            onClick={() => onOpenImage(imagePreview)}
          >
            {t("查看截图", "Image")}
          </button>
        )}
        {fileRef && onOpenFile && (
          <button
            type="button"
            className={styles.toolAction}
            onClick={() => onOpenFile(fileRef)}
          >
            {fileRef.tab === "diff"
              ? t("Diff", "Diff")
              : t("查看文件", "Open")}
          </button>
        )}
      </div>
      {expanded && (
        <div className={styles.toolDetail}>
          {inputText && (
            <pre className={styles.input} tabIndex={0}>
              {inputText}
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
                <p className={styles.hint}>
                  {t("输出已截断。", "Output truncated.")}
                </p>
              )}
            </>
          )}
          {!hasDetails && (
            <p className={styles.hint}>
              {activity.status === "running" || activity.status === "queued"
                ? t("工具正在执行，暂无输出。", "Tool is running; no output yet.")
                : t("无额外参数或输出。", "No additional arguments or output.")}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

function ThinkingStepItem({
  step,
}: {
  step: Extract<ProcessStep, { kind: "thinking" }>;
}) {
  return (
    <div
      className={styles.thinkingBlock}
      data-running={step.running ? "true" : undefined}
    >
      <div className={styles.thinkingHead}>
        <span className={styles.thinkingDot} aria-hidden="true" />
        <span className={styles.thinkingLabel}>
          {step.running
            ? t("思考中…", "Thinking…")
            : step.placeholder
              ? t("已完成思考", "Thought")
              : t("思考过程", "Thought")}
        </span>
      </div>
      {!step.placeholder && step.text && (
        <div className={styles.thinkingBody}>{step.text}</div>
      )}
      {step.truncated && (
        <p className={styles.hint}>
          {t("思考过程已截断。", "Thought process truncated.")}
        </p>
      )}
    </div>
  );
}

function NarrationStepItem({
  step,
}: {
  step: Extract<ProcessStep, { kind: "narration" }>;
}) {
  return <div className={styles.narrationBody}>{step.text}</div>;
}

/**
 * One member step's chronological execution process (chain of thought + tool
 * calls). Open automatically while the member is actively executing—with each
 * individual tool call collapsed by default—and automatically collapses once
 * the member finishes so only the final result stays prominent. An explicit
 * reader toggle overrides the automatic state.
 */
export function ToolActivityBlock({
  group,
  active = false,
  showNarration = true,
  sources,
  onOpenFile,
  onOpenImage,
  onHistory,
}: {
  group: ToolActivityGroup;
  /** True while the owning member step/reply is still executing. */
  active?: boolean;
  /** Include pre-tool narration inside the process timeline when split from the final result. */
  showNarration?: boolean;
  sources?: Source[];
  onOpenFile?: (ref: FileRef) => void;
  onOpenImage?: (preview: ImagePreview) => void;
  onHistory?: (activity: ToolActivity) => void;
}) {
  useLanguage();
  const decisionKey = activityDecisionKey(group);
  const [decision, setDecision] = useState<boolean | undefined>(() =>
    readActivityDecision(decisionKey),
  );
  const open = decision ?? active;
  const listID = `tool-activity-${group.key.replace(/[^a-zA-Z0-9_-]+/g, "-")}`;
  const toggle = () => {
    const next = !open;
    setDecision(next);
    saveActivityDecision(decisionKey, next);
  };
  const allSteps: ProcessStep[] =
    group.steps && group.steps.length > 0
      ? group.steps
      : group.activities.map((activity) => ({
          kind: "tool",
          key: activity.key,
          seq: activity.seq,
          activity,
        }));
  const steps = showNarration
    ? allSteps
    : allSteps.filter((step) => step.kind !== "narration");
  const duration = group.activities.reduce(
    (sum, activity) => sum + (activity.durationMs ?? 0),
    0,
  );
  const running =
    active ||
    group.activities.some(
      (activity) =>
        activity.status === "running" || activity.status === "queued",
    ) ||
    steps.some((step) => step.kind === "thinking" && step.running);
  const unknown = group.activities.find(
    (activity) => activity.status === "unknown",
  );
  if (!steps.length && !group.summary) return null;
  return (
    <div
      className={styles.activity}
      data-testid="tool-activity"
      data-step-id={group.stepID || undefined}
      data-running={running ? "true" : undefined}
      data-open={open ? "true" : undefined}
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
            {t("查看历史", "View history")}
          </button>
        )}
      </div>
      {open && (
        <ol className={styles.list} id={listID}>
          {steps.map((step) => (
            <li
              key={step.key}
              className={
                step.kind === "thinking"
                  ? styles.thinkingItem
                  : step.kind === "narration"
                    ? styles.narrationItem
                    : styles.item
              }
              data-process-kind={step.kind}
            >
              {step.kind === "thinking" ? (
                <ThinkingStepItem step={step} />
              ) : step.kind === "narration" ? (
                <NarrationStepItem step={step} />
              ) : (
                <ToolStepItem
                  activity={step.activity}
                  sources={sources}
                  onOpenFile={onOpenFile}
                  onOpenImage={onOpenImage}
                />
              )}
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
  activeStepIDs,
  sources,
  onOpenFile,
  onOpenImage,
  onHistory,
  showMember = true,
}: {
  groups: ToolActivityGroup[];
  agents?: Agent[];
  activeStepIDs?: Set<string>;
  sources?: Source[];
  onOpenFile?: (ref: FileRef) => void;
  onOpenImage?: (preview: ImagePreview) => void;
  onHistory?: (activity: ToolActivity) => void;
  showMember?: boolean;
}) {
  useLanguage();
  if (!groups.length) return null;
  return (
    <div
      className={styles.timeline}
      aria-label={t("工具活动", "Tool activity")}
    >
      {groups.map((group) => {
        const name = memberDisplayName(group.memberID, agents);
        const isActive = Boolean(
          group.stepID && activeStepIDs?.has(group.stepID),
        );
        return (
          <section
            key={group.key}
            className={styles.group}
            aria-label={
              name
                ? t(`${name}的工具活动`, `${name}'s tool activity`)
                : t("工具活动", "Tool activity")
            }
          >
            {showMember && name && (
              <small className={styles.member}>{name}</small>
            )}
            <ToolActivityBlock
              group={group}
              active={isActive}
              sources={sources}
              onOpenFile={onOpenFile}
              onOpenImage={onOpenImage}
              onHistory={onHistory}
            />
          </section>
        );
      })}
    </div>
  );
}
