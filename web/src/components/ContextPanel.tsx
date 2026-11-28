import {
  sourceOriginalImageURL,
  type Artifact,
  type Source,
} from "../api/client";
import {
  isImageMediaOrPath,
  type FileRef,
  type ImagePreview,
} from "../lib/fileRefs";
import { t, useLanguage } from "../lib/i18n";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import panels from "../styles/panels.module.css";
import settings from "../styles/settings.module.css";
import {
  formatDateTime,
  latestSources,
  sourceKindLabel,
} from "../lib/presentation";
import { ArtifactCard } from "./ArtifactCard";
import { Icon } from "./Icon";

const cx = scoped(base, controls, layout, panels, settings);

export type PanelTab = "sources" | "artifacts";

/**
 * Sources and artifacts only. Tool activity lives in the conversation body
 * (D08), so a stale "tools" selection falls back to sources instead of leaving
 * an empty panel.
 */
export function ContextPanel({
  open,
  close,
  tab,
  setTab,
  sources,
  source,
  sourceID,
  sourceLoading,
  onSource,
  onOpenFile,
  onOpenImage,
  artifacts,
  onArtifact,
}: {
  open: boolean;
  close: () => void;
  tab: PanelTab;
  setTab: (tab: PanelTab) => void;
  sources: Source[];
  source?: Source;
  sourceID?: string;
  sourceLoading?: boolean;
  onSource: (source: Source | string) => void;
  onOpenFile?: (ref: FileRef) => void;
  onOpenImage?: (preview: ImagePreview) => void;
  artifacts: Artifact[];
  onArtifact: (artifact: Artifact) => void;
}) {
  useLanguage();
  const tabs: [PanelTab, string][] = [
    ["sources", t("来源", "Sources")],
    ["artifacts", t("产物", "Deliverables")],
  ];
  const activeTab: PanelTab = tab === "artifacts" ? "artifacts" : "sources";
  const shownSources = latestSources(sources);
  const counts: Partial<Record<PanelTab, number>> = {
    sources: shownSources.length,
    artifacts: artifacts.length,
  };
  return (
    <aside
      className={cx(`context-panel ${open ? "open" : ""}`)}
      id="collaboration-panel"
      inert={!open}
      aria-label={t("协作资料", "Collaboration resources")}
    >
      <div className={cx("panel-head")}>
        <div
          className={cx("panel-tabs")}
          role="tablist"
          aria-label={t("协作资料", "Collaboration resources")}
        >
          {tabs.map(([key, name], index) => (
            <button
              key={key}
              role="tab"
              id={`tab-${key}`}
              aria-controls="context-panel"
              aria-selected={activeTab === key}
              tabIndex={activeTab === key ? 0 : -1}
              onClick={() => setTab(key)}
              onKeyDown={(event) => {
                const next = (
                  {
                    ArrowRight: (index + 1) % tabs.length,
                    ArrowLeft: (index + tabs.length - 1) % tabs.length,
                    Home: 0,
                    End: tabs.length - 1,
                  } as Record<string, number>
                )[event.key];
                if (next === undefined) return;
                event.preventDefault();
                setTab(tabs[next][0]);
                document.getElementById(`tab-${tabs[next][0]}`)?.focus();
              }}
            >
              {name}
              {counts[key] ? (
                <span className={cx("count")}>{counts[key]}</span>
              ) : null}
            </button>
          ))}
        </div>
        <button
          className={cx("icon-button panel-close")}
          aria-label={t("关闭协作面板", "Close collaboration panel")}
          onClick={close}
        >
          <Icon name="close" size={18} />
        </button>
      </div>
      <div
        className={cx("panel-body")}
        id="context-panel"
        role="tabpanel"
        aria-labelledby={`tab-${activeTab}`}
      >
        {activeTab === "sources" && (
          <>
            {!shownSources.length && (
              <p className={cx("panel-empty")}>
                {t(
                  "成员读取资料后，来源会出现在这里。",
                  "Sources read by team members will appear here.",
                )}
              </p>
            )}
            {shownSources.map((item) => (
              <button
                className={cx(
                  `source-card ${sourceID === item.id ? "selected" : ""}`,
                )}
                key={item.id}
                aria-pressed={sourceID === item.id}
                onClick={() => onSource(item)}
              >
                <span className={cx("card-kicker")}>
                  <Icon
                    name={
                      item.kind === "file" ||
                      item.kind === "native_file" ||
                      item.kind === "diff" ||
                      item.kind === "test"
                        ? "file"
                        : "globe"
                    }
                    size={12}
                  />
                  {sourceKindLabel(item)}
                </span>
                <strong>{item.title}</strong>
                <small>{item.path || item.url}</small>
              </button>
            ))}
            {sourceLoading && (
              <p className={cx("muted")} role="status">
                {t("正在读取来源正文…", "Loading source content…")}
              </p>
            )}
            {source && (
              <section
                data-testid="source-detail"
                className={cx("source-detail")}
              >
                <h3>{source.title}</h3>
                <small>
                  {formatDateTime(source.captured_at)}
                  {source.path &&
                    source.end_line > 0 &&
                    t(
                      ` · 第 ${source.start_line}–${source.end_line} 行`,
                      ` · Lines ${source.start_line}–${source.end_line}`,
                    )}
                  {source.kind === "test" &&
                    ` · exit ${
                      source.timed_out
                        ? t("超时", "timed out")
                        : (source.exit_code ?? 0)
                    }`}
                </small>
                {(source.path || (isImageMediaOrPath(source.media_type || source.path || source.title, source.kind) && onOpenImage)) && (
                  <div className={cx("source-actions")}>
                    {source.path && onOpenFile && (
                      <button
                        type="button"
                        className={cx("ghost", "compact")}
                        onClick={() =>
                          onOpenFile({
                            path: source.path,
                            startLine: source.start_line > 0 ? source.start_line : undefined,
                            endLine: source.end_line > 0 ? source.end_line : undefined,
                            tab: source.kind === "diff" ? "diff" : "file",
                          })
                        }
                      >
                        {source.kind === "diff"
                          ? t("打开完整 Diff", "Open full diff")
                          : t("打开完整文件", "Open file")}
                      </button>
                    )}
                  </div>
                )}
                {isImageMediaOrPath(
                  source.media_type || source.path || source.title,
                  source.kind,
                ) ? (
                  <button
                    type="button"
                    className={cx("source-image-preview")}
                    onClick={() => {
                      const imgURL = sourceOriginalImageURL(source.id);
                      const imgTitle =
                        source.title || source.path || t("视觉证据", "Visual evidence");
                      onOpenImage?.({
                        src: imgURL,
                        title: imgTitle,
                        downloadUrl: imgURL,
                        sourceId: source.id,
                        path: source.path,
                      });
                    }}
                  >
                    <img
                      src={sourceOriginalImageURL(source.id)}
                      alt={source.title || source.path || "preview"}
                    />
                  </button>
                ) : (
                  <pre className={cx("pre")} tabIndex={0}>
                    {source.content}
                  </pre>
                )}
                {source.truncated && (
                  <p className={cx("muted")}>
                    {t("内容已截断。", "Content truncated.")}
                  </p>
                )}
                {source.sha256 && (
                  <small title={source.sha256}>
                    SHA-256 {source.sha256.slice(0, 12)}…
                  </small>
                )}
              </section>
            )}
          </>
        )}
        {tab === "artifacts" && (
          <>
            {!artifacts.length && (
              <p className={cx("panel-empty")}>
                {t(
                  "协作生成的纪要和报告会出现在这里。",
                  "Summaries and reports produced during collaboration will appear here.",
                )}
              </p>
            )}
            {artifacts.map((item) => (
              <ArtifactCard
                key={`${item.id}:${item.version}`}
                artifact={item}
                onOpen={onArtifact}
              />
            ))}
          </>
        )}
      </div>
    </aside>
  );
}
