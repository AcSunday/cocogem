import type { Artifact, Source } from "../api/client";
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
const tabs: [PanelTab, string][] = [
  ["sources", "来源"],
  ["artifacts", "产物"],
];

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
  artifacts: Artifact[];
  onArtifact: (artifact: Artifact) => void;
}) {
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
      aria-label="协作资料"
    >
      <div className={cx("panel-head")}>
        <div className={cx("panel-tabs")} role="tablist" aria-label="协作资料">
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
          aria-label="关闭协作面板"
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
                成员读取资料后，来源会出现在这里。
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
                正在读取来源正文…
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
                    ` · 第 ${source.start_line}–${source.end_line} 行`}
                  {source.kind === "test" &&
                    ` · exit ${source.timed_out ? "超时" : (source.exit_code ?? 0)}`}
                </small>
                <pre className={cx("pre")} tabIndex={0}>
                  {source.content}
                </pre>
                {source.truncated && (
                  <p className={cx("muted")}>内容已截断。</p>
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
                协作生成的纪要和报告会出现在这里。
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
