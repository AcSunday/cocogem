import type { Agent, Event } from "../api/client";
import { cx } from "../styles/classes";

function data(event: Event): Record<string, unknown> {
  return event.data && typeof event.data === "object"
    ? (event.data as Record<string, unknown>)
    : {};
}

export function ToolTimeline({
  events,
  agents,
}: {
  events: Event[];
  agents: Agent[];
}) {
  const starts = events.filter((event) => event.type === "tool_started");
  const handoffs = events.filter((event) => event.type === "handoff_prepared");
  const usage = events.filter((event) => event.type === "usage_updated");
  const member = (event: Event) =>
    agents.find((agent) => agent.id === event.agent_id)?.display_name ?? "成员";
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
      {handoffs.map((event) => (
        <details
          className={cx("tool-record")}
          key={`${event.run_id}:${event.seq}`}
        >
          <summary>交给 {member(event)} · 工作包已准备</summary>
          <p>{String(data(event).instruction)}</p>
          <small>上游步骤：{JSON.stringify(data(event).from_steps)}</small>
          <p>表示输入已准备，尚不表示成员已理解。</p>
        </details>
      ))}
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
