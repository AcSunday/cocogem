import { useState, type ReactNode, type SyntheticEvent } from "react";
import type {
  Artifact,
  Attempt,
  Event,
  Message,
  Run,
  Source,
  Step,
} from "../api/client";
import { statusText, terminal } from "../api/client";
import { deliveredRun } from "../lib/delivery";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import {
  livenessLabel,
  runDuration,
  runErrorText,
  runUsageSummary,
  runMethod,
} from "../lib/presentation";
import { Icon } from "./Icon";
import { CollaborationTasks } from "./CollaborationTasks";

const cx = scoped(base, controls, layout, conversation);

const PROCESS_KEY = "cocogem.run-process";
const PROCESS_LIMIT = 200;

const processKey = (run: Run) =>
  [run.workspace_id, run.thread_id, run.id].join("/");

/** The reader's explicit choice wins over the automatic collapsed default. */
function readProcessDecision(run: Run): boolean | undefined {
  try {
    const stored = JSON.parse(localStorage.getItem(PROCESS_KEY) ?? "{}");
    const decision = stored?.[processKey(run)];
    return typeof decision === "boolean" ? decision : undefined;
  } catch {
    return undefined;
  }
}

function saveProcessDecision(run: Run, open: boolean) {
  try {
    const stored = JSON.parse(localStorage.getItem(PROCESS_KEY) ?? "{}");
    const decisions =
      stored && typeof stored === "object" && !Array.isArray(stored)
        ? (stored as Record<string, boolean>)
        : {};
    decisions[processKey(run)] = open;
    const keys = Object.keys(decisions);
    for (const key of keys.slice(0, Math.max(0, keys.length - PROCESS_LIMIT)))
      delete decisions[key];
    localStorage.setItem(PROCESS_KEY, JSON.stringify(decisions));
  } catch {
    // A disabled storage only costs the preference, never the conversation.
  }
}

/**
 * One bar per collaboration: what ran, how it went, and what can be done with it.
 * Tool activity is rendered by each member reply, not as one global latest line.
 */
export function RunDivider({
  run,
  index,
  steps,
  attempts = [],
  messages = [],
  sources = [],
  artifacts = [],
  connection,
  liveness,
  latest,
  busy,
  processCount = steps?.length ?? 0,
  children,
  onSnapshot,
  onResume,
  onSource,
  onArtifact,
}: {
  run: Run;
  index: number;
  steps?: Step[];
  attempts?: Attempt[];
  messages?: Message[];
  sources?: Source[];
  artifacts?: Artifact[];
  connection?: string;
  liveness?: Event[];
  latest: boolean;
  busy: boolean;
  processCount?: number;
  children?: ReactNode;
  onSnapshot: () => void;
  onResume: () => void;
  onSource?: (id: string) => void;
  onArtifact?: (artifact: Artifact) => void;
}) {
  const { label: workflow, reason } = runMethod(run);
  const [decision, setDecision] = useState<boolean | undefined>(() =>
    readProcessDecision(run),
  );
  const foldable = deliveredRun(run);
  const open = decision ?? !foldable;
  const toggleProcess = (event: SyntheticEvent<HTMLDetailsElement>) => {
    const next = event.currentTarget.open;
    // Programmatic open changes (running to a delivered success) are not a
    // reader decision and must not enter the preference map.
    if (next === open) return;
    setDecision(next);
    saveProcessDecision(run, next);
  };
  const duration = runDuration(run);
  const usage = runUsageSummary(run, attempts);
  return (
    <section className={cx("run-card")} aria-label={`第 ${index} 次协作`}>
      <div className={cx("run-card-head")}>
        <span className={cx("run-index")}>第 {index} 次</span>
        <span title={reason || undefined}>{workflow}</span>
        {run.input.retry_of_run_id && (
          <span className={cx("muted")}>重试</span>
        )}
        {reason && (
          <span className={cx("muted")} title={reason}>
            {reason.length > 80 ? `${reason.slice(0, 80)}…` : reason}
          </span>
        )}
        <span
          className={cx(`status ${run.status}`)}
          {...(latest ? { "data-testid": "run-status" } : {})}
        >
          {statusText[run.status]}
        </span>
        {duration && <span className={cx("muted")}>用时 {duration}</span>}
        {usage && (
          <span
            className={cx("muted")}
            title="来自持久化的执行尝试；未知用量不按 0 计"
          >
            {usage}
          </span>
        )}
        {!!run.recovery_count && (
          <span className={cx("muted")} title={run.recovery_note}>
            已恢复 {run.recovery_count} 次
            {run.recovery_note?.includes("cli_changed")
              ? " · CLI 已重新检查"
              : ""}
          </span>
        )}
        {connection && !terminal(run.status) && (
          <span className={cx("muted")} role="status">
            {connection}
          </span>
        )}
        {!!liveness?.length &&
          !terminal(run.status) &&
          liveness.map((warning) => {
            const data =
              warning.data && typeof warning.data === "object"
                ? (warning.data as Record<string, unknown>)
                : {};
            return (
              <span
                className={cx("muted")}
                role="status"
                key={`${warning.step_id ?? ""}:${String(data.observed_at ?? warning.seq)}`}
              >
                {livenessLabel(String(data.state ?? "")) || "等待执行"}
                {typeof data.silence_seconds === "number"
                  ? ` · 已 ${data.silence_seconds} 秒`
                  : ""}
              </span>
            );
          })}
        <span className={cx("run-actions")}>
          <button
            type="button"
            className={cx("icon-button small")}
            aria-label="配置快照"
            title="配置快照"
            onClick={onSnapshot}
          >
            <Icon name="settings" size={14} />
          </button>
          {run.status === "interrupted" && (
            <button
              type="button"
              className={cx("ghost small")}
              onClick={onResume}
              disabled={busy}
            >
              <Icon name="refresh" size={14} /> 继续执行
            </button>
          )}
        </span>
      </div>
      {processCount > 0 && (
        <details
          className={cx("run-process")}
          data-testid="run-process"
          open={open}
          onToggle={toggleProcess}
        >
          <summary>
            <Icon name="chevron" size={12} />
            {open || !foldable ? "协作过程" : "协作已完成"}
            <span> · {processCount} 条过程</span>
            {steps?.some((step) => step.status === "running") && (
              <span className={cx("current-members")}>
                {steps
                  .filter((step) => step.status === "running")
                  .map((step) => step.agent_name)
                  .join("、")}
                正在处理
              </span>
            )}
          </summary>
          {steps && steps.length > 0 && (
            <CollaborationTasks
              run={run}
              steps={steps}
              attempts={attempts}
              messages={messages}
              sources={sources}
              artifacts={artifacts}
              onSource={onSource}
              onArtifact={onArtifact}
            />
          )}
          {children}
        </details>
      )}
      {run.error && (
        <p
          className={cx(run.status === "canceled" ? "muted" : "error")}
          role="status"
        >
          {runErrorText(run.error, run.status)}
        </p>
      )}
    </section>
  );
}
