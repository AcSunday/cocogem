import { useCallback, useEffect, useState } from "react";
import {
  api,
  type ProjectTaskView,
  type Question,
  type Run,
  type TaskBoard,
  type TaskDeletion,
} from "../api/client";
import { errorText, formatDateTime, formatRelativeTime } from "../lib/presentation";
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
type FilterName = "status" | "workspace" | "owner";

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
  name,
  label,
  value,
  options,
  open,
  onToggle,
  onChange,
}: {
  name: FilterName;
  label: string;
  value: string;
  options: { value: string; label: string }[];
  open: boolean;
  onToggle: () => void;
  onChange: (value: string) => void;
}) {
  const selected = options.find((option) => option.value === value);
  return (
    <div className={cx("task-filter")} data-task-filter={name}>
      <button
        type="button"
        className={cx("task-filter-button")}
        data-active={value ? "true" : "false"}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={onToggle}
      >
        <span className={cx("sr-only")}>{label}</span>
        <span className={cx("task-filter-value")}>
          {selected?.label ?? options[0]?.label}
        </span>
        <Icon name="chevron" size={12} />
      </button>
      {open && (
        <div className={cx("task-filter-menu")} role="listbox" aria-label={label}>
          {options.map((option) => {
            const active = option.value === value;
            return (
              <button
                key={option.value || "all"}
                type="button"
                role="option"
                aria-selected={active}
                className={cx("task-filter-option")}
                onClick={() => onChange(option.value)}
              >
                <span>{option.label}</span>
                {active && <Icon name="check" size={12} />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** Global task surface: a focus stream with an optional board lens. */
export function TaskBoardPage({
  onOpenTask,
  onOpenThread,
}: {
  onOpenTask: (workspaceID: string, taskID: string) => void;
  onOpenThread?: (workspaceID: string, threadID: string) => void;
}) {
  const [board, setBoard] = useState<TaskBoard>();
  const [error, setError] = useState("");
  const [filters, setFilters] = useState(savedFilters);
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState("");
  const [openFilter, setOpenFilter] = useState<FilterName | "">("");
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
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

  useEffect(() => {
    if (!openFilter) return;
    const close = (event: PointerEvent) => {
      const target = event.target as Element | null;
      if (target?.closest("[data-task-filter]")) return;
      setOpenFilter("");
    };
    const closeKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpenFilter("");
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", closeKey);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", closeKey);
    };
  }, [openFilter]);

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
        ? `已永久删除 ${count} 个任务（含子任务）。`
        : `已永久删除 ${count} 个任务。`,
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
      setNotice("任务已恢复。");
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
      setNotice("任务已开始执行。");
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
    if (phase === "running" || phase === "waiting_budget") {
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
        return "回答";
      case "needs_retry":
        return "处理";
      case "needs_revalidation":
        return "复验";
      case "running":
        return "查看";
      case "not_started":
        return "开始执行";
      case "paused":
        return "恢复任务";
      case "waiting_budget":
        return item.task.thread_id && onOpenThread ? "打开会话" : "查看任务";
      case "review":
        return "验收";
      case "completed":
        return "查看成果";
      default:
        return "打开";
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
        <p>{error || "正在读取任务…"}</p>
        {error && <button onClick={() => void load()}>重新加载任务</button>}
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
  const workspace = (id: string) => board.workspaces?.[id] || "工作空间";
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
  const completedCount = filtered.filter(
    (item) => taskFocus(item, questions) === "completed",
  ).length;
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
                aria-label={`选择任务：${taskTitle(item)}`}
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
                ? `负责人 ${owner(item.task.owner_member_id)}`
                : "任务负责人"}
            </span>
            {sub > 0 && (
              <>
                <span aria-hidden="true">·</span>
                <span>
                  子任务 {subDone}/{sub}
                </span>
              </>
            )}
          </div>
          <div className={cx("task-focus-line")}>
            <span className={cx("task-focus-label")}>当前</span>
            {meta.current}
          </div>
          <div className={cx("task-focus-line")}>
            <span className={cx("task-focus-label")}>下一步</span>
            {meta.next}
          </div>
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
                  打开
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
          aria-label={`打开任务：${title}`}
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
              "未分配"
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

  return (
    <div
      className={cx(
        "task-workspace",
        "task-focus-page",
        filters.view === "board" && "task-board-surface",
      )}
    >
      <header className={cx("task-focus-head")}>
        <div>
          <h1>任务清单</h1>
          {tasks.length ? (
            <p className={cx("task-focus-summary")}>
              <span
                className={cx(
                  "task-summary-attention",
                  summary.attention > 0 && "on",
                )}
              >
                {summary.attention} 件需要你
              </span>
              <span aria-hidden="true">·</span>
              <span>{summary.running} 个进行中</span>
              <span aria-hidden="true">·</span>
              <span>{summary.review} 个待验收</span>
            </p>
          ) : (
            <p className={cx("task-focus-summary")}>
              还没有任务，先从一个目标开始。
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
              退出选择
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
                  选择
                </button>
              )}
              <div
                className={cx("task-view-switch")}
                role="group"
                aria-label="任务视图"
              >
                <button
                  type="button"
                  className={cx("task-view-option", "task-head-control")}
                  aria-pressed={filters.view === "list"}
                  onClick={() => update({ view: "list" })}
                >
                  <Icon name="tasks" size={14} />
                  清单
                </button>
                <button
                  type="button"
                  className={cx("task-view-option", "task-head-control")}
                  aria-pressed={filters.view === "board"}
                  onClick={() => update({ view: "board" })}
                >
                  <Icon name="layers" size={14} />
                  看板
                </button>
              </div>
              <button
                type="button"
                className={cx("primary", "task-head-control")}
                onClick={() => setCreating(true)}
              >
                <Icon name="plus" size={14} />
                新建任务
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
            aria-label="搜索任务"
            placeholder="搜索任务…"
            value={filters.query}
            onChange={(event) => update({ query: event.target.value })}
          />
        </label>
        <FilterMenu
          name="status"
          label="筛选状态"
          value={filters.status}
          options={taskFocusFilters}
          open={openFilter === "status"}
          onToggle={() =>
            setOpenFilter(openFilter === "status" ? "" : "status")
          }
          onChange={(value) => {
            update({ status: value as TaskFocus | "" });
            setOpenFilter("");
          }}
        />
        <FilterMenu
          name="workspace"
          label="筛选项目"
          value={filters.workspace}
          options={[
            { value: "", label: "全部项目" },
            ...Object.entries(board.workspaces ?? {}).map(([id, name]) => ({
              value: id,
              label: name,
            })),
          ]}
          open={openFilter === "workspace"}
          onToggle={() =>
            setOpenFilter(openFilter === "workspace" ? "" : "workspace")
          }
          onChange={(value) => {
            update({ workspace: value });
            setOpenFilter("");
          }}
        />
        <FilterMenu
          name="owner"
          label="筛选负责人"
          value={filters.owner}
          options={[
            { value: "", label: "全部负责人" },
            ...Object.entries(board.members ?? {}).map(([id, name]) => ({
              value: id,
              label: name,
            })),
          ]}
          open={openFilter === "owner"}
          onToggle={() =>
            setOpenFilter(openFilter === "owner" ? "" : "owner")
          }
          onChange={(value) => {
            update({ owner: value });
            setOpenFilter("");
          }}
        />
      </div>

      {selecting && filters.view === "list" && (
        <div className={cx("task-select-bar")} role="group" aria-label="任务选择">
          <span className={cx("task-select-count")}>
            已选 {selected.size} 项
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
              {allSelected ? "取消全选" : "全选当前"}
            </button>
            <button
              type="button"
              className={cx("small", "ghost")}
              disabled={!selected.size}
              onClick={() => setSelected(new Set())}
            >
              清除
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
              永久删除所选
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
                ? "还没有已验收的任务。"
                : filtering
                  ? "没有匹配的任务。"
                  : "还没有任务。"}
            </strong>
            {filtering ? (
              <button
                type="button"
                className={cx("small")}
                onClick={() =>
                  update({ query: "", workspace: "", owner: "", status: "" })
                }
              >
                清除筛选
              </button>
            ) : (
              <button
                type="button"
                className={cx("small", "primary")}
                onClick={() => setCreating(true)}
              >
                新建任务
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
                aria-label={`${column.label}任务`}
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
                    <p className={cx("task-column-empty")}>暂无任务</p>
                  )}
                </div>
              </section>
            );
          })}
        </div>
      )}

      {filters.view === "list" && !filters.status && completedCount > 0 && (
        <div className={cx("task-focus-foot")}>
          <button
            type="button"
            className={cx("small", "ghost")}
            onClick={() => update({ status: "completed" })}
          >
            查看已验收任务 →
          </button>
        </div>
      )}

      {creating && (
        <Modal title="新建任务" close={() => setCreating(false)}>
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
          title="确认永久删除任务"
          action="永久删除"
          close={() => setConfirming(false)}
          confirm={deleteSelected}
        >
          <p>
            将永久删除选中的 {selected.size}{" "}
            个任务及其子任务、关联会话、执行记录和成果。此操作无法恢复。
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}
