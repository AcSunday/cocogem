import { useState } from "react";
import type { CandidateView } from "../api/client";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import conversation from "../styles/conversation.module.css";

const cx = scoped(base, controls, conversation);

const stateLabels: Record<string, string> = {
  verified: "已验证",
  unverified_mutation: "未验证变更",
  unchanged: "无改动",
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
  if (!paths.length) return "没有文件差异";
  if (paths.length === 1) return paths[0];
  return `${paths[0]} 等 ${paths.length} 个文件`;
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
}: {
  items: CandidateView[];
  locked?: boolean;
  onApply: (item: CandidateView) => Promise<void>;
  onDiscard: (item: CandidateView) => Promise<void>;
}) {
  const [confirm, setConfirm] = useState<{
    id: string;
    kind: "apply" | "discard";
  } | null>(null);
  const [working, setWorking] = useState("");
  const [error, setError] = useState("");
  if (!items.length) return null;
  const decide = async (item: CandidateView, kind: "apply" | "discard") => {
    setWorking(item.execution.id);
    setError("");
    try {
      if (kind === "apply") await onApply(item);
      else await onDiscard(item);
      setConfirm(null);
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setWorking("");
    }
  };
  return (
    <section className={cx("candidate-bar")} aria-label="候选变更">
      <div className={cx("candidate-bar-head")}>
        <strong>候选变更</strong>
        <span className={cx("candidate-bar-hint")}>
          {locked
            ? "本轮协作结束后可应用或丢弃；项目目录尚未改动"
            : "未点应用前，项目目录不会有任何改动"}
        </span>
      </div>
      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}
      {items.map((item) => {
        const { execution, verification } = item;
        const pending = confirm?.id === execution.id ? confirm.kind : "";
        const busy = working === execution.id;
        const paths = verification.changed_paths ?? [];
        // A candidate whose tree still matches its derived snapshot has nothing
        // to write back; offering Apply would imply a change that does not exist.
        const nothingToApply = verification.state === "unchanged";
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
                {rootName(execution.canonical_root) || "项目"}
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
                {verification.hygiene_hints?.join("；")}
              </p>
            )}
            {paths.length > 1 && (
              <details className={cx("candidate-paths")}>
                <summary>查看 {paths.length} 个变更文件</summary>
                <ul>
                  {paths.map((path) => (
                    <li key={path}>{path}</li>
                  ))}
                </ul>
              </details>
            )}
            {pending ? (
              <div className={cx("candidate-confirm")}>
                <span>
                  {pending === "apply"
                    ? "将把这些文件写入项目目录；若原目录已被外部修改会拒绝覆盖。"
                    : "将清理该候选目录；项目目录保持不变。"}
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
                    ? "正在处理…"
                    : pending === "apply"
                      ? "确认应用"
                      : "确认丢弃"}
                </button>
                <button
                  type="button"
                  className={cx("small")}
                  disabled={busy}
                  onClick={() => setConfirm(null)}
                >
                  取消
                </button>
              </div>
            ) : (
              <div className={cx("candidate-actions")}>
                <button
                  type="button"
                  className={cx("candidate-primary", "small")}
                  disabled={locked || busy || nothingToApply}
                  title={
                    locked
                      ? "本轮协作结束后才能应用到项目目录"
                      : nothingToApply
                        ? "该候选没有文件差异，无需应用"
                        : undefined
                  }
                  onClick={() => {
                    setError("");
                    setConfirm({ id: execution.id, kind: "apply" });
                  }}
                >
                  应用到项目目录
                </button>
                <button
                  type="button"
                  className={cx("small")}
                  disabled={locked || busy}
                  title={locked ? "本轮协作结束后才能丢弃候选变更" : undefined}
                  onClick={() => {
                    setError("");
                    setConfirm({ id: execution.id, kind: "discard" });
                  }}
                >
                  丢弃变更
                </button>
              </div>
            )}
          </article>
        );
      })}
    </section>
  );
}
