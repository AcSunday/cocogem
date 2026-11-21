import { useState } from "react";
import type { CandidateView } from "../api/client";
import { isEnglish, t, useLanguage } from "../lib/i18n";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import conversation from "../styles/conversation.module.css";

const cx = scoped(base, controls, conversation);

const stateLabelsZh: Record<string, string> = {
  verified: "已验证",
  unverified_mutation: "未验证变更",
  unchanged: "无改动",
};

const stateLabelsEn: Record<string, string> = {
  verified: "Verified",
  unverified_mutation: "Unverified changes",
  unchanged: "Unchanged",
};

const stateTones: Record<string, string> = {
  verified: "verified",
  unverified_mutation: "warn",
  unchanged: "muted",
};

function rootName(root: string) {
  const parts = root.split(/[\\/]/).filter(Boolean);
  return parts.at(-1) || root;
}

function changedSummary(paths: string[]) {
  if (!paths.length) return t("没有文件差异", "No file changes");
  if (paths.length === 1) return paths[0];
  return t(
    `${paths[0]} 等 ${paths.length} 个文件`,
    `${paths[0]} and ${paths.length - 1} other file(s)`,
  );
}

/**
 * The conversation delivery bar for unapplied candidate directories.
 *
 * A candidate never touches the bound project until the reader explicitly
 * applies it. Verification is shown before the decision, an unverified mutation
 * is never presented as verified work, and both actions require a second,
 * labelled confirmation. While a collaboration is running the bar stays
 * readable but locked, because writing a worktree back mid-run would race the
 * member still editing it.
 */
export function CandidateBar({
  items,
  locked = false,
  onApply,
  onDiscard,
  onDiff,
  onExportBranch,
}: {
  items: CandidateView[];
  locked?: boolean;
  onApply: (item: CandidateView) => Promise<void>;
  onDiscard: (item: CandidateView) => Promise<void>;
  onDiff?: (item: CandidateView) => Promise<string>;
  onExportBranch?: (item: CandidateView) => Promise<string>;
}) {
  useLanguage();
  const [confirm, setConfirm] = useState<{
    id: string;
    kind: "apply" | "discard";
  } | null>(null);
  const [working, setWorking] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [diffs, setDiffs] = useState<
    Record<string, { open: boolean; loading: boolean; text: string }>
  >({});
  const [branches, setBranches] = useState<Record<string, string>>({});
  if (!items.length) return null;
  const stateLabels = isEnglish() ? stateLabelsEn : stateLabelsZh;
  const setItemError = (id: string, message: string) =>
    setErrors((current) => {
      if (!message) {
        const next = { ...current };
        delete next[id];
        return next;
      }
      return { ...current, [id]: message };
    });
  const decide = async (item: CandidateView, kind: "apply" | "discard") => {
    setWorking(item.execution.id);
    setItemError(item.execution.id, "");
    try {
      if (kind === "apply") await onApply(item);
      else await onDiscard(item);
      setConfirm(null);
    } catch (failure) {
      setItemError(item.execution.id, errorText(failure));
    } finally {
      setWorking("");
    }
  };
  const toggleDiff = async (item: CandidateView) => {
    if (!onDiff) return;
    const id = item.execution.id;
    const current = diffs[id];
    if (current?.open) {
      setDiffs((prev) => ({ ...prev, [id]: { ...current, open: false } }));
      return;
    }
    setDiffs((prev) => ({
      ...prev,
      [id]: { open: true, loading: true, text: current?.text ?? "" },
    }));
    setItemError(id, "");
    try {
      const text = await onDiff(item);
      setDiffs((prev) => ({
        ...prev,
        [id]: { open: true, loading: false, text },
      }));
    } catch (failure) {
      setDiffs((prev) => ({
        ...prev,
        [id]: { open: false, loading: false, text: "" },
      }));
      setItemError(id, errorText(failure));
    }
  };
  const exportBranch = async (item: CandidateView) => {
    if (!onExportBranch) return;
    const id = item.execution.id;
    setWorking(id);
    setItemError(id, "");
    try {
      const branch = await onExportBranch(item);
      setBranches((prev) => ({ ...prev, [id]: branch }));
    } catch (failure) {
      setItemError(id, errorText(failure));
    } finally {
      setWorking("");
    }
  };
  return (
    <section
      className={cx("candidate-bar")}
      aria-label={t("候选变更", "Candidate changes")}
    >
      <div className={cx("candidate-bar-head")}>
        <strong>{t("候选变更", "Candidate changes")}</strong>
        <span className={cx("candidate-bar-hint")}>
          {locked
            ? t(
                "本轮协作结束后可应用或丢弃；项目目录尚未改动",
                "Apply or discard after the current run finishes; your project directory is untouched",
              )
            : t(
                "未点应用前，项目目录不会有任何改动",
                "Your project directory remains untouched until you click Apply",
              )}
        </span>
      </div>
      {items.map((item) => {
        const { execution, verification } = item;
        const pending = confirm?.id === execution.id ? confirm.kind : "";
        const busy = working === execution.id;
        const paths = verification.changed_paths ?? [];
        const itemError = errors[execution.id] ?? "";
        const diffState = diffs[execution.id];
        const exportedBranch = branches[execution.id] ?? "";
        // A candidate whose tree still matches its derived snapshot has nothing
        // to write back; offering Apply would imply a change that does not exist.
        const nothingToApply = verification.state === "unchanged";
        const canExportBranch =
          !!onExportBranch &&
          execution.kind === "git_worktree" &&
          !nothingToApply;
        return (
          <article key={execution.id} className={cx("candidate-item")}>
            <div className={cx("candidate-item-head")}>
              <span
                className={cx("candidate-state")}
                data-tone={stateTones[verification.state] ?? "muted"}
              >
                {stateLabels[verification.state] ?? verification.state}
              </span>
              <span
                className={cx("candidate-project")}
                title={execution.canonical_root}
              >
                {rootName(execution.canonical_root) || t("项目", "Project")}
              </span>
              <span className={cx("candidate-summary")}>
                {changedSummary(paths)}
              </span>
            </div>
            {verification.reason && (
              <p className={cx("candidate-reason")}>{verification.reason}</p>
            )}
            {(verification.hygiene_hints ?? []).length > 0 && (
              <p className={cx("candidate-hygiene")} role="status">
                {verification.hygiene_hints?.join(t("；", "; "))}
              </p>
            )}
            {itemError && (
              <p className={cx("error")} role="alert">
                {itemError}
              </p>
            )}
            {exportedBranch && (
              <p className={cx("candidate-hygiene")} role="status">
                {t("已导出为本地 Git 分支：", "Exported to local Git branch: ")}
                <code>{exportedBranch}</code>
                {t("（可使用 ", " (merge with ")}
                <code>git merge {exportedBranch}</code>
                {t(" 合并）", ")")}
              </p>
            )}
            {paths.length > 1 && (
              <details className={cx("candidate-paths")}>
                <summary>
                  {t(
                    `查看 ${paths.length} 个变更文件`,
                    `View ${paths.length} changed files`,
                  )}
                </summary>
                <ul>
                  {paths.map((path) => (
                    <li key={path}>{path}</li>
                  ))}
                </ul>
              </details>
            )}
            {diffState?.open && (
              <div className={cx("candidate-diff")}>
                {diffState.loading ? (
                  <p className={cx("candidate-reason")}>
                    {t("正在加载变更内容…", "Loading diff…")}
                  </p>
                ) : (
                  <pre
                    style={{
                      margin: "8px 0 0",
                      padding: "10px 12px",
                      maxHeight: "320px",
                      overflow: "auto",
                      fontSize: "12px",
                      lineHeight: 1.5,
                      borderRadius: "8px",
                      background: "var(--surface-2, rgba(0,0,0,0.04))",
                      whiteSpace: "pre-wrap",
                      wordBreak: "break-word",
                    }}
                  >
                    {diffState.text ||
                      t("（当前无文本差异）", "(No text diff available)")}
                  </pre>
                )}
              </div>
            )}
            {pending ? (
              <div className={cx("candidate-confirm")}>
                <span>
                  {pending === "apply"
                    ? t(
                        "将把这些文件写入项目目录；若原目录已被外部修改会拒绝覆盖。",
                        "Writes these files to the project directory; refuses to overwrite if the target files were modified externally.",
                      )
                    : t(
                        "将清理该候选目录；项目目录保持不变。",
                        "Removes this candidate directory; your project directory remains unchanged.",
                      )}
                </span>
                <button
                  type="button"
                  className={
                    pending === "discard"
                      ? cx("danger", "small")
                      : cx("candidate-primary", "small")
                  }
                  disabled={busy}
                  onClick={() => void decide(item, pending)}
                >
                  {busy
                    ? t("正在处理…", "Working…")
                    : pending === "apply"
                      ? t("确认应用", "Confirm apply")
                      : t("确认丢弃", "Confirm discard")}
                </button>
                <button
                  type="button"
                  className={cx("small")}
                  disabled={busy}
                  onClick={() => setConfirm(null)}
                >
                  {t("取消", "Cancel")}
                </button>
              </div>
            ) : (
              <div className={cx("candidate-actions")}>
                {onDiff && !nothingToApply && (
                  <button
                    type="button"
                    className={cx("small")}
                    disabled={busy}
                    onClick={() => void toggleDiff(item)}
                  >
                    {diffState?.open
                      ? t("收起 Diff", "Hide diff")
                      : t("查看 Diff", "View diff")}
                  </button>
                )}
                <button
                  type="button"
                  className={cx("candidate-primary", "small")}
                  disabled={locked || busy || nothingToApply}
                  title={
                    locked
                      ? t(
                          "本轮协作结束后才能应用到项目目录",
                          "Wait for the current run to finish before applying to the project directory",
                        )
                      : nothingToApply
                        ? t(
                            "该候选没有文件差异，无需应用",
                            "No file differences to apply",
                          )
                        : undefined
                  }
                  onClick={() => {
                    setItemError(execution.id, "");
                    setConfirm({ id: execution.id, kind: "apply" });
                  }}
                >
                  {t("应用到项目目录", "Apply to project")}
                </button>
                {canExportBranch && (
                  <button
                    type="button"
                    className={cx("small")}
                    disabled={locked || busy}
                    title={t(
                      "导出为本地 Git 分支，便于手动合并或解决冲突",
                      "Export as a local Git branch for manual merge or conflict resolution",
                    )}
                    onClick={() => void exportBranch(item)}
                  >
                    {t("导出本地分支", "Export local branch")}
                  </button>
                )}
                <button
                  type="button"
                  className={cx("small")}
                  disabled={locked || busy}
                  title={
                    locked
                      ? t(
                          "本轮协作结束后才能丢弃候选变更",
                          "Wait for the current run to finish before discarding candidate changes",
                        )
                      : undefined
                  }
                  onClick={() => {
                    setItemError(execution.id, "");
                    setConfirm({ id: execution.id, kind: "discard" });
                  }}
                >
                  {t("丢弃变更", "Discard changes")}
                </button>
              </div>
            )}
          </article>
        );
      })}
    </section>
  );
}
