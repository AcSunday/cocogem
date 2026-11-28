import { useEffect, useRef, useState } from "react";
import {
  messageContent,
  statusText,
  terminal,
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
import {
  projectToolActivity,
  splitProcessReply,
  type ToolActivity,
} from "../lib/toolActivity";
import {
  isImageMediaOrPath,
  type FileRef,
  type ImagePreview,
} from "../lib/fileRefs";
import { t, useLanguage } from "../lib/i18n";
import type { RetryRef } from "../lib/retry";
import {
  errorText,
  messageModel,
  roleLabel,
  runDuration,
  runMethod,
  runUsageSummary,
  runUsageTokens,
  statusIconName,
} from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import { ArtifactCard } from "./ArtifactCard";
import { Avatar } from "./Avatar";
import { EventHistory } from "./EventHistory";
import { Icon } from "./Icon";
import {
  MemberToolActivity,
  PendingToolAuthorization,
  ToolActivityBlock,
  type PermissionSlot,
} from "./MemberToolActivity";
import { MessageCard, rememberCursor, type CursorStack } from "./MessageCard";
import { RunDivider } from "./RunDivider";
import { Markdown } from "./Markdown";

const cx = scoped(base, controls, layout, conversation);

type FinalPage = {
  page: MessageContentPage;
  index: number;
  stack: CursorStack;
};

/** The exact page whose request failed, so Retry never reloads a good page. */
type FinalPageError = { index: number; cursor: string; message: string };

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
  useLanguage();
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<Record<string, FinalPage>>({});
  const [loading, setLoading] = useState<Record<string, boolean>>({});
  const [errors, setErrors] = useState<Record<string, FinalPageError>>({});
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
        throw new Error(
          t(
            "返回的最终结果与请求不一致",
            "Returned final result does not match the requested message",
          ),
        );
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
          [messageID]: {
            index,
            cursor,
            message: errorText(failure),
          },
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
      if (!items[messageID] && !loading[messageID]) void load(messageID, 0, "");
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
        {open
          ? t("收起最终结果", "Hide final result")
          : t("查看最终结果", "View final result")}
        {messageIDs.length > 1
          ? t(`（${messageIDs.length} 条）`, ` (${messageIDs.length})`)
          : ""}
      </button>
      {open &&
        messageIDs.map((messageID, position) => {
          const item = items[messageID];
          const failure = errors[messageID];
          const label =
            messageIDs.length > 1
              ? t(`最终结果 ${position + 1}`, `Final result ${position + 1}`)
              : t("最终结果", "Final result");
          if (!item) {
            return (
              <section
                key={messageID}
                className={cx("final-result")}
                aria-label={label}
              >
                {failure ? (
                  <span className={cx("error")} role="alert">
                    {failure.message}
                    <button
                      type="button"
                      className={cx("ghost small")}
                      disabled={loading[messageID]}
                      onClick={() =>
                        void load(messageID, failure.index, failure.cursor)
                      }
                    >
                      {t("重试", "Retry")}
                    </button>
                  </span>
                ) : (
                  <small className={cx("muted")} role="status">
                    {t("正在加载最终结果…", "Loading final result…")}
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
                      avatar={agent?.avatar}
                      small
                    />
                    <strong>{name}</strong>
                  </>
                ) : (
                  <strong>{t("历史最终回复", "Historical final reply")}</strong>
                )}
                {model && (
                  <small className={cx("message-model")}>
                    {t(`（${model}）`, `(${model})`)}
                  </small>
                )}
                {role && (
                  <span className={cx("role-chip")}>{roleLabel(role)}</span>
                )}
              </div>
              <Markdown
                text={
                  item.page.content ||
                  t("本次回复没有正文。", "This reply has no text content.")
                }
                members={agents}
              />
              {(item.page.has_more ||
                item.index > 0 ||
                loading[messageID] ||
                failure) && (
                <div className={cx("message-content-paging")}>
                  {(item.page.has_more || item.index > 0) && (
                    <small className={cx("muted")}>
                      {t(`第 ${item.index + 1} 页`, `Page ${item.index + 1}`)}
                    </small>
                  )}
                  {loading[messageID] && (
                    <small className={cx("muted")} role="status">
                      {t("正在加载…", "Loading…")}
                    </small>
                  )}
                  {failure && (
                    <span className={cx("error")} role="alert">
                      {failure.message}
                      <button
                        type="button"
                        className={cx("ghost small")}
                        disabled={loading[messageID]}
                        onClick={() =>
                          void load(messageID, failure.index, failure.cursor)
                        }
                      >
                        {t("重试", "Retry")}
                      </button>
                    </span>
                  )}
                  <span className={cx("paging-actions")}>
                    <button
                      type="button"
                      className={cx("ghost small")}
                      aria-label={t(
                        "查看上一页最终结果",
                        "Previous page of final result",
                      )}
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
                      {t("上一页", "Previous")}
                    </button>
                    {item.index > 0 && item.index - 1 < item.stack.base && (
                      <button
                        type="button"
                        className={cx("ghost small")}
                        aria-label={t(
                          "回到第一页最终结果",
                          "First page of final result",
                        )}
                        disabled={loading[messageID]}
                        onClick={() => void load(messageID, 0, "")}
                      >
                        {t("第一页", "First page")}
                      </button>
                    )}
                    <button
                      type="button"
                      className={cx("ghost small")}
                      aria-label={t(
                        "查看下一页最终结果",
                        "Next page of final result",
                      )}
                      disabled={!item.page.has_more || loading[messageID]}
                      onClick={() =>
                        void load(
                          messageID,
                          item.index + 1,
                          item.page.next_cursor ?? "",
                        )
                      }
                    >
                      {t("下一页", "Next")}
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

/** One member retry folded into the user turn it replaces. */
export type RetryRun = { run: Run; steps?: Step[]; attempts?: Attempt[] };

/** One user turn: the reader's request, the folded process and the final delivery. */
export function RunSegment({
  run,
  retries = [],
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
  retryableSteps = [],
  onResume,
  onRefresh,
  onSource,
  onArtifact,
  onOpenFile,
  onOpenImage,
  permissionSlot,
}: {
  run: Run;
  retries?: RetryRun[];
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
  onRetry: (runID: string, stepID: string) => void;
  retryableSteps?: RetryRef[];
  onResume: (runID: string) => void;
  onRefresh?: () => void;
  onSource?: (id: string) => void;
  onArtifact?: (artifact: Artifact) => void;
  onOpenFile?: (ref: FileRef) => void;
  onOpenImage?: (preview: ImagePreview) => void;
  /** Approval actions for a pending tool request; decisions stay server-owned. */
  permissionSlot?: PermissionSlot;
}) {
  useLanguage();
  const groupRuns = [run, ...retries.map((item) => item.run)];
  const latestRun = retries.at(-1)?.run ?? run;
  // The latest retry owns the turn's visible lifecycle, while the original
  // run keeps its user message, workflow identity and roster.
  const displayRun: Run = retries.length
    ? {
        ...run,
        status: latestRun.status,
        error: latestRun.error,
        failure_kind: latestRun.failure_kind,
        finished_at: latestRun.finished_at,
        delivery: latestRun.delivery ?? run.delivery,
        recovery_count: latestRun.recovery_count,
        recovery_note: latestRun.recovery_note,
        agents: groupRuns
          .flatMap((item) => item.agents ?? [])
          .filter(
            (agent, index, all) =>
              all.findIndex((candidate) => candidate.id === agent.id) === index,
          ),
      }
    : run;
  const groupIDs = new Set(groupRuns.map((item) => item.id));
  const superseded = new Set(
    retries.flatMap((item) =>
      item.run.retry_of_step_id
        ? [`${item.run.input?.retry_of_run_id}/${item.run.retry_of_step_id}`]
        : [],
    ),
  );
  const own = messages.filter((message) => groupIDs.has(message.run_id));
  // A failed reply disappears when its exact retry is accepted. Keeping the
  // rule here also covers a stale page loaded before that replacement.
  const visibleOwn = own.filter(
    (message) =>
      message.role === "user" ||
      message.status === "completed" ||
      !superseded.has(`${message.run_id}/${message.step_id}`),
  );
  const finalIDs = new Set<string>();
  for (const item of groupRuns) {
    const scoped = visibleOwn.filter(
      (message) => message.run_id === item.id && message.role !== "user",
    );
    const runSteps =
      item.id === run.id
        ? (steps ?? [])
        : (retries.find((entry) => entry.run.id === item.id)?.steps ?? []);
    const stepByID = new Map(runSteps.map((step) => [step.id, step]));
    if (deliveredRun(item)) {
      const delivered = deliveredMessageIDs(item);
      for (const message of scoped)
        if (delivered.has(message.id)) finalIDs.add(message.id);
    } else if (!terminal(item.status)) {
      for (const message of scoped) {
        const step = stepByID.get(message.step_id);
        const hasDownstream =
          runSteps.length > 1 &&
          runSteps.some(
            (other) =>
              other.id !== message.step_id &&
              (other.from_step_id === message.step_id ||
                other.depends_on?.includes(message.step_id)),
          );
        const isProcessOnly =
          step?.role === "task_intake" ||
          item.task_promotion_step_id === message.step_id ||
          (hasDownstream && message.status === "completed");
        if (!isProcessOnly) finalIDs.add(message.id);
      }
    } else {
      for (const message of scoped) {
        const step = stepByID.get(message.step_id);
        if (step?.role === "task_intake" && scoped.length > 1) continue;
        finalIDs.add(message.id);
      }
    }
  }
  // A retried member replaces its failed slot instead of moving to the end of
  // the turn. Steps that were completed peers keep their original order, and a
  // retry of a retry resolves back to the first accepted slot.
  const logicalOrder = new Map(
    (steps ?? []).map((step, position) => [step.id, position]),
  );
  let nextOrder = logicalOrder.size;
  for (const item of retries)
    for (const step of item.steps ?? []) {
      const replaced = step.retry_of_step_id
        ? logicalOrder.get(step.retry_of_step_id)
        : undefined;
      logicalOrder.set(step.id, replaced ?? nextOrder++);
    }
  const ordered = visibleOwn.map((message, position) => ({
    message,
    position,
  }));
  const stepRank = (stepID: string) =>
    logicalOrder.get(stepID) ?? Number.MAX_SAFE_INTEGER;
  if (retries.length)
    ordered.sort((left, right) => {
      return (
        stepRank(left.message.step_id) -
          stepRank(right.message.step_id) ||
        left.position - right.position
      );
    });
  const finalMessages = ordered
    .map((item) => item.message)
    .filter(
      (message) =>
        message.role !== "user" && finalIDs.has(message.id),
    );
  const processMessages = ordered
    .map((item) => item.message)
    .filter(
      (message) =>
        message.role !== "user" && !finalIDs.has(message.id),
    );
  const finalArtifacts = groupRuns.flatMap((item) =>
    deliveredArtifacts(item, artifacts),
  );
  const finalRefs = groupRuns.flatMap((item) =>
    deliveredRun(item) ? (item.delivery?.artifact_refs ?? []) : [],
  );
  // Delivery refs are authoritative; an artifact page that has not caught up
  // must stay visible with a reload path instead of collapsing to nothing.
  const missingArtifacts = finalRefs.filter(
    (ref) =>
      !finalArtifacts.some(
        (artifact) =>
          artifact.id === ref.id && artifact.version === ref.version,
      ),
  );
  const retriedSourceSteps = new Set(
    retries
      .map((item) => item.run.retry_of_step_id)
      .filter((id): id is string => !!id),
  );
  const allSteps = [
    ...(steps ?? []).filter((step) => !retriedSourceSteps.has(step.id)),
    ...retries
      .flatMap((item) => item.steps ?? [])
      .filter((step) => !retriedSourceSteps.has(step.id)),
  ].sort((left, right) => stepRank(left.id) - stepRank(right.id));
  const allAttempts = [
    ...attempts,
    ...retries.flatMap((item) => item.attempts ?? []),
  ];
  // Tool activity belongs to the member step that produced it. `events` is the
  // inspected run's live window; a retried attempt never pairs with an older
  // native call id because the projection keys run + step + attempt + call id.
  // The window may carry other runs' events, so scope it to this turn first.
  const activityGroups = projectToolActivity(
    (events ?? []).filter((event) => groupIDs.has(event.run_id)),
    { mode: "live", runStatus: displayRun.status },
  );
  // The run snapshot is authoritative for historical names and aliases; the
  // current workspace roster is only a fallback for legacy records.
  const mentionMembers = displayRun.agents?.length
    ? displayRun.agents
    : members;
  const badgeStatusIcon = statusIconName(displayRun.status);
  const duration = runDuration(displayRun);
  const usage = runUsageTokens(displayRun, allAttempts);
  const [footerOpen, setFooterOpen] = useState(false);
  const [history, setHistory] = useState<{
    runID: string;
    after: number;
  } | null>(null);
  const footer = useRef<HTMLDivElement>(null);
  const footerPopover = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!footerOpen) return;
    footerPopover.current?.scrollIntoView?.({ block: "nearest" });
  }, [footerOpen]);
  useEffect(() => {
    if (!footerOpen) return;
    const dismiss = (event: PointerEvent | KeyboardEvent) => {
      if (event instanceof KeyboardEvent) {
        if (event.key !== "Escape") return;
      } else if (footer.current?.contains(event.target as Node)) {
        return;
      }
      setFooterOpen(false);
    };
    document.addEventListener("pointerdown", dismiss);
    document.addEventListener("keydown", dismiss);
    return () => {
      document.removeEventListener("pointerdown", dismiss);
      document.removeEventListener("keydown", dismiss);
    };
  }, [footerOpen]);
  const retryFor = (message: Message) =>
    message.role === "user"
      ? undefined
      : retryableSteps.find(
          (failure) =>
            failure.runID === message.run_id &&
            failure.step.id === message.step_id,
        );
  const activityAtStep = new Map(
    activityGroups
      .filter((group) => group.stepID)
      .map((group) => [group.stepID, group] as const),
  );
  // One block per step: the first rendered reply for that step carries it.
  const shownActivity = new Set<string>();
  const activeStepIDs = new Set(
    terminal(displayRun.status)
      ? []
      : allSteps
          .filter((step) => step.status === "running")
          .map((step) => step.id),
  );
  const card = (message: Message, final = false) => {
    const failure = retryFor(message);
    const author = displayRun.agents?.find(
      (agent) => agent.id === message.agent_id,
    );
    const step = allSteps.find((item) => item.id === message.step_id);
    const group =
      message.role === "user" ? undefined : activityAtStep.get(message.step_id);
    const attachGroup = group && !shownActivity.has(group.key) ? group : undefined;
    if (attachGroup) shownActivity.add(attachGroup.key);
    const isActive =
      !terminal(displayRun.status) &&
      (message.status === "processing" || step?.status === "running");
    const { visibleContent, showNarration } = splitProcessReply(
      message.content,
      message.content_truncated,
      message.status === "processing",
      attachGroup,
    );
    const displayMessage =
      visibleContent !== message.content
        ? { ...message, content: visibleContent }
        : message;
    const activity = attachGroup ? (
      <ToolActivityBlock
        group={attachGroup}
        active={isActive}
        showNarration={showNarration}
        sources={sources}
        onOpenFile={onOpenFile}
        onOpenImage={onOpenImage}
        onHistory={(item: ToolActivity) =>
          setHistory({
            runID: item.runID,
            after: Math.max(0, item.seq - 1),
          })
        }
      />
    ) : undefined;
    const stepVisualSources = sources.filter((src) => {
      if (!isImageMediaOrPath(src.media_type || src.path || src.title, src.kind)) {
        return false;
      }
      if (src.capture_step_id && message.step_id) {
        return src.capture_step_id === message.step_id;
      }
      return final && src.run_id === displayRun.id;
    });
    return (
      <MessageCard
        key={message.id}
        message={displayMessage}
        avatar={author?.avatar}
        executor={author?.executor}
        role={step?.role}
        members={mentionMembers}
        threadID={run.thread_id}
        sources={sources}
        visualSources={stepVisualSources}
        onOpenFile={onOpenFile}
        onOpenImage={onOpenImage}
        final={final}
        activity={activity}
        retryLabel={failure?.step.agent_name}
        retryDisabled={busy}
        onRetry={
          failure
            ? () => onRetry(failure.runID, failure.step.id)
            : undefined
        }
      />
    );
  };
  const processCards = processMessages.map((message) => card(message));
  const finalCards = finalMessages.map((message) => card(message, true));
  const unattachedActivity = activityGroups.filter(
    (group) => !shownActivity.has(group.key),
  );
  const processCount =
    allSteps.length ||
    processMessages.length ||
    (finalMessages.length ? 0 : unattachedActivity.length);
  const missingFinal = groupRuns.flatMap((item) => {
    const messageIDs = missingDeliveryMessages(item, messages);
    return messageIDs.length ? [{ run: item, messageIDs }] : [];
  });
  return (
    <div className={cx("run-segment")}>
      {visibleOwn
        .filter((message) => message.role === "user")
        .map((message) => (
          <MessageCard
            key={message.id}
            message={message}
            members={mentionMembers}
          />
        ))}
      <RunDivider
        run={displayRun}
        index={index}
        steps={allSteps}
        attempts={allAttempts}
        messages={messages}
        sources={sources}
        artifacts={artifacts}
        connection={connection}
        liveness={liveness}
        latest={latest}
        busy={busy}
        processCount={processCount}
        onSnapshot={onSnapshot}
        onResume={() => onResume(displayRun.id)}
        onSource={onSource}
        onArtifact={onArtifact}
      >
        {processCards}
        {unattachedActivity.length > 0 && (
          <MemberToolActivity
            groups={unattachedActivity}
            agents={mentionMembers}
            activeStepIDs={activeStepIDs}
            sources={sources}
            onOpenFile={onOpenFile}
            onOpenImage={onOpenImage}
            onHistory={(activity) =>
              setHistory({
                runID: activity.runID,
                after: Math.max(0, activity.seq - 1),
              })
            }
          />
        )}
      </RunDivider>
      {/* Pending authorization sits outside every run/process disclosure so
          folding the log cannot hide a blocked operation. */}
      <PendingToolAuthorization
        groups={activityGroups}
        agents={mentionMembers}
        permissionSlot={permissionSlot}
      />
      {finalCards}
      {history && (
        <EventHistory
          key={`${history.runID}:${history.after}`}
          runID={history.runID}
          after={history.after}
          agents={mentionMembers}
          sources={sources}
          onSource={onSource}
          close={() => setHistory(null)}
        />
      )}
      {terminal(displayRun.status) && (
        <div
          className={cx("run-footer-badge-wrap")}
          ref={footer}
          data-open={footerOpen ? "true" : undefined}
        >
          <button
            type="button"
            className={cx("run-footer-badge")}
            aria-expanded={footerOpen}
            aria-label={t("查看协作摘要详情", "View run summary details")}
            onClick={() => setFooterOpen((current) => !current)}
          >
            <span className={cx("run-footer-identity")}>
              <span>{runMethod(displayRun).label}</span>
              {badgeStatusIcon && (
                <>
                  <span>·</span>
                  <span
                    className={cx(`thread-status ${displayRun.status}`)}
                    role="img"
                    aria-label={statusText[displayRun.status]}
                    title={statusText[displayRun.status]}
                  >
                    <Icon name={badgeStatusIcon} size={12} />
                  </span>
                </>
              )}
            </span>
            {(duration || usage) && (
              <span className={cx("run-footer-meta-sep")}>·</span>
            )}
            {duration && (
              <span className={cx("run-footer-duration")}>{duration}</span>
            )}
            {duration && usage && (
              <span className={cx("run-footer-sep")}>·</span>
            )}
            {usage && (
              <span className={cx("run-footer-usage")} title={usage}>
                {usage}
              </span>
            )}
            {!duration && !usage && (
              <span className={cx("run-footer-mobile-fallback")}>
                {statusText[displayRun.status]}
              </span>
            )}
          </button>
          <div className={cx("run-footer-popover")} ref={footerPopover}>
            <strong>
              {t(`第 ${index} 次协作`, `Run #${index}`)} ·{" "}
              {runMethod(displayRun).label}
            </strong>
            <div>
              {t("状态：", "Status: ")}
              {statusText[displayRun.status]}
            </div>
            {duration && (
              <div>
                {t("耗时：", "Duration: ")}
                {duration}
              </div>
            )}
            {runUsageSummary(displayRun, allAttempts) && (
              <div>{runUsageSummary(displayRun, allAttempts)}</div>
            )}
            {allSteps.length > 0 && (
              <div>
                {t(
                  `过程：共 ${allSteps.length} 步（${allSteps
                    .map((s) => s.agent_name)
                    .filter(Boolean)
                    .join("、")}）`,
                  `Steps: ${allSteps.length} (${allSteps
                    .map((s) => s.agent_name)
                    .filter(Boolean)
                    .join(", ")})`,
                )}
              </div>
            )}
            {displayRun.error && (
              <div className={cx("error")}>
                {t("异常：", "Error: ")}
                {displayRun.error}
              </div>
            )}
            <small className={cx("muted")}>
              {t(
                "详细日志与交接记录已保存在右侧协作面板",
                "Detailed logs and handoff records are available in the collaboration panel",
              )}
            </small>
          </div>
        </div>
      )}
      {missingFinal.map(({ run: missingRun, messageIDs }) => (
        <MissingFinalResults
          key={`${missingRun.id}:${messageIDs.join(",")}`}
          runID={missingRun.id}
          messageIDs={messageIDs}
          agents={displayRun.agents?.length ? displayRun.agents : members}
          steps={allSteps}
        />
      ))}
      {missingArtifacts.length > 0 && (
        <p className={cx("muted")} role="status">
          {finalArtifacts.length === 0
            ? t("最终产物正在加载…", "Loading final deliverables…")
            : t("部分最终产物正在加载…", "Loading remaining deliverables…")}
          {onRefresh && (
            <button
              type="button"
              className={cx("ghost small")}
              onClick={onRefresh}
            >
              {t("重新加载", "Reload")}
            </button>
          )}
        </p>
      )}
      <div
        className={cx("run-artifacts")}
        aria-label={t(`第 ${index} 次协作产物`, `Run #${index} deliverables`)}
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
