import { useState } from "react";
import type { Artifact, Source } from "../api/client";
import { cx } from "../styles/classes";
import {
  artifactTypeLabel,
  findingStatusLabel,
  formatDateTime,
  severityLabel,
} from "../lib/presentation";
import { Icon } from "./Icon";
import { Markdown } from "./Markdown";
import { Modal } from "./Modal";

/** Reports read best at full width, so they open in a wide dialog rather than the side panel. */
export function ArtifactReader({
  artifact,
  sources,
  close,
  onFeedback,
  onReference,
  onSource,
  onError,
}: {
  artifact: Artifact;
  sources: Source[];
  close: () => void;
  onFeedback: (value: "accepted" | "dismissed") => void;
  onReference: () => void;
  onSource: (id: string) => void;
  onError: (error: unknown) => void;
}) {
  const [copied, setCopied] = useState(false);
  const title = (id: string) =>
    sources.find((source) => source.id === id)?.title ?? id.slice(0, 8);
  const sourceButtons = (ids: string[] | null | undefined) =>
    (ids ?? []).length > 0 && (
      <div className={cx("source-links")}>
        {(ids ?? []).map((id) => (
          <button key={id} className={cx("small")} onClick={() => onSource(id)}>
            <Icon name="file" size={14} />
            打开来源：{title(id)}
          </button>
        ))}
      </div>
    );
  return (
    <Modal title={artifact.title} close={close} wide>
      <div data-testid="artifact-detail">
        <div className={cx("reader-meta")}>
          <span className={cx("role-chip")}>
            {artifactTypeLabel(artifact.type)} · v{artifact.version}
          </span>
          <small>{formatDateTime(artifact.created_at)}</small>
        </div>
        <div className={cx("artifact-actions")}>
          <button
            aria-pressed={artifact.feedback === "accepted"}
            onClick={() => onFeedback("accepted")}
          >
            <Icon name="check" size={14} />
            {artifact.feedback === "accepted" ? "已接受" : "接受"}
          </button>
          <button
            aria-pressed={artifact.feedback === "dismissed"}
            onClick={() => onFeedback("dismissed")}
          >
            {artifact.feedback === "dismissed" ? "已搁置" : "搁置"}
          </button>
          <button onClick={onReference}>
            <Icon name="reply" size={14} />
            引用后追问
          </button>
          <button
            onClick={() =>
              void navigator.clipboard
                .writeText(artifact.body)
                .then(() => {
                  setCopied(true);
                  setTimeout(() => setCopied(false), 1500);
                })
                .catch(onError)
            }
          >
            <Icon name="copy" size={14} />
            {copied ? "已复制" : "复制"}
          </button>
          <a
            className={cx("button")}
            href={`/api/v1/artifacts/${artifact.id}/versions/${artifact.version}/export`}
          >
            <Icon name="download" size={14} />
            下载 Markdown
          </a>
        </div>
        <Markdown text={artifact.body} />
        {(artifact.findings ?? []).length > 0 && (
          <section className={cx("reader-section")}>
            <h3>结构化发现</h3>
            {(artifact.findings ?? []).map((finding, index) => (
              <article className={cx("finding")} key={index}>
                <h4>
                  {finding.title}
                  <span className={cx("role-chip")}>
                    {findingStatusLabel(finding.status)}
                  </span>
                  {finding.severity && (
                    <span className={cx("role-chip")}>
                      影响：{severityLabel(finding.severity)}
                    </span>
                  )}
                </h4>
                <Markdown text={finding.detail} />
                {sourceButtons(finding.source_ids)}
              </article>
            ))}
          </section>
        )}
        {(artifact.limitations ?? []).length > 0 && (
          <section className={cx("reader-section")}>
            <h3>限制</h3>
            <ul>
              {(artifact.limitations ?? []).map((limit, index) => (
                <li key={index}>{limit}</li>
              ))}
            </ul>
          </section>
        )}
        <section className={cx("reader-section")}>
          <h3>捕获来源</h3>
          {sourceButtons(artifact.source_ids) || (
            <p className={cx("muted")}>本产物没有引用来源。</p>
          )}
        </section>
        <p className={cx("muted")}>人工标记不改变成员原文或任务结果。</p>
      </div>
    </Modal>
  );
}
