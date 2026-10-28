import { useRef, useState } from "react";
import {
  messageContent,
  type Agent,
  type Artifact,
  type Attempt,
  type Event,
  type Message,
  type MessageContentPage,
  type Run,
  type Source,
  type Step,
} from "../api/client";
import {
  deliveredArtifacts,
  deliveredMessageIDs,
  deliveredRun,
  missingDeliveryMessages,
} from "../lib/delivery";
import { errorText, messageModel, roleLabel } from "../lib/presentation";
import { cx } from "../styles/classes";
import { ArtifactCard } from "./ArtifactCard";
import { Avatar } from "./Avatar";
import { MessageCard, rememberCursor, type CursorStack } from "./MessageCard";
import { RunDivider } from "./RunDivider";
import { Markdown } from "./Markdown";

type FinalPage = {
  page: MessageContentPage;
  index: number;
  stack: CursorStack;
};

const firstCursor = (): CursorStack => ({ base: 0, items: [""] });

/** Final messages outside the current page are read by exact id, never by paging all history. */
function MissingFinalResults({
  runID,
  messageIDs,
  agents = [],
  steps = [],
}: {
  runID: string;
  messageIDs: string[];
  agents?: Agent[];
  steps?: Step[];
}) {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<Record<string, FinalPage>>({});
  const [loading, setLoading] = useState<Record<string, boolean>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  // A newer request for a message supersedes an older response for it.
  const requests = useRef<Record<string, number>>({});

  const load = async (messageID: string, index: number, cursor: string) => {
    const request = (requests.current[messageID] ?? 0) + 1;
    requests.current[messageID] = request;
    setLoading((current) => ({ ...current, [messageID]: true }));
    setErrors((current) => {
      if (!(messageID in current)) return current;
      const next = { ...current };
      delete next[messageID];
      return next;
    });
    try {
      const page = await messageContent(runID, messageID, cursor);
      if (page.message_id !== messageID)
        throw new Error("返回的最终结果与请求不一致");
      if (requests.current[messageID] !== request) return;
      setItems((current) => ({
        ...current,
        [messageID]: {
          page,
          index,
          stack: rememberCursor(
            current[messageID]?.stack ?? firstCursor(),
            index,
            cursor,
          ),
        },
      }));
    } catch (failure) {
      if (requests.current[messageID] === request)
        setErrors((current) => ({
          ...current,
          [messageID]: errorText(failure),
        }));
    } finally {
      if (requests.current[messageID] === request)
        setLoading((current) => ({ ...current, [messageID]: false }));
    }
  };

  const expand = () => {
    const next = !open;
    setOpen(next);
    if (!next) return;
    // Loaded pages stay; only missing targets are retried on a fresh expand.
    for (const messageID of messageIDs)
      if (!items[messageID] && !loading[messageID])
        void load(messageID, 0, "");
  };

  const cursorAt = (messageID: string, absolute: number) => {
    const stack = items[messageID]?.stack ?? firstCursor();
    const relative = absolute - stack.base;
    return relative >= 0 && relative < stack.items.length
      ? stack.items[relative]
      : "";
  };

  return (
    <div className={cx("final-results")}>
      <button
        type="button"
        className={cx("ghost small")}
        aria-expanded={open}
        onClick={expand}
      >
        {open ? "收起最终结果" : "查看最终结果"}
        {messageIDs.length > 1 ? `（${messageIDs.length} 条）` : ""}
      </button>
      {open &&
        messageIDs.map((messageID, position) => {
          const item = items[messageID];
          const label =
            messageIDs.length > 1 ? `最终结果 ${position + 1}` : "最终结果";
          if (!item) {
            return (
              <section
                key={messageID}
                className={cx("final-result")}
                aria-label={label}
              >
                {errors[messageID] ? (
                  <span className={cx("error")} role="alert">
                    {errors[messageID]}
                    <button
                      type="button"
                      className={cx("ghost small")}
                      disabled={loading[messageID]}
                      onClick={() => void load(messageID, 0, "")}
                    >
                      重试
                    </button>
                  </span>
                ) : (
                  <small className={cx("muted")} role="status">
                    正在加载最终结果…
                  </small>
                )}
              </section>
            );
          }
          // Author and model come from the message's own snapshot. A legacy
          // response without that metadata stays visibly unlabelled.
          const meta = item.page.message;
          const agent = meta?.agent_id
            ? agents.find((candidate) => candidate.id === meta.agent_id)
            : undefined;
          const name =
            meta?.agent_name?.trim() || agent?.display_name?.trim() || "";
          const model = meta ? messageModel(meta) : "";
          const role = meta?.step_id
            ? steps.find((step) => step.id === meta.step_id)?.role
            : undefined;
          return (
            <section
              key={messageID}
              className={cx("final-result")}
              aria-label={label}
            >
              <div className={cx("message-meta")}>
                {name ? (
                  <>
                    <Avatar
                      name={name}
                      id={meta?.agent_id}
                      avatar={agent?.avatar}
                      index={Math.max(
                        0,
                        agents.findIndex(
                          (candidate) => candidate.id === meta?.agent_id,
                        ),
                      )}
                      small
                    />
                    <strong>{name}</strong>
                  </>
                ) : (
                  <strong>历史最终回复</strong>
                )}
                {model && (
                  <small className={cx("message-model")}>（{model}）</small>
                )}
                {role && (
                  <span className={cx("role-chip")}>{roleLabel(role)}</span>
                )}
              </div>
              <Markdown text={item.page.content || "本次回复没有正文。"} />
              {(item.page.has_more ||
                item.index > 0 ||
                loading[messageID] ||
                errors[messageID]) && (
                <div className={cx("message-content-paging")}>
                  {(item.page.has_more || item.index > 0) && (
                    <small className={cx("muted")}>
                      第 {item.index + 1} 页
                    </small>
                  )}
                  {loading[messageID] && (
                    <small className={cx("muted")} role="status">
                      正在加载…
                    </small>
                  )}
                  {errors[messageID] && (
                    <span className={cx("error")} role="alert">
                      {errors[messageID]}
                      <button
                        type="button"
                        className={cx("ghost small")}
                        disabled={loading[messageID]}
                        onClick={() =>
                          void load(
                            messageID,
                            item.index,
                            cursorAt(messageID, item.index),
                          )
                        }
                      >
                        重试
                      </button>
                    </span>
                  )}
                  <span className={cx("paging-actions")}>
                    <button
                      type="button"
                      className={cx("ghost small")}
                      aria-label="查看上一页最终结果"
                      disabled={
                        item.index === 0 ||
                        loading[messageID] ||
                        item.index - 1 < item.stack.base
                      }
                      onClick={() =>
                        void load(
                          messageID,
                          item.index - 1,
                          cursorAt(messageID, item.index - 1),
                        )
                      }
                    >
                      上一页
                    </button>
                    {item.index > 0 && item.index - 1 < item.stack.base && (
                      <button
                        type="button"
                        className={cx("ghost small")}
                        aria-label="回到第一页最终结果"
                        disabled={loading[messageID]}
                        onClick={() => void load(messageID, 0, "")}
                      >
                        第一页
                      </button>
                    )}
                    <button
                      type="button"
                      className={cx("ghost small")}
                      aria-label="查看下一页最终结果"
                      disabled={!item.page.has_more || loading[messageID]}
                      onClick={() =>
                        void load(
                          messageID,
                          item.index + 1,
                          item.page.next_cursor ?? "",
                        )
                      }
                    >
                      下一页
                    </button>
                  </span>
                </div>
              )}
            </section>
          );
        })}
    </div>
  );
}

/** One user turn: the reader's request, the folded process and the final delivery. */
export function RunSegment({
  run,
  index,
  steps,
  attempts = [],
  messages,
  sources = [],
  artifacts = [],
  members = [],
  connection,
  liveness,
  events,
  latest,
  busy,
  onSnapshot,
  onRetry,
  onResume,
  onRefresh,
  onSource,
  onArtifact,
}: {
  run: Run;
  index: number;
  steps?: Step[];
  attempts?: Attempt[];
  messages: Message[];
  sources?: Source[];
  artifacts?: Artifact[];
  members?: Agent[];
  connection?: string;
  liveness?: Event[];
  events?: Event[];
  latest: boolean;
  busy: boolean;
  onSnapshot: () => void;
  onRetry: () => void;
  onResume: () => void;
  onRefresh?: () => void;
  onSource?: (id: string) => void;
  onArtifact?: (artifact: Artifact) => void;
}) {
  const own = messages.filter((message) => message.run_id === run.id);
  const delivered = deliveredRun(run);
  const finalIDs = deliveredMessageIDs(run);
  const finalMessages = delivered
    ? own.filter(
        (message) => message.role !== "user" && finalIDs.has(message.id),
      )
    : own.filter((message) => message.role !== "user");
  const processMessages = delivered
    ? own.filter(
        (message) => message.role !== "user" && !finalIDs.has(message.id),
      )
    : [];
  const finalArtifacts = deliveredArtifacts(run, artifacts);
  const missingFinal = missingDeliveryMessages(run, own);
  // Delivery refs are authoritative; an artifact page that has not caught up
  // must stay visible with a reload path instead of collapsing to nothing.
  const missingArtifacts = delivered
    ? (run.delivery?.artifact_refs ?? []).filter(
        (ref) =>
          !finalArtifacts.some(
            (artifact) =>
              artifact.id === ref.id && artifact.version === ref.version,
          ),
      )
    : [];
  const processCount = steps?.length || processMessages.length;
  const card = (message: Message) => (
    <MessageCard
      key={message.id}
      message={message}
      index={Math.max(
        0,
        members.findIndex((member) => member.id === message.agent_id),
      )}
      avatar={
        run.agents?.find((agent) => agent.id === message.agent_id)?.avatar
      }
      role={steps?.find((step) => step.id === message.step_id)?.role}
    />
  );
  return (
    <div className={cx("run-segment")}>
      {own
        .filter((message) => message.role === "user")
        .map((message) => (
          <MessageCard key={message.id} message={message} index={0} />
        ))}
      <RunDivider
        run={run}
        index={index}
        steps={steps}
        attempts={attempts}
        messages={messages}
        sources={sources}
        artifacts={artifacts}
        connection={connection}
        liveness={liveness}
        events={events}
        latest={latest}
        busy={busy}
        processCount={processCount}
        onSnapshot={onSnapshot}
        onRetry={onRetry}
        onResume={onResume}
        onSource={onSource}
        onArtifact={onArtifact}
      >
        {processMessages.map(card)}
      </RunDivider>
      {finalMessages.map(card)}
      {missingFinal.length > 0 && (
        <MissingFinalResults
          key={`${run.id}:${missingFinal.join(",")}`}
          runID={run.id}
          messageIDs={missingFinal}
          agents={run.agents ?? []}
          steps={steps}
        />
      )}
      {missingArtifacts.length > 0 && (
        <p className={cx("muted")} role="status">
          {finalArtifacts.length === 0 ? "最终产物正在加载…" : "部分最终产物正在加载…"}
          {onRefresh && (
            <button
              type="button"
              className={cx("ghost small")}
              onClick={onRefresh}
            >
              重新加载
            </button>
          )}
        </p>
      )}
      <div
        className={cx("run-artifacts")}
        aria-label={`第 ${index} 次协作产物`}
      >
        {finalArtifacts.map((artifact) => (
          <ArtifactCard
            key={`${artifact.id}:${artifact.version}`}
            artifact={artifact}
            onOpen={(selected) => onArtifact?.(selected)}
          />
        ))}
      </div>
    </div>
  );
}
