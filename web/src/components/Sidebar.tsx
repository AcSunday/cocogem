import { Select } from "./Select";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { Thread, Workspace } from "../api/client";
import { cx } from "../styles/classes";
import { formatMessageTime } from "../lib/presentation";
import { Icon } from "./Icon";
import { AppearanceSelect, type Appearance } from "./AppearanceSelect";

export function Sidebar({
  open,
  close,
  workspaces,
  workspaceID,
  workspace,
  threads,
  threadID,
  onWorkspace,
  onHome,
  onThread,
  onNewThread,
  onEditThread,
  onRenameThread,
  onDeleteThread,
  onCreateWorkspace,
  onEditWorkspace,
  onSettings,
  appearance,
  onAppearance,
}: {
  open: boolean;
  close: () => void;
  workspaces: Workspace[];
  workspaceID: string;
  workspace?: Workspace;
  threads: Thread[];
  threadID: string;
  onWorkspace: (id: string) => void;
  onHome: () => void;
  onThread: (id: string) => void;
  onNewThread: () => void;
  onEditThread: (thread: Thread) => void;
  onRenameThread: (thread: Thread) => void;
  onDeleteThread: (thread: Thread) => void;
  onCreateWorkspace: () => void;
  onEditWorkspace: () => void;
  onSettings: () => void;
  appearance: Appearance;
  onAppearance: (value: Appearance) => void;
}) {
  const [filter, setFilter] = useState("");
  const query = filter.trim().toLowerCase();
  const shown = threads.filter((thread) =>
    thread.title.toLowerCase().includes(query),
  );
  return (
    <aside className={cx(`sidebar ${open ? "open" : ""}`)} aria-label="导航">
      <div className={cx("sidebar-head")}>
        <a
          className={cx("brand")}
          href="/"
          onClick={(event) => {
            event.preventDefault();
            onHome();
          }}
        >
          <span className={cx("brand-mark")}>
            <Icon name="logo" size={18} />
          </span>
          Cocogem
        </a>
        <button
          className={cx("icon-button sidebar-close")}
          aria-label="关闭导航"
          onClick={close}
        >
          <Icon name="close" size={18} />
        </button>
      </div>
      <label className={cx("sr-only")} htmlFor="workspace-switch">
        切换工作空间
      </label>
      <Select
        id="workspace-switch"
        value={workspaceID}
        onChange={(selected) => onWorkspace(selected)}
      >
        {!workspaceID && <option value="">选择工作空间</option>}
        {workspaces.map((ws) => (
          <option key={ws.id} value={ws.id}>
            {ws.name}
          </option>
        ))}
      </Select>
      <button
        className={cx("new-thread")}
        onClick={onNewThread}
        disabled={!workspace}
      >
        <Icon name="plus" /> 新建会话
      </button>
      <label className={cx("search")}>
        <Icon name="search" size={14} />
        <input
          aria-label="搜索会话"
          placeholder="搜索会话标题"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
        />
      </label>
      <div className={cx("section-label")}>
        <span>会话</span>
        <span>{threads.length}</span>
      </div>
      <nav className={cx("thread-list")} aria-label="会话列表">
        {shown.map((thread) => (
          <div
            key={thread.id}
            className={cx(
              `thread-item ${thread.id === threadID ? "selected" : ""}`,
            )}
          >
            <button
              className={cx("thread")}
              aria-label={thread.title}
              aria-current={thread.id === threadID ? "page" : undefined}
              onClick={() => onThread(thread.id)}
            >
              <strong>{thread.title}</strong>
              <small>{formatMessageTime(thread.updated_at)}</small>
            </button>
            <ThreadMenu
              thread={thread}
              session={threadID}
              onEdit={() => onEditThread(thread)}
              onRename={() => onRenameThread(thread)}
              onDelete={() => onDeleteThread(thread)}
            />
          </div>
        ))}
        {!threads.length && (
          <p className={cx("empty-small")}>
            {workspace
              ? "还没有会话，从一个问题开始。"
              : "创建工作空间后开始。"}
          </p>
        )}
        {threads.length > 0 && !shown.length && (
          <p className={cx("empty-small")}>没有匹配的会话。</p>
        )}
      </nav>
      <div className={cx("sidebar-footer")}>
        <button onClick={onCreateWorkspace}>
          <Icon name="plus" size={14} /> 创建工作空间
        </button>
        <button disabled={!workspace} onClick={onEditWorkspace}>
          <Icon name="edit" size={14} /> 编辑工作空间
        </button>
        <button onClick={onSettings}>
          <Icon name="settings" size={14} /> 设置
        </button>
        <AppearanceSelect value={appearance} onChange={onAppearance} />
        <p>本地工作空间 · 数据留在本机</p>
      </div>
    </aside>
  );
}

/** One row's actions; switching conversations drops every stale menu. */
function ThreadMenu({
  thread,
  session,
  onEdit,
  onRename,
  onDelete,
}: {
  thread: Thread;
  session: string;
  onEdit: () => void;
  onRename: () => void;
  onDelete: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ left: 0, top: 0 });
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const dismiss = (refocus: boolean) => {
    setOpen(false);
    if (refocus) trigger.current?.focus();
  };
  const choose = (action: () => void) => {
    // Focus the trigger first so the dialog restores focus to this row.
    dismiss(true);
    action();
  };
  useEffect(() => {
    setOpen(false);
  }, [session]);
  useLayoutEffect(() => {
    const anchor = trigger.current;
    const list = menu.current;
    if (!open || !anchor || !list) return;
    const rect = anchor.getBoundingClientRect();
    const width = 140;
    const height = 84;
    setPosition({
      left: Math.max(8, Math.min(rect.right - width, innerWidth - width - 8)),
      top:
        rect.bottom + height + 4 > innerHeight
          ? Math.max(8, rect.top - height - 4)
          : rect.bottom + 4,
    });
    list.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => {
      const target = event.target as Node;
      if (menu.current?.contains(target) || trigger.current?.contains(target))
        return;
      setOpen(false);
    };
    // Fixed positioning would detach from the anchor if the list scrolls.
    const close = () => setOpen(false);
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
    };
  }, [open]);
  return (
    <div className={cx("thread-actions")}>
      <button
        ref={trigger}
        type="button"
        className={cx("icon-button small thread-more")}
        aria-label={`更多会话操作：${thread.title}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        onKeyDown={(event) => {
          if (event.key === "Escape" && open) {
            event.preventDefault();
            event.stopPropagation();
            dismiss(true);
          }
        }}
      >
        <Icon name="more" size={16} />
      </button>
      {open && (
        <div
          ref={menu}
          role="menu"
          aria-label={`${thread.title} 的会话操作`}
          className={cx("thread-menu")}
          style={{ left: position.left, top: position.top }}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.preventDefault();
              event.stopPropagation();
              dismiss(true);
              return;
            }
            if (
              !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)
            )
              return;
            event.preventDefault();
            const items = [
              ...(menu.current?.querySelectorAll<HTMLElement>(
                '[role="menuitem"]',
              ) ?? []),
            ];
            const at = items.indexOf(document.activeElement as HTMLElement);
            const next =
              event.key === "Home"
                ? 0
                : event.key === "End"
                  ? items.length - 1
                  : (at + (event.key === "ArrowUp" ? -1 : 1) + items.length) %
                    items.length;
            items[next]?.focus();
          }}
        >
          <button type="button" role="menuitem" onClick={() => choose(onEdit)}>
            编辑
          </button>
          <button type="button" role="menuitem" onClick={() => choose(onRename)}>
            重命名
          </button>
          <button
            type="button"
            role="menuitem"
            className={cx("danger")}
            onClick={() => choose(onDelete)}
          >
            删除
          </button>
        </div>
      )}
    </div>
  );
}
