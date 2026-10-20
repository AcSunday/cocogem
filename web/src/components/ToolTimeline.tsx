import type { Agent, Artifact, Event, Message, Source } from "../api/client";
import { cx } from "../styles/classes";
import { Markdown } from "./Markdown";
import { memberLabel, toolLabel } from "../lib/presentation";

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

function usageText(events: Event[]) {
  const usage = events.filter((event) => event.type === "usage_updated");
  if (!usage.length) return "";
  if (usage.some((event) => !data(event).known)) return "用量未知";
  const sum = (field: string) =>
    usage.reduce((total, event) => total + Number(data(event)[field] ?? 0), 0);
  return `${sum("input_tokens")} / ${sum("output_tokens")} tokens`;
}

export function ToolTimeline({
  events,
  agents,
  sources = [],
  onSource,
}: {
  events: Event[];
  agents: Agent[];
  sources?: Source[];
  onSource?: (id: string) => void;
}) {
  const member = (id: string) => {
    const agent = agents.find((agent) => agent.id === id);
    return agent ? memberLabel(agent) : "成员";
  };
  return (
    <div>
      {groups(events).map((group) => {
        const usage = usageText(group.events);
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
              if (event.type === "handoff_prepared")
                return (
                  <HandoffRecord
                    key={event.seq}
                    event={event}
                    sources={sources}
                    onSource={onSource}
                  />
                );
              if (event.type !== "tool_started") return null;
              const input = data(event);
              const end = group.events.find(
                (item) =>
                  item.type === "tool_finished" &&
                  data(item).tool_call_id === input.tool_call_id,
              );
              const result = end ? data(end) : undefined;
              const status = result
                ? result.status === "ok"
                  ? ["succeeded", "完成"]
                  : ["failed", "失败"]
                : ["running", "执行中"];
              return (
                <details className={cx("tool-record")} key={event.seq}>
                  <summary>
                    <span>{toolLabel(String(input.name))}</span>
                    <span className={cx(`status ${status[0]}`)}>
                      {status[1]}
                    </span>
                    {result && <small>{String(result.duration_ms)} ms</small>}
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
