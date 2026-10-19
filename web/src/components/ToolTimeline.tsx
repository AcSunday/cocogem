import type { Agent, Artifact, Event, Message, Source } from "../api/client";
import { cx } from "../styles/classes";
import { Markdown } from "./Markdown";
import { memberLabel } from "../lib/presentation";

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
  const starts = events.filter((event) => event.type === "tool_started");
  const handoffs = events.filter((event) => event.type === "handoff_prepared");
  const usage = events.filter((event) => event.type === "usage_updated");
  const member = (event: Event) => {
    const agent = agents.find((agent) => agent.id === event.agent_id);
    return agent ? memberLabel(agent) : "成员";
  };
  return (
    <div>
      {starts.map((start) => {
        const input = data(start);
        const end = events.find(
          (event) =>
            event.type === "tool_finished" &&
            data(event).tool_call_id === input.tool_call_id &&
            event.step_id === start.step_id,
        );
        const result = end ? data(end) : undefined;
        return (
          <details
            className={cx("tool-record")}
            key={`${start.run_id}:${start.seq}`}
          >
            <summary>
              {member(start)} · {String(input.name)} ·{" "}
              {result ? (result.status === "ok" ? "完成" : "失败") : "执行中"}
            </summary>
            <p>
              耗时：{result ? `${String(result.duration_ms)} ms` : "等待结果"}
            </p>
            <h4>调用参数</h4>
            <pre>{JSON.stringify(input.input, null, 2)}</pre>
            {result && (
              <>
                <h4>工具观察结果</h4>
                <pre>{String(result.output)}</pre>
              </>
            )}
          </details>
        );
      })}
      {handoffs.map((event) => {
        const input = data(event) as Handoff;
        return (
          <details
            className={cx("tool-record")}
            key={`${event.run_id}:${event.seq}`}
          >
            <summary>交给 {member(event)} · 工作包已准备</summary>
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
            {input.source_ids?.map((id) => {
              const title =
                sources.find((source) => source.id === id)?.title ||
                `来源 ${id.slice(0, 8)}`;
              return onSource ? (
                <button key={id} onClick={() => onSource(id)}>
                  {title}
                </button>
              ) : (
                <p key={id}>{title}</p>
              );
            })}
            <p>表示输入已准备，尚不表示成员已理解。</p>
          </details>
        );
      })}
      {usage.map((event) => (
        <details
          className={cx("tool-record")}
          key={`${event.run_id}:${event.seq}`}
        >
          <summary>{member(event)} · 模型用量</summary>
          {data(event).known ? (
            <p>
              输入 {String(data(event).input_tokens)} / 输出{" "}
              {String(data(event).output_tokens)} tokens
            </p>
          ) : (
            <p>供应商未提供实际用量，当前为未知。</p>
          )}
        </details>
      ))}
    </div>
  );
}
