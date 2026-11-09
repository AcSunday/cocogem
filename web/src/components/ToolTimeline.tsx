import type { Agent, Artifact, Event, Message, Source } from "../api/client";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import panels from "../styles/panels.module.css";
import settings from "../styles/settings.module.css";
import { Markdown } from "./Markdown";
import {
  livenessLabel,
  memberLabel,
  methodEventLabel,
  noticeLabel,
  usageSummary,
} from "../lib/presentation";
import {
  missingFinishLabel,
  projectToolActivity,
  toolStatusLabel,
  type ToolActivity,
  type ToolActivityStatus,
} from "../lib/toolActivity";

const cx = scoped(base, controls, layout, panels, settings);

type Handoff = {
  instruction?: string;
  original_task?: string;
  messages?: Pick<Message, "id" | "content" | "agent_name">[];
  artifacts?: Pick<Artifact, "id" | "version" | "title">[];
  source_ids?: string[];
};

function data(event: Event): Record<string, unknown> {
  return event.data && typeof event.data === "object"
    ? (event.data as Record<string, unknown>)
    : {};
}

type Group = { key: string; agentID: string; events: Event[] };

/** Events grouped per step, in the order each member started working. */
function groups(events: Event[]) {
  const out: Group[] = [];
  for (const event of events) {
    if (!event.agent_id) continue;
    // Routine notices render no row; a step that only sent them must not leave
    // an empty member header behind.
    if (
      event.type === "native_notice" &&
      !noticeLabel(String(data(event).kind || ""))
    )
      continue;
    const key = event.step_id || event.agent_id;
    let group = out.find((item) => item.key === key);
    if (!group) {
      group = { key, agentID: event.agent_id, events: [] };
      out.push(group);
    }
    group.events.push(event);
  }
  return out;
}

function statusClass(status: ToolActivityStatus): string {
  switch (status) {
    case "succeeded":
      return "succeeded";
    case "failed":
    case "denied":
      return "failed";
    case "running":
    case "queued":
      return "running";
    case "awaiting_permission":
      return "pending";
    case "canceled":
    case "unknown":
      return "canceled";
  }
}

function ToolRecord({
  activity,
  mode,
  onHistory,
}: {
  activity: ToolActivity;
  mode: "live" | "history";
  onHistory?: (after?: number) => void;
}) {
  return (
    <details className={cx("tool-record")}>
      <summary>
        <span>{activity.label}</span>
        <span className={cx(`status ${statusClass(activity.status)}`)}>
          {toolStatusLabel(activity.status)}
        </span>
        {activity.status === "unknown" && (
          <>
            <small className={cx("muted")}>{missingFinishLabel(activity)}</small>
            {onHistory && mode === "live" && (
              <button
                type="button"
                className={cx("ghost small")}
                aria-label="查看这次工具调用的历史记录"
                onClick={(click) => {
                  click.preventDefault();
                  click.stopPropagation();
                  onHistory(Math.max(0, activity.seq - 1));
                }}
              >
                查看历史
              </button>
            )}
          </>
        )}
        {activity.durationMs !== undefined && (
          <small>{activity.durationMs} ms</small>
        )}
        {activity.exitCode !== undefined && (
          <small>exit {activity.exitCode}</small>
        )}
      </summary>
      {activity.missingStart && (
        <p className={cx("muted")}>调用开始记录不在当前页。</p>
      )}
      <h4>调用参数</h4>
      <pre className={cx("pre")}>{JSON.stringify(activity.input, null, 2)}</pre>
      {activity.output && (
        <>
          <h4>工具观察结果</h4>
          <pre className={cx("pre")}>{activity.output}</pre>
          {activity.outputTruncated && (
            <p className={cx("muted")}>输出已截断。</p>
          )}
        </>
      )}
    </details>
  );
}

export function ToolTimeline({
  events,
  agents,
  sources = [],
  onSource,
  runStatus,
  onHistory,
  mode = "live",
}: {
  events: Event[];
  agents: Agent[];
  sources?: Source[];
  onSource?: (id: string) => void;
  runStatus?: string;
  onHistory?: (after?: number) => void;
  mode?: "live" | "history";
}) {
  const member = (id: string) => {
    const agent = agents.find((agent) => agent.id === id);
    return agent ? memberLabel(agent) : "成员";
  };
  // One projection for both the live body and on-demand history: the pairing
  // key is run + step + attempt + tool_call_id, so a retried native-1 never
  // inherits an older attempt's finish.
  const projected = projectToolActivity(events, { mode, runStatus });
  const activityAtSeq = new Map<number, ToolActivity>();
  const attachedFinishes = new Set<number>();
  for (const group of projected)
    for (const activity of group.activities) {
      activityAtSeq.set(activity.seq, activity);
      if (activity.finishSeq !== undefined && !activity.missingStart)
        attachedFinishes.add(activity.finishSeq);
    }
  // method_recorded is a run-level event without a step or member group.
  const runNotices = events.filter(
    (event) => !event.agent_id && methodEventLabel(event),
  );
  return (
    <div>
      {groups(events).map((group) => {
        const usage = usageSummary(group.events);
        return (
          <section className={cx("tool-group")} key={group.key}>
            <div className={cx("tool-group-head")}>
              <strong>{member(group.agentID)}</strong>
              {usage && (
                <small title="输入 / 输出 tokens；供应商未提供时为未知">
                  {usage}
                </small>
              )}
            </div>
            {group.events.map((event) => {
              if (
                event.type === "workflow_selected" ||
                event.type === "method_recorded"
              ) {
                const label = methodEventLabel(event);
                return label ? (
                  <p className={cx("muted")} key={event.seq}>
                    {label}
                  </p>
                ) : null;
              }
              if (event.type === "handoff_prepared")
                return (
                  <HandoffRecord
                    key={event.seq}
                    event={event}
                    sources={sources}
                    onSource={onSource}
                  />
                );
              if (event.type === "liveness_warning") {
                const input = data(event);
                return (
                  <p className={cx("muted")} key={event.seq}>
                    {livenessLabel(String(input.state)) || "执行状态"} · 已{" "}
                    {String(input.silence_seconds)} 秒
                    {input.last_event_type
                      ? ` · 最后事件 ${String(input.last_event_type)}`
                      : ""}
                  </p>
                );
              }
              if (event.type === "step_waiting")
                return (
                  <p className={cx("muted")} key={event.seq}>
                    等待上游成员完成
                  </p>
                );
              if (event.type === "cli_changed")
                return (
                  <p className={cx("muted")} key={event.seq}>
                    检测到 CLI 程序变化，已重新检查兼容性。
                  </p>
                );
              if (event.type === "native_notice") {
                const notice = noticeLabel(String(data(event).kind || ""));
                return notice ? (
                  <p className={cx("muted")} key={event.seq}>
                    {notice}
                  </p>
                ) : null;
              }
              if (event.type === "tool_finished") {
                if (attachedFinishes.has(event.seq)) return null;
              } else if (event.type !== "tool_started") {
                return null;
              }
              const activity = activityAtSeq.get(event.seq);
              return activity ? (
                <ToolRecord
                  key={event.seq}
                  activity={activity}
                  mode={mode}
                  onHistory={onHistory}
                />
              ) : null;
            })}
          </section>
        );
      })}
      {runNotices.map((event) => (
        <p className={cx("muted")} key={event.seq}>
          {methodEventLabel(event)}
        </p>
      ))}
    </div>
  );
}

function HandoffRecord({
  event,
  sources,
  onSource,
}: {
  event: Event;
  sources: Source[];
  onSource?: (id: string) => void;
}) {
  const input = data(event) as Handoff;
  return (
    <details className={cx("tool-record")}>
      <summary>
        <span>收到工作包</span>
        <small>
          {input.messages?.length ?? 0} 条上游回复 ·{" "}
          {input.source_ids?.length ?? 0} 个来源
        </small>
      </summary>
      <p>{input.instruction}</p>
      {input.original_task && (
        <>
          <h4>原始问题</h4>
          <Markdown text={input.original_task} />
        </>
      )}
      {(input.messages?.length ?? 0) > 0 && <h4>上游成员回复</h4>}
      {input.messages?.map((message) => (
        <section key={message.id}>
          <strong>{message.agent_name}</strong>
          <Markdown text={message.content} />
        </section>
      ))}
      {(input.artifacts?.length ?? 0) > 0 && <h4>参考产物</h4>}
      {input.artifacts?.map((artifact) => (
        <p key={`${artifact.id}:${artifact.version}`}>
          {artifact.title} · v{artifact.version}
        </p>
      ))}
      {(input.source_ids?.length ?? 0) > 0 && <h4>可用来源</h4>}
      <div className={cx("source-links")}>
        {input.source_ids?.map((id) => {
          const title =
            sources.find((source) => source.id === id)?.title ||
            `来源 ${id.slice(0, 8)}`;
          return onSource ? (
            <button
              key={id}
              className={cx("small")}
              onClick={() => onSource(id)}
            >
              {title}
            </button>
          ) : (
            <p key={id}>{title}</p>
          );
        })}
      </div>
      <p className={cx("muted")}>表示输入已准备，尚不表示成员已理解。</p>
    </details>
  );
}
