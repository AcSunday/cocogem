import type { Agent, Message } from "../api/client";
import { statusText } from "../api/client";
import { cx } from "../styles/classes";
import { catIcon } from "../lib/presentation";
import { Markdown } from "./Markdown";

export function MessageCard({
  message,
  agents,
}: {
  message: Message;
  agents: Agent[];
}) {
  const index = Math.max(
    0,
    agents.findIndex((a) => a.id === message.agent_id),
  );
  return (
    <article
      className={cx(`message ${message.role === "user" ? "user-message" : ""}`)}
    >
      <div
        className={cx(
          `avatar ${message.role === "user" ? "user-avatar" : `cat-${index % 3}`}`,
        )}
      >
        {message.role === "user" ? "你" : catIcon(index)}
      </div>
      <div className={cx("message-content")}>
        <div className={cx("message-meta")}>
          <strong>{message.agent_name}</strong>
          <time>
            {new Date(message.created_at).toLocaleTimeString([], {
              hour: "2-digit",
              minute: "2-digit",
            })}
          </time>
          {message.status !== "completed" && (
            <small>{statusText[message.status]}</small>
          )}
        </div>
        <Markdown text={message.content || "正在准备回复…"} />
      </div>
    </article>
  );
}
