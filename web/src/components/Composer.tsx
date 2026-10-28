import { Select } from "./Select";
import {
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import type { Agent, Artifact, Routing } from "../api/client";
import { cx } from "../styles/classes";
import {
  canServeRole,
  memberLabel,
  roleLabel,
  workflowRoles,
  workflows,
} from "../lib/presentation";
import { applyMention, mentionAt, mentionMatches } from "../lib/mention";
import { Avatar } from "./Avatar";
import { Icon } from "./Icon";

export type WorkflowKey = keyof typeof workflows;

const rolesFor = (workflow: WorkflowKey) => workflowRoles[workflow] ?? [];

export function Composer({
  agents,
  allAgents,
  draft,
  setDraft,
  workflow,
  setWorkflow,
  selected,
  toggleSelected,
  assignments,
  setAssignment,
  artifactRef,
  clearArtifactRef,
  coordinator,
  routing,
  active,
  canceling,
  busy,
  disabled,
  pending,
  onSend,
  onStop,
}: {
  agents: Agent[];
  allAgents: Agent[];
  draft: string;
  setDraft: (value: string) => void;
  workflow: WorkflowKey;
  setWorkflow: (value: WorkflowKey) => void;
  selected: string[];
  toggleSelected: (id: string) => void;
  assignments: Record<string, string>;
  setAssignment: (role: string, id: string) => void;
  artifactRef?: Artifact;
  clearArtifactRef: () => void;
  coordinator?: { name: string; fallback: boolean };
  routing?: Routing;
  active: boolean;
  canceling: boolean;
  busy: boolean;
  disabled: boolean;
  pending?: ReactNode;
  onSend: () => void;
  onStop: () => void;
}) {
  const input = useRef<HTMLTextAreaElement>(null);
  useLayoutEffect(() => {
    const textarea = input.current;
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = `${textarea.scrollHeight}px`;
  }, [draft]);
  const composing = useRef(false);
  const canSend = !active && !busy && !disabled && !!draft.trim();
  const [caret, setCaret] = useState(0);
  const [highlight, setHighlight] = useState(0);
  const [dismissed, setDismissed] = useState(-1);
  const mention = mentionAt(draft, caret);
  const matches =
    mention && mention.start !== dismissed
      ? mentionMatches(agents, mention.query)
      : [];
  const index = Math.min(highlight, Math.max(0, matches.length - 1));
  const pick = (agent: Agent) => {
    if (!mention) return;
    const next = applyMention(draft, mention.start, caret, agent.handle);
    setDraft(next.text);
    setCaret(next.caret);
    requestAnimationFrame(() => {
      input.current?.focus();
      input.current?.setSelectionRange(next.caret, next.caret);
    });
  };
  const routeLabel = routing
    ? routing.clarification ||
      (routing.targets ?? [])
        .map((id) => allAgents.find((agent) => agent.id === id)?.display_name)
        .filter(Boolean)
        .join("、") ||
      workflows[routing.workflow as WorkflowKey] ||
      ""
    : "";
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (canSend) onSend();
  };
  const placeholder = disabled
    ? "先创建一个工作空间，再开始提问"
    : "描述任务或贴上链接，输入 @ 邀请成员协作";
  // The accepted preview owns the actual roles; local picks are only a preview
  // until the server resolves defaults and permissions.
  const shownAssignments =
    routing?.assignments && Object.keys(routing.assignments).length
      ? routing.assignments
      : assignments;
  const recipientSummary = selected.length
    ? agents
        .filter((agent) => selected.includes(agent.id))
        .map((agent) => agent.display_name)
        .join("、")
    : [
        ...(workflow === "discussion"
          ? [agents.map((agent) => agent.display_name).join("、")]
          : []),
        ...rolesFor(workflow).map((role) => {
          const id = shownAssignments[role];
          const member =
            agents.find((agent) => agent.id === id) ??
            allAgents.find((agent) => agent.id === id);
          return `${roleLabel(role)}：${member?.display_name || "请选择"}`;
        }),
      ].join(" · ");
  return (
    <form
      className={cx("composer")}
      onSubmit={submit}
      onKeyDown={(event) => {
        if (event.key !== "Escape" || event.defaultPrevented) return;
        const expanded =
          (event.target as HTMLElement).closest<HTMLDetailsElement>(
            "details[open]",
          ) ??
          event.currentTarget.querySelector<HTMLDetailsElement>(
            "details[open]",
          );
        if (expanded) {
          event.preventDefault();
          event.stopPropagation();
          expanded.open = false;
          expanded.querySelector("summary")?.focus();
        }
      }}
    >
      {pending}
      {artifactRef && (
        <div className={cx("reference-pill")}>
          <span>
            引用：{artifactRef.title} v{artifactRef.version}
          </span>
          <button
            type="button"
            className={cx("icon-button small")}
            aria-label="移除产物引用"
            onClick={clearArtifactRef}
          >
            <Icon name="close" size={14} />
          </button>
        </div>
      )}
      {matches.length > 0 && (
        <ul
          className={cx("mention-options")}
          role="listbox"
          id="mention-options"
          aria-label="选择成员"
        >
          {matches.map((agent, i) => (
            <li
              key={agent.id}
              id={`mention-${agent.id}`}
              role="option"
              aria-selected={i === index}
              onMouseDown={(event) => {
                event.preventDefault();
                pick(agent);
              }}
            >
              <Avatar
                name={agent.display_name}
                id={agent.id}
                avatar={agent.avatar}
                small
              />
              {memberLabel(agent)}
            </li>
          ))}
        </ul>
      )}
      <textarea
        ref={input}
        id="composer-input"
        aria-label="输入问题"
        placeholder={placeholder}
        value={draft}
        disabled={disabled}
        rows={2}
        aria-controls={matches.length ? "mention-options" : undefined}
        aria-autocomplete="list"
        aria-activedescendant={
          matches.length ? `mention-${matches[index].id}` : undefined
        }
        onChange={(event) => {
          setDraft(event.target.value);
          setCaret(event.target.selectionStart);
          setHighlight(0);
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
          if (matches.length) {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault();
              const step = event.key === "ArrowDown" ? 1 : -1;
              setHighlight((index + step + matches.length) % matches.length);
              return;
            }
            if (
              !event.shiftKey &&
              (event.key === "Enter" || event.key === "Tab")
            ) {
              event.preventDefault();
              pick(matches[index]);
              return;
            }
            if (event.key === "Escape") {
              event.preventDefault();
              setDismissed(mention?.start ?? -1);
              return;
            }
          }
          if (event.key === "Enter" && !event.shiftKey) {
            event.preventDefault();
            if (canSend && !event.repeat)
              event.currentTarget.form?.requestSubmit();
          }
        }}
      />
      <div className={cx("composer-toolbar")}>
        <div className={cx("composer-options")}>
          <label>
            <span className={cx("sr-only")}>工作方式</span>
            <Select
              aria-label="工作方式"
              value={workflow}
              disabled={disabled}
              onChange={(selected) => setWorkflow(selected as WorkflowKey)}
            >
              {Object.entries(workflows).map(([key, name]) => (
                <option key={key} value={key}>
                  {name}
                </option>
              ))}
            </Select>
          </label>
          {workflow !== "free" && (
            <details
              className={cx("collaboration-options")}
              name="composer-options"
            >
              <summary
                aria-label={`成员与分工：${recipientSummary}`}
                title={recipientSummary}
              >
                <Icon name="users" size={14} />
                <span>分工</span>
                <Icon name="chevron" size={12} />
              </summary>
              <div className={cx("composer-popup member-options")}>
                {workflow === "discussion" &&
                  agents.map((agent) => (
                    <button
                      type="button"
                      key={agent.id}
                      aria-pressed={selected.includes(agent.id)}
                      aria-label={memberLabel(agent)}
                      title={memberLabel(agent)}
                      className={cx(
                        `recipient ${selected.includes(agent.id) ? "active" : ""}`,
                      )}
                      onClick={() => toggleSelected(agent.id)}
                    >
                      <Avatar
                        name={agent.display_name}
                        id={agent.id}
                        avatar={agent.avatar}
                        small
                      />
                      {agent.display_name}
                    </button>
                  ))}
                {rolesFor(workflow).map((role) => (
                  <label key={role}>
                    {roleLabel(role)}
                    <Select
                      aria-label={roleLabel(role)}
                      value={assignments[role] ?? ""}
                      onChange={(selected) => setAssignment(role, selected)}
                    >
                      <option value="">请选择</option>
                      {agents
                        .filter((agent) => canServeRole(agent, role))
                        .map((agent) => (
                          <option value={agent.id} key={agent.id}>
                            {agent.display_name}
                          </option>
                        ))}
                    </Select>
                  </label>
                ))}
              </div>
            </details>
          )}
        </div>
        {active ? (
          <button
            type="button"
            className={cx("stop composer-send")}
            aria-label={canceling ? "正在停止" : "停止协作"}
            aria-busy={canceling || undefined}
            title={canceling ? "正在停止…" : "停止协作"}
            onClick={onStop}
            disabled={canceling}
          >
            <Icon name="stop" size={14} />
          </button>
        ) : (
          <button
            className={cx("primary composer-send")}
            type="submit"
            aria-label={busy ? "正在提交" : "发送"}
            title={busy ? "正在提交…" : "发送 · Enter"}
            disabled={!canSend}
          >
            <Icon name="send" size={16} />
          </button>
        )}
      </div>
      <div className={cx("composer-bottom")}>
        <small>
          {active
            ? "本次协作结束后可以继续发送"
            : "Enter 发送 · Shift + Enter 换行"}
        </small>
        {coordinator && (
          <span
            className={cx("route-chip")}
            role="status"
            title={
              coordinator.fallback
                ? "全局默认协调者不在当前工作空间或已停用，已回退到本会话成员"
                : "本次会话的协调者"
            }
          >
            {coordinator.fallback
              ? `默认协调者不可用，使用${coordinator.name}`
              : `协调者：${coordinator.name}`}
          </span>
        )}
        {routeLabel && (
          <span
            className={cx("route-chip")}
            role="status"
            title={routing?.clarification || routing?.explanation}
          >
            {routing?.clarification ? routeLabel : `→ ${routeLabel}`}
          </span>
        )}
      </div>
    </form>
  );
}
