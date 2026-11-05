import { useMemo, useState } from "react";
import {
  api,
  type ProjectTaskView,
  type Question,
} from "../api/client";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";

const cx = scoped(base, controls, styles);

type Filter = "open" | "attention" | "done" | "all";

const filters: [Filter, string][] = [
  ["open", "待办"],
  ["attention", "需要我"],
  ["done", "已验收"],
  ["all", "全部"],
];

const statusLabels: Record<string, string> = {
  draft: "草稿",
  ready: "待推进",
  active: "正在推进",
  waiting: "等待条件",
  paused: "已暂停",
  completed: "已验收",
  canceled: "已取消",
};

const waitingLabels: Record<string, string> = {
  budget: "等待预算",
  fix_limit: "等待修复额度",
  no_progress: "等待新进展",
  question: "等待你的回答",
  external: "等待外部条件",
  dependency: "等待依赖任务",
  environment: "等待环境",
};

const statusOrder: Record<string, number> = {
  attention: 0,
  active: 1,
  ready: 2,
  waiting: 3,
  paused: 4,
  completed: 5,
  canceled: 6,
};

function statusOf(
  task: ProjectTaskView,
  questions: Question[],
): { key: string; label: string; icon: string } {
  if (questions.length > 0) {
    return { key: "attention", label: "需要你处理", icon: "!" };
  }
  const key = task.task.status;
  return {
    key,
    label: statusLabels[key] || key,
    icon:
      key === "active"
        ? "◐"
        : key === "waiting"
          ? "◷"
          : key === "paused"
            ? "Ⅱ"
            : key === "completed"
              ? "✓"
              : key === "canceled"
                ? "×"
                : "○",
  };
}

/** A compact project todo list whose state comes from durable task records. */
export function TaskTodoList({
  tasks,
  questions,
  workspaces,
  onOpenTask,
  onChanged,
}: {
  tasks: ProjectTaskView[];
  questions: Question[];
  workspaces?: Record<string, string>;
  onOpenTask: (workspaceID: string, taskID: string) => void;
  onChanged: () => Promise<void> | void;
}) {
  const [filter, setFilter] = useState<Filter>("open");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const openQuestions = useMemo(() => {
    const byTask = new Map<string, Question[]>();
    for (const question of questions) {
      if (question.status !== "open") continue;
      const list = byTask.get(question.task_id) ?? [];
      list.push(question);
      byTask.set(question.task_id, list);
    }
    return byTask;
  }, [questions]);

  const roots = useMemo(() => {
    // Keep the list focused on root work, but never hide a child task that is
    // currently waiting for the user's answer.
    const top = tasks.filter(
      (item) =>
        !item.task.parent_id || (openQuestions.get(item.task.id) ?? []).length,
    );
    return top.length ? top : tasks;
  }, [tasks, openQuestions]);

  const rows = useMemo(() => {
    const visible = roots.filter((item) => {
      const status = item.task.status;
      const needsAttention = (openQuestions.get(item.task.id) ?? []).length > 0;
      if (filter === "attention") return needsAttention;
      if (filter === "done") return status === "completed";
      if (filter === "all") return true;
      return status !== "completed" && status !== "canceled";
    });
    return [...visible].sort((a, b) => {
      const aStatus = openQuestions.has(a.task.id)
        ? "attention"
        : a.task.status;
      const bStatus = openQuestions.has(b.task.id)
        ? "attention"
        : b.task.status;
      const order =
        (statusOrder[aStatus] ?? 99) - (statusOrder[bStatus] ?? 99);
      if (order !== 0) return order;
      return b.task.updated_at.localeCompare(a.task.updated_at);
    });
  }, [roots, openQuestions, filter]);

  const counts = useMemo(() => {
    const result: Record<Filter, number> = {
      open: 0,
      attention: 0,
      done: 0,
      all: roots.length,
    };
    for (const item of roots) {
      const needsAttention = (openQuestions.get(item.task.id) ?? []).length > 0;
      if (needsAttention) result.attention++;
      if (item.task.status === "completed") result.done++;
      if (
        item.task.status !== "completed" &&
        item.task.status !== "canceled"
      )
        result.open++;
    }
    return result;
  }, [roots, openQuestions]);

  const act = async (taskID: string, action: "pause" | "resume") => {
    if (busy) return;
    setBusy(taskID);
    setError("");
    try {
      await api(`/api/v1/tasks/${taskID}/actions`, "POST", {
        action,
        request_id: crypto.randomUUID(),
      });
      await onChanged();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };

  return (
    <section aria-labelledby="project-todo">
      <div className={cx("todo-heading")}>
        <h2 id="project-todo">任务</h2>
        <span className={cx("todo-count")}>{counts.open} 项待办</span>
      </div>
      <div className={cx("todo-filters")} role="group" aria-label="筛选任务">
        {filters.map(([key, label]) => (
          <button
            key={key}
            type="button"
            className={cx("todo-filter")}
            aria-pressed={filter === key}
            onClick={() => setFilter(key)}
          >
            {label}
            <span aria-hidden="true">{counts[key]}</span>
          </button>
        ))}
      </div>
      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}
      {rows.length ? (
        <ul className={cx("todo-list")} role="list">
          {rows.map((item) => {
            const taskQuestions = openQuestions.get(item.task.id) ?? [];
            const status = statusOf(item, taskQuestions);
            const remaining =
              item.budget.policy.auto_continuations -
              item.budget.used_continuations -
              item.budget.reserved_runs;
            const waitingReason = item.task.waiting_reason ?? "";
            const detail =
              status.key === "attention"
                ? taskQuestions[0]?.body
                : waitingLabels[waitingReason] || waitingReason;
            return (
              <li
                key={item.task.id}
                className={cx(`todo-item ${status.key}`)}
              >
                <span
                  className={cx(`todo-status ${status.key}`)}
                  aria-hidden="true"
                >
                  {status.icon}
                </span>
                <div className={cx("todo-main")}>
                  <button
                    type="button"
                    className={cx("todo-title")}
                    onClick={() => onOpenTask(item.task.workspace_id, item.task.id)}
                  >
                    {item.revision.goal}
                  </button>
                  <span className={cx("todo-meta")}>
                    <span>{status.label}</span>
                    {workspaces?.[item.task.workspace_id] && (
                      <span>{workspaces[item.task.workspace_id]}</span>
                    )}
                    <span>目标 v{item.task.current_revision}</span>
                    {detail && <span>{detail}</span>}
                    {status.key !== "completed" &&
                      status.key !== "canceled" && (
                        <span>剩余续办 {remaining} 次</span>
                      )}
                    {item.task.evaluation_status === "needs_revalidation" && (
                      <span>当前结果待复验</span>
                    )}
                    {taskQuestions.length > 1 && (
                      <span>另有 {taskQuestions.length - 1} 个问题</span>
                    )}
                  </span>
                </div>
                <div className={cx("todo-actions")}>
                  {item.task.status === "active" && (
                    <button
                      type="button"
                      className={cx("small")}
                      disabled={!!busy}
                      onClick={() => void act(item.task.id, "pause")}
                    >
                      暂停
                    </button>
                  )}
                  {item.task.status === "paused" && (
                    <button
                      type="button"
                      className={cx("small")}
                      disabled={!!busy}
                      onClick={() => void act(item.task.id, "resume")}
                    >
                      恢复
                    </button>
                  )}
                  <button
                    type="button"
                    className={cx("small")}
                    onClick={() => onOpenTask(item.task.workspace_id, item.task.id)}
                  >
                    打开
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      ) : (
        <p className={cx("project-empty")}>
          {filter === "attention"
            ? "没有需要你处理的问题。"
            : filter === "done"
              ? "还没有通过验收的任务。"
              : "没有待办任务。直接在下方描述目标，或点击新任务。"}
        </p>
      )}
    </section>
  );
}
