import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  messageContent,
  sourceOriginalImageURL,
  statusText,
  type Message,
  type MessageContentPage,
  type Source,
} from "../api/client";
import type { FileRef, ImagePreview } from "../lib/fileRefs";
import { t, useLanguage } from "../lib/i18n";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import {
  bytesLabel,
  errorText,
  formatMessageTime,
  messageModel,
  roleLabel,
} from "../lib/presentation";
import { Avatar } from "./Avatar";
import { Icon } from "./Icon";
import { Markdown, type MentionMember } from "./Markdown";

const cx = scoped(base, controls, layout, conversation);

/** Cursors are small, but a long read must not grow the page state forever. */
const MAX_CONTENT_CURSORS = 64;

/**
 * Maps a message status to its visual treatment. Terminal-good states stay
 * silent: the delivered message is the evidence. Live states pulse, failures
 * stand out.
 */
function messageStatusKind(
  status: string,
): "live" | "error" | "warn" | "done" {
  if (status === "processing" || status === "queued" || status === "running")
    return "live";
  if (status === "failed") return "error";
  if (status === "succeeded" || status === "completed" || status === "")
    return "done";
  return "warn";
}

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
  role,
  avatar,
  executor,
  members = [],
  threadID,
  sources,
  visualSources = [],
  onOpenFile,
  onOpenImage,
  final = false,
  activity,
  retryLabel,
  retryDisabled = false,
  onRetry,
}: {
  message: Message;
  role?: string;
  avatar?: string;
  executor?: string;
  members?: MentionMember[];
  threadID?: string;
  sources?: Source[];
  visualSources?: Source[];
  onOpenFile?: (ref: FileRef) => void;
  onOpenImage?: (preview: ImagePreview) => void;
  /** Delivered results fill the member column so the run footer can align. */
  final?: boolean;
  /** This member step's tool activity, shown above its reply. */
  activity?: ReactNode;
  /** A precise member retry stays beside the reply it replaces. */
  retryLabel?: string;
  retryDisabled?: boolean;
  onRetry?: () => void;
}) {
  useLanguage();
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
  const retryTarget =
    retryLabel || message.agent_name || t("成员", "member");
  return (
    <article
      className={cx(
        `message ${user ? "user-message" : "member-message"} ${final ? "final-message" : ""}`,
      )}
      aria-label={user ? t("你的消息", "Your message") : undefined}
    >
      {!user && (
        <Avatar
          name={message.agent_name}
          avatar={avatar}
          executor={executor}
        />
      )}
      <div className={cx("message-content")}>
        <div className={cx("message-meta")}>
          <strong>{user ? t("你", "You") : message.agent_name}</strong>
          {model && (
            <small className={cx("message-model")}>
              {t(`（${model}）`, `(${model})`)}
            </small>
          )}
          {role && <span className={cx("role-chip")}>{roleLabel(role)}</span>}
          <time dateTime={message.created_at}>
            {formatMessageTime(message.created_at)}
          </time>
          {messageStatusKind(message.status) !== "done" && (
            <small
              className={cx(`status-${messageStatusKind(message.status)}`)}
            >
              {statusText[message.status]}
            </small>
          )}
        </div>
        <div className={cx("message-body")}>
          {!user && activity}
          {(user || shown || !generating || !activity) && (
            <div className={cx(!user && activity ? "message-result" : "")}>
              <Markdown
                text={
                  shown ||
                  (user
                    ? ""
                    : generating
                      ? t("正在准备回复…", "Preparing reply…")
                      : t("本次回复没有正文。", "This reply has no text content."))
                }
                members={members}
                threadID={threadID}
                sources={sources}
                onOpenFile={onOpenFile}
                onOpenImage={onOpenImage}
              />
            </div>
          )}
          {!user && visualSources.length > 0 && (
            <div
              className={cx("visual-evidence-strip")}
              data-testid="visual-evidence-strip"
              aria-label={t("视觉验收截图", "Visual evidence")}
            >
              {visualSources.map((srcItem) => {
                const imgURL = sourceOriginalImageURL(srcItem.id);
                const imgTitle =
                  srcItem.title || srcItem.path || t("验收截图", "Visual evidence");
                return (
                  <button
                    key={srcItem.id}
                    type="button"
                    className={cx("visual-evidence-thumb")}
                    title={t(`点击放大查看：${imgTitle}`, `Inspect image: ${imgTitle}`)}
                    onClick={() =>
                      onOpenImage?.({
                        src: imgURL,
                        title: imgTitle,
                        downloadUrl: imgURL,
                        sourceId: srcItem.id,
                        path: srcItem.path,
                      })
                    }
                  >
                    <img src={imgURL} alt={imgTitle} loading="lazy" />
                    <span className={cx("visual-evidence-caption")}>
                      {imgTitle}
                    </span>
                  </button>
                );
              })}
            </div>
          )}
        </div>
        {onRetry && (
          <div className={cx("message-retry")}>
            <button
              type="button"
              className={cx("ghost small")}
              aria-label={t(`重试 ${retryTarget}`, `Retry ${retryTarget}`)}
              title={t(
                "只补跑该成员并保留已有上下文",
                "Retries only this member while preserving existing context",
              )}
              disabled={retryDisabled}
              onClick={onRetry}
            >
              <Icon name="refresh" size={14} />
              {t(`重试 ${retryTarget}`, `Retry ${retryTarget}`)}
            </button>
          </div>
        )}
        {message.content_truncated && (
          <div className={cx("message-content-paging")}>
            <small className={cx("muted")}>
              {generating
                ? t(
                    `正在生成 · 已生成 ${bytesLabel(message.content_bytes ?? total)}`,
                    `Generating · ${bytesLabel(message.content_bytes ?? total)} generated`,
                  )
                : open
                  ? hasNewContent
                    ? t(
                        `已生成 ${bytesLabel(message.content_bytes ?? total)} · 当前页为较早快照`,
                        `Generated ${bytesLabel(message.content_bytes ?? total)} · Viewing an earlier snapshot`,
                      )
                    : t(`全文 ${bytesLabel(total)}`, `Full text ${bytesLabel(total)}`)
                  : t(
                      `全文 ${bytesLabel(total)} · 当前为部分预览`,
                      `Full text ${bytesLabel(total)} · Showing preview`,
                    )}
              {open && page?.has_more
                ? t(" · 还有更多内容", " · More content available")
                : ""}
            </small>
            {open && loading && (
              <small className={cx("muted")} role="status">
                {t("正在加载全文…", "Loading full text…")}
              </small>
            )}
            {open && failure && (
              <span className={cx("error")} role="alert">
                {failure.message}
                <button
                  type="button"
                  className={cx("ghost small")}
                  aria-label={t("重新加载全文", "Reload full text")}
                  onClick={() => void load(failure.index, failure.cursor)}
                >
                  {t("重试", "Retry")}
                </button>
              </span>
            )}
            {open && page && (
              <span className={cx("paging-actions")}>
                <button
                  type="button"
                  className={cx("ghost small")}
                  aria-label={t("查看上一页全文", "Previous page of full text")}
                  disabled={
                    pageIndex === 0 || loading || pageIndex - 1 < stack.base
                  }
                  onClick={() =>
                    void load(pageIndex - 1, cursorAt(pageIndex - 1))
                  }
                >
                  {t("上一页", "Previous")}
                </button>
                {pageIndex > 0 && pageIndex - 1 < stack.base && (
                  <button
                    type="button"
                    className={cx("ghost small")}
                    aria-label={t("回到第一页全文", "First page of full text")}
                    disabled={loading}
                    onClick={() => void load(0, "")}
                  >
                    {t("第一页", "First page")}
                  </button>
                )}
                <small className={cx("muted")}>
                  {t(`第 ${pageIndex + 1} 页`, `Page ${pageIndex + 1}`)}
                </small>
                <button
                  type="button"
                  className={cx("ghost small")}
                  aria-label={t("查看下一页全文", "Next page of full text")}
                  disabled={!page.has_more || loading}
                  onClick={() =>
                    void load(pageIndex + 1, page.next_cursor ?? "")
                  }
                >
                  {t("下一页", "Next")}
                </button>
                {hasNewContent && (
                  <button
                    type="button"
                    className={cx("ghost small")}
                    aria-label={t("查看最新全文", "View latest full text")}
                    disabled={loading}
                    onClick={() => void load(0, "")}
                  >
                    {t("查看最新全文", "View latest full text")}
                  </button>
                )}
              </span>
            )}
            <button
              type="button"
              className={cx("ghost small")}
              aria-expanded={open}
              aria-label={
                open
                  ? t("收起全文", "Collapse full text")
                  : t("展开全文", "Expand full text")
              }
              onClick={open ? collapse : expand}
            >
              {open
                ? t("收起全文", "Collapse full text")
                : t("展开全文", "Expand full text")}
            </button>
          </div>
        )}
      </div>
    </article>
  );
}
