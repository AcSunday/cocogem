import type {
  Artifact,
  Attempt,
  Message,
  Run,
  Source,
  Step,
} from "../api/client";
import { statusText } from "../api/client";
import { t, useLanguage } from "../lib/i18n";
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
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import { Markdown } from "./Markdown";
import { Icon } from "./Icon";

const cx = scoped(base, controls, layout, conversation);

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
  useLanguage();
  const member = (id: string) =>
    run.agents?.find((agent) => agent.id === id)?.display_name ??
    t("成员", "Member");
  return (
    <ol
      className={cx("collaboration-tasks")}
      aria-label={t("协作任务", "Collaboration steps")}
    >
      {steps.map((step) => {
        const upstream = (step.depends_on ?? [])
          .map((id) => steps.find((item) => item.id === id))
          .filter((item): item is Step => !!item);
        const from =
          step.kind !== "continuation" && step.from_agent_id
            ? member(step.from_agent_id)
            : [...new Set(upstream.map((item) => item.agent_name))].join(
                t("、", ", "),
              );
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
                    ? t("结果回传", "Result return")
                    : step.kind === "relay"
                      ? t(
                          `接力 · ${roleLabel(step.role) || "协作"}`,
                          `Relay · ${roleLabel(step.role) || "Collaborate"}`,
                        )
                      : roleLabel(step.role) || t("任务交接", "Task handoff")}
                </span>
                {step.review_verdict && (
                  <span className={cx("role-chip")}>
                    {step.review_verdict === "approved"
                      ? t("复核通过", "Approved")
                      : t("请求修改", "Changes requested")}
                  </span>
                )}
                <span className={cx(`status ${step.status}`)}>
                  {waiting
                    ? step.kind === "continuation"
                      ? t("等待结果", "Waiting for results")
                      : t("等待上游", "Waiting for upstream")
                    : step.status === "pending"
                      ? t("等待调度", "Queued")
                      : step.status === "skipped" && step.converged
                        ? t("已收敛跳过", "Converged (skipped)")
                        : statusText[step.status] || step.status}
                </span>
                {stepAttempts.length > 0 && (
                  <small className={cx("muted")}>
                    {t(
                      `${stepAttempts.length} 次尝试`,
                      `${stepAttempts.length} attempt${stepAttempts.length === 1 ? "" : "s"}`,
                    )}{" "}
                    ·{" "}
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
                      ? t(
                          `开始：${formatDateTime(step.started_at)}`,
                          `Started: ${formatDateTime(step.started_at)}`,
                        )
                      : t("尚未开始", "Not started")}
                    {step.deadline_at
                      ? t(
                          ` · 截止：${formatDateTime(step.deadline_at)}`,
                          ` · Deadline: ${formatDateTime(step.deadline_at)}`,
                        )
                      : ""}
                  </p>
                )}
                {step.instruction && (
                  <>
                    <h4>
                      {step.kind === "continuation"
                        ? t("继续处理", "Continuation")
                        : t("任务说明", "Instructions")}
                    </h4>
                    <Markdown text={step.instruction} />
                  </>
                )}
                {!!step.context_refs?.length && (
                  <>
                    <h4>{t("交接依据", "Handoff context")}</h4>
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
                    <h4>{t("执行尝试", "Execution attempts")}</h4>
                    <ul className={cx("task-references")}>
                      {stepAttempts.map((attempt) => (
                        <li key={attempt.id}>
                          <span className={cx(`status ${attempt.status}`)}>
                            {attemptStatusLabel(attempt.status) ||
                              attempt.status}
                          </span>{" "}
                          {t(`第 ${attempt.number} 次`, `Attempt #${attempt.number}`)}
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
                            ? t(
                                ` · 模型 ${attempt.resolved_model}`,
                                ` · Model ${attempt.resolved_model}`,
                              )
                            : ""}
                          <span className={cx("muted")}>
                            {" "}
                            · {attemptUsageLabel(attempt)}
                          </span>
                          {attempt.status === "running" &&
                            attempt.liveness_warning && (
                              <span className={cx("muted")}>
                                {" "}
                                · {t("告警：", "Alert: ")}
                                {livenessLabel(
                                  attempt.liveness_warning.state,
                                ) || attempt.liveness_warning.state}
                                {" · "}
                                {t(
                                  `已 ${attempt.liveness_warning.silence_seconds} 秒`,
                                  `${attempt.liveness_warning.silence_seconds}s elapsed`,
                                )}
                              </span>
                            )}
                          {attempt.connection_identity ? (
                            <small title={attempt.connection_identity}>
                              {" "}
                              · {t("连接", "Conn")}{" "}
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
                        ? t("正在回复", "Replying")
                        : t("处理结果", "Output")}
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
        {t(`来源：${source.title}`, `Source: ${source.title}`)}
      </button>
    ) : (
      <span>{t(`来源：${source.title}`, `Source: ${source.title}`)}</span>
    );
  const message = messages.find((item) => item.id === reference);
  if (message)
    return (
      <blockquote>
        <strong>{message.agent_name || t("用户", "User")}</strong>
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
  return (
    <span className={cx("muted")}>
      {t("引用内容暂不可用", "Referenced content unavailable")}
    </span>
  );
}
