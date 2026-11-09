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

type Category =
  | "todo"
  | "running"
  | "failed"
  | "attention"
  | "done"
  | "canceled";

const categories: { key: Category; label: string; hint: string }[] = [
  { key: "todo", label: "待办", hint: "尚未开始或等待条件" },
  { key: "running", label: "进行中", hint: "当前有执行分支在推进" },
  { key: "failed", label: "失败", hint: "最近执行失败或等待修复" },
  { key: "attention", label: "需要我", hint: "有等待你回答的问题" },
  { key: "done", label: "已验收", hint: "当前目标版本已通过验收" },
  { key: "canceled", label: "已取消", hint: "任务已停止，不再唤醒" },
];

const defaultSelected: Category[] = categories.map((item) => item.key);

const waitingLabels: Record<string, string> = {
  budget: "等待预算",
  fix_limit: "等待修复额度",
  no_progress: "等待新进展",
  question: "等待你的回答",
  external: "等待外部条件",
  dependency: "等待依赖任务",
  environment: "等待环境",
  uncertain_effect: "等待副作用核实",
};

const failedRuns = new Set(["failed", "interrupted", "uncertain"]);

function categoriesFor(
  task: ProjectTaskView,
  questions: Question[],
): Set<Category> {
  const result = new Set<Category>();
  const status = task.task.status;
  const reason = task.task.waiting_reason ?? "";
  const runStatus = task.last_run_status ?? "";
  if (questions.length > 0) result.add("attention");
  if (status === "active" || ["queued", "running", "canceling"].includes(runStatus)) {
    result.add("running");
  }
  if (
    failedRuns.has(runStatus) ||
    reason === "fix_limit" ||
    reason === "no_progress" ||
    reason === "uncertain_effect"
  ) {
    result.add("failed");
  }
  if (["draft", "ready", "waiting", "paused"].includes(status)) {
    result.add("todo");
  }
  if (status === "completed") result.add("done");
  if (status === "canceled") result.add("canceled");
  return result;
}

function statusMeta(
  task: ProjectTaskView,
  questions: Question[],
): { label: string; tone: string; icon: string } {
  const categories = categoriesFor(task, questions);
  if (questions.length > 0)
    return { label: "需要你处理", tone: "attention", icon: "!" };
  if (categories.has("failed"))
    return { label: "执行失败", tone: "failed", icon: "!" };
  if (categories.has("running"))
    return { label: "进行中", tone: "running", icon: "◐" };
  if (task.task.status === "waiting")
    return { label: "等待条件", tone: "waiting", icon: "◷" };
  if (task.task.status === "paused")
    return { label: "已暂停", tone: "paused", icon: "Ⅱ" };
  if (task.task.status === "completed")
    return { label: "已验收", tone: "done", icon: "✓" };
  if (task.task.status === "canceled")
    return { label: "已取消", tone: "canceled", icon: "×" };
  return { label: "待推进", tone: "todo", icon: "○" };
}

/** A global task board with multi-select status filters and inline actions. */
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
  const [selected, setSelected] = useState<Set<Category>>(
    () => new Set(defaultSelected),
  );
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
    const top = tasks.filter(
      (item) =>
        !item.task.parent_id || (openQuestions.get(item.task.id) ?? []).length,
    );
    return top.length ? top : tasks;
  }, [tasks, openQuestions]);

  const counts = useMemo(() => {
    const result = Object.fromEntries(
      categories.map((category) => [category.key, 0]),
    ) as Record<Category, number>;
    for (const item of roots) {
      for (const category of categoriesFor(
        item,
        openQuestions.get(item.task.id) ?? [],
      )) {
        result[category]++;
      }
    }
    return result;
  }, [roots, openQuestions]);

  const rows = useMemo(() => {
    return roots
      .filter((item) => {
        const taskCategories = categoriesFor(
          item,
          openQuestions.get(item.task.id) ?? [],
        );
        return [...taskCategories].some((category) => selected.has(category));
      })
      .sort((a, b) => {
        const aMeta = statusMeta(a, openQuestions.get(a.task.id) ?? []);
        const bMeta = statusMeta(b, openQuestions.get(b.task.id) ?? []);
        const order: Record<string, number> = {
          attention: 0,
          failed: 1,
          running: 2,
          waiting: 3,
          paused: 4,
          todo: 5,
          done: 6,
          canceled: 7,
        };
        const rank = (order[aMeta.tone] ?? 99) - (order[bMeta.tone] ?? 99);
        if (rank !== 0) return rank;
        return b.task.updated_at.localeCompare(a.task.updated_at);
      });
  }, [roots, openQuestions, selected]);

  const allSelected = selected.size === categories.length;

  const toggle = (category: Category) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(category)) next.delete(category);
      else next.add(category);
      return next;
    });
  };

  const selectAll = () => {
    setSelected(new Set(categories.map((item) => item.key)));
  };

  const clearAll = () => {
    setSelected(new Set());
  };

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
    <section className={cx("todo-section")} aria-label="任务">
      <div className={cx("todo-toolbar")}>
        <div className={cx("todo-filters")} role="group" aria-label="筛选任务状态">
          {categories.map((category) => (
            <button
              key={category.key}
              type="button"
              className={cx("todo-filter")}
              aria-pressed={selected.has(category.key)}
              title={category.hint}
              onClick={() => toggle(category.key)}
            >
              <span>{category.label}</span>
              <span className={cx("todo-filter-count")}>
                {counts[category.key]}
              </span>
            </button>
          ))}
        </div>
        <div className={cx("todo-toolbar-actions")}>
          <button
            type="button"
            className={cx("todo-select-all")}
            aria-pressed={allSelected}
            onClick={selectAll}
          >
            全选
          </button>
          <button
            type="button"
            className={cx("todo-select-all")}
            disabled={selected.size === 0}
            onClick={clearAll}
          >
            清空
          </button>
        </div>
      </div>
      <div className={cx("todo-summary")}>
        <span>{rows.length} 项符合筛选</span>
        <span>{roots.length} 项任务</span>
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
            const meta = statusMeta(item, taskQuestions);
            const remaining =
              item.budget.policy.auto_continuations -
              item.budget.used_continuations -
              item.budget.reserved_runs;
            const waitingReason = item.task.waiting_reason ?? "";
            const detail =
              meta.tone === "attention"
                ? taskQuestions[0]?.body
                : waitingLabels[waitingReason] || waitingReason;
            return (
              <li
                key={item.task.id}
                className={cx(`todo-item tone-${meta.tone}`)}
              >
                <span className={cx("todo-status")} aria-hidden="true">
                  {meta.icon}
                </span>
                <div className={cx("todo-main")}>
                  <button
                    type="button"
                    className={cx("todo-title")}
                    onClick={() => onOpenTask(item.task.workspace_id, item.task.id)}
                  >
                    {item.revision.goal}
                  </button>
                  <div className={cx("todo-meta")}>
                    <span className={cx("todo-state")}>{meta.label}</span>
                    {workspaces?.[item.task.workspace_id] && (
                      <span className={cx("todo-workspace")}>
                        {workspaces[item.task.workspace_id]}
                      </span>
                    )}
                    <span>目标 v{item.task.current_revision}</span>
                    {detail && <span>{detail}</span>}
                    {meta.tone !== "done" && meta.tone !== "canceled" && (
                      <span>剩余续办 {remaining} 次</span>
                    )}
                    {item.task.evaluation_status === "needs_revalidation" && (
                      <span>当前结果待复验</span>
                    )}
                    {taskQuestions.length > 1 && (
                      <span>另有 {taskQuestions.length - 1} 个问题</span>
                    )}
                  </div>
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
                    className={cx("small primary-quiet")}
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
          {selected.size === 0
            ? "没有选择筛选条件。点击“全选”查看任务。"
            : "当前筛选下没有任务。"}
        </p>
      )}
    </section>
  );
}
