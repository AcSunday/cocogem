import { Select } from "./Select";
import { useState } from "react";
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
            <button
              className={cx("icon-button small thread-delete")}
              aria-label="删除会话"
              title={`删除“${thread.title}”`}
              onClick={() => onDeleteThread(thread)}
            >
              <Icon name="trash" size={14} />
            </button>
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
          <Icon name="settings" size={14} /> 服务与设置
        </button>
        <AppearanceSelect value={appearance} onChange={onAppearance} />
        <p>本地工作空间 · 数据留在本机</p>
      </div>
    </aside>
  );
}
