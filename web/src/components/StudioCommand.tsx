import { useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { StudioAgentActivity } from "../api/client";
import { t } from "../lib/i18n";
import { applyMention, mentionAt, mentionMatches } from "../lib/mention";
import { useAnchoredOverlay } from "../lib/overlay";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import studio from "../styles/studio.module.css";
import { Avatar } from "./Avatar";
import { Icon } from "./Icon";

const cx = scoped(base, controls, studio);

export function StudioCommand({
  agents,
  sending,
  error,
  onDismissError,
  onSend,
}: {
  agents: StudioAgentActivity[];
  sending: boolean;
  error: string;
  onDismissError: () => void;
  onSend: (text: string) => Promise<boolean>;
}) {
  const [draft, setDraft] = useState("");
  const [caret, setCaret] = useState(0);
  const [selected, setSelected] = useState(0);
  const [dismissed, setDismissed] = useState(false);
  const [focused, setFocused] = useState(false);
  const composing = useRef(false);
  const input = useRef<HTMLTextAreaElement>(null);
  const options = useRef<HTMLUListElement>(null);
  const optionID = useId();
  const mention = !dismissed && focused ? mentionAt(draft, caret) : null;
  const matches = mention ? mentionMatches(agents, mention.query) : [];
  const highlight = Math.min(selected, matches.length - 1);
  const pendingSelection = useRef<{ text: string; caret: number } | undefined>(
    undefined,
  );
  const canSend = !sending && draft.trim().length > 0;
  useAnchoredOverlay(matches.length > 0, input, options, {
    preferAbove: true,
    limitHeight: 240,
  });
  useLayoutEffect(() => {
    const next = pendingSelection.current;
    if (!next) return;
    pendingSelection.current = undefined;
    if (input.current === document.activeElement && next.text === draft)
      input.current?.setSelectionRange(next.caret, next.caret);
  }, [draft]);
  useLayoutEffect(() => {
    const list = options.current;
    const option = list?.children[highlight] as HTMLElement | undefined;
    if (!list || !option) return;
    if (option.offsetTop < list.scrollTop) list.scrollTop = option.offsetTop;
    else if (
      option.offsetTop + option.offsetHeight >
      list.scrollTop + list.clientHeight
    )
      list.scrollTop =
        option.offsetTop + option.offsetHeight - list.clientHeight;
  }, [highlight]);

  const pick = (agent: StudioAgentActivity) => {
    if (!mention) return;
    const next = applyMention(draft, mention.start, caret, agent.handle);
    pendingSelection.current = next;
    setDraft(next.text);
    setCaret(next.caret);
  };

  return (
    <>
      {error && (
        <div role="alert" className={cx("studio-command-error")}>
          <span>{error}</span>
          <button
            type="button"
            className={cx("studio-top-icon")}
            aria-label={t("关闭错误提示", "Dismiss error")}
            onClick={() => {
              onDismissError();
              input.current?.focus();
            }}
          >
            <Icon name="close" size={14} />
          </button>
        </div>
      )}
      <form
        className={cx("studio-command-bar")}
        data-testid="studio-command-bar"
        onSubmit={async (event) => {
          event.preventDefault();
          if (!canSend || composing.current) return;
          setDismissed(true);
          if (await onSend(draft.trim()))
            setDraft((current) => (current === draft ? "" : current));
        }}
      >
        <textarea
          ref={input}
          id="studio-command"
          rows={1}
          maxLength={16000}
          value={draft}
          aria-label={t("猫猫研发室指令输入框", "Cat Studio command input")}
          aria-autocomplete="list"
          aria-controls={matches.length ? optionID : undefined}
          aria-activedescendant={
            highlight >= 0 ? `${optionID}-${highlight}` : undefined
          }
          placeholder={t(
            "给研发室发消息，或 @成员…",
            "Message studio or @mention…",
          )}
          onFocus={(event) => {
            setFocused(true);
            setCaret(event.currentTarget.selectionStart);
          }}
          onBlur={() => setFocused(false)}
          onChange={(event) => {
            pendingSelection.current = undefined;
            setDraft(event.target.value);
            setCaret(event.target.selectionStart);
            setSelected(0);
            setDismissed(false);
          }}
          onSelect={(event) => setCaret(event.currentTarget.selectionStart)}
          onCompositionStart={() => {
            composing.current = true;
          }}
          onCompositionEnd={() => {
            composing.current = false;
          }}
          onKeyDown={(event) => {
            if (
              composing.current ||
              event.nativeEvent.isComposing ||
              event.keyCode === 229
            )
              return;
            if (
              matches.length &&
              (event.key === "ArrowDown" || event.key === "ArrowUp")
            ) {
              event.preventDefault();
              const delta = event.key === "ArrowDown" ? 1 : -1;
              setSelected(
                (highlight + delta + matches.length) % matches.length,
              );
            } else if (matches.length && event.key === "Escape") {
              event.preventDefault();
              setDismissed(true);
            } else if (
              !event.shiftKey &&
              (event.key === "Enter" || (matches.length && event.key === "Tab"))
            ) {
              event.preventDefault();
              if (matches.length) pick(matches[highlight]);
              else if (canSend && !event.repeat)
                event.currentTarget.form?.requestSubmit();
            }
          }}
        />
        <button
          type="submit"
          className={cx("studio-send")}
          aria-label={t("发送指令", "Send command")}
          disabled={!canSend}
        >
          <Icon name={sending ? "refresh" : "send"} size={18} />
        </button>
      </form>
      {matches.length > 0 &&
        createPortal(
          <ul
            className={cx("studio-mentions")}
            ref={options}
            id={optionID}
            role="listbox"
            aria-label={t("选择成员", "Select member")}
          >
            {matches.map((agent, index) => (
              <li
                key={agent.agent_id}
                role="option"
                id={`${optionID}-${index}`}
                aria-selected={index === highlight}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => pick(agent)}
              >
                <Avatar name={agent.display_name} avatar={agent.avatar} small />
                <span>{agent.display_name}</span>
                <small>@{agent.handle}</small>
              </li>
            ))}
          </ul>,
          document.body,
        )}
    </>
  );
}
