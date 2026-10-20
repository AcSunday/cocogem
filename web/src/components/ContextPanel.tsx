import type { Agent, Artifact, Event, Run, Source } from "../api/client";
import { statusText } from "../api/client";
import { cx } from "../styles/classes";
import {
  artifactTypeLabel,
  formatDateTime,
  latestSources,
  sourceKindLabel,
} from "../lib/presentation";
import { Avatar } from "./Avatar";
import { Icon } from "./Icon";
import { ToolTimeline } from "./ToolTimeline";

export type PanelTab = "members" | "sources" | "artifacts" | "tools";
const tabs: [PanelTab, string][] = [
  ["members", "成员"],
  ["sources", "来源"],
  ["artifacts", "产物"],
  ["tools", "过程"],
];

export function ContextPanel({
  open,
  close,
  tab,
  setTab,
  agents,
  canManage,
  onManage,
  sources,
  source,
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
  agents: Agent[];
  canManage: boolean;
  onManage: () => void;
  sources: Source[];
  source?: Source;
  onSource: (source: Source | string) => void;
  artifacts: Artifact[];
  onArtifact: (artifact: Artifact) => void;
  runs: Run[];
  inspectedRun?: Run;
  onInspect: (id: string) => void;
  events: Event[];
}) {
  const shownSources = latestSources(sources);
  const counts: Partial<Record<PanelTab, number>> = {
    sources: shownSources.length,
    artifacts: artifacts.length,
  };
  return (
    <aside
      className={cx(`context-panel ${open ? "open" : ""}`)}
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
                    ArrowRight: (index + 1) % 4,
                    ArrowLeft: (index + 3) % 4,
                    Home: 0,
                    End: 3,
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
          aria-label="关闭资料面板"
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
        {tab === "members" && (
          <>
            {agents.map((agent, index) => (
              <article className={cx("member-card")} key={agent.id}>
                <Avatar name={agent.display_name} index={index} />
                <div>
                  <strong>{agent.display_name}</strong>
                  <small>
                    @{agent.handle}
                    {agent.disabled && " · 已停用"}
                  </small>
                  {agent.role_description && <p>{agent.role_description}</p>}
                  <span className={cx("model-label")}>
                    {agent.executor ? `${agent.executor} · ` : ""}
                    {agent.model === "default"
                      ? "CLI 默认模型"
                      : agent.model === "demo"
                        ? "离线演示模型"
                        : agent.model}
                  </span>
                </div>
              </article>
            ))}
            {!agents.length && (
              <p className={cx("panel-empty")}>创建工作空间后会有默认成员。</p>
            )}
            <button
              className={cx("panel-action")}
              onClick={onManage}
              disabled={!canManage}
            >
              <Icon name="users" size={14} />
              管理成员与称呼
            </button>
          </>
        )}
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
                  `source-card ${source?.id === item.id ? "selected" : ""}`,
                )}
                key={item.id}
                aria-pressed={source?.id === item.id}
                onClick={() => onSource(item)}
              >
                <span className={cx("card-kicker")}>
                  <Icon
                    name={item.kind === "file" ? "file" : "globe"}
                    size={12}
                  />
                  {sourceKindLabel(item)}
                </span>
                <strong>{item.title}</strong>
                <small>{item.path || item.url}</small>
              </button>
            ))}
            {source && (
              <section
                data-testid="source-detail"
                className={cx("source-detail")}
              >
                <h3>{source.title}</h3>
                <small>
                  {formatDateTime(source.captured_at)}
                  {source.path &&
                    ` · 第 ${source.start_line}–${source.end_line} 行`}
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
              <button
                className={cx("artifact-card")}
                key={`${item.id}:${item.version}`}
                onClick={() => onArtifact(item)}
              >
                <span className={cx("card-kicker")}>
                  <Icon name="report" size={12} />
                  {artifactTypeLabel(item.type)} · v{item.version}
                </span>
                <strong>{item.title}</strong>
                <small>
                  {item.feedback === "accepted"
                    ? "已接受"
                    : item.feedback === "dismissed"
                      ? "已搁置"
                      : "待查看"}
                </small>
              </button>
            ))}
          </>
        )}
        {tab === "tools" && (
          <>
            {runs.length > 1 && (
              <label className={cx("panel-select")}>
                历史协作
                <select
                  value={inspectedRun?.id ?? ""}
                  onChange={(event) => onInspect(event.target.value)}
                >
                  {runs.map((run, index) => (
                    <option key={run.id} value={run.id}>
                      第 {index + 1} 次 · {statusText[run.status]}
                    </option>
                  ))}
                </select>
              </label>
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
            />
          </>
        )}
      </div>
    </aside>
  );
}
