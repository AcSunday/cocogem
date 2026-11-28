import { useEffect, useMemo, useRef, useState } from "react";
import { threadFile, type ThreadFileView } from "../api/client";
import type { ImagePreview } from "../lib/fileRefs";
import { t } from "../lib/i18n";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import panels from "../styles/panels.module.css";
import { Modal } from "./Modal";

const cx = scoped(base, controls, panels);

export type FileViewerModalProps = {
  threadID: string;
  path: string;
  startLine?: number;
  endLine?: number;
  initialTab?: "file" | "diff";
  onOpenImage?: (preview: ImagePreview) => void;
  close: () => void;
};

function rootKindLabel(rootKind: string): string {
  switch (rootKind) {
    case "candidate":
      return t("候选变更", "Candidate");
    case "workspace":
      return t("工作空间", "Workspace");
    case "task":
      return t("任务目录", "Task dir");
    case "host":
      return t("主机文件", "Host");
    case "source":
      return t("已捕获快照", "Snapshot");
    default:
      return rootKind;
  }
}

function diffLineKind(line: string): string {
  if (
    line.startsWith("+++") ||
    line.startsWith("---") ||
    line.startsWith("diff --git") ||
    line.startsWith("index ")
  ) {
    return "meta";
  }
  if (line.startsWith("@@")) return "hunk";
  if (line.startsWith("+")) return "add";
  if (line.startsWith("-")) return "del";
  return "ctx";
}

/** Displays a thread file with line numbers, highlighted ranges, unified diff tab, or image preview. */
export function FileViewerModal({
  threadID,
  path,
  startLine,
  endLine,
  initialTab = "file",
  onOpenImage,
  close,
}: FileViewerModalProps) {
  const [view, setView] = useState<ThreadFileView | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<"file" | "diff">(initialTab);
  const [copied, setCopied] = useState(false);
  const highlightRowRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    setView(null);
    setTab(initialTab);
    threadFile(threadID, path, startLine, endLine)
      .then((result) => {
        if (!active) return;
        setView(result);
        if (result.kind === "diff") {
          setTab("diff");
        } else if (initialTab === "diff" && result.diff) {
          setTab("diff");
        } else {
          setTab(initialTab);
        }
      })
      .catch((err: unknown) => {
        if (!active) return;
        setError(
          err instanceof Error
            ? err.message
            : t("无法加载文件内容", "Failed to load file"),
        );
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [threadID, path, startLine, endLine, initialTab]);

  const lines = useMemo(() => {
    if (!view || view.kind !== "text") return [];
    const rawLines = (view.content ?? "").split("\n");
    const first = view.start_line > 0 ? view.start_line : 1;
    return rawLines.map((text, index) => ({
      lineNumber: first + index,
      text,
    }));
  }, [view]);

  const diffLines = useMemo(() => {
    if (!view?.diff) return [];
    return view.diff.split("\n");
  }, [view?.diff]);

  const hlStart = view?.highlight_start ?? startLine ?? 0;
  const hlEnd =
    view?.highlight_end && view.highlight_end >= hlStart
      ? view.highlight_end
      : hlStart;

  useEffect(() => {
    if (tab !== "file" || hlStart <= 0 || !view || view.kind !== "text") return;
    highlightRowRef.current?.scrollIntoView?.({
      block: "center",
      behavior: "auto",
    });
  }, [tab, hlStart, view]);

  const title = view?.display_path || path;
  const copyPath = async () => {
    const target = view?.path || path;
    try {
      await navigator.clipboard?.writeText(target);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard may be blocked in non-HTTPS test environments.
    }
  };

  return (
    <Modal title={title} close={close} wide>
      <div className={cx("file-viewer-shell")} data-testid="file-viewer-modal">
        {loading && (
          <p className={cx("empty-note")} role="status">
            {t("正在读取文件…", "Loading file…")}
          </p>
        )}
        {error && (
          <p className={cx("error")} role="alert">
            {error}
          </p>
        )}
        {view && (
          <>
            <div className={cx("reader-meta", "file-viewer-meta")}>
              <span className={cx("role-chip")}>
                {rootKindLabel(view.root_kind)}
              </span>
              {view.total_lines > 0 && (
                <span className={cx("badge")}>
                  {t(
                    `${view.start_line}-${view.end_line} / 共 ${view.total_lines} 行`,
                    `L${view.start_line}-${view.end_line} of ${view.total_lines}`,
                  )}
                </span>
              )}
              {hlStart > 0 && (
                <button
                  type="button"
                  className={cx("badge", "file-range-badge")}
                  onClick={() => {
                    setTab("file");
                    highlightRowRef.current?.scrollIntoView?.({
                      block: "center",
                      behavior: "smooth",
                    });
                  }}
                >
                  {hlEnd > hlStart ? `L${hlStart}-L${hlEnd}` : `L${hlStart}`}
                </button>
              )}
              {view.truncated && (
                <span className={cx("badge")}>
                  {t("已按行窗截取", "Windowed slice")}
                </span>
              )}
              <button
                type="button"
                className={cx("ghost", "compact")}
                onClick={copyPath}
              >
                {copied ? t("已复制路径", "Copied") : t("复制路径", "Copy path")}
              </button>
              {(view.kind === "text" || view.diff) && (
                <div className={cx("file-viewer-tabs")} role="tablist">
                  {view.kind === "text" && (
                    <button
                      type="button"
                      role="tab"
                      aria-selected={tab === "file"}
                      className={cx("ghost", "compact")}
                      onClick={() => setTab("file")}
                    >
                      {t("文件内容", "File")}
                    </button>
                  )}
                  <button
                    type="button"
                    role="tab"
                    aria-selected={tab === "diff"}
                    className={cx("ghost", "compact")}
                    onClick={() => setTab("diff")}
                    disabled={!view.diff && view.kind !== "diff"}
                  >
                    {view.diff
                      ? t("变更 Diff", "Diff")
                      : t("无 Diff", "No diff")}
                  </button>
                </div>
              )}
            </div>

            {view.kind === "image" && view.raw_url && tab !== "diff" && (
              <div className={cx("file-viewer-image-wrap")}>
                <button
                  type="button"
                  className={cx("file-viewer-image-button")}
                  onClick={() =>
                    onOpenImage?.({
                      src: view.raw_url!,
                      title: view.display_path || view.path,
                      downloadUrl: view.raw_url,
                      sourceId: view.source_id,
                      path: view.path,
                    })
                  }
                >
                  <img
                    src={view.raw_url}
                    alt={view.display_path || view.path}
                    className={cx("file-viewer-image")}
                  />
                </button>
              </div>
            )}

            {tab === "file" && view.kind === "text" && (
              <div
                className={cx("file-code-viewport")}
                data-testid="file-code-viewport"
              >
                {lines.map((entry) => {
                  const isHighlighted =
                    hlStart > 0 &&
                    entry.lineNumber >= hlStart &&
                    entry.lineNumber <= hlEnd;
                  const isAnchor =
                    hlStart > 0 && entry.lineNumber === hlStart;
                  return (
                    <div
                      key={entry.lineNumber}
                      ref={isAnchor ? highlightRowRef : undefined}
                      data-line={entry.lineNumber}
                      data-highlighted={isHighlighted ? "true" : undefined}
                      className={cx(
                        "file-code-line",
                        isHighlighted && "file-code-line-highlight",
                      )}
                    >
                      <span className={cx("file-line-num")}>
                        {entry.lineNumber}
                      </span>
                      <code className={cx("file-line-text")}>
                        {entry.text || " "}
                      </code>
                    </div>
                  );
                })}
              </div>
            )}

            {tab === "diff" && (
              <div
                className={cx("file-diff-viewport")}
                data-testid="file-diff-viewport"
              >
                {diffLines.length === 0 ? (
                  <p className={cx("empty-note")}>
                    {t(
                      "当前工作区或候选目录中暂无该文件的变更 Diff。",
                      "No diff recorded for this file.",
                    )}
                  </p>
                ) : (
                  diffLines.map((line, index) => (
                    <div
                      key={index}
                      className={cx(
                        "file-diff-line",
                        `file-diff-${diffLineKind(line)}`,
                      )}
                    >
                      <code>{line || " "}</code>
                    </div>
                  ))
                )}
              </div>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}
