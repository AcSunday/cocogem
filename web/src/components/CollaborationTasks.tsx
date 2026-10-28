import type {
  Artifact,
  Attempt,
  Message,
  Run,
  Source,
  Step,
} from "../api/client";
import { statusText } from "../api/client";
import {
  attemptReasonLabel,
  attemptStatusLabel,
  attemptUsageLabel,
  failureKindLabel,
  formatDateTime,
  livenessLabel,
  roleLabel,
  runErrorText,
  sessionModeLabel,
} from "../lib/presentation";
import { cx } from "../styles/classes";
import { Markdown } from "./Markdown";
import { Icon } from "./Icon";

/** Persisted task records keep handoffs visible after streaming or reloading. */
export function CollaborationTasks({
  run,
  steps,
  attempts = [],
  messages,
  sources,
  artifacts,
  onSource,
  onArtifact,
}: {
  run: Run;
  steps: Step[];
  attempts?: Attempt[];
  messages: Message[];
  sources: Source[];
  artifacts: Artifact[];
  onSource?: (id: string) => void;
  onArtifact?: (artifact: Artifact) => void;
}) {
  const member = (id: string) =>
    run.agents?.find((agent) => agent.id === id)?.display_name ?? "成员";
  return (
    <ol className={cx("collaboration-tasks")} aria-label="协作任务">
      {steps.map((step) => {
        const upstream = (step.depends_on ?? [])
          .map((id) => steps.find((item) => item.id === id))
          .filter((item): item is Step => !!item);
        const from =
          step.kind !== "continuation" && step.from_agent_id
            ? member(step.from_agent_id)
            : [...new Set(upstream.map((item) => item.agent_name))].join("、");
        const result = messages.filter(
          (message) => message.step_id === step.id,
        );
        const waiting =
          step.status === "pending" && (step.depends_on?.length ?? 0) > 0;
        const stepAttempts = attempts.filter(
          (attempt) => attempt.step_id === step.id,
        );
        return (
          <li key={step.id}>
            <details className={cx("collaboration-task")}>
              <summary>
                <span className={cx("task-disclosure")}>
                  <Icon name="chevron" size={12} />
                </span>
                <span className={cx("task-members")}>
                  {from && <span>{from} → </span>}
                  <strong>{step.agent_name}</strong>
                </span>
                <span className={cx("role-chip")}>
                  {step.kind === "continuation"
                    ? "结果回传"
                    : roleLabel(step.role) || "任务交接"}
                </span>
                <span className={cx(`status ${step.status}`)}>
                  {waiting
                    ? step.kind === "continuation"
                      ? "等待结果"
                      : "等待上游"
                    : step.status === "pending"
                      ? "等待调度"
                      : statusText[step.status] || step.status}
                </span>
                {stepAttempts.length > 0 && (
                  <small className={cx("muted")}>
                    {stepAttempts.length} 次尝试 ·{" "}
                    {attemptStatusLabel(
                      stepAttempts[stepAttempts.length - 1].status,
                    ) ||
                      stepAttempts[stepAttempts.length - 1].status}
                  </small>
                )}
              </summary>
              <div className={cx("task-detail")}>
                {(step.started_at || step.deadline_at) && (
                  <p className={cx("muted")}>
                    {step.started_at
                      ? `开始：${formatDateTime(step.started_at)}`
                      : "尚未开始"}
                    {step.deadline_at
                      ? ` · 截止：${formatDateTime(step.deadline_at)}`
                      : ""}
                  </p>
                )}
                {step.instruction && (
                  <>
                    <h4>
                      {step.kind === "continuation" ? "继续处理" : "任务说明"}
                    </h4>
                    <Markdown text={step.instruction} />
                  </>
                )}
                {!!step.context_refs?.length && (
                  <>
                    <h4>交接依据</h4>
                    <ul className={cx("task-references")}>
                      {step.context_refs.map((ref) => (
                        <li key={ref}>
                          <TaskReference
                            reference={ref}
                            messages={messages}
                            sources={sources}
                            artifacts={artifacts}
                            onSource={onSource}
                            onArtifact={onArtifact}
                          />
                        </li>
                      ))}
                    </ul>
                  </>
                )}
                {stepAttempts.length > 0 && (
                  <>
                    <h4>执行尝试</h4>
                    <ul className={cx("task-references")}>
                      {stepAttempts.map((attempt) => (
                        <li key={attempt.id}>
                          <span className={cx(`status ${attempt.status}`)}>
                            {attemptStatusLabel(attempt.status) ||
                              attempt.status}
                          </span>{" "}
                          第 {attempt.number} 次
                          {attempt.reason
                            ? ` · ${attemptReasonLabel(attempt.reason) || attempt.reason}`
                            : ""}
                          {attempt.session_mode
                            ? ` · ${sessionModeLabel(attempt.session_mode) || attempt.session_mode}`
                            : ""}
                          {attempt.cli_version
                            ? ` · ${attempt.executor} ${attempt.cli_version}`
                            : attempt.executor
                              ? ` · ${attempt.executor}`
                              : ""}
                          {attempt.resolved_model
                            ? ` · 模型 ${attempt.resolved_model}`
                            : ""}
                          <span className={cx("muted")}>
                            {" "}
                            · {attemptUsageLabel(attempt)}
                          </span>
                          {attempt.status === "running" &&
                            attempt.liveness_warning && (
                              <span className={cx("muted")}>
                                {" "}
                                · 告警：
                                {livenessLabel(
                                  attempt.liveness_warning.state,
                                ) || attempt.liveness_warning.state}
                                {" · 已 "}
                                {attempt.liveness_warning.silence_seconds} 秒
                              </span>
                            )}
                          {attempt.connection_identity ? (
                            <small title={attempt.connection_identity}>
                              {" "}· 连接{" "}
                              {attempt.connection_identity.slice(0, 8)}
                            </small>
                          ) : null}
                          {attempt.failure_kind ? (
                            <p className={cx("muted")}>
                              {failureKindLabel(attempt.failure_kind) ||
                                attempt.failure_kind}
                            </p>
                          ) : (
                            attempt.error && (
                              <p className={cx("muted")}>
                                {runErrorText(attempt.error, attempt.status)}
                              </p>
                            )
                          )}
                        </li>
                      ))}
                    </ul>
                  </>
                )}
                {result.map((message) => (
                  <section key={message.id}>
                    <h4>
                      {message.status === "processing"
                        ? "正在回复"
                        : "处理结果"}
                    </h4>
                    <Markdown text={message.content} />
                  </section>
                ))}
                {step.error && (
                  <p
                    className={cx(
                      step.status === "canceled" ? "muted" : "error",
                    )}
                  >
                    {runErrorText(step.error, step.status)}
                  </p>
                )}
              </div>
            </details>
          </li>
        );
      })}
    </ol>
  );
}

function TaskReference({
  reference,
  messages,
  sources,
  artifacts,
  onSource,
  onArtifact,
}: {
  reference: string;
  messages: Message[];
  sources: Source[];
  artifacts: Artifact[];
  onSource?: (id: string) => void;
  onArtifact?: (artifact: Artifact) => void;
}) {
  const source = sources.find((item) => item.id === reference);
  if (source)
    return onSource ? (
      <button
        type="button"
        className={cx("small")}
        onClick={() => onSource(source.id)}
      >
        来源：{source.title}
      </button>
    ) : (
      <span>来源：{source.title}</span>
    );
  const message = messages.find((item) => item.id === reference);
  if (message)
    return (
      <blockquote>
        <strong>{message.agent_name || "用户"}</strong>
        <Markdown text={message.content} />
      </blockquote>
    );
  const artifact = artifacts.find(
    (item) => `${item.id}@${item.version}` === reference,
  );
  if (artifact)
    return onArtifact ? (
      <button
        type="button"
        className={cx("small")}
        onClick={() => onArtifact(artifact)}
      >
        {artifact.title} · v{artifact.version}
      </button>
    ) : (
      <span>
        {artifact.title} · v{artifact.version}
      </span>
    );
  return <span className={cx("muted")}>引用内容暂不可用</span>;
}
