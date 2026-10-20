import { useRef, useState, type FormEvent, type ReactNode } from "react";
import type { Agent, Artifact, Routing } from "../api/client";
import { cx } from "../styles/classes";
import { memberLabel, roleLabel, workflows } from "../lib/presentation";
import { applyMention, mentionAt, mentionMatches } from "../lib/mention";
import { Avatar } from "./Avatar";
import { Icon } from "./Icon";

export type WorkflowKey = keyof typeof workflows;

const rolesFor = (workflow: WorkflowKey) =>
  workflow === "discussion"
    ? ["coordinator"]
    : workflow === "research"
      ? ["researcher", "critic", "coordinator"]
      : workflow === "code_analysis"
        ? ["analyst", "reviewer"]
        : [];

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
  urls,
  setURLs,
  scope,
  setScope,
  artifactRef,
  clearArtifactRef,
  routing,
  active,
  canceling,
  busy,
  disabled,
  demo,
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
  urls: string;
  setURLs: (value: string) => void;
  scope: string;
  setScope: (value: string) => void;
  artifactRef?: Artifact;
  clearArtifactRef: () => void;
  routing?: Routing;
  active: boolean;
  canceling: boolean;
  busy: boolean;
  disabled: boolean;
  demo: boolean;
  pending?: ReactNode;
  onSend: () => void;
  onStop: () => void;
}) {
  const input = useRef<HTMLTextAreaElement>(null);
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
    onSend();
  };
  const placeholder = disabled
    ? "先创建一个工作空间，再开始提问"
    : "说说你想完成什么，输入 @ 指定成员";
  return (
    <form className={cx("composer")} onSubmit={submit}>
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
                index={allAgents.indexOf(agent)}
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
          // Grow with content up to the CSS max-height.
          event.target.style.height = "auto";
          event.target.style.height = `${event.target.scrollHeight}px`;
        }}
        onSelect={(event) => setCaret(event.currentTarget.selectionStart)}
        onKeyDown={(event) => {
          if (matches.length) {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault();
              const step = event.key === "ArrowDown" ? 1 : -1;
              setHighlight((index + step + matches.length) % matches.length);
              return;
            }
            if (event.key === "Enter" || event.key === "Tab") {
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
          if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
            event.preventDefault();
            onSend();
          }
        }}
      />
      <div className={cx("composer-options")}>
        <label>
          <span className={cx("sr-only")}>工作方式</span>
          <select
            aria-label="工作方式"
            value={workflow}
            disabled={disabled}
            onChange={(event) => setWorkflow(event.target.value as WorkflowKey)}
          >
            {Object.entries(workflows).map(([key, name]) => (
              <option key={key} value={key}>
                {name}
              </option>
            ))}
          </select>
        </label>
        {(workflow === "free" || workflow === "discussion") &&
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
                index={allAgents.indexOf(agent)}
                small
              />
              {agent.display_name}
            </button>
          ))}
        {rolesFor(workflow).map((role) => (
          <label key={role}>
            {roleLabel(role)}
            <select
              aria-label={roleLabel(role)}
              value={assignments[role] ?? ""}
              onChange={(event) => setAssignment(role, event.target.value)}
            >
              <option value="">请选择</option>
              {agents.map((agent) => (
                <option value={agent.id} key={agent.id}>
                  {agent.display_name}
                </option>
              ))}
            </select>
          </label>
        ))}
        <details className={cx("source-input")}>
          <summary>
            <Icon name="globe" size={14} />
            资料{urls.trim() ? ` · ${urls.trim().split("\n").length}` : ""}
          </summary>
          <div className={cx("source-fields")}>
            <label>
              公共 URL（每行一个）
              <textarea
                aria-label="资料链接"
                rows={2}
                value={urls}
                onChange={(event) => setURLs(event.target.value)}
                placeholder="https://…"
              />
            </label>
            <label>
              研究范围
              <select
                value={scope}
                onChange={(event) => setScope(event.target.value)}
              >
                <option value="given_sources">只研究给定资料</option>
                <option value="web_discovery">在线发现资料</option>
              </select>
            </label>
          </div>
        </details>
      </div>
      <div className={cx("composer-bottom")}>
        <small>
          {active
            ? "本次协作结束后可以继续发送"
            : (workflow === "free" || workflow === "discussion") &&
                !selected.length
              ? "未选成员时按问题内容自动指派"
              : demo
                ? "离线演示 · ⌘/Ctrl + Enter 发送"
                : "问题与读取的资料片段会发送至成员的模型 · ⌘/Ctrl + Enter 发送"}
        </small>
        {routeLabel && (
          <span
            className={cx("route-chip")}
            role="status"
            title={routing?.clarification || routing?.explanation}
          >
            {routing?.clarification ? routeLabel : `→ ${routeLabel}`}
          </span>
        )}
        {active ? (
          <button
            type="button"
            className={cx("stop")}
            onClick={onStop}
            disabled={canceling}
          >
            <Icon name="stop" size={14} />
            {canceling ? "正在停止" : "停止协作"}
          </button>
        ) : (
          <button
            className={cx("primary composer-send")}
            type="submit"
            aria-label="发送"
            disabled={busy || disabled || !draft.trim()}
          >
            {busy ? "正在提交…" : "发送"}
            <Icon name="send" size={14} />
          </button>
        )}
      </div>
    </form>
  );
}
