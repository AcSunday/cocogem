import { useState } from "react";
import { api, type Question } from "../api/client";
import type { FileRef, ImagePreview } from "../lib/fileRefs";
import { t, useLanguage } from "../lib/i18n";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import conversation from "../styles/conversation.module.css";
import project from "../styles/project.module.css";
import { Markdown } from "./Markdown";

const cx = scoped(base, controls, conversation, project);

function blockingScopeLabel(scope: string): string {
  switch (scope) {
    case "task":
      return t("影响整个任务继续", "Required for task continuation");
    case "branch":
      return t("影响当前分支步骤", "Affects current branch");
    default:
      return scope || t("需要你确认", "Decision needed");
  }
}

/**
 * Renders open task questions directly inside the conversation above the
 * composer so users never have to leave the chat to confirm or answer.
 */
export function QuestionPromptCard({
  questions,
  threadID,
  onOpenFile,
  onOpenImage,
  onAnswered,
}: {
  questions: Question[];
  threadID?: string;
  onOpenFile?: (ref: FileRef) => void;
  onOpenImage?: (preview: ImagePreview) => void;
  onAnswered?: (question: Question) => Promise<void> | void;
}) {
  useLanguage();
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [submittingID, setSubmittingID] = useState("");
  const [error, setError] = useState("");

  const openList = questions.filter((item) => item.status === "open");
  if (!openList.length) return null;

  const submit = async (question: Question, answer: string) => {
    const trimmed = answer.trim();
    if (!trimmed || submittingID) return;
    setSubmittingID(question.id);
    setError("");
    try {
      const answered = await api<Question>(
        `/api/v1/tasks/${encodeURIComponent(question.task_id)}/questions/${encodeURIComponent(question.id)}/answers`,
        "POST",
        {
          answer: trimmed,
          answer_ref: `user-answer:${crypto.randomUUID()}`,
        },
        { "If-Match": String(question.task_revision) },
      );
      setAnswers((current) => {
        const next = { ...current };
        delete next[question.id];
        return next;
      });
      await onAnswered?.(answered);
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setSubmittingID("");
    }
  };

  return (
    <section
      className={cx("conversation-questions")}
      data-testid="conversation-questions"
      aria-label={t("需要你确认", "Decision needed")}
    >
      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}
      {openList.map((question) => {
        const answer = answers[question.id] ?? question.recommended ?? "";
        const busy = submittingID === question.id;
        return (
          <article
            className={cx("task-brief-ask", "conversation-question-card")}
            key={question.id}
            data-testid={`question-card-${question.id}`}
          >
            <div className={cx("task-brief-ask-label")}>
              <span
                className={cx("task-focus-dot", "task-focus-dot-attention")}
                aria-hidden="true"
              />
              {t("需要你确认", "Decision needed")} ·{" "}
              <span>{blockingScopeLabel(question.blocking_scope)}</span>
            </div>
            <div className={cx("conversation-question-body")}>
              <Markdown
                text={question.body}
                threadID={threadID}
                onOpenFile={onOpenFile}
                onOpenImage={onOpenImage}
              />
            </div>
            {!!question.options?.length && (
              <div
                className={cx("task-brief-options")}
                role="group"
                aria-label={t("可选回答", "Suggested answers")}
              >
                {question.options.map((option) => (
                  <button
                    key={option}
                    type="button"
                    disabled={busy}
                    className={cx(
                      "task-brief-option",
                      answer === option && "selected",
                    )}
                    aria-pressed={answer === option}
                    onClick={() =>
                      setAnswers((current) => ({
                        ...current,
                        [question.id]: option,
                      }))
                    }
                  >
                    {option}
                    {option === question.recommended
                      ? t("（建议）", " (recommended)")
                      : ""}
                  </button>
                ))}
              </div>
            )}
            <label className={cx("task-brief-answer")}>
              {t("补充说明或自定义决定", "Your decision")}
              <textarea
                value={answer}
                disabled={busy}
                rows={2}
                onChange={(event) =>
                  setAnswers((current) => ({
                    ...current,
                    [question.id]: event.target.value,
                  }))
                }
                placeholder={t("选择上方选项或输入你的决定…", "Select an option or write your decision…")}
              />
            </label>
            <div className={cx("task-brief-ask-actions")}>
              <button
                type="button"
                className={cx("primary", "task-brief-action")}
                disabled={busy || !answer.trim()}
                onClick={() => void submit(question, answer)}
              >
                {busy
                  ? t("提交中…", "Submitting…")
                  : t("确认并继续", "Confirm & continue")}
              </button>
              <span className={cx("task-brief-ask-hint")}>
                {t(
                  "提交后将自动唤醒任务并继续在当前会话中执行",
                  "Submitting wakes the task and resumes right here",
                )}
              </span>
            </div>
          </article>
        );
      })}
    </section>
  );
}
