import { useEffect, useRef, useState } from "react";
import {
  eventHistory,
  type Agent,
  type EventPage,
  type Source,
} from "../api/client";
import { t, useLanguage } from "../lib/i18n";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import panels from "../styles/panels.module.css";
import { errorText } from "../lib/presentation";
import { rememberCursor } from "./MessageCard";
import { ToolTimeline } from "./ToolTimeline";

const cx = scoped(base, controls, layout, conversation, panels);

type EventHistoryProps = {
  runID: string;
  after: number;
  agents: Agent[];
  sources: Source[];
  onSource?: (id: string) => void;
  close: () => void;
};

/**
 * One page of run history at a time. `after` is the sequence to start from, so
 * opening it from a tool record jumps straight to that call's later events.
 */
export function EventHistory(props: EventHistoryProps) {
  // A new run or start sequence is a different history: remount so no cursor or
  // in-flight page from the previous view can leak into it.
  return (
    <EventHistoryView key={`${props.runID}:${props.after}`} {...props} />
  );
}

function EventHistoryView({
  runID,
  after,
  agents,
  sources,
  onSource,
  close,
}: EventHistoryProps) {
  useLanguage();
  const initialCursor = after ? String(after) : "";
  const [page, setPage] = useState<EventPage>();
  // Page indexes are absolute from this history's start; base is the oldest
  // cursor still retained after the bounded window advances.
  const [stack, setStack] = useState({ base: 0, items: [initialCursor] });
  const [index, setIndex] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const cursor = stack.items[index - stack.base] ?? initialCursor;
  const [requested, setRequested] = useState(-1);

  useEffect(() => {
    const request = ++generation.current;
    setLoading(true);
    setError("");
    eventHistory(runID, cursor)
      .then((result) => {
        if (request === generation.current) setPage(result);
      })
      .catch((loadError) => {
        if (request === generation.current) setError(errorText(loadError));
      })
      .finally(() => {
        if (request === generation.current) setLoading(false);
      });
    return () => {
      generation.current++;
    };
  }, [runID, cursor, requested]);

  const retry = () => setRequested((value) => value + 1);
  return (
    <section
      className={cx("event-history")}
      aria-label={t("历史事件", "Event history")}
    >
      <div className={cx("event-history-head")}>
        <strong>{t("历史事件", "Event history")}</strong>
        <button
          type="button"
          className={cx("ghost small")}
          aria-label={t("关闭历史事件", "Close event history")}
          onClick={close}
        >
          {t("关闭", "Close")}
        </button>
      </div>
      {loading && (
        <p className={cx("muted")} role="status">
          {t("正在加载历史事件…", "Loading event history…")}
        </p>
      )}
      {!loading && error && (
        <p className={cx("error")} role="alert">
          {error}
          <button type="button" className={cx("ghost small")} onClick={retry}>
            {t("重试", "Retry")}
          </button>
        </p>
      )}
      {!loading && !error && page && (
        <>
          {!page.items.length ? (
            <p className={cx("muted")}>
              {t("没有更早的事件记录。", "No earlier event records.")}
            </p>
          ) : (
            <ToolTimeline
              mode="history"
              events={page.items}
              agents={agents}
              sources={sources}
              onSource={onSource}
            />
          )}
          <div className={cx("paging-actions")}>
            <button
              type="button"
              className={cx("ghost small")}
              aria-label={t("查看上一页历史事件", "Previous page of event history")}
              disabled={index === stack.base || loading}
              onClick={() =>
                setIndex((value) => Math.max(stack.base, value - 1))
              }
            >
              {t("上一页", "Previous")}
            </button>
            <small className={cx("muted")}>
              {t(`第 ${index + 1} 页`, `Page ${index + 1}`)}
            </small>
            {stack.base > 0 && (
              <button
                type="button"
                className={cx("ghost small")}
                aria-label={t("回到历史事件第一页", "First page of event history")}
                onClick={() => {
                  setStack((current) =>
                    rememberCursor(current, 0, initialCursor),
                  );
                  setIndex(0);
                }}
              >
                {t("回到第一页", "First page")}
              </button>
            )}
            <button
              type="button"
              className={cx("ghost small")}
              aria-label={t("查看下一页历史事件", "Next page of event history")}
              disabled={!page.next_cursor || loading}
              onClick={() => {
                const next = index + 1;
                setStack((current) =>
                  rememberCursor(current, next, page.next_cursor),
                );
                setIndex(next);
              }}
            >
              {t("下一页", "Next")}
            </button>
          </div>
        </>
      )}
    </section>
  );
}
