import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import {
  api,
  type ProjectTaskView,
  type Question,
  type Run,
  type TaskBoard,
  type TaskDeletion,
} from "../api/client";
import { errorText, formatDateTime, formatRelativeTime } from "../lib/presentation";
import { t, useLanguage } from "../lib/i18n";
import {
  taskAcceptance,
  taskColumn,
  taskColumns,
  taskFocus,
  taskFocusFilters,
  taskFocusGroups,
  taskFocusMeta,
  taskTitle,
  type TaskFocus,
} from "../lib/tasks";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";
import { Icon } from "./Icon";
import { Modal } from "./Modal";
import { ConfirmDialog } from "./ConfirmDialog";
import { TaskCreateForm } from "./TaskCreateForm";
import { Select } from "./Select";

const cx = scoped(base, controls, styles);
const STORAGE_KEY = "cocogem-task-filters-v2";
const defaults = {
  query: "",
  workspace: "",
  owner: "",
  status: "" as TaskFocus | "",
  view: "list" as "list" | "board",
};
type Filters = typeof defaults;

function savedFilters(): Filters {
  try {
    const raw = JSON.parse(
      sessionStorage.getItem(STORAGE_KEY) || "{}",
    ) as Partial<Filters>;
    return {
      query: typeof raw.query === "string" ? raw.query : "",
      workspace: typeof raw.workspace === "string" ? raw.workspace : "",
      owner: typeof raw.owner === "string" ? raw.owner : "",
      status: taskFocusFilters.some((option) => option.value === raw.status)
        ? (raw.status ?? "")
        : "",
      view: raw.view === "board" ? "board" : "list",
    };
  } catch {
    return { ...defaults };
  }
}

function FilterMenu({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: readonly { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  return (
    <div className={cx("task-filter")}>
      <Select
        aria-label={label}
        className={cx("task-filter-button", value && "task-filter-active")}
        value={value}
        onChange={onChange}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </Select>
    </div>
  );
}

/** Global task surface: a focus stream with an optional board lens. */
export function TaskBoardPage({
  onOpenTask,
  onOpenThread,
  viewSwitchTarget,
}: {
  onOpenTask: (workspaceID: string, taskID: string) => void;
  onOpenThread?: (workspaceID: string, threadID: string) => void;
  viewSwitchTarget?: HTMLElement | null;
}) {
  useLanguage();
  const [board, setBoard] = useState<TaskBoard>();
  const [error, setError] = useState("");
  const [filters, setFilters] = useState(savedFilters);
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState("");
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  // Archived work is collapsed by default; the footer drawer opens it in place.
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [notice, setNotice] = useState("");

  const load = useCallback(async () => {
    try {
      setBoard(await api<TaskBoard>("/api/v1/tasks"));
      setError("");
    } catch (failure) {
      setError(errorText(failure));
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  useEffect(() => {
    try {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(filters));
    } catch {
      /* Filters still work without browser storage. */
    }
  }, [filters]);

  useEffect(() => {
    if (!board) return;
    setFilters((current) => {
      const workspace =
        current.workspace && !board.workspaces?.[current.workspace]
          ? ""
          : current.workspace;
      const owner =
        current.owner && !board.members?.[current.owner] ? "" : current.owner;
      return workspace === current.workspace && owner === current.owner
        ? current
        : { ...current, workspace, owner };
    });
  }, [board]);

  const update = (patch: Partial<Filters>) => {
    if (selecting) {
      setSelecting(false);
      setSelected(new Set());
      setConfirming(false);
    }
    setNotice("");
    setFilters((current) => ({ ...current, ...patch }));
  };
  const enterSelection = () => {
    setSelecting(true);
    setSelected(new Set());
    setConfirming(false);
    setNotice("");
  };
  const leaveSelection = () => {
    setSelecting(false);
    setSelected(new Set());
    setConfirming(false);
  };
  const toggleSelected = (id: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const deleteSelected = async () => {
    const ids = [...selected];
    if (!ids.length) return;
    const result = await api<TaskDeletion>("/api/v1/tasks/delete", "POST", {
      task_ids: ids,
    });
    const count = result.count ?? ids.length;
    setConfirming(false);
    setSelecting(false);
    setSelected(new Set());
    setNotice(
      count > ids.length
        ? t(
            `已永久删除 ${count} 个任务（含子任务）。`,
            `Permanently deleted ${count} task(s) (including subtasks).`,
          )
        : t(
            `已永久删除 ${count} 个任务。`,
            `Permanently deleted ${count} task(s).`,
          ),
    );
    await load();
  };

  const resume = async (taskID: string) => {
    if (busy) return;
    setBusy(taskID);
    setError("");
    try {
      await api(`/api/v1/tasks/${taskID}/actions`, "POST", {
        action: "resume",
        request_id: crypto.randomUUID(),
      });
      setNotice(t("任务已恢复。", "Task resumed."));
      await load();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };

  const start = async (item: ProjectTaskView) => {
    if (busy) return;
    setBusy(item.task.id);
    setError("");
    try {
      const run = await api<Run>(
        `/api/v1/tasks/${item.task.id}/start`,
        "POST",
        {},
      );
      setNotice(t("任务已开始执行。", "Task execution started."));
      if (onOpenThread) onOpenThread(item.task.workspace_id, run.thread_id);
      else onOpenTask(item.task.workspace_id, item.task.id);
      await load();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };

  const openExecution = (item: ProjectTaskView) => {
    if (onOpenThread && item.task.thread_id) {
      onOpenThread(item.task.workspace_id, item.task.thread_id);
      return;
    }
    onOpenTask(item.task.workspace_id, item.task.id);
  };

  const primaryFor = (item: ProjectTaskView, phase: string) => {
    if (
      phase === "running" ||
      phase === "waiting_budget" ||
      (phase === "needs_answer" && (item.pending_permissions ?? 0) > 0)
    ) {
      openExecution(item);
      return;
    }
    if (phase === "not_started") {
      void start(item);
      return;
    }
    if (phase === "paused") {
      void resume(item.task.id);
      return;
    }
    onOpenTask(item.task.workspace_id, item.task.id);
  };

  const listActionLabel = (item: ProjectTaskView, phase: string) => {
    switch (phase) {
      case "needs_answer":
        return (item.pending_permissions ?? 0) > 0
          ? t("去审批", "Approve")
          : t("回答", "Answer");
      case "needs_retry":
        return t("处理", "Handle");
      case "needs_revalidation":
        return t("复验", "Re-verify");
      case "running":
        return t("查看", "View");
      case "not_started":
        return t("开始执行", "Start run");
      case "paused":
        return t("恢复任务", "Resume task");
      case "waiting_budget":
        return item.task.thread_id && onOpenThread
          ? t("打开会话", "Open conversation")
          : t("查看任务", "View task");
      case "review":
        return t("验收", "Review");
      case "completed":
        return t("查看成果", "View deliverables");
      default:
        return t("打开", "Open");
    }
  };

  const pillClass = (tone: string) => {
    switch (tone) {
      case "failed":
        return "task-status-failed";
      case "review":
        return "task-status-review";
      case "done":
        return "task-status-done";
      case "running":
        return "task-status-running";
      case "waiting":
        return "task-status-waiting";
      case "paused":
      case "canceled":
        return "task-status-canceled";
      default:
        return "task-status-attention";
    }
  };

  if (!board)
    return (
      <div
        className={cx("task-workspace", "task-focus-loading")}
        role={error ? "alert" : "status"}
      >
        <p>{error || t("正在读取任务…", "Loading tasks…")}</p>
        {error && (
          <button onClick={() => void load()}>
            {t("重新加载任务", "Reload tasks")}
          </button>
        )}
      </div>
    );

  const questions: Question[] = board.needs_attention ?? [];
  const tasks = board.tasks ?? [];
  const query = filters.query.trim().toLowerCase();
  const childCount = (id: string) =>
    tasks.filter((item) => item.task.parent_id === id).length;
  const childrenDone = (id: string) =>
    tasks.filter(
      (item) =>
        item.task.parent_id === id && item.task.status === "completed",
    ).length;
  const workspace = (id: string) =>
    board.workspaces?.[id] || t("工作空间", "Project");
  const owner = (id: string) => board.members?.[id];
  const needsAttention = (item: ProjectTaskView) =>
    taskFocus(item, questions) === "attention";

  const filtered = tasks.filter((item) => {
    if (item.task.parent_id && !query && !needsAttention(item)) return false;
    const search = `${taskTitle(item)} ${item.revision.goal}`.toLowerCase();
    return (
      (!filters.workspace || item.task.workspace_id === filters.workspace) &&
      (!filters.owner || item.task.owner_member_id === filters.owner) &&
      search.includes(query)
    );
  });
  const focused = filtered.map((item) => ({
    item,
    meta: taskFocusMeta(item, questions),
  }));
  const visible = focused.filter(({ meta }) =>
    filters.status
      ? meta.focus === filters.status
      : !["completed", "canceled"].includes(meta.focus),
  );
  const visibleIDs = visible.map(({ item }) => item.task.id);
  const allSelected =
    visibleIDs.length > 0 && visibleIDs.every((id) => selected.has(id));
  const groupIDs: TaskFocus[] = filters.status
    ? [filters.status]
    : ["attention", "running", "waiting", "review"];
  const groups = groupIDs
    .map((id) => {
      const group = taskFocusGroups.find((item) => item.id === id)!;
      return {
        ...group,
        items: visible
          .filter(({ meta }) => meta.focus === id)
          .map(({ item }) => item)
          .sort((a, b) =>
            b.task.updated_at.localeCompare(a.task.updated_at),
          ),
      };
    })
    .filter((group) => group.items.length > 0);

  const summaryTasks = tasks.filter(
    (item) => !item.task.parent_id || needsAttention(item),
  );
  const summary = summaryTasks.reduce(
    (result, item) => {
      const focus = taskFocus(item, questions);
      if (focus === "attention") result.attention += 1;
      if (focus === "running") result.running += 1;
      if (focus === "review") result.review += 1;
      return result;
    },
    { attention: 0, running: 0, review: 0 },
  );
  const archivedCount = filtered.filter((item) => {
    const focus = taskFocus(item, questions);
    return focus === "completed" || focus === "canceled";
  }).length;
  const filtering = !!(
    filters.query ||
    filters.workspace ||
    filters.owner ||
    filters.status
  );

  const boardRows = focused
    .filter(({ meta }) => !filters.status || meta.focus === filters.status)
    .map(({ item }) => item);
  const columns = taskColumns.filter(
    (column) =>
      column.id !== "canceled" ||
      boardRows.some((item) => item.task.status === "canceled") ||
      filters.status === "canceled",
  );

  const row = (item: ProjectTaskView) => {
    const meta = taskFocusMeta(item, questions);
    const progress = taskAcceptance(item);
    const segments = Math.min(Math.max(progress.total, 4), 6);
    const filled = progress.total
      ? Math.round((progress.passed / progress.total) * segments)
      : 0;
    const sub = childCount(item.task.id);
    const subDone = childrenDone(item.task.id);
    const dotTone =
      meta.focus === "review" ? "done" : meta.tone === "failed" ? "failed" : meta.tone;
    return (
      <article
        key={item.task.id}
        className={cx(
          "task-focus-row",
          meta.focus === "attention" && "task-focus-row-attention",
          meta.tone === "failed" && "task-focus-row-failed",
          ["waiting", "review", "completed", "canceled"].includes(meta.focus) &&
            "task-focus-row-quiet",
          selecting && "task-focus-row-selecting",
          selecting && selected.has(item.task.id) && "task-focus-row-selected",
        )}
        onClick={(event) => {
          if (selecting) {
            if ((event.target as Element).closest("input, button")) return;
            toggleSelected(item.task.id);
            return;
          }
          if ((event.target as Element).closest("button")) return;
          onOpenTask(item.task.workspace_id, item.task.id);
        }}
      >
        <span
          className={cx("task-focus-dot", `task-focus-dot-${dotTone}`)}
          aria-hidden="true"
        />
        <div className={cx("task-focus-main")}>
          <div className={cx("task-focus-titleline")}>
            {selecting && (
              <input
                type="checkbox"
                className={cx("task-focus-select")}
                checked={selected.has(item.task.id)}
                onChange={() => toggleSelected(item.task.id)}
                onClick={(event) => event.stopPropagation()}
                aria-label={t(
                  `选择任务：${taskTitle(item)}`,
                  `Select task: ${taskTitle(item)}`,
                )}
              />
            )}
            <button
              type="button"
              className={cx("task-focus-title")}
              onClick={(event) => {
                if (selecting) {
                  event.stopPropagation();
                  toggleSelected(item.task.id);
                  return;
                }
                onOpenTask(item.task.workspace_id, item.task.id);
              }}
            >
              {taskTitle(item)}
            </button>
            <span
              className={cx("task-focus-time")}
              title={formatDateTime(item.task.updated_at)}
            >
              {formatRelativeTime(item.task.updated_at)}
            </span>
          </div>
          <div className={cx("task-focus-meta")}>
            <span>{workspace(item.task.workspace_id)}</span>
            <span aria-hidden="true">·</span>
            <span>
              {owner(item.task.owner_member_id)
                ? t(
                    `负责人 ${owner(item.task.owner_member_id)}`,
                    `Owner ${owner(item.task.owner_member_id)}`,
                  )
                : t("任务负责人", "Task owner")}
            </span>
            {sub > 0 && (
              <>
                <span aria-hidden="true">·</span>
                <span>
                  {t(`子任务 ${subDone}/${sub}`, `Subtasks ${subDone}/${sub}`)}
                </span>
              </>
            )}
          </div>
          {/* Normal rows stay a compact two-line layout. Only an attention or
              failed row prints its real blocking reason, on one line. */}
          {(meta.focus === "attention" || meta.tone === "failed") && (
            <div className={cx("task-focus-blocker")}>
              {meta.current}
            </div>
          )}
        </div>
        <div className={cx("task-focus-side")}>
          <div className={cx("task-focus-side-top")}>
            {progress.total > 0 && (
              <div className={cx("task-progress")}>
                <span className={cx("task-progress-segments")}>
                  {Array.from({ length: segments }, (_, index) => (
                    <span
                      key={index}
                      className={cx(
                        "task-progress-segment",
                        index < filled && "on",
                      )}
                    />
                  ))}
                </span>
                <span className={cx("task-progress-text")}>
                  {progress.passed}/{progress.total}
                </span>
              </div>
            )}
            {[
              "review",
              "needs_revalidation",
              "needs_retry",
              "paused",
              "completed",
              "canceled",
            ].includes(meta.phase) && (
              <span className={cx("task-status-pill", pillClass(meta.tone))}>
                {meta.label}
              </span>
            )}
          </div>
          {!selecting && (
            <div className={cx("task-focus-actions")}>
              <button
                type="button"
                className={cx(
                  "small",
                  ["attention", "review", "waiting"].includes(meta.focus) &&
                    meta.phase !== "waiting_other"
                    ? "primary"
                    : "ghost",
                )}
                disabled={!!busy}
                onClick={(event) => {
                  event.stopPropagation();
                  primaryFor(item, meta.phase);
                }}
              >
                {listActionLabel(item, meta.phase)}
              </button>
              {meta.phase === "paused" && (
                <button
                  type="button"
                  className={cx("small", "ghost")}
                  disabled={!!busy}
                  onClick={(event) => {
                    event.stopPropagation();
                    onOpenTask(item.task.workspace_id, item.task.id);
                  }}
                >
                  {t("打开", "Open")}
                </button>
              )}
            </div>
          )}
        </div>
      </article>
    );
  };

  const card = (item: ProjectTaskView) => {
    const meta = taskFocusMeta(item, questions);
    const title = taskTitle(item);
    const ownerName = owner(item.task.owner_member_id);
    return (
      <article
        key={item.task.id}
        className={cx("task-card", `task-card-${meta.column}`)}
      >
        <button
          type="button"
          className={cx("task-card-open")}
          aria-label={t(`打开任务：${title}`, `Open task: ${title}`)}
          title={title}
          onClick={() => onOpenTask(item.task.workspace_id, item.task.id)}
        >
          <strong className={cx("task-card-title")}>{title}</strong>
        </button>
        <div className={cx("task-card-footer")}>
          <span className={cx("task-status-pill", pillClass(meta.tone))}>
            {meta.label}
          </span>
          <span
            className={cx(
              "task-card-owner",
              !ownerName && "task-card-owner-empty",
            )}
          >
            {ownerName ? (
              <>
                <span className={cx("task-member-avatar")} aria-hidden="true">
                  {ownerName.slice(0, 1)}
                </span>
                {ownerName}
              </>
            ) : (
              t("未分配", "Unassigned")
            )}
          </span>
          <button
            type="button"
            className={cx(
              "small",
              ["attention", "review", "waiting"].includes(meta.focus) &&
                meta.phase !== "waiting_other"
                ? "primary"
                : "ghost",
              "task-card-action",
            )}
            disabled={!!busy}
            onClick={(event) => {
              event.stopPropagation();
              primaryFor(item, meta.phase);
            }}
          >
            {listActionLabel(item, meta.phase)}
          </button>
        </div>
      </article>
    );
  };

  const viewSwitch = !selecting && (
    <div
      className={cx("task-view-switch")}
      role="group"
      aria-label={t("任务视图", "Task view")}
    >
      <button
        type="button"
        className={cx("task-view-option", "task-head-control")}
        aria-pressed={filters.view === "list"}
        onClick={() => update({ view: "list" })}
      >
        <Icon name="tasks" size={14} />
        {t("清单", "List")}
      </button>
      <button
        type="button"
        className={cx("task-view-option", "task-head-control")}
        aria-pressed={filters.view === "board"}
        onClick={() => update({ view: "board" })}
      >
        <Icon name="layers" size={14} />
        {t("看板", "Board")}
      </button>
    </div>
  );

  return (
    <div
      className={cx(
        "task-workspace",
        "task-focus-page",
        filters.view === "board" && "task-board-surface",
      )}
    >
      {viewSwitchTarget && createPortal(viewSwitch, viewSwitchTarget)}
      <header className={cx("task-focus-head")}>
        <div>
          <h1>{t("任务清单", "Tasks")}</h1>
          {tasks.length ? (
            <p className={cx("task-focus-summary")}>
              <span
                className={cx(
                  "task-summary-attention",
                  summary.attention > 0 && "on",
                )}
              >
                {t(`${summary.attention} 件需要你`, `${summary.attention} needing you`)}
              </span>
              <span aria-hidden="true">·</span>
              <span>
                {t(`${summary.running} 个进行中`, `${summary.running} in progress`)}
              </span>
              <span aria-hidden="true">·</span>
              <span>
                {t(`${summary.review} 个待验收`, `${summary.review} ready for review`)}
              </span>
            </p>
          ) : (
            <p className={cx("task-focus-summary")}>
              {t(
                "还没有任务，先从一个目标开始。",
                "No tasks yet. Start with an objective.",
              )}
            </p>
          )}
        </div>
        <div className={cx("task-focus-head-actions")}>
          {selecting ? (
            <button
              type="button"
              className={cx("ghost", "small", "task-head-control")}
              onClick={leaveSelection}
            >
              {t("退出选择", "Done selecting")}
            </button>
          ) : (
            <>
              {tasks.length > 0 && filters.view === "list" && (
                <button
                  type="button"
                  className={cx(
                    "ghost",
                    "small",
                    "task-head-control",
                    "task-select-toggle",
                  )}
                  onClick={enterSelection}
                >
                  {t("选择", "Select")}
                </button>
              )}
              {!viewSwitchTarget && viewSwitch}
              <button
                type="button"
                className={cx("primary", "task-head-control")}
                onClick={() => setCreating(true)}
              >
                <Icon name="plus" size={14} />
                {t("新建任务", "New task")}
              </button>
            </>
          )}
        </div>
      </header>

      <div className={cx("task-focus-filters")} role="search">
        <label className={cx("task-focus-search")}>
          <Icon name="search" size={15} />
          <input
            type="search"
            aria-label={t("搜索任务", "Search tasks")}
            placeholder={t("搜索任务…", "Search tasks…")}
            value={filters.query}
            onChange={(event) => update({ query: event.target.value })}
          />
        </label>
        <FilterMenu
          label={t("筛选状态", "Filter by status")}
          value={filters.status}
          options={taskFocusFilters}
          onChange={(value) => update({ status: value as TaskFocus | "" })}
        />
        <FilterMenu
          label={t("筛选项目", "Filter by project")}
          value={filters.workspace}
          options={[
            { value: "", label: t("全部项目", "All projects") },
            ...Object.entries(board.workspaces ?? {}).map(([id, name]) => ({
              value: id,
              label: name,
            })),
          ]}
          onChange={(value) => update({ workspace: value })}
        />
        <FilterMenu
          label={t("筛选负责人", "Filter by owner")}
          value={filters.owner}
          options={[
            { value: "", label: t("全部负责人", "All owners") },
            ...Object.entries(board.members ?? {}).map(([id, name]) => ({
              value: id,
              label: name,
            })),
          ]}
          onChange={(value) => update({ owner: value })}
        />
      </div>

      {selecting && filters.view === "list" && (
        <div
          className={cx("task-select-bar")}
          role="group"
          aria-label={t("任务选择", "Task selection")}
        >
          <span className={cx("task-select-count")}>
            {t(`已选 ${selected.size} 项`, `${selected.size} selected`)}
          </span>
          <div className={cx("task-select-actions")}>
            <button
              type="button"
              className={cx("small", "ghost")}
              disabled={!visibleIDs.length}
              onClick={() =>
                setSelected(allSelected ? new Set() : new Set(visibleIDs))
              }
            >
              {allSelected
                ? t("取消全选", "Deselect all")
                : t("全选当前", "Select all visible")}
            </button>
            <button
              type="button"
              className={cx("small", "ghost")}
              disabled={!selected.size}
              onClick={() => setSelected(new Set())}
            >
              {t("清除", "Clear")}
            </button>
            <button
              type="button"
              className={cx("danger", "small")}
              disabled={!selected.size}
              onClick={() => {
                setNotice("");
                setConfirming(true);
              }}
            >
              {t("永久删除所选", "Delete permanently")}
            </button>
          </div>
        </div>
      )}

      {notice && (
        <p role="status" className={cx("task-focus-notice")}>
          {notice}
        </p>
      )}

      {error && (
        <p role="alert" className={cx("error")}>
          {error}
        </p>
      )}

      {filters.view === "list" ? (
        groups.length ? (
          <div className={cx("task-focus-groups")}>
            {groups.map((group) => (
              <section
                key={group.id}
                className={cx("task-focus-group")}
                aria-label={group.ariaLabel}
              >
                <div className={cx("task-focus-group-head")}>
                  <h2>
                    <span
                      className={cx(
                        "task-focus-dot",
                        `task-focus-dot-${group.id === "review" ? "done" : group.id}`,
                      )}
                      aria-hidden="true"
                    />
                    {group.label}
                    <span className={cx("task-focus-count")}>
                      {group.items.length}
                    </span>
                  </h2>
                  {group.hint && (
                    <span className={cx("task-focus-group-hint")}>
                      {group.hint}
                    </span>
                  )}
                </div>
                <div className={cx("task-focus-rows")}>
                  {group.items.map(row)}
                </div>
              </section>
            ))}
          </div>
        ) : (
          <div className={cx("task-focus-empty")}>
            <strong>
              {filters.status === "completed"
                ? t("还没有已验收的任务。", "No accepted tasks yet.")
                : filtering
                  ? t("没有匹配的任务。", "No matching tasks.")
                  : t("还没有任务。", "No tasks yet.")}
            </strong>
            {filtering ? (
              <button
                type="button"
                className={cx("small")}
                onClick={() =>
                  update({ query: "", workspace: "", owner: "", status: "" })
                }
              >
                {t("清除筛选", "Clear filters")}
              </button>
            ) : (
              <button
                type="button"
                className={cx("small", "primary")}
                onClick={() => setCreating(true)}
              >
                {t("新建任务", "New task")}
              </button>
            )}
          </div>
        )
      ) : (
        <div className={cx("task-columns")}>
          {columns.map((column) => {
            const items = boardRows.filter(
              (item) => taskColumn(item, questions) === column.id,
            );
            return (
              <section
                key={column.id}
                data-status={column.id}
                className={cx(`task-column task-column-${column.id}`)}
                aria-label={t(`${column.label}任务`, `${column.label} tasks`)}
              >
                <h2>
                  <span>
                    {column.icon} {column.label}
                  </span>
                  <span className={cx("task-column-count")}>
                    {items.length}
                  </span>
                </h2>
                <div className={cx("task-column-cards")}>
                  {items.map(card)}
                  {!items.length && (
                    <p className={cx("task-column-empty")}>
                      {t("暂无任务", "No tasks")}
                    </p>
                  )}
                </div>
              </section>
            );
          })}
        </div>
      )}

      {filters.view === "list" && !filters.status && archivedCount > 0 && (
        <div className={cx("task-focus-foot")}>
          <button
            type="button"
            className={cx("small", "ghost")}
            aria-expanded={drawerOpen}
            onClick={() => setDrawerOpen((current) => !current)}
          >
            {t(
              `已归档 ${archivedCount} 项`,
              `Archived (${archivedCount})`,
            )}{" "}
            {drawerOpen ? "⌄" : "›"}
          </button>
          {drawerOpen && (
            <div
              className={cx("task-focus-drawer")}
              aria-label={t("已归档任务", "Archived tasks")}
            >
              {focused
                .filter(
                  ({ meta }) =>
                    meta.focus === "completed" || meta.focus === "canceled",
                )
                .map(({ item }) => row(item))}
            </div>
          )}
        </div>
      )}

      {creating && (
        <Modal
          title={t("新建任务", "New Task")}
          close={() => setCreating(false)}
        >
          <TaskCreateForm
            workspaces={board.workspaces ?? {}}
            workspaceID={filters.workspace}
            onCreated={(task, run) => {
              setCreating(false);
              if (run && onOpenThread)
                onOpenThread(task.task.workspace_id, run.thread_id);
              else onOpenTask(task.task.workspace_id, task.task.id);
            }}
          />
        </Modal>
      )}

      {confirming && (
        <ConfirmDialog
          title={t("确认永久删除任务", "Confirm Permanent Task Deletion")}
          action={t("永久删除", "Delete permanently")}
          close={() => setConfirming(false)}
          confirm={deleteSelected}
        >
          <p>
            {t(
              `将永久删除选中的 ${selected.size} 个任务及其子任务、关联会话、执行记录和成果。此操作无法恢复。`,
              `Permanently delete ${selected.size} selected task(s), along with their subtasks, linked conversations, run records, and deliverables? This action cannot be undone.`,
            )}
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}
