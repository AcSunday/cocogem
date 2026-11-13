import { Select } from "./Select";
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import type { Agent, Artifact, Routing, Step } from "../api/client";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import panels from "../styles/panels.module.css";
import {
  memberLabel,
  roleLabel,
  runErrorText,
  workflowRoles,
  workflows,
} from "../lib/presentation";
import { applyMention, mentionAt, mentionMatches } from "../lib/mention";
import {
  applySlash,
  methodLabel,
  slashAt,
  slashMatches,
  workflowCommands,
} from "../lib/slash";
import { errorText } from "../lib/presentation";
import { Avatar } from "./Avatar";
import { Icon } from "./Icon";

const cx = scoped(base, controls, layout, conversation, panels);

const workflowIcons: Record<string, string> = {
  free: "💬",
  discussion: "👥",
  research: "🔬",
  code_analysis: "💻",
  engineering: "🛠️",
};

export type WorkflowKey = keyof typeof workflows;

/** A selected original stays local until the user actually sends it. */
export type ComposerAttachment = {
  id: string;
  title: string;
  file_name?: string;
  kind?: string;
  parse_error?: string;
};

export function Composer({
  agents,
  allAgents,
  draft,
  setDraft,
  workflow,
  setWorkflow,
  artifactRef,
  clearArtifactRef,
  attachments = [],
  uploading = false,
  onAttach,
  onRemoveAttachment,
  coordinator,
  executionMode,
  onExecutionModeChange,
  routing,
  error,
  onDismissError,
  failureNoticeID,
  failedSteps = [],
  onRetryMember,
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
  artifactRef?: Artifact;
  clearArtifactRef: () => void;
  attachments?: ComposerAttachment[];
  uploading?: boolean;
  onAttach?: (file: File) => void;
  onRemoveAttachment?: (id: string) => void;
  coordinator?: { name: string; fallback: boolean };
  executionMode?: string;
  onExecutionModeChange?: (mode: string) => Promise<void> | void;
  routing?: Routing;
  error?: string;
  onDismissError?: () => void;
  failureNoticeID?: string;
  failedSteps?: Step[];
  onRetryMember?: (stepID: string) => void;
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
  const canSend = !busy && !disabled && !!draft.trim();
  const [caret, setCaret] = useState(0);
  const [highlight, setHighlight] = useState(0);
  const [dismissed, setDismissed] = useState(-1);
  const [dismissedRouting, setDismissedRouting] = useState<Routing>();
  const [dismissedFailure, setDismissedFailure] = useState("");
  const mention = mentionAt(draft, caret);
  const command = slashAt(draft, caret);
  const slashOpen = !!command && command.start !== dismissed;
  const commandMatches = slashOpen ? slashMatches(command.query) : [];
  const matches =
    mention && mention.start !== dismissed
      ? mentionMatches(agents, mention.query)
      : [];
  const options = mention ? matches : commandMatches;
  const optionKind = mention ? "member" : "method";
  const index = Math.min(highlight, Math.max(0, options.length - 1));
  // A completion restores focus/caret on the next frame. Editing or moving the
  // caret before that frame runs owns the selection now, so a stale callback
  // must never pull the caret back (Shift+Enter would land at the old offset).
  const caretFrame = useRef(0);
  const caretRevision = useRef(0);
  const pendingCaret = useRef<
    { text: string; caret: number; revision: number } | undefined
  >(undefined);
  const restoringCaret = useRef(false);
  const cancelPendingCaret = () => {
    if (caretFrame.current) cancelAnimationFrame(caretFrame.current);
    caretFrame.current = 0;
    pendingCaret.current = undefined;
  };
  const restoreCaret = (text: string, caret: number) => {
    const textarea = input.current;
    // Without a live textarea there is nothing to restore; also skip if the
    // user has already moved focus into another control.
    if (
      !textarea ||
      (document.activeElement &&
        document.activeElement !== textarea &&
        document.activeElement !== document.body)
    )
      return;
    const revision = caretRevision.current;
    cancelPendingCaret();
    pendingCaret.current = { text, caret, revision };
    caretFrame.current = requestAnimationFrame(() => {
      caretFrame.current = 0;
      pendingCaret.current = undefined;
      const element = input.current;
      if (!element) return;
      // A later edit, external draft change or selection change wins.
      if (revision !== caretRevision.current) return;
      if (element.value !== text) return;
      const active = document.activeElement;
      if (active && active !== element && active !== document.body) return;
      if (active !== element) element.focus();
      if (element.selectionStart !== caret || element.selectionEnd !== caret) {
        restoringCaret.current = true;
        element.setSelectionRange(caret, caret);
        restoringCaret.current = false;
      }
    });
  };
  const pick = (agent: Agent) => {
    if (!mention) return;
    const next = applyMention(draft, mention.start, caret, agent.handle);
    setDraft(next.text);
    setCaret(next.caret);
    restoreCaret(next.text, next.caret);
  };
  const pickCommand = (next: (typeof workflowCommands)[number]) => {
    if (!command) return;
    setWorkflow(next.key);
    const applied = applySlash(draft, command.start, caret);
    setDraft(applied.text);
    setCaret(applied.caret);
    restoreCaret(applied.text, applied.caret);
  };
  const pickOption = (position: number) => {
    if (optionKind === "member") pick(matches[position]);
    else pickCommand(commandMatches[position]);
  };
  useEffect(
    () => () => {
      if (caretFrame.current) cancelAnimationFrame(caretFrame.current);
      caretFrame.current = 0;
    },
    [],
  );
  const routeLabel = routing
    ? (routing.targets ?? [])
        .map((id) => allAgents.find((agent) => agent.id === id)?.display_name)
        .filter(Boolean)
        .join("、") ||
      workflows[routing.workflow as WorkflowKey] ||
      ""
    : "";
  // The accepted routing owns the actual roles; the input area only reports it.
  const assignmentSummary = routing?.assignments
    ? (workflowRoles[routing.workflow] ?? Object.keys(routing.assignments))
        .map((role) => {
          const id = routing.assignments?.[role];
          const member =
            agents.find((agent) => agent.id === id) ??
            allAgents.find((agent) => agent.id === id);
          return member ? `${roleLabel(role)}：${member.display_name}` : "";
        })
        .filter(Boolean)
        .join(" · ")
    : "";
  const [modeError, setModeError] = useState("");
  const clarification = routing !== dismissedRouting ? routing?.clarification : "";
  const failedNotice = !!failureNoticeID && failureNoticeID !== dismissedFailure && failedSteps.length > 0;
  const notice = error || modeError || clarification || (failedNotice ? "成员回复未完成，已完成的回复会保留。重试只补跑该成员并沿用原流程。" : "");
  const dismissNotice = () => {
    if (error) onDismissError?.();
    else if (modeError) setModeError("");
    else if (clarification) setDismissedRouting(routing);
    else setDismissedFailure(failureNoticeID || "");
    input.current?.focus();
  };
  const [modeSaving, setModeSaving] = useState(false);
  const changeMode = async (next: string) => {
    if (!onExecutionModeChange || next === executionMode || modeSaving) return;
    setModeSaving(true);
    setModeError("");
    try {
      await onExecutionModeChange(next);
    } catch (failure) {
      setModeError(errorText(failure));
    } finally {
      setModeSaving(false);
    }
  };
  const [compact, setCompact] = useState(
    () => window.matchMedia?.("(max-width: 900px)")?.matches ?? false,
  );
  useEffect(() => {
    const media = window.matchMedia?.("(max-width: 900px)");
    if (!media) return;
    const update = () => setCompact(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  const hintButton = useRef<HTMLButtonElement>(null);
  const hintTooltip = useRef<HTMLDivElement>(null);
  const hintPointerFocus = useRef(false);
  const hintID = useId();
  const [hintHovered, setHintHovered] = useState(false);
  const [hintFocused, setHintFocused] = useState(false);
  const [hintIntent, setHintIntent] = useState<"none" | "open" | "closed">(
    "none",
  );
  const hintOpen =
    hintIntent === "open" ||
    (hintIntent !== "closed" && (hintHovered || hintFocused));
  const [hintPlacement, setHintPlacement] = useState<"top" | "bottom">("top");
  const [hintBox, setHintBox] = useState({ top: 0, left: 0, width: 280 });
  useLayoutEffect(() => {
    if (!hintOpen) return;
    let frame = 0;
    const place = () => {
      const button = hintButton.current;
      if (!button) return;
      const rect = button.getBoundingClientRect();
      const viewport = window.visualViewport;
      const width = Math.min(
        280,
        Math.max(0, (viewport?.width ?? window.innerWidth) - 16),
      );
      const viewportHeight = viewport?.height ?? window.innerHeight;
      const viewportWidth = viewport?.width ?? window.innerWidth;
      const offsetTop = viewport?.offsetTop ?? 0;
      const offsetLeft = viewport?.offsetLeft ?? 0;
      const height = hintTooltip.current?.offsetHeight || 72;
      const margin = 8;
      const gap = 8;
      const above = rect.top - offsetTop - gap - margin;
      const below = offsetTop + viewportHeight - rect.bottom - gap - margin;
      const placement = above >= height || above >= below ? "top" : "bottom";
      const top =
        placement === "top"
          ? Math.max(offsetTop + margin, rect.top - gap - height)
          : Math.max(
              offsetTop + margin,
              Math.min(
                offsetTop + viewportHeight - margin - height,
                rect.bottom + gap,
              ),
            );
      const left = Math.max(
        offsetLeft + margin,
        Math.min(
          rect.right - width,
          offsetLeft + viewportWidth - width - margin,
        ),
      );
      setHintPlacement(placement);
      setHintBox({ top, left, width });
    };
    place();
    const schedule = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(place);
    };
    window.addEventListener("resize", schedule);
    window.addEventListener("scroll", schedule, true);
    const viewport = window.visualViewport;
    viewport?.addEventListener("resize", schedule);
    viewport?.addEventListener("scroll", schedule);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("resize", schedule);
      window.removeEventListener("scroll", schedule, true);
      viewport?.removeEventListener("resize", schedule);
      viewport?.removeEventListener("scroll", schedule);
    };
  }, [hintOpen]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (canSend) onSend();
  };
  const placeholder = disabled
    ? "先创建一个工作空间，再开始提问"
    : compact
      ? "一起构建什么？"
      : "一起构建什么？（Enter 发送 · Shift + Enter 换行）";
  return (
    <form className={cx("composer")} data-composer="" onSubmit={submit}>
      {notice && (
        <div
          className={cx("composer-notice")}
          role={error || modeError ? "alert" : "status"}
        >
          <span className={cx("composer-notice-body")}>
            {notice}
            {failedNotice && !error && !modeError && !clarification && failedSteps.map((step) => (
              <span key={step.id} className={cx("composer-member-failure")}>
                <span>{step.agent_name || "成员"}：{runErrorText(step.error || "回复未完成")}</span>
                <button type="button" className={cx("composer-retry")} disabled={busy || active} onClick={() => onRetryMember?.(step.id)} title="只补跑该成员并沿用原流程；输入区新选的流程不会应用">
                  <Icon name="refresh" size={13} />重试 {step.agent_name || "成员"}
                </button>
              </span>
            ))}
          </span>
          <button
            type="button"
            className={cx("composer-notice-close")}
            aria-label={error ? "关闭错误提示" : "关闭提示"}
            title="关闭提示"
            onClick={dismissNotice}
          >
            <Icon name="close" size={12} />
          </button>
        </div>
      )}
      {pending}
      {workflow !== "free" && (
        <div className={cx("composer-capsules")}>
          <span
            className={cx("method-chip")}
            role="status"
            data-testid="composer-method"
          >
            <Icon name="layers" size={13} />
            {methodLabel(workflow)}
            <button
              type="button"
              className={cx("method-chip-clear")}
              aria-label="清除本次协作模式"
              title="清除本次协作模式，交回协调者判断"
              disabled={disabled}
              onClick={() => setWorkflow("free")}
            >
              <Icon name="close" size={12} />
            </button>
          </span>
        </div>
      )}
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
      {uploading && (
        <p className={cx("notice")} role="status">
          正在解析并保存资料…
        </p>
      )}
      {attachments.map((attachment) => (
        <div className={cx("reference-pill")} key={attachment.id}>
          <span title={attachment.parse_error || undefined}>
            资料：{attachment.file_name || attachment.title}
            {attachment.kind ? ` · ${attachment.kind}` : ""}
          </span>
          {onRemoveAttachment && (
            <button
              type="button"
              className={cx("icon-button small")}
              aria-label={`移除资料 ${attachment.file_name || attachment.title}`}
              onClick={() => onRemoveAttachment(attachment.id)}
            >
              <Icon name="close" size={14} />
            </button>
          )}
        </div>
      ))}
      {options.length > 0 && (
        <ul
          className={cx("mention-options")}
          role="listbox"
          id="composer-options"
          aria-label={optionKind === "member" ? "选择成员" : "选择协作模式"}
        >
          {optionKind === "member"
            ? matches.map((agent, i) => (
                <li
                  key={agent.id}
                  id={`composer-option-${agent.id}`}
                  role="option"
                  aria-selected={i === index}
                  onMouseDown={(event) => {
                    event.preventDefault();
                    pickOption(i);
                  }}
                >
                  <Avatar
                    name={agent.display_name}
                    avatar={agent.avatar}
                    small
                  />
                  {memberLabel(agent)}
                </li>
              ))
            : commandMatches.map((item, i) => (
                <li
                  key={item.key}
                  id={`composer-option-${item.key}`}
                  role="option"
                  aria-selected={i === index}
                  onMouseDown={(event) => {
                    event.preventDefault();
                    pickOption(i);
                  }}
                >
                  <span aria-hidden="true">{workflowIcons[item.key] || "⚡"}</span>
                  {item.label}
                </li>
              ))}
        </ul>
      )}
      <div className={cx("composer-input-wrap")}>
        <button
          ref={hintButton}
          type="button"
          className={cx("composer-hint")}
          aria-label="输入提示：/ 选择协作模式，@ 指定成员"
          aria-describedby={hintOpen ? hintID : undefined}
          data-open={hintOpen ? "true" : undefined}
          onPointerEnter={(event) => {
            if (event.pointerType === "mouse") setHintHovered(true);
          }}
          onPointerLeave={(event) => {
            if (event.pointerType === "mouse") {
              setHintHovered(false);
              setHintIntent("none");
            }
          }}
          onPointerDown={() => {
            hintPointerFocus.current = true;
          }}
          onFocus={() => {
            // A pointer press opens through hover/click; only keyboard focus
            // sets the transient focus source, so a tap is not toggled twice.
            if (hintPointerFocus.current) {
              hintPointerFocus.current = false;
              return;
            }
            setHintFocused(true);
          }}
          onBlur={() => {
            setHintFocused(false);
            setHintIntent("none");
          }}
          onClick={(event) => {
            if (event.detail === 0) {
              setHintIntent("open");
              return;
            }
            setHintIntent((current) =>
              current === "open" ? "closed" : "open",
            );
          }}
          onKeyDown={(event) => {
            if (event.key === "Escape" && hintOpen) {
              event.preventDefault();
              setHintIntent("closed");
            }
          }}
        >
          <Icon name="info" size={14} />
        </button>
        <textarea
        ref={input}
        id="composer-input"
        className={cx("composer-input")}
        aria-label="输入问题"
        placeholder={placeholder}
        value={draft}
        disabled={disabled}
        rows={2}
        aria-controls={options.length ? "composer-options" : undefined}
        aria-autocomplete="list"
        aria-activedescendant={
          options.length
            ? `composer-option-${command ? commandMatches[index].key : matches[index].id}`
            : undefined
        }
        onChange={(event) => {
          // Any real edit supersedes an older completion's caret restore.
          caretRevision.current += 1;
          cancelPendingCaret();
          setDraft(event.target.value);
          setCaret(event.target.selectionStart);
          setHighlight(0);
        }}
        onSelect={(event) => {
          const selection = event.currentTarget.selectionStart;
          if (restoringCaret.current) return;
          const pending = pendingCaret.current;
          if (
            pending &&
            event.currentTarget.value === pending.text &&
            selection === pending.caret
          ) {
            // Caused by applying the completion itself; keep its restore.
            setCaret(selection);
            return;
          }
          // A later selection change owns the caret.
          caretRevision.current += 1;
          cancelPendingCaret();
          setCaret(selection);
        }}
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
          if (options.length) {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault();
              const step = event.key === "ArrowDown" ? 1 : -1;
              setHighlight((index + step + options.length) % options.length);
              return;
            }
            if (
              !event.shiftKey &&
              (event.key === "Enter" || event.key === "Tab")
            ) {
              event.preventDefault();
              pickOption(index);
              return;
            }
            if (event.key === "Escape") {
              event.preventDefault();
              setDismissed(mention?.start ?? command?.start ?? -1);
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
      </div>
      <div className={cx("composer-bottom")}>
        <div className={cx("composer-meta-chips")}>
          {executionMode && onExecutionModeChange && (
            <span className={cx("mode-chip")}>
              <Select
                aria-label="权限模式"
                className={cx("mode-select")}
                value={executionMode}
                disabled={disabled || modeSaving}
                onChange={(selected) => void changeMode(selected)}
              >
                <option value="controlled">受控</option>
                <option value="project_autonomous">项目内自主</option>
                <option value="full_access">完全访问</option>
              </Select>
            </span>
          )}
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
          {routeLabel && !routing?.clarification && (
            <span
              className={cx("route-chip")}
              role="status"
              title={routing?.explanation}
            >
              {`→ ${routeLabel}`}
            </span>
          )}
          {assignmentSummary && (
            <span className={cx("route-chip")} role="status" title="本轮分工">
              {assignmentSummary}
            </span>
          )}
          {workflow === "engineering" && (
            <small className={cx("muted")} role="note">
              按成员已获授权的能力执行，独立复核后交付工程报告
            </small>
          )}
        </div>
        <div className={cx("composer-actions")}>
          {onAttach && (
            <label
              className={cx("icon-button")}
              title={uploading ? "正在解析资料…" : "添加资料"}
              aria-busy={uploading || undefined}
            >
              <Icon name="file" size={16} />
              <span className={cx("sr-only")}>添加资料</span>
              <input
                type="file"
                className={cx("sr-only")}
                disabled={uploading}
                onChange={(event) => {
                  const file = event.currentTarget.files?.[0];
                  event.currentTarget.value = "";
                  if (file) onAttach(file);
                }}
              />
            </label>
          )}
          {active && (
            <button
              type="button"
              className={cx("stop composer-send")}
              aria-label={canceling ? "正在停止" : "停止协作"}
              aria-busy={canceling || undefined}
              title={canceling ? "正在停止…" : "停止协作"}
              onClick={onStop}
              disabled={canceling}
            >
              <Icon name="stop_filled" size={14} />
            </button>
          )}
          <button
            className={cx("primary composer-send")}
            type="submit"
            aria-label={busy ? "正在提交" : active ? "保存补充" : "发送"}
            title={busy ? "正在提交…" : active ? "保存补充 · Enter" : "发送 · Enter"}
            disabled={!canSend}
          >
            <Icon name="send" size={16} />
          </button>
        </div>
      </div>
      {hintOpen &&
        createPortal(
          <div
            ref={hintTooltip}
            id={hintID}
            role="tooltip"
            data-placement={hintPlacement}
            className={cx("composer-hint-tooltip")}
            style={{
              top: hintBox.top,
              left: hintBox.left,
              width: hintBox.width,
            }}
          >
            @ 指定成员，/ 选择协作模式；都不使用时由协调者按任务判断。
          </div>,
          document.body,
        )}
    </form>
  );
}
