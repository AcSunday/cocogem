import { Select } from "./Select";
import { useEffect, useState } from "react";
import type { Artifact, Event, Run, Source } from "../api/client";
import { EVENT_WINDOW, statusText } from "../api/client";
import { cx } from "../styles/classes";
import {
  formatDateTime,
  latestSources,
  sourceKindLabel,
} from "../lib/presentation";
import { ArtifactCard } from "./ArtifactCard";
import { EventHistory } from "./EventHistory";
import { Icon } from "./Icon";
import { ToolTimeline } from "./ToolTimeline";

export type PanelTab = "sources" | "artifacts" | "tools";
const tabs: [PanelTab, string][] = [
  ["sources", "来源"],
  ["artifacts", "产物"],
  ["tools", "过程"],
];

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
  runs,
  inspectedRun,
  onInspect,
  events,
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
  runs: Run[];
  inspectedRun?: Run;
  onInspect: (id: string) => void;
  events: Event[];
}) {
  const shownSources = latestSources(sources);
  const [historyAfter, setHistoryAfter] = useState<number | null>(null);
  useEffect(() => setHistoryAfter(null), [inspectedRun?.id]);
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
              aria-selected={tab === key}
              tabIndex={tab === key ? 0 : -1}
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
        aria-labelledby={`tab-${tab}`}
      >
        {tab === "sources" && (
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
        {tab === "tools" && (
          <>
            {runs.length > 1 && (
              <label className={cx("panel-select")}>
                历史协作
                <Select
                  value={inspectedRun?.id ?? ""}
                  onChange={(selected) => onInspect(selected)}
                >
                  {runs.map((run, index) => (
                    <option key={run.id} value={run.id}>
                      第 {index + 1} 次 · {statusText[run.status]}
                    </option>
                  ))}
                </Select>
              </label>
            )}
            {inspectedRun && (
              <div className={cx("history-actions")}>
                <button
                  type="button"
                  className={cx("ghost small")}
                  onClick={() => setHistoryAfter(0)}
                >
                  浏览历史事件
                </button>
                {events.length >= EVENT_WINDOW && (
                  <small className={cx("muted")}>
                    仅保留最近 {EVENT_WINDOW} 条事件
                  </small>
                )}
              </div>
            )}
            {historyAfter !== null && inspectedRun && (
              <EventHistory
                key={`${inspectedRun.id}:${historyAfter}`}
                runID={inspectedRun.id}
                after={historyAfter}
                agents={inspectedRun.agents ?? []}
                sources={sources}
                onSource={onSource}
                close={() => setHistoryAfter(null)}
              />
            )}
            {!events.length && (
              <p className={cx("panel-empty")}>
                工具调用和交接记录会在协作期间实时出现。
              </p>
            )}
            <ToolTimeline
              events={events}
              agents={inspectedRun?.agents ?? []}
              sources={sources}
              onSource={onSource}
              runStatus={inspectedRun?.status}
              onHistory={(after) => setHistoryAfter(after ?? 0)}
            />
          </>
        )}
      </div>
    </aside>
  );
}
