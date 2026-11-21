import type { ProjectTaskView, Question } from "../api/client";
import { isEnglish, t } from "./i18n";

export const taskColumns = [
  {
    id: "todo",
    get label() {
      return t("待办", "To do");
    },
    icon: "○",
  },
  {
    id: "active",
    get label() {
      return t("进行中", "In progress");
    },
    icon: "◐",
  },
  {
    id: "waiting",
    get label() {
      return t("等待处理", "Waiting");
    },
    icon: "◷",
  },
  {
    id: "review",
    get label() {
      return t("待验收", "Ready for review");
    },
    icon: "◉",
  },
  {
    id: "completed",
    get label() {
      return t("已验收", "Accepted");
    },
    icon: "✓",
  },
  {
    id: "canceled",
    get label() {
      return t("已取消", "Canceled");
    },
    icon: "×",
  },
] as const;

export type TaskBoardColumn = (typeof taskColumns)[number]["id"];

/** The user-visible phase. Every surface derives its copy and action from this. */
export type TaskPhase =
  | "needs_answer"
  | "needs_retry"
  | "needs_revalidation"
  | "running"
  | "not_started"
  | "paused"
  | "waiting_budget"
  | "waiting_other"
  | "review"
  | "completed"
  | "canceled";

/** The four focus groups plus the two archived filters. */
export type TaskFocus =
  | "attention"
  | "running"
  | "waiting"
  | "review"
  | "completed"
  | "canceled";

export type TaskTone =
  | "attention"
  | "failed"
  | "running"
  | "waiting"
  | "paused"
  | "review"
  | "done"
  | "canceled";

export type TaskActionKind =
  | "answer"
  | "retry"
  | "revalidate"
  | "open_execution"
  | "start"
  | "resume"
  | "budget"
  | "wait"
  | "review"
  | "view";

export type TaskAction = {
  kind: TaskActionKind;
  label: string;
};

export const taskFocusGroups: {
  id: TaskFocus;
  label: string;
  hint: string;
  filterLabel: string;
  ariaLabel: string;
}[] = [
  {
    id: "attention",
    get label() {
      return t("需要你", "Needs you");
    },
    get hint() {
      return t("回答、继续或处理失败", "Answer, resume, or handle failures");
    },
    get filterLabel() {
      return t("需要你", "Needs you");
    },
    get ariaLabel() {
      return t("需要你任务", "Tasks needing attention");
    },
  },
  {
    id: "running",
    get label() {
      return t("进行中", "In progress");
    },
    get hint() {
      return t("按最近更新排序", "Sorted by recent updates");
    },
    get filterLabel() {
      return t("进行中", "In progress");
    },
    get ariaLabel() {
      return t("进行中任务", "In-progress tasks");
    },
  },
  {
    id: "waiting",
    get label() {
      return t("待推进", "Up next");
    },
    get hint() {
      return t(
        "未开始、暂停或等待条件",
        "Not started, paused, or waiting on conditions",
      );
    },
    get filterLabel() {
      return t("待推进", "Up next");
    },
    get ariaLabel() {
      return t("待推进任务", "Up-next tasks");
    },
  },
  {
    id: "review",
    get label() {
      return t("待验收", "Ready for review");
    },
    get hint() {
      return t(
        "运行成功不等于已完成",
        "Verify deliverables before marking accepted",
      );
    },
    get filterLabel() {
      return t("待验收", "Ready for review");
    },
    get ariaLabel() {
      return t("待验收任务", "Tasks ready for review");
    },
  },
  {
    id: "completed",
    get label() {
      return t("已验收", "Accepted");
    },
    hint: "",
    get filterLabel() {
      return t("已验收", "Accepted");
    },
    get ariaLabel() {
      return t("已验收任务", "Accepted tasks");
    },
  },
  {
    id: "canceled",
    get label() {
      return t("已取消", "Canceled");
    },
    hint: "",
    get filterLabel() {
      return t("已取消", "Canceled");
    },
    get ariaLabel() {
      return t("已取消任务", "Canceled tasks");
    },
  },
];

export const taskFocusFilters: { value: TaskFocus | ""; label: string }[] = [
  {
    value: "",
    get label() {
      return t("当前任务", "Active tasks");
    },
  },
  ...taskFocusGroups.map((group) => ({
    value: group.id,
    get label() {
      return group.filterLabel;
    },
  })),
];

const waitingLabelsZh: Record<string, string> = {
  budget: "等待预算",
  fix_limit: "等待修复额度",
  no_progress: "等待新进展",
  question: "等待你的回答",
  external: "等待外部条件",
  dependency: "等待依赖任务",
  environment: "等待环境",
  uncertain_effect: "等待副作用核实",
};

const waitingLabelsEn: Record<string, string> = {
  budget: "Waiting for budget",
  fix_limit: "Waiting for fix allowance",
  no_progress: "Waiting for progress",
  question: "Waiting for your answer",
  external: "Waiting for external condition",
  dependency: "Waiting for dependency",
  environment: "Waiting for environment",
  uncertain_effect: "Waiting for side-effect verification",
};

export const waitingLabels: Record<string, string> = new Proxy(
  waitingLabelsZh,
  {
    get(_target, prop: string) {
      const dict = isEnglish() ? waitingLabelsEn : waitingLabelsZh;
      return dict[prop];
    },
  },
);

const activeRuns = new Set(["queued", "running", "canceling"]);
const retryRuns = new Set(["failed", "interrupted", "canceled"]);

const failureLabelsZh: Record<string, string> = {
  network: "无法连接本机 CLI 的端点",
  authentication: "本机 CLI 认证失败",
  quota: "额度或频率受限",
  context_overflow: "上下文超出限制",
  host_input_limit: "输入超出限制",
  first_response_timeout: "模型长时间没有响应",
  stall_timeout: "执行超时",
  permissions: "当前授权不足",
  tool_unavailable: "本机 CLI 缺少所需能力",
  model_unavailable: "本机 CLI 或模型不可用",
  provider_transient: "模型服务暂时不可用",
  process: "执行进程异常结束",
  process_uncertain: "上次执行可能产生了未核实结果",
  protocol: "本机 CLI 输出异常",
  unknown: "上次执行失败",
};

const failureLabelsEn: Record<string, string> = {
  network: "Could not connect to the local CLI endpoint",
  authentication: "Local CLI authentication failed",
  quota: "Quota or rate limit reached",
  context_overflow: "Context window limit exceeded",
  host_input_limit: "Input size limit exceeded",
  first_response_timeout: "Model did not respond in time",
  stall_timeout: "Execution timed out",
  permissions: "Insufficient permissions",
  tool_unavailable: "Local CLI is missing required capabilities",
  model_unavailable: "Local CLI or model is unavailable",
  provider_transient: "Model service is temporarily unavailable",
  process: "Execution process exited unexpectedly",
  process_uncertain: "Previous run may have unverified side effects",
  protocol: "Unexpected output from local CLI",
  unknown: "Previous run failed",
};

const failureLabels: Record<string, string> = new Proxy(failureLabelsZh, {
  get(_target, prop: string) {
    const dict = isEnglish() ? failureLabelsEn : failureLabelsZh;
    return dict[prop];
  },
});

export function taskTitle(view: ProjectTaskView) {
  return view.task.title || view.revision.goal.split("\n")[0].slice(0, 100);
}

export function openQuestions(questions: Question[], taskID: string) {
  return questions.filter(
    (question) => question.task_id === taskID && question.status === "open",
  );
}

/** A bounded, deterministic summary of the accepted goal. */
export function taskGoalSummary(goal: string, max = 180) {
  const text = goal.trim().replace(/\s+/g, " ");
  if (text.length <= max) return text;
  const slice = text.slice(0, max);
  const boundary = Math.max(
    slice.lastIndexOf("。"),
    slice.lastIndexOf("；"),
    slice.lastIndexOf(". "),
    slice.lastIndexOf("; "),
    slice.lastIndexOf("，"),
    slice.lastIndexOf(", "),
  );
  const cutoff = boundary > max * 0.45 ? boundary + 1 : max;
  return `${slice.slice(0, cutoff).trim()}…`;
}

/** A one-line, user-facing explanation of the latest terminal run. */
export function taskFailureText(view: ProjectTaskView) {
  const kind = view.last_run_failure_kind ?? "";
  if (failureLabels[kind]) return failureLabels[kind];
  const error = view.last_run_error ?? "";
  if (/network|connection|connect|endpoint/i.test(error)) {
    return failureLabels.network;
  }
  if (/auth|unauthor|forbidden|401|403/i.test(error)) {
    return failureLabels.authentication;
  }
  if (/quota|rate limit|429/i.test(error)) {
    return failureLabels.quota;
  }
  if (/timeout|timed out/i.test(error)) {
    return failureLabels.stall_timeout;
  }
  if (view.task.waiting_reason === "uncertain_effect") {
    return failureLabels.process_uncertain;
  }
  switch (view.last_run_status) {
    case "canceled":
      return t("上次执行在完成前停止", "Previous run stopped before finishing");
    case "yielded":
      return t(
        "上次执行主动让出，尚未完成",
        "Previous run paused to wait for follow-up",
      );
    case "interrupted":
      return t("上次执行被中断", "Previous run was interrupted");
    case "failed":
      return failureLabels.unknown;
    default:
      return "";
  }
}

/** The single phase classifier shared by the list, board and detail. */
export function taskPhase(
  view: ProjectTaskView,
  questions: Question[] = [],
): TaskPhase {
  if (view.task.status === "completed") {
    return view.task.evaluation_status === "needs_revalidation"
      ? "needs_revalidation"
      : "completed";
  }
  if (view.task.status === "canceled") return "canceled";

  const hasQuestion =
    (view.open_questions ?? 0) > 0 ||
    openQuestions(questions, view.task.id).length > 0 ||
    view.task.waiting_reason === "question";
  if (hasQuestion) return "needs_answer";
  // A pending authorization is the same "needs you" state as a question.
  if ((view.pending_permissions ?? 0) > 0) return "needs_answer";
  if (view.task.status === "paused") return "paused";

  const reason = view.task.waiting_reason ?? "";
  if (reason === "budget") return "waiting_budget";
  if (reason === "uncertain_effect") return "needs_retry";
  if (reason) return "waiting_other";

  const runStatus = view.last_run_status ?? "";
  if (activeRuns.has(runStatus)) return "running";
  if (retryRuns.has(runStatus)) return "needs_retry";
  if (runStatus === "yielded") return "waiting_other";
  if (view.task.evaluation_status === "needs_revalidation") {
    return "needs_revalidation";
  }
  // A parent with unfinished children has not converged: its own success never
  // means the objective is ready for review.
  if ((view.active_children_count ?? 0) > 0) {
    return runStatus === "succeeded" ? "running" : "waiting_other";
  }
  if (runStatus === "succeeded") return "review";
  if (view.task.status === "active") {
    return runStatus ? "needs_retry" : "running";
  }
  if (view.task.status === "waiting") return "waiting_other";
  if (!runStatus) return "not_started";
  return "waiting_other";
}

export type TaskPhaseMeta = {
  phase: TaskPhase;
  group: TaskFocus;
  column: TaskBoardColumn;
  label: string;
  tone: TaskTone;
  current: string;
  next: string;
  action: TaskAction;
};

function truncate(text: string, max: number) {
  return text.length > max ? `${text.slice(0, max).trim()}…` : text;
}

/** The copy and primary action for every user-visible phase. */
export function taskPhaseMeta(
  view: ProjectTaskView,
  questions: Question[] = [],
): TaskPhaseMeta {
  const phase = taskPhase(view, questions);
  const question = openQuestions(questions, view.task.id)[0];
  switch (phase) {
    case "needs_answer": {
      // An authorization request is answered by the approval card, not by a
      // question answer; the label stays honest about which one it is.
      const pending = (view.pending_permissions ?? 0) > 0;
      return {
        phase,
        group: "attention",
        column: "waiting",
        label: t("需要你", "Needs you"),
        tone: "attention",
        current: pending
          ? t(
              `等待你授权 ${view.pending_permissions} 个操作`,
              `Waiting for your approval on ${view.pending_permissions} action(s)`,
            )
          : question
            ? t(
                `等待你回答：${truncate(question.body, 60)}`,
                `Waiting for your answer: ${truncate(question.body, 60)}`,
              )
            : t("等待你的回答", "Waiting for your answer"),
        next: pending
          ? t("在会话中批准或拒绝", "Approve or deny in conversation")
          : t("回答后继续", "Continue after answering"),
        action: {
          kind: pending ? "open_execution" : "answer",
          label: pending
            ? t("去审批", "Review request")
            : t("回答问题", "Answer question"),
        },
      };
    }
    case "needs_retry": {
      const failed =
        view.last_run_status === "failed" ||
        view.task.waiting_reason === "uncertain_effect";
      const interrupted = view.last_run_status === "interrupted";
      return {
        phase,
        group: "attention",
        column: "waiting",
        label: failed
          ? t("执行失败", "Failed")
          : interrupted
            ? t("执行已中断", "Interrupted")
            : t("执行已停止", "Stopped"),
        tone: failed ? "failed" : "paused",
        current:
          taskFailureText(view) ||
          t("上次执行没有完成", "Previous run did not finish"),
        next:
          view.task.waiting_reason === "uncertain_effect"
            ? t("先核实副作用再继续", "Verify side effects before continuing")
            : interrupted
              ? t("恢复未完成的执行", "Resume unfinished run")
              : t("只补跑未完成步骤", "Re-run only unfinished steps"),
        action: {
          kind: "retry",
          label: interrupted
            ? t("恢复执行", "Resume run")
            : t("从停止处继续", "Continue from stop"),
        },
      };
    }
    case "needs_revalidation":
      return {
        phase,
        group: "review",
        column: "review",
        label: t("待复验", "Needs re-check"),
        tone: "review",
        current: t(
          "输入或依赖已变化，结果需要复核",
          "Inputs or dependencies changed; results need re-verification",
        ),
        next: t(
          "重新核对成果并记录验收",
          "Re-verify deliverables and record acceptance",
        ),
        action: {
          kind: "revalidate",
          label: t("去复验", "Re-verify"),
        },
      };
    case "running":
      return {
        phase,
        group: "running",
        column: "active",
        label: t("进行中", "In progress"),
        tone: "running",
        current: t("正在执行", "Running"),
        next: t("完成后进入待验收", "Moves to review when finished"),
        action: {
          kind: "open_execution",
          label: t("查看执行过程", "View run"),
        },
      };
    case "not_started":
      return {
        phase,
        group: "waiting",
        column: "todo",
        label: t("待开始", "Not started"),
        tone: "waiting",
        current: t("尚未开始", "Not started yet"),
        next: t("创建会话并开始执行", "Open conversation and start run"),
        action: {
          kind: "start",
          label: t("开始执行", "Start run"),
        },
      };
    case "paused":
      return {
        phase,
        group: "waiting",
        column: "waiting",
        label: t("已暂停", "Paused"),
        tone: "paused",
        current: t("任务已暂停", "Task is paused"),
        next: t("恢复后继续", "Resume to continue"),
        action: {
          kind: "resume",
          label: t("恢复任务", "Resume task"),
        },
      };
    case "waiting_budget":
      return {
        phase,
        group: "waiting",
        column: "waiting",
        label: t("等待预算", "Waiting for budget"),
        tone: "waiting",
        current: t("等待预算", "Waiting for budget"),
        next: t("调整预算后继续", "Adjust budget to continue"),
        action: {
          kind: "budget",
          label: t("调整预算", "Adjust budget"),
        },
      };
    case "waiting_other": {
      if (!view.task.waiting_reason && view.last_run_status === "yielded") {
        return {
          phase,
          group: "waiting",
          column: "waiting",
          label: t("已让出", "Yielded"),
          tone: "waiting",
          current: t(
            "上次执行主动让出，尚未完成",
            "Previous run paused to wait for follow-up",
          ),
          next: t("查看执行过程", "View run"),
          action: { kind: "wait", label: t("查看任务", "View task") },
        };
      }
      const reason = view.task.waiting_reason ?? "";
      return {
        phase,
        group: "waiting",
        column: "waiting",
        label: reason
          ? waitingLabels[reason] || t("等待条件", "Waiting on condition")
          : t("等待推进", "Waiting to proceed"),
        tone: "waiting",
        current:
          waitingLabels[reason] || t("等待下一步", "Waiting for next step"),
        next: reason
          ? t("满足条件后自动继续", "Continues automatically once met")
          : t("查看任务说明或打开会话", "View task details or open conversation"),
        action: { kind: "wait", label: t("查看任务", "View task") },
      };
    }
    case "review":
      return {
        phase,
        group: "review",
        column: "review",
        label: t("待验收", "Ready for review"),
        tone: "review",
        current: t("已交付，等待验收", "Delivered, awaiting acceptance"),
        next: t(
          "核对成果并记录验收",
          "Verify deliverables and record acceptance",
        ),
        action: { kind: "review", label: t("去验收", "Review") },
      };
    case "completed":
      return {
        phase,
        group: "completed",
        column: "completed",
        label: t("已验收", "Accepted"),
        tone: "done",
        current: t("目标已验收", "Objective accepted"),
        next: t(
          "查看成果与验收记录",
          "View deliverables and acceptance record",
        ),
        action: { kind: "view", label: t("查看成果", "View deliverables") },
      };
    case "canceled":
      return {
        phase,
        group: "canceled",
        column: "canceled",
        label: t("已取消", "Canceled"),
        tone: "canceled",
        current: t("任务已取消", "Task canceled"),
        next: t("只读，不再唤醒", "Read-only; will not resume"),
        action: { kind: "view", label: t("查看任务", "View task") },
      };
  }
}

/** Backwards-compatible group projection used by filters and summaries. */
export function taskFocus(
  view: ProjectTaskView,
  questions: Question[] = [],
): TaskFocus {
  return taskPhaseMeta(view, questions).group;
}

export type TaskFocusMeta = {
  focus: TaskFocus;
  phase: TaskPhase;
  column: TaskBoardColumn;
  label: string;
  tone: TaskTone;
  current: string;
  next: string;
  action: TaskAction;
};

/** Backwards-compatible meta projection with the canonical phase attached. */
export function taskFocusMeta(
  view: ProjectTaskView,
  questions: Question[] = [],
): TaskFocusMeta {
  const meta = taskPhaseMeta(view, questions);
  return {
    focus: meta.group,
    phase: meta.phase,
    column: meta.column,
    label: meta.label,
    tone: meta.tone,
    current: meta.current,
    next: meta.next,
    action: meta.action,
  };
}

/** Current accepted criteria for the displayed goal revision. */
export function taskAcceptance(view: ProjectTaskView) {
  const criteria = view.revision.acceptance ?? [];
  const ids = new Set(criteria.map((criterion) => criterion.criterion_id));
  const completions = view.completions ?? [];
  const current = [...completions]
    .reverse()
    .find(
      (completion) =>
        completion.validity === "current" &&
        completion.task_revision === view.task.current_revision &&
        completion.evaluation_generation === view.task.evaluation_generation &&
        completion.input_snapshot_digest === view.revision.digest,
    );
  const passed =
    current?.results?.filter(
      (result) => ids.has(result.criterion_id) && result.result === "passed",
    ).length ?? 0;
  return { passed, total: criteria.length };
}

export function taskColumn(
  view: ProjectTaskView,
  questions: Question[] = [],
) {
  return taskPhaseMeta(view, questions).column;
}
