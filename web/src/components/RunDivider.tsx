import type { Run, Step } from "../api/client";
import { statusText, terminal } from "../api/client";
import { cx } from "../styles/classes";
import {
  roleLabel,
  runDuration,
  runErrorText,
  workflows,
} from "../lib/presentation";
import { Icon } from "./Icon";

/** One bar per collaboration: what ran, how it went, and what can be done with it. */
export function RunDivider({
  run,
  index,
  steps,
  connection,
  latest,
  busy,
  onSnapshot,
  onRetry,
}: {
  run: Run;
  index: number;
  steps?: Step[];
  connection?: string;
  latest: boolean;
  busy: boolean;
  onSnapshot: () => void;
  onRetry: () => void;
}) {
  const workflow =
    workflows[(run.input.workflow || "free") as keyof typeof workflows];
  const duration = runDuration(run);
  const failed = ["failed", "interrupted", "canceled"].includes(run.status);
  return (
    <section className={cx("run-card")} aria-label={`第 ${index} 次协作`}>
      <div className={cx("run-card-head")}>
        <span className={cx("run-index")}>第 {index} 次</span>
        <span>{workflow}</span>
        <span
          className={cx(`status ${run.status}`)}
          {...(latest ? { "data-testid": "run-status" } : {})}
        >
          {statusText[run.status]}
        </span>
        {duration && <span className={cx("muted")}>用时 {duration}</span>}
        {connection && !terminal(run.status) && (
          <span className={cx("muted")} role="status">
            {connection}
          </span>
        )}
        <span className={cx("run-actions")}>
          <button
            type="button"
            className={cx("ghost small")}
            onClick={onSnapshot}
          >
            配置快照
          </button>
          {latest && failed && (
            <button
              type="button"
              className={cx("ghost small")}
              onClick={onRetry}
              disabled={busy}
            >
              <Icon name="refresh" size={14} /> 重新运行
            </button>
          )}
        </span>
      </div>
      {steps && steps.length > 0 && (
        <details className={cx("run-steps")}>
          <summary>{steps.length} 个步骤</summary>
          <ol>
            {steps.map((step) => (
              <li key={step.id}>
                <strong>{step.agent_name}</strong>
                <span className={cx("role-chip")}>{roleLabel(step.role)}</span>
                <span className={cx(`status ${step.status}`)}>
                  {statusText[step.status]}
                </span>
                {step.error && (
                  <p className={cx("error")}>
                    {runErrorText(step.error, step.status)}
                  </p>
                )}
              </li>
            ))}
          </ol>
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
