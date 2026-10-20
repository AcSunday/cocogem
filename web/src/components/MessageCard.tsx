import type { Message } from "../api/client";
import { statusText } from "../api/client";
import { cx } from "../styles/classes";
import { formatMessageTime, roleLabel } from "../lib/presentation";
import { Avatar } from "./Avatar";
import { Markdown } from "./Markdown";

export function MessageCard({
  message,
  index,
  role,
}: {
  message: Message;
  index: number;
  role?: string;
}) {
  const user = message.role === "user";
  return (
    <article className={cx(`message ${user ? "user-message" : ""}`)}>
      {user ? (
        <span className={cx("avatar tone-user")} aria-hidden="true">
          你
        </span>
      ) : (
        <Avatar name={message.agent_name} index={index} />
      )}
      <div className={cx("message-content")}>
        <div className={cx("message-meta")}>
          <strong>{user ? "你" : message.agent_name}</strong>
          {role && <span className={cx("role-chip")}>{roleLabel(role)}</span>}
          <time dateTime={message.created_at}>
            {formatMessageTime(message.created_at)}
          </time>
          {message.status !== "completed" && (
            <small className={cx("live")}>{statusText[message.status]}</small>
          )}
        </div>
        <Markdown text={message.content || "正在准备回复…"} />
      </div>
    </article>
  );
}
