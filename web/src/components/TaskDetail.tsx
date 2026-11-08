import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  type Agent,
  type Artifact,
  type Message,
  type ProjectTaskView,
  type Question,
  type Run,
  type RunView,
} from "../api/client";
import {
  taskAcceptance,
  taskFailureText,
  taskFocusMeta,
  taskGoalSummary,
  taskTitle,
} from "../lib/tasks";
import {
  artifactTypeLabel,
  errorText,
  formatDateTime,
  formatRelativeTime,
} from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";
import { ConfirmDialog } from "./ConfirmDialog";
import { Icon } from "./Icon";
import { Modal } from "./Modal";
import {
  TaskAcceptanceDialog,
  type AcceptanceCandidate,
  type AcceptanceCriterion,
} from "./TaskAcceptanceDialog";
import { TaskCreateForm } from "./TaskCreateForm";

const cx = scoped(base, controls, styles);

/**
 * The narrow task-delivery projection. It intentionally has no sources: the
 * detail page must not load a full ThreadView just to read deliveries.
 * Contract requested from the integrator as
 * `GET /api/v1/tasks/{id}/deliveries`.
 */
type TaskDeliveryPage = {
  task_id: string;
  runs: Run[];
  artifacts: Artifact[];
  messages: Message[];
};

type Snapshot = {
  view: ProjectTaskView;
  children: ProjectTaskView[];
  childQuestions: Record<string, Question[]>;
  questions: Question[];
  deliveries: TaskDeliveryPage;
};

/**
 * The acceptance form is frozen when it opens. A background poll can change
 * the goal digest, generation, revision or the legal candidate set; stale
 * judgements must never be submitted against the new basis.
 */
type AcceptanceSession = {
  id: string;
  taskID: string;
  goal: string;
  revision: number;
  evaluationGeneration: number;
  digest: string;
  criteria: AcceptanceCriterion[];
  candidates: AcceptanceCandidate[];
};

type BadgeTone =
  | "running"
  | "success"
  | "review"
  | "pending"
  | "failed"
  | "stopped"
  | "neutral";

const runStatuses: Record<string, { label: string; tone: BadgeTone }> = {
  queued: { label: "排队中", tone: "pending" },
  running: { label: "执行中", tone: "running" },
  canceling: { label: "正在停止", tone: "pending" },
  succeeded: { label: "已完成", tone: "success" },
  failed: { label: "失败", tone: "failed" },
  canceled: { label: "已停止", tone: "stopped" },
  interrupted: { label: "已中断", tone: "failed" },
  uncertain: { label: "待核实", tone: "pending" },
  yielded: { label: "已让出", tone: "pending" },
};

function runStatus(status = "") {
  return runStatuses[status] ?? { label: status || "未知", tone: "neutral" };
}

function phaseStatus(phase: string, tone: string, label: string) {
  // The shared projection still says 执行已停止 for a stopped Run. The detail
  // page renders the single 已停止 fact; the integrator will align the shared map.
  const text = label === "执行已停止" ? "已停止" : label;
  const badge: BadgeTone =
    phase === "completed"
      ? "success"
      : phase === "canceled"
        ? "stopped"
        : phase === "review" || phase === "needs_revalidation"
          ? "review"
          : phase === "running"
            ? "running"
            : phase === "needs_answer"
              ? "pending"
              : phase === "needs_retry"
                ? tone === "failed"
                  ? "failed"
                  : "stopped"
                : phase === "paused"
                  ? "stopped"
                  : phase === "not_started"
                    ? "neutral"
                    : "pending";
  return { label: text, tone: badge };
}

function StatusBadge({ label, tone }: { label: string; tone: BadgeTone }) {
  return (
    <span className={cx("task-badge", `task-badge-${tone}`)}>
      <span className={cx("task-badge-dot")} aria-hidden="true" />
      {label}
    </span>
  );
}

const resultLabels: Record<string, string> = {
  passed: "通过",
  failed: "未通过",
  inconclusive: "无法确认",
  not_run: "未验证",
  not_applicable: "不适用",
};

const producerLabels: Record<string, string> = {
  human_confirmed: "人工确认",
  host_observed: "宿主验证",
  member_reported: "成员报告",
};

const failureStatuses = new Set([
  "failed",
  "interrupted",
  "uncertain",
  "canceled",
  "yielded",
]);

let acceptanceSequence = 0;
const nextAcceptanceID = () => `acceptance-${++acceptanceSequence}`;

const artifactRef = (item: Artifact) => `artifact:${item.id}@v${item.version}`;
const messageRef = (item: Message) => `message:${item.id}`;

function fileSize(bytes: number) {
  if (!bytes) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function deliveryCount(run?: Run) {
  return run?.delivery?.message_ids?.length ?? 0;
}

/** A durable task brief: the decision, the goal, the evidence and the record. */
export function TaskDetail({
  taskID,
  agents,
  workspaceName,
  onOpenThread,
  onOpenArtifact,
  onOpenTask,
  onBack,
}: {
  taskID: string;
  agents: Agent[];
  workspaceName?: string;
  onOpenThread: (threadID: string) => void;
  onOpenArtifact: (artifact: Artifact) => void;
  onOpenTask?: (taskID: string) => void;
  onBack?: () => void;
}) {
  const [creatingChild, setCreatingChild] = useState(false);
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [acceptance, setAcceptance] = useState<AcceptanceSession>();
  const [confirmingRetry, setConfirmingRetry] = useState(false);
  const askRef = useRef<HTMLElement>(null);
  const deliverablesRef = useRef<HTMLElement>(null);

  const load = useCallback(async () => {
    try {
      const view = await api<ProjectTaskView>(`/api/v1/tasks/${taskID}`);
      const [deliveryPage, questionPage, taskPage] = await Promise.all([
        api<TaskDeliveryPage>(`/api/v1/tasks/${taskID}/deliveries`),
        api<{ items: Question[] }>(`/api/v1/tasks/${taskID}/questions`),
        api<{ items: ProjectTaskView[] }>(
          `/api/v1/workspaces/${view.task.workspace_id}/tasks`,
        ),
      ]);
      const children = (taskPage.items ?? []).filter(
        (item) => item.task.parent_id === view.task.id,
      );
      const childQuestions: Record<string, Question[]> = {};
      await Promise.all(
        children.map(async (child) => {
          try {
            const page = await api<{ items: Question[] }>(
              `/api/v1/tasks/${child.task.id}/questions`,
            );
            childQuestions[child.task.id] = page.items ?? [];
          } catch {
            childQuestions[child.task.id] = [];
          }
        }),
      );
      setSnapshot({
        view,
        children,
        childQuestions,
        questions: questionPage.items ?? [],
        deliveries: {
          task_id: deliveryPage?.task_id ?? view.task.id,
          runs: deliveryPage?.runs ?? [],
          artifacts: deliveryPage?.artifacts ?? [],
          messages: deliveryPage?.messages ?? [],
        },
      });
      setError("");
    } catch (failure) {
      setError(errorText(failure));
    }
  }, [taskID]);

  useEffect(() => {
    setSnapshot(undefined);
    void load();
  }, [load]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  const mutate = async (
    name: string,
    action: () => Promise<unknown>,
    success = "",
  ) => {
    if (busy) return;
    setBusy(name);
    setError("");
    setNotice("");
    try {
      await action();
      if (success) setNotice(success);
      await load();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };

  if (!snapshot)
    return (
      <div className={cx("project-home")} role={error ? "alert" : "status"}>
        <p>{error || "正在读取任务、执行记录和验收状态…"}</p>
        {error && <button onClick={() => void load()}>重新加载任务</button>}
      </div>
    );

  const { view, children, childQuestions, questions, deliveries } = snapshot;
  const { task, revision } = view;
  const completions = view.completions ?? [];
  const criteria = revision.acceptance ?? [];
  const taskRuns = [...deliveries.runs]
    .filter((run) => run.task_id === task.id)
    .sort((a, b) => a.created_at.localeCompare(b.created_at));
  const latestRun = taskRuns.at(-1);
  const latestRunStatus = latestRun?.status ?? view.last_run_status ?? "";
  const latestRunID = latestRun?.id ?? view.last_run_id ?? "";
  const latestRunFailureKind =
    latestRun?.failure_kind ?? view.last_run_failure_kind ?? "";
  const successfulRuns = taskRuns.filter((run) => run.status === "succeeded");
  const deliveredMessageIDs = new Set(
    successfulRuns.flatMap((run) => run.delivery?.message_ids ?? []),
  );
  const deliveredArtifactRefs = new Set(
    successfulRuns.flatMap(
      (run) =>
        run.delivery?.artifact_refs?.map(
          (item) => `${item.id}@v${item.version}`,
        ) ?? [],
    ),
  );
  // Defensive projection filter: the server remains authoritative, but drafts,
  // failed Runs, other Tasks and non-delivered versions never become candidates.
  const artifacts = deliveries.artifacts.filter((item) =>
    deliveredArtifactRefs.has(`${item.id}@v${item.version}`),
  );
  const deliveredMessages = deliveries.messages.filter(
    (message) =>
      deliveredMessageIDs.has(message.id) && message.status === "completed",
  );
  const member = (id: string) =>
    agents.find((agent) => agent.id === id)?.display_name || id;
  const openQuestions = questions.filter(
    (question) => question.status === "open",
  );
  const focus = taskFocusMeta(view, openQuestions);
  const focusBadge = phaseStatus(focus.phase, focus.tone, focus.label);
  const focusNext =
    focus.phase === "waiting_budget"
      ? "打开会话查看原因；已有账本不会重置"
      : focus.next;
  const progress = taskAcceptance(view);
  const currentCompletion = [...completions]
    .reverse()
    .find(
      (completion) =>
        completion.validity === "current" &&
        completion.task_revision === task.current_revision &&
        completion.evaluation_generation === task.evaluation_generation &&
        completion.input_snapshot_digest === revision.digest,
    );
  const candidateOptions: AcceptanceCandidate[] = [
    ...artifacts.map((item) => ({
      ref: artifactRef(item),
      label: `${item.title} · v${item.version}`,
    })),
    ...deliveredMessages.map((item) => ({
      ref: messageRef(item),
      label: `会话交付 · ${formatDateTime(item.created_at)}`,
    })),
  ];
  const criterionText = (statement: string) =>
    statement.trim() === revision.goal.trim()
      ? "交付满足上述任务目标"
      : statement;
  const acceptanceAvailable =
    criteria.length > 0 &&
    candidateOptions.length > 0 &&
    focus.phase !== "completed" &&
    focus.phase !== "canceled";
  const frozenCriteria = criteria.map((criterion) => ({
    criterion_id: criterion.criterion_id,
    statement: criterion.statement,
    required: criterion.required,
  }));
  const acceptanceStale =
    !!acceptance &&
    (acceptance.taskID !== task.id ||
      acceptance.revision !== task.current_revision ||
      acceptance.evaluationGeneration !== task.evaluation_generation ||
      acceptance.digest !== revision.digest ||
      JSON.stringify(acceptance.criteria) !== JSON.stringify(frozenCriteria) ||
      acceptance.candidates.map((item) => item.ref).join("\n") !==
        candidateOptions.map((item) => item.ref).join("\n"));
  const freezeAcceptance = (): AcceptanceSession => ({
    id: nextAcceptanceID(),
    taskID: task.id,
    goal: revision.goal,
    revision: task.current_revision,
    evaluationGeneration: task.evaluation_generation,
    digest: revision.digest,
    criteria: frozenCriteria,
    candidates: candidateOptions.map((item) => ({ ...item })),
  });
  const openAcceptance = () => {
    // Reopening behind an active dialog must never replace the frozen basis.
    if (acceptance) return;
    setAcceptance(freezeAcceptance());
  };
  const refreshAcceptance = () => setAcceptance(freezeAcceptance());
  const closeAcceptance = (id: string) =>
    setAcceptance((current) => (current?.id === id ? undefined : current));
  const acceptSegments = Math.min(Math.max(progress.total, 4), 6);
  const acceptFilled = progress.total
    ? Math.round((progress.passed / progress.total) * acceptSegments)
    : 0;
  const canRetry = ["failed", "canceled", "interrupted"].includes(
    latestRunStatus,
  );
  const requiresRetryConfirmation =
    task.waiting_reason === "uncertain_effect" ||
    latestRunFailureKind === "process_uncertain";
  const completedChildren = children.filter(
    (child) =>
      child.task.status === "completed" &&
      child.task.evaluation_status !== "needs_revalidation",
  ).length;
  const latestRunBadge = runStatus(latestRunStatus);
  const latestRunText = (() => {
    if (!latestRunStatus) return "";
    if (latestRunStatus === "succeeded") {
      const count = deliveryCount(latestRun);
      return count ? `执行已完成，交付 ${count} 条回复。` : "执行已完成。";
    }
    if (failureStatuses.has(latestRunStatus))
      return taskFailureText(view) || "上次执行没有完成。";
    return "正在执行，完成后会进入待验收。";
  })();
  const runNote = (run: Run) => {
    if (run.status === "succeeded") {
      const count = deliveryCount(run);
      return count ? `交付 ${count} 条回复` : "执行完成";
    }
    if (run.status === "canceled") return "已停止";
    if (run.status === "interrupted") return "已中断";
    if (run.status === "failed") return "执行失败";
    return "";
  };

  const resume = () =>
    void mutate(
      "resume",
      () =>
        api(`/api/v1/tasks/${taskID}/actions`, "POST", {
          action: "resume",
          request_id: crypto.randomUUID(),
        }),
      "任务已恢复。",
    );

  const openExecution = () => {
    if (task.thread_id && (taskRuns.length || view.last_run_status)) {
      onOpenThread(task.thread_id);
      return;
    }
    void mutate(
      "start",
      async () => {
        const run = await api<Run>(`/api/v1/tasks/${taskID}/start`, "POST", {});
        onOpenThread(run.thread_id);
      },
      "任务已开始执行。",
    );
  };

  const continueExecution = async () => {
    if (busy) return;
    if (!latestRunID) {
      openExecution();
      return;
    }
    if (!canRetry) {
      openExecution();
      return;
    }
    setBusy("continue");
    setError("");
    setNotice("");
    try {
      if (latestRunStatus === "interrupted") {
        await api(`/api/v1/runs/${latestRunID}/resume`, "POST", {});
        setNotice("已恢复执行。");
      } else {
        const detail = await api<RunView>(`/api/v1/runs/${latestRunID}`);
        const step = (detail.steps ?? []).find(
          (item) => item.status === "failed" || item.status === "canceled",
        );
        if (step) {
          await api(
            `/api/v1/runs/${latestRunID}/retry`,
            "POST",
            { step_id: step.id },
            { "Idempotency-Key": crypto.randomUUID() },
          );
        } else {
          await api(`/api/v1/runs/${latestRunID}/retry`, "POST", {});
        }
        setNotice("已继续执行；只会补跑未完成的步骤。");
      }
      await load();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };

  const openDeliverables = () => {
    deliverablesRef.current?.scrollIntoView({
      behavior: "smooth",
      block: "start",
    });
  };

  const scrollToQuestions = () => {
    askRef.current?.scrollIntoView({
      behavior: "smooth",
      block: "start",
    });
  };

  const primaryAction = (() => {
    switch (focus.phase) {
      case "needs_answer":
        return { label: focus.action.label, run: scrollToQuestions };
      case "needs_retry":
        return {
          label: focus.action.label,
          run: () =>
            requiresRetryConfirmation
              ? setConfirmingRetry(true)
              : void continueExecution(),
        };
      case "needs_revalidation":
      case "review":
        return acceptanceAvailable
          ? { label: focus.action.label, run: openAcceptance }
          : task.thread_id
            ? { label: "打开会话", run: () => onOpenThread(task.thread_id) }
            : null;
      case "running":
        return { label: focus.action.label, run: openExecution };
      case "not_started":
        return { label: focus.action.label, run: openExecution };
      case "paused":
        return { label: focus.action.label, run: resume };
      case "waiting_budget":
        // The manual budget editor is gone. A real exhausted root ledger is
        // explained in the conversation, not "unlocked" from this page.
        return task.thread_id
          ? { label: "打开会话", run: () => onOpenThread(task.thread_id) }
          : null;
      case "waiting_other":
        return task.thread_id
          ? { label: "打开会话", run: () => onOpenThread(task.thread_id) }
          : null;
      case "completed":
      case "canceled":
        return artifacts.length || deliveredMessages.length
          ? { label: focus.action.label, run: openDeliverables }
          : null;
    }
  })();

  const secondaryOpen =
    !!task.thread_id &&
    focus.phase !== "running" &&
    focus.phase !== "waiting_other" &&
    primaryAction?.label !== "打开会话";

  return (
    <div className={cx("task-workspace", "task-detail", "task-brief")}>
      {onBack && (
        <button
          className={cx("ghost", "small", "task-brief-back")}
          onClick={onBack}
        >
          <Icon name="reply" size={14} /> 返回任务清单
        </button>
      )}

      <header className={cx("task-brief-head")}>
        <div className={cx("task-brief-eyebrow")}>
          <StatusBadge label={focusBadge.label} tone={focusBadge.tone} />
          {workspaceName && (
            <>
              <span>{workspaceName}</span>
              <span aria-hidden="true">·</span>
            </>
          )}
          <span>{formatRelativeTime(task.updated_at)}</span>
        </div>
        <h1>{taskTitle(view)}</h1>
        <p className={cx("task-brief-sub")}>
          <span>当前：{focus.current}</span>
          <span className={cx("task-brief-sep")} aria-hidden="true">
            ·
          </span>
          <span>下一步：{focusNext}</span>
        </p>
        <div className={cx("task-brief-actions")}>
          {primaryAction && (
            <button
              className={cx("primary", "task-brief-action")}
              disabled={!!busy}
              onClick={primaryAction.run}
            >
              {primaryAction.label}
            </button>
          )}
          {secondaryOpen && (
            <button
              type="button"
              className={cx(
                "small",
                "task-brief-action",
                "task-brief-secondary",
              )}
              onClick={() => onOpenThread(task.thread_id)}
            >
              <Icon name="open" size={14} /> 打开会话
            </button>
          )}
        </div>
      </header>

      {notice && (
        <p className={cx("task-focus-notice")} role="status">
          {notice}
        </p>
      )}

      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}

      <div className={cx("task-brief-grid")}>
        <div className={cx("task-brief-main")}>
          {openQuestions.length > 0 && (
            <section
              ref={askRef}
              className={cx("task-brief-ask-list")}
              aria-label="需要你决定"
            >
              {openQuestions.map((question) => {
                const answer =
                  answers[question.id] ?? question.recommended ?? "";
                return (
                  <article className={cx("task-brief-ask")} key={question.id}>
                    <div className={cx("task-brief-ask-label")}>
                      <span
                        className={cx(
                          "task-focus-dot",
                          "task-focus-dot-attention",
                        )}
                        aria-hidden="true"
                      />
                      需要你决定
                    </div>
                    <h2>{question.body}</h2>
                    <p className={cx("task-brief-ask-note")}>
                      {question.blocking_scope
                        ? `影响：${question.blocking_scope}`
                        : "影响任务继续"}{" "}
                      · 回答后会在下一个执行边界应用
                    </p>
                    {!!question.options?.length && (
                      <div
                        className={cx("task-brief-options")}
                        role="group"
                        aria-label="可选回答"
                      >
                        {question.options.map((option) => (
                          <button
                            key={option}
                            type="button"
                            className={cx(
                              "task-brief-option",
                              answer === option && "selected",
                            )}
                            aria-pressed={answer === option}
                            onClick={() =>
                              setAnswers((current) => ({
                                ...current,
                                [question.id]: option,
                              }))
                            }
                          >
                            {option}
                            {option === question.recommended ? "（建议）" : ""}
                          </button>
                        ))}
                      </div>
                    )}
                    <label className={cx("task-brief-answer")}>
                      回答
                      <textarea
                        value={answer}
                        onChange={(event) =>
                          setAnswers((current) => ({
                            ...current,
                            [question.id]: event.target.value,
                          }))
                        }
                        placeholder="写下你的决定…"
                      />
                    </label>
                    <div className={cx("task-brief-ask-actions")}>
                      <button
                        className={cx("primary", "task-brief-action")}
                        disabled={!!busy || !answer.trim()}
                        onClick={() =>
                          void mutate(
                            `answer-${question.id}`,
                            () =>
                              api(
                                `/api/v1/tasks/${taskID}/questions/${question.id}/answers`,
                                "POST",
                                {
                                  answer,
                                  answer_ref: `user-answer:${crypto.randomUUID()}`,
                                },
                                { "If-Match": String(task.current_revision) },
                              ),
                            "回答已提交；任务会在下一个执行边界继续。",
                          )
                        }
                      >
                        提交回答
                      </button>
                      <span className={cx("task-brief-ask-hint")}>
                        回答后将在下一个执行边界应用
                      </span>
                    </div>
                  </article>
                );
              })}
            </section>
          )}

          <section className={cx("task-brief-section")} aria-label="最近进展">
            <div className={cx("task-brief-section-head")}>
              <h2>最近进展</h2>
              {task.thread_id && (
                <button
                  type="button"
                  className={cx(
                    "small",
                    "task-brief-action",
                    "task-brief-secondary",
                  )}
                  onClick={() => onOpenThread(task.thread_id)}
                >
                  查看执行过程 <Icon name="open" size={13} />
                </button>
              )}
            </div>
            {latestRunStatus ? (
              <div className={cx("task-brief-run")}>
                <div className={cx("task-brief-run-head")}>
                  <StatusBadge
                    label={latestRunBadge.label}
                    tone={latestRunBadge.tone}
                  />
                  <span>
                    {member(latestRun?.coordinator_id || task.owner_member_id)}
                  </span>
                  <span>
                    {formatRelativeTime(
                      latestRun?.created_at ?? view.last_run_started_at,
                    )}
                  </span>
                </div>
                <p className={cx("task-brief-run-text")}>{latestRunText}</p>
                {latestRun?.error && (
                  <details className={cx("task-advanced")}>
                    <summary>技术详情</summary>
                    <p className={cx("section-note")}>{latestRun.error}</p>
                  </details>
                )}
                {taskRuns.length > 1 && (
                  <details className={cx("task-advanced")}>
                    <summary>{taskRuns.length} 次执行记录</summary>
                    <ol className={cx("task-brief-activity")}>
                      {[...taskRuns]
                        .reverse()
                        .slice(0, 5)
                        .map((run) => {
                          const badge = runStatus(run.status);
                          return (
                            <li key={run.id}>
                              <time>{formatRelativeTime(run.created_at)}</time>
                              <div>
                                <StatusBadge
                                  label={badge.label}
                                  tone={badge.tone}
                                />{" "}
                                {runNote(run)}
                              </div>
                            </li>
                          );
                        })}
                    </ol>
                  </details>
                )}
              </div>
            ) : (
              <p className={cx("section-note")}>
                还没有开始执行。开始后，结果会回到这里。
              </p>
            )}
          </section>

          <section className={cx("task-brief-section")} aria-label="目标与验收">
            <div className={cx("task-brief-section-head")}>
              <h2>目标与验收</h2>
              <span className={cx("task-brief-section-meta")}>
                {progress.total
                  ? `${progress.passed}/${progress.total} 通过`
                  : "未设置验收项"}
              </span>
            </div>
            {progress.total > 0 && (
              <div className={cx("task-accept-bar")} aria-hidden="true">
                {Array.from({ length: acceptSegments }, (_, index) => (
                  <span
                    key={index}
                    className={cx(index < acceptFilled ? "on" : "")}
                  />
                ))}
              </div>
            )}
            <p className={cx("task-brief-goal")}>
              {taskGoalSummary(revision.goal)}
            </p>
            <details className={cx("task-brief-requirements")}>
              <summary>
                展开完整要求
                <Icon name="chevron" size={14} />
              </summary>
              <p className={cx("task-brief-goal-full")}>{revision.goal}</p>
            </details>
            {criteria.length > 0 && (
              <ul className={cx("task-criteria-summary")}>
                {criteria.map((criterion) => {
                  const saved = currentCompletion?.results?.find(
                    (result) => result.criterion_id === criterion.criterion_id,
                  );
                  return (
                    <li key={criterion.criterion_id}>
                      <span>{criterionText(criterion.statement)}</span>
                      <span
                        className={cx(
                          "task-criterion-result",
                          saved?.result === "passed" && "passed",
                        )}
                      >
                        {saved
                          ? resultLabels[saved.result] || saved.result
                          : "待验收"}
                      </span>
                    </li>
                  );
                })}
              </ul>
            )}
            {!acceptanceAvailable && (
              <p className={cx("section-note")}>
                {["completed", "canceled"].includes(focus.phase)
                  ? "当前任务已结束；验收记录保留在下方。"
                  : criteria.length === 0
                    ? "这个任务没有设置验收项；交付成果仍可在下方查看。"
                    : !latestRunStatus
                      ? "开始执行后，交付成果会出现在这里，之后可以验收。"
                      : failureStatuses.has(latestRunStatus)
                        ? "上次执行在完成前停止，暂无可以验收的交付。"
                        : candidateOptions.length === 0
                          ? "没有本任务成功交付的确切成果或回复；请打开会话查看或继续执行，不能凭空验收。"
                          : "执行已完成，但没有可展示的确切交付；请打开会话查看最终回复。"}
              </p>
            )}
            {!!completions.length && (
              <details className={cx("task-brief-records")}>
                <summary>{completions.length} 条验收记录</summary>
                <div className={cx("record-list")}>
                  {[...completions].reverse().map((item) => (
                    <article className={cx("project-record")} key={item.id}>
                      <strong>
                        {item.validity === "current" &&
                        item.task_revision === task.current_revision
                          ? "当前版本验收记录"
                          : `历史验收 · 目标 v${item.task_revision}`}
                      </strong>
                      <span>
                        {producerLabels[item.evidence_producer] ||
                          item.evidence_producer}{" "}
                        ·{" "}
                        {(item.results ?? [])
                          .map((result) => {
                            const criterion = criteria.find(
                              (value) =>
                                value.criterion_id === result.criterion_id,
                            );
                            return `${criterion ? criterionText(criterion.statement) : "验收项"}：${
                              resultLabels[result.result] || result.result
                            }`;
                          })
                          .join(" · ")}{" "}
                        · {formatDateTime(item.created_at)}
                      </span>
                    </article>
                  ))}
                </div>
              </details>
            )}
          </section>

          <section
            ref={deliverablesRef}
            className={cx("task-brief-section")}
            aria-label="成果"
          >
            <div className={cx("task-brief-section-head")}>
              <h2>成果</h2>
              <span className={cx("task-brief-section-meta")}>
                {artifacts.length
                  ? `${artifacts.length} 份`
                  : deliveredMessages.length
                    ? `${deliveredMessages.length} 条交付`
                    : ""}
              </span>
            </div>
            {artifacts.length ? (
              <div className={cx("task-brief-artifacts")}>
                {artifacts.map((item) => (
                  <div
                    className={cx("task-brief-artifact-wrap")}
                    key={artifactRef(item)}
                  >
                    <button
                      type="button"
                      className={cx("task-brief-artifact")}
                      onClick={() => onOpenArtifact(item)}
                    >
                      <span className={cx("task-brief-artifact-icon")}>
                        {item.type === "report" ? "▤" : "▥"}
                      </span>
                      <span>
                        <strong>
                          {item.title} · v{item.version}
                        </strong>
                        <small>
                          {artifactTypeLabel(item.type) || item.type} ·{" "}
                          {formatDateTime(item.created_at)}
                        </small>
                      </span>
                      <span aria-hidden="true">→</span>
                    </button>
                    {!!item.files?.length && (
                      <ul className={cx("task-brief-files")}>
                        {item.files.map((file) => (
                          <li key={file.source_id}>
                            <span>{file.name}</span>
                            <span>{fileSize(file.size)}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                ))}
              </div>
            ) : deliveredMessages.length ? (
              <div className={cx("task-brief-deliveries")}>
                {deliveredMessages.map((message) => (
                  <button
                    type="button"
                    className={cx("task-brief-artifact")}
                    key={message.id}
                    onClick={() => onOpenThread(task.thread_id)}
                  >
                    <span className={cx("task-brief-artifact-icon")}>▤</span>
                    <span>
                      <strong>{message.agent_name || "成员"} 的交付</strong>
                      <small>
                        {taskGoalSummary(message.content, 120)}
                        {" · "}
                        {formatDateTime(message.created_at)}
                      </small>
                    </span>
                    <span aria-hidden="true">→</span>
                  </button>
                ))}
              </div>
            ) : (
              <p className={cx("section-note")}>
                {!latestRunStatus
                  ? "开始执行后，交付成果会出现在这里。"
                  : failureStatuses.has(latestRunStatus)
                    ? "上次执行在完成前停止，暂无交付。"
                    : "执行已完成，但没有可展示的文件成果；请打开会话查看最终回复。"}
              </p>
            )}
          </section>

          <section className={cx("task-brief-section")} aria-label="子任务">
            <div className={cx("task-brief-section-head")}>
              <h2>
                子任务 · {completedChildren}/{children.length} 完成
              </h2>
              {!["paused", "canceled", "completed"].includes(task.status) && (
                <button
                  type="button"
                  className={cx("small", "ghost", "task-brief-action")}
                  onClick={() => setCreatingChild(true)}
                >
                  ＋ 拆分子任务
                </button>
              )}
            </div>
            {children.length ? (
              <div className={cx("task-brief-subtasks")}>
                {children.map((child) => {
                  const childMeta = taskFocusMeta(
                    child,
                    childQuestions[child.task.id] ?? [],
                  );
                  return (
                    <button
                      type="button"
                      className={cx("task-brief-subtask")}
                      key={child.task.id}
                      onClick={() => onOpenTask?.(child.task.id)}
                    >
                      <span
                        className={cx(
                          "task-focus-dot",
                          `task-focus-dot-${childMeta.tone}`,
                        )}
                        aria-hidden="true"
                      />
                      <span>
                        <strong>{taskTitle(child)}</strong>
                        <small>
                          {childMeta.label} ·{" "}
                          {member(child.task.owner_member_id)}
                        </small>
                      </span>
                      <span aria-hidden="true">→</span>
                    </button>
                  );
                })}
              </div>
            ) : (
              <p className={cx("section-note")}>
                可以把独立工作拆给不同成员，进度与成果回到这里。
              </p>
            )}
          </section>
        </div>

        <aside className={cx("task-brief-facts")} aria-label="任务属性">
          <h2>任务属性</h2>
          <dl>
            <div>
              <dt>状态</dt>
              <dd>
                <StatusBadge label={focusBadge.label} tone={focusBadge.tone} />
              </dd>
            </div>
            <div>
              <dt>负责人</dt>
              <dd>{member(task.owner_member_id)}</dd>
            </div>
            {workspaceName && (
              <div>
                <dt>项目</dt>
                <dd>{workspaceName}</dd>
              </div>
            )}
            <div>
              <dt>更新时间</dt>
              <dd>{formatRelativeTime(task.updated_at)}</dd>
            </div>
            <div>
              <dt>上次执行</dt>
              <dd>
                {latestRunStatus ? (
                  <StatusBadge
                    label={latestRunBadge.label}
                    tone={latestRunBadge.tone}
                  />
                ) : (
                  "尚未开始"
                )}
              </dd>
            </div>
            <div>
              <dt>关联会话</dt>
              <dd>
                {task.thread_id ? (
                  <button
                    type="button"
                    className={cx(
                      "small",
                      "task-brief-action",
                      "task-brief-secondary",
                    )}
                    onClick={() => onOpenThread(task.thread_id)}
                  >
                    打开会话 <Icon name="open" size={13} />
                  </button>
                ) : (
                  "开始执行时创建"
                )}
              </dd>
            </div>
            <div>
              <dt>验收</dt>
              <dd>
                {progress.total
                  ? `${progress.passed}/${progress.total} 通过`
                  : "未设置"}
              </dd>
            </div>
          </dl>
        </aside>
      </div>

      {(primaryAction || secondaryOpen) && (
        <div className={cx("task-brief-mobile-bar")}>
          {secondaryOpen && (
            <button
              className={cx(
                "small",
                "task-brief-action",
                "task-brief-secondary",
              )}
              onClick={() => onOpenThread(task.thread_id)}
            >
              打开会话
            </button>
          )}
          {primaryAction && (
            <button
              className={cx("primary", "task-brief-action")}
              disabled={!!busy}
              onClick={primaryAction.run}
            >
              {primaryAction.label}
            </button>
          )}
        </div>
      )}

      {creatingChild && (
        <Modal title="拆分子任务" close={() => setCreatingChild(false)}>
          <TaskCreateForm
            workspaceID={task.workspace_id}
            workspaces={{
              [task.workspace_id]: workspaceName || "当前工作空间",
            }}
            parentID={task.id}
            onCreated={(child, run) => {
              setCreatingChild(false);
              if (run) onOpenThread(run.thread_id);
              else onOpenTask?.(child.task.id);
              void load();
            }}
          />
        </Modal>
      )}

      {acceptance && acceptance.candidates.length > 0 && (
        <TaskAcceptanceDialog
          key={acceptance.id}
          goal={acceptance.goal}
          criteria={acceptance.criteria}
          candidates={acceptance.candidates}
          expired={acceptanceStale}
          refresh={refreshAcceptance}
          close={() => closeAcceptance(acceptance.id)}
          submit={async (submission) => {
            if (acceptanceStale) {
              throw new Error("验收依据已更新，请重新核对后再提交。");
            }
            const required = new Set(
              acceptance.criteria
                .filter((criterion) => criterion.required)
                .map((criterion) => criterion.criterion_id),
            );
            const unresolved = submission.results.some(
              (item) =>
                required.has(item.criterion_id) && item.result !== "passed",
            );
            await api(
              `/api/v1/tasks/${acceptance.taskID}/completions`,
              "POST",
              {
                evaluation_generation: acceptance.evaluationGeneration,
                input_snapshot_digest: acceptance.digest,
                candidate_refs: submission.candidate_refs,
                results: submission.results,
                judge_member_id: "user",
                evidence_producer: "human_confirmed",
                unresolved: unresolved ? ["仍有必需验收未通过或未核实"] : [],
              },
            );
            setNotice(unresolved ? "验收已记录。" : "验收已记录，任务已完成。");
            await load();
          }}
        />
      )}

      {confirmingRetry && (
        <ConfirmDialog
          title="确认继续执行"
          action="继续执行"
          close={() => setConfirmingRetry(false)}
          confirm={async () => {
            setConfirmingRetry(false);
            await continueExecution();
          }}
        >
          <p>
            上次执行可能产生了未核实的写入或命令结果。请先确认这些结果可以安全继续，再重试。
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}
