import type { ProjectTaskView, Question } from "../api/client";

export const taskColumns = [
  { id: "todo", label: "待办", icon: "○" },
  { id: "active", label: "进行中", icon: "◐" },
  { id: "waiting", label: "等待处理", icon: "◷" },
  { id: "review", label: "待验收", icon: "◉" },
  { id: "completed", label: "已验收", icon: "✓" },
  { id: "canceled", label: "已取消", icon: "×" },
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
    label: "需要你",
    hint: "回答、继续或处理失败",
    filterLabel: "需要你",
    ariaLabel: "需要你任务",
  },
  {
    id: "running",
    label: "进行中",
    hint: "按最近更新排序",
    filterLabel: "进行中",
    ariaLabel: "进行中任务",
  },
  {
    id: "waiting",
    label: "待推进",
    hint: "未开始、暂停或等待条件",
    filterLabel: "待推进",
    ariaLabel: "待推进任务",
  },
  {
    id: "review",
    label: "待验收",
    hint: "运行成功不等于已完成",
    filterLabel: "待验收",
    ariaLabel: "待验收任务",
  },
  {
    id: "completed",
    label: "已验收",
    hint: "",
    filterLabel: "已验收",
    ariaLabel: "已验收任务",
  },
  {
    id: "canceled",
    label: "已取消",
    hint: "",
    filterLabel: "已取消",
    ariaLabel: "已取消任务",
  },
];

export const taskFocusFilters: { value: TaskFocus | ""; label: string }[] = [
  { value: "", label: "当前任务" },
  ...taskFocusGroups.map((group) => ({
    value: group.id,
    label: group.filterLabel,
  })),
];

export const waitingLabels: Record<string, string> = {
  budget: "等待预算",
  fix_limit: "等待修复额度",
  no_progress: "等待新进展",
  question: "等待你的回答",
  external: "等待外部条件",
  dependency: "等待依赖任务",
  environment: "等待环境",
  uncertain_effect: "等待副作用核实",
};

const activeRuns = new Set(["queued", "running", "canceling"]);
const retryRuns = new Set(["failed", "interrupted", "canceled"]);

const failureLabels: Record<string, string> = {
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
      return "上次执行在完成前停止";
    case "yielded":
      return "上次执行主动让出，尚未完成";
    case "interrupted":
      return "上次执行被中断";
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
    openQuestions(questions, view.task.id).length > 0 ||
    view.task.waiting_reason === "question";
  if (hasQuestion) return "needs_answer";
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
    case "needs_answer":
      return {
        phase,
        group: "attention",
        column: "waiting",
        label: "需要你",
        tone: "attention",
        current: question ? `等待你回答：${truncate(question.body, 60)}` : "等待你的回答",
        next: "回答后继续",
        action: { kind: "answer", label: "回答问题" },
      };
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
          ? "执行失败"
          : interrupted
            ? "执行已中断"
            : "执行已停止",
        tone: failed ? "failed" : "paused",
        current: taskFailureText(view) || "上次执行没有完成",
        next:
          view.task.waiting_reason === "uncertain_effect"
            ? "先核实副作用再继续"
            : interrupted
              ? "恢复未完成的执行"
              : "只补跑未完成步骤",
        action: {
          kind: "retry",
          label: interrupted ? "恢复执行" : "从停止处继续",
        },
      };
    }
    case "needs_revalidation":
      return {
        phase,
        group: "review",
        column: "review",
        label: "待复验",
        tone: "review",
        current: "输入或依赖已变化，结果需要复核",
        next: "重新核对成果并记录验收",
        action: { kind: "revalidate", label: "去复验" },
      };
    case "running":
      return {
        phase,
        group: "running",
        column: "active",
        label: "进行中",
        tone: "running",
        current: "正在执行",
        next: "完成后进入待验收",
        action: { kind: "open_execution", label: "查看执行过程" },
      };
    case "not_started":
      return {
        phase,
        group: "waiting",
        column: "todo",
        label: "待开始",
        tone: "waiting",
        current: "尚未开始",
        next: "创建会话并开始执行",
        action: { kind: "start", label: "开始执行" },
      };
    case "paused":
      return {
        phase,
        group: "waiting",
        column: "waiting",
        label: "已暂停",
        tone: "paused",
        current: "任务已暂停",
        next: "恢复后继续",
        action: { kind: "resume", label: "恢复任务" },
      };
    case "waiting_budget":
      return {
        phase,
        group: "waiting",
        column: "waiting",
        label: "等待预算",
        tone: "waiting",
        current: "等待预算",
        next: "调整预算后继续",
        action: { kind: "budget", label: "调整预算" },
      };
    case "waiting_other": {
      if (!view.task.waiting_reason && view.last_run_status === "yielded") {
        return {
          phase,
          group: "waiting",
          column: "waiting",
          label: "已让出",
          tone: "waiting",
          current: "上次执行主动让出，尚未完成",
          next: "查看执行过程",
          action: { kind: "wait", label: "查看任务" },
        };
      }
      const reason = view.task.waiting_reason ?? "";
      return {
        phase,
        group: "waiting",
        column: "waiting",
        label: reason ? waitingLabels[reason] || "等待条件" : "等待推进",
        tone: "waiting",
        current: waitingLabels[reason] || "等待下一步",
        next: reason ? "满足条件后自动继续" : "查看任务说明或打开会话",
        action: { kind: "wait", label: "查看任务" },
      };
    }
    case "review":
      return {
        phase,
        group: "review",
        column: "review",
        label: "待验收",
        tone: "review",
        current: "已交付，等待验收",
        next: "核对成果并记录验收",
        action: { kind: "review", label: "去验收" },
      };
    case "completed":
      return {
        phase,
        group: "completed",
        column: "completed",
        label: "已验收",
        tone: "done",
        current: "目标已验收",
        next: "查看成果与验收记录",
        action: { kind: "view", label: "查看成果" },
      };
    case "canceled":
      return {
        phase,
        group: "canceled",
        column: "canceled",
        label: "已取消",
        tone: "canceled",
        current: "任务已取消",
        next: "只读，不再唤醒",
        action: { kind: "view", label: "查看任务" },
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
