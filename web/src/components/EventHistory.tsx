import { useEffect, useRef, useState } from "react";
import {
  eventHistory,
  type Agent,
  type EventPage,
  type Source,
} from "../api/client";
import { cx } from "../styles/classes";
import { errorText } from "../lib/presentation";
import { rememberCursor } from "./MessageCard";
import { ToolTimeline } from "./ToolTimeline";

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
    <section className={cx("event-history")} aria-label="历史事件">
      <div className={cx("event-history-head")}>
        <strong>历史事件</strong>
        <button
          type="button"
          className={cx("ghost small")}
          aria-label="关闭历史事件"
          onClick={close}
        >
          关闭
        </button>
      </div>
      {loading && (
        <p className={cx("muted")} role="status">
          正在加载历史事件…
        </p>
      )}
      {!loading && error && (
        <p className={cx("error")} role="alert">
          {error}
          <button type="button" className={cx("ghost small")} onClick={retry}>
            重试
          </button>
        </p>
      )}
      {!loading && !error && page && (
        <>
          {!page.items.length ? (
            <p className={cx("muted")}>没有更早的事件记录。</p>
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
              aria-label="查看上一页历史事件"
              disabled={index === stack.base || loading}
              onClick={() =>
                setIndex((value) => Math.max(stack.base, value - 1))
              }
            >
              上一页
            </button>
            <small className={cx("muted")}>第 {index + 1} 页</small>
            {stack.base > 0 && (
              <button
                type="button"
                className={cx("ghost small")}
                aria-label="回到历史事件第一页"
                onClick={() => {
                  setStack((current) =>
                    rememberCursor(current, 0, initialCursor),
                  );
                  setIndex(0);
                }}
              >
                回到第一页
              </button>
            )}
            <button
              type="button"
              className={cx("ghost small")}
              aria-label="查看下一页历史事件"
              disabled={!page.next_cursor || loading}
              onClick={() => {
                const next = index + 1;
                setStack((current) =>
                  rememberCursor(current, next, page.next_cursor),
                );
                setIndex(next);
              }}
            >
              下一页
            </button>
          </div>
        </>
      )}
    </section>
  );
}
