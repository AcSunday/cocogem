import type { Agent, Artifact, Event, Message, Source } from "../api/client";
import { EVENT_WINDOW, terminal } from "../api/client";
import { cx } from "../styles/classes";
import { Markdown } from "./Markdown";
import {
  livenessLabel,
  memberLabel,
  methodEventLabel,
  noticeLabel,
  toolLabel,
  usageSummary,
} from "../lib/presentation";

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
  const endedRun =
    mode === "history" || (runStatus ? terminal(runStatus) : false);
  // The live window evicts old events; a start without a finish then cannot
  // prove the tool is still running.
  const windowEvicted = mode === "live" && events.length >= EVENT_WINDOW;
  // method_recorded is a run-level event without a step or member group.
  const runNotices = events.filter(
    (event) => !event.agent_id && methodEventLabel(event),
  );
  return (
    <div>
      {groups(events).map((group) => {
        const usage = usageSummary(group.events);
        // Native tool ids restart at native-1 for every attempt, so a finish
        // only pairs with a start from the same attempt segment.
        const attemptAt = new Map<number, string>();
        const endedAttempts = new Set<string>();
        let attempt = "";
        for (const event of group.events) {
          if (event.type === "attempt_started")
            attempt = String(data(event).id ?? "") || `seq:${event.seq}`;
          attemptAt.set(event.seq, attempt);
          if (event.type === "attempt_finished") {
            if (attempt) endedAttempts.add(attempt);
            attempt = "";
          }
        }
        const openStarts = new Map<string, Event[]>();
        const finishByStart = new Map<number, Event>();
        for (const event of group.events) {
          if (event.type !== "tool_started" && event.type !== "tool_finished")
            continue;
          const key = `${attemptAt.get(event.seq) ?? ""}\u0000${String(data(event).tool_call_id ?? "")}`;
          if (event.type === "tool_started") {
            const starts = openStarts.get(key) ?? [];
            starts.push(event);
            openStarts.set(key, starts);
            continue;
          }
          const start = openStarts.get(key)?.shift();
          if (start) finishByStart.set(start.seq, event);
        }
        const attachedFinishes = new Set(
          [...finishByStart.values()].map((event) => event.seq),
        );
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
                const output = data(event);
                const status =
                  output.status === "ok"
                    ? ["succeeded", "完成"]
                    : ["failed", "失败"];
                return (
                  <details className={cx("tool-record")} key={event.seq}>
                    <summary>
                      <span>{toolLabel(String(output.name || "")) || "工具调用"}</span>
                      <span className={cx(`status ${status[0]}`)}>
                        {status[1]}
                      </span>
                      {typeof output.duration_ms === "number" && (
                        <small>{output.duration_ms} ms</small>
                      )}
                    </summary>
                    <p className={cx("muted")}>调用开始记录不在当前页。</p>
                    {output.output !== undefined && (
                      <>
                        <h4>工具观察结果</h4>
                        <pre className={cx("pre")}>{String(output.output)}</pre>
                      </>
                    )}
                  </details>
                );
              }
              if (event.type !== "tool_started") return null;
              const input = data(event);
              const end = finishByStart.get(event.seq);
              const result = end ? data(end) : undefined;
              const status = result
                ? result.status === "ok"
                  ? ["succeeded", "完成"]
                  : ["failed", "失败"]
                : ["running", "执行中"];
              const missingFinish =
                !result &&
                (endedRun ||
                  windowEvicted ||
                  endedAttempts.has(attemptAt.get(event.seq) ?? ""));
              return (
                <details className={cx("tool-record")} key={event.seq}>
                  <summary>
                    <span>{toolLabel(String(input.name))}</span>
                    {result ? (
                      <span className={cx(`status ${status[0]}`)}>
                        {status[1]}
                      </span>
                    ) : missingFinish ? (
                      <>
                        <span className={cx("muted")}>完成记录不在当前页</span>
                        {onHistory && mode === "live" && (
                          <button
                            type="button"
                            className={cx("ghost small")}
                            aria-label="查看这次工具调用的历史记录"
                            onClick={(click) => {
                              click.preventDefault();
                              click.stopPropagation();
                              onHistory(Math.max(0, event.seq - 1));
                            }}
                          >
                            查看历史
                          </button>
                        )}
                      </>
                    ) : (
                      <span className={cx("status running")}>执行中</span>
                    )}
                    {result && typeof result.duration_ms === "number" && (
                      <small>{result.duration_ms} ms</small>
                    )}
                  </summary>
                  <h4>调用参数</h4>
                  <pre className={cx("pre")}>
                    {JSON.stringify(input.input, null, 2)}
                  </pre>
                  {result && (
                    <>
                      <h4>工具观察结果</h4>
                      <pre className={cx("pre")}>{String(result.output)}</pre>
                    </>
                  )}
                </details>
              );
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
