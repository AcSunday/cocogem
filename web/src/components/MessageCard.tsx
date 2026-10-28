import { useCallback, useEffect, useRef, useState } from "react";
import {
  messageContent,
  statusText,
  type Message,
  type MessageContentPage,
} from "../api/client";
import { cx } from "../styles/classes";
import {
  bytesLabel,
  errorText,
  formatMessageTime,
  memberTone,
  messageModel,
  roleLabel,
} from "../lib/presentation";
import { Avatar } from "./Avatar";
import { Markdown } from "./Markdown";

/** Cursors are small, but a long read must not grow the page state forever. */
const MAX_CONTENT_CURSORS = 64;

export type CursorStack = { base: number; items: string[] };

/** The exact page whose request failed, so Retry never reloads a good page. */
type PageFailure = { index: number; cursor: string; message: string };

/** Retains only the recent cursor window; `base` keeps absolute page numbers. */
export function rememberCursor(
  current: CursorStack,
  absolute: number,
  cursor: string,
): CursorStack {
  const relative = absolute - current.base;
  if (relative < 0) return { base: absolute, items: [cursor] };
  const items = current.items.slice(
    0,
    Math.min(relative, current.items.length),
  );
  items[relative] = cursor;
  let base = current.base;
  const drop = items.length - MAX_CONTENT_CURSORS;
  if (drop > 0) {
    items.splice(0, drop);
    base += drop;
  }
  return { base, items };
}

export function MessageCard({
  message,
  index,
  role,
  avatar,
}: {
  message: Message;
  index: number;
  role?: string;
  avatar?: string;
}) {
  const user = message.role === "user";
  const model = user ? "" : messageModel(message);
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState<MessageContentPage>();
  // Page indexes are absolute; base is the first cursor still retained.
  const [pageIndex, setPageIndex] = useState(0);
  const [stack, setStack] = useState({ base: 0, items: [""] });
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState<PageFailure>();
  // A different message or run must never receive a response started for the old one.
  const generation = useRef(0);
  const record = `${message.run_id}:${message.id}`;

  useEffect(() => {
    generation.current++;
    setOpen(false);
    setPage(undefined);
    setPageIndex(0);
    setStack({ base: 0, items: [""] });
    setLoading(false);
    setFailure(undefined);
  }, [record]);

  const load = useCallback(
    async (nextIndex: number, cursor: string) => {
      const request = ++generation.current;
      setLoading(true);
      setFailure(undefined);
      try {
        const result = await messageContent(message.run_id, message.id, cursor);
        if (request !== generation.current) return;
        setPage(result);
        setPageIndex(nextIndex);
        setStack((current) => rememberCursor(current, nextIndex, cursor));
      } catch (error) {
        if (request === generation.current)
          setFailure({ index: nextIndex, cursor, message: errorText(error) });
      } finally {
        if (request === generation.current) setLoading(false);
      }
    },
    [message.run_id, message.id],
  );

  const cursorAt = (absolute: number) => {
    const relative = absolute - stack.base;
    return relative >= 0 && relative < stack.items.length
      ? stack.items[relative]
      : "";
  };
  const expand = () => {
    setOpen(true);
    void load(pageIndex, cursorAt(pageIndex));
  };
  const collapse = () => {
    generation.current++;
    setOpen(false);
    setLoading(false);
  };
  const total = page?.content_bytes ?? message.content_bytes ?? 0;
  const generating = message.status === "processing";
  // Snapshot cursors never see later appended text; offer an explicit jump to
  // the newest snapshot instead of silently moving the reader.
  const hasNewContent =
    !!page && (message.content_bytes ?? 0) > (page.content_bytes ?? 0);
  const shown = open && page ? page.content : message.content;
  return (
    <article
      className={cx(
        `message ${user ? "user-message" : `member-message tone-${memberTone(message.agent_id || message.id)}`}`,
      )}
      aria-label={user ? "你的消息" : undefined}
    >
      <div className={cx("message-content")}>
        <div className={cx("message-meta")}>
          {!user && (
            <Avatar
              name={message.agent_name}
              id={message.agent_id}
              avatar={avatar}
              index={index}
              small
            />
          )}
          <strong>{user ? "你" : message.agent_name}</strong>
          {model && (
            <small className={cx("message-model")}>（{model}）</small>
          )}
          {role && <span className={cx("role-chip")}>{roleLabel(role)}</span>}
          <time dateTime={message.created_at}>
            {formatMessageTime(message.created_at)}
          </time>
          {message.status !== "completed" && (
            <small className={cx("live")}>{statusText[message.status]}</small>
          )}
        </div>
        <div className={cx("message-body")}>
          <Markdown
            text={
              shown ||
              (user ? "" : generating ? "正在准备回复…" : "本次回复没有正文。")
            }
          />
        </div>
        {message.content_truncated && (
          <div className={cx("message-content-paging")}>
            <small className={cx("muted")}>
              {generating
                ? `正在生成 · 已生成 ${bytesLabel(message.content_bytes ?? total)}`
                : open
                  ? hasNewContent
                    ? `已生成 ${bytesLabel(message.content_bytes ?? total)} · 当前页为较早快照`
                    : `全文 ${bytesLabel(total)}`
                  : `全文 ${bytesLabel(total)} · 当前为部分预览`}
              {open && page?.has_more ? " · 还有更多内容" : ""}
            </small>
            {open && loading && (
              <small className={cx("muted")} role="status">
                正在加载全文…
              </small>
            )}
            {open && failure && (
              <span className={cx("error")} role="alert">
                {failure.message}
                <button
                  type="button"
                  className={cx("ghost small")}
                  aria-label="重新加载全文"
                  onClick={() => void load(failure.index, failure.cursor)}
                >
                  重试
                </button>
              </span>
            )}
            {open && page && (
              <span className={cx("paging-actions")}>
                <button
                  type="button"
                  className={cx("ghost small")}
                  aria-label="查看上一页全文"
                  disabled={
                    pageIndex === 0 || loading || pageIndex - 1 < stack.base
                  }
                  onClick={() => void load(pageIndex - 1, cursorAt(pageIndex - 1))}
                >
                  上一页
                </button>
                {pageIndex > 0 && pageIndex - 1 < stack.base && (
                  <button
                    type="button"
                    className={cx("ghost small")}
                    aria-label="回到第一页全文"
                    disabled={loading}
                    onClick={() => void load(0, "")}
                  >
                    第一页
                  </button>
                )}
                <small className={cx("muted")}>第 {pageIndex + 1} 页</small>
                <button
                  type="button"
                  className={cx("ghost small")}
                  aria-label="查看下一页全文"
                  disabled={!page.has_more || loading}
                  onClick={() => void load(pageIndex + 1, page.next_cursor ?? "")}
                >
                  下一页
                </button>
                {hasNewContent && (
                  <button
                    type="button"
                    className={cx("ghost small")}
                    aria-label="查看最新全文"
                    disabled={loading}
                    onClick={() => void load(0, "")}
                  >
                    查看最新全文
                  </button>
                )}
              </span>
            )}
            <button
              type="button"
              className={cx("ghost small")}
              aria-expanded={open}
              aria-label={open ? "收起全文" : "展开全文"}
              onClick={open ? collapse : expand}
            >
              {open ? "收起全文" : "展开全文"}
            </button>
          </div>
        )}
      </div>
    </article>
  );
}
