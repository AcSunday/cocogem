import { useState } from "react";
import type { Artifact, Source } from "../api/client";
import { t, useLanguage } from "../lib/i18n";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import panels from "../styles/panels.module.css";
import {
  artifactTypeLabel,
  findingStatusLabel,
  formatDateTime,
  severityLabel,
} from "../lib/presentation";
import { Icon } from "./Icon";
import { DataChart } from "./DataChart";
import { Markdown } from "./Markdown";
import { Modal } from "./Modal";

const cx = scoped(base, controls, layout, panels);

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
  useLanguage();
  const [copied, setCopied] = useState(false);
  const title = (id: string) =>
    sources.find((source) => source.id === id)?.title ?? id.slice(0, 8);
  const sourceButtons = (ids: string[] | null | undefined) =>
    (ids ?? []).length > 0 && (
      <div className={cx("source-links")}>
        {(ids ?? []).map((id) => (
          <button key={id} className={cx("small")} onClick={() => onSource(id)}>
            <Icon name="file" size={14} />
            {t(`打开来源：${title(id)}`, `Open source: ${title(id)}`)}
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
            {artifact.feedback === "accepted"
              ? t("已接受", "Accepted")
              : t("接受", "Accept")}
          </button>
          <button
            aria-pressed={artifact.feedback === "dismissed"}
            onClick={() => onFeedback("dismissed")}
          >
            {artifact.feedback === "dismissed"
              ? t("已搁置", "Dismissed")
              : t("搁置", "Dismiss")}
          </button>
          <button onClick={onReference}>
            <Icon name="reply" size={14} />
            {t("引用后追问", "Quote & follow up")}
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
            {copied ? t("已复制", "Copied") : t("复制", "Copy")}
          </button>
          <a
            className={cx("button")}
            href={`/api/v1/artifacts/${artifact.id}/versions/${artifact.version}/export`}
          >
            <Icon name="download" size={14} />
            {t("下载 Markdown", "Download Markdown")}
          </a>
        </div>
        {!!artifact.files?.length && (
          <section
            className={cx("reader-section")}
            aria-label={t("交付文件", "Delivered files")}
          >
            <h3>{t("交付文件", "Delivered files")}</h3>
            <div className={cx("artifact-actions")}>
              {artifact.files.map((file) => (
                <a
                  key={file.source_id}
                  className={cx("button")}
                  href={`/api/v1/artifacts/${artifact.id}/versions/${artifact.version}/files/${file.source_id}`}
                  download={file.name}
                >
                  <Icon name="download" size={14} />
                  {t(
                    `下载 ${file.name}（${Math.ceil(file.size / 1024)} KiB）`,
                    `Download ${file.name} (${Math.ceil(file.size / 1024)} KiB)`,
                  )}
                </a>
              ))}
            </div>
          </section>
        )}
        {artifact.type === "data_chart" ? (
          <DataChart source={artifact.body} />
        ) : (
          <Markdown text={artifact.body} />
        )}
        {(artifact.findings ?? []).length > 0 && (
          <section className={cx("reader-section")}>
            <h3>{t("结构化发现", "Structured findings")}</h3>
            {(artifact.findings ?? []).map((finding, index) => (
              <article className={cx("finding")} key={index}>
                <h4>
                  {finding.title}
                  <span className={cx("role-chip")}>
                    {findingStatusLabel(finding.status)}
                  </span>
                  {finding.severity && (
                    <span className={cx("role-chip")}>
                      {t("影响：", "Impact: ")}
                      {severityLabel(finding.severity)}
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
            <h3>{t("限制", "Limitations")}</h3>
            <ul>
              {(artifact.limitations ?? []).map((limit, index) => (
                <li key={index}>{limit}</li>
              ))}
            </ul>
          </section>
        )}
        <section className={cx("reader-section")}>
          <h3>{t("捕获来源", "Captured sources")}</h3>
          {sourceButtons(artifact.source_ids) || (
            <p className={cx("muted")}>
              {t(
                "本产物没有引用来源。",
                "This deliverable does not reference any sources.",
              )}
            </p>
          )}
        </section>
        <p className={cx("muted")}>
          {t(
            "人工标记不改变成员原文或任务结果。",
            "Manual labels do not alter member replies or task outcomes.",
          )}
        </p>
      </div>
    </Modal>
  );
}
