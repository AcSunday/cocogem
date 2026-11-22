import { useCallback, useEffect, useMemo, useState } from "react";
import {
  usageReport,
  type UsageBreakdown,
  type UsageBucket,
  type UsageRange,
  type UsageReport,
} from "../api/client";
import { t, useLanguage } from "../lib/i18n";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import usageStyles from "../styles/usage.module.css";
import { Icon } from "./Icon";

const cx = scoped(base, controls, usageStyles);

type BreakdownTab = "model" | "executor" | "agent" | "workspace";

function formatTokens(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0";
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(2)}B`;
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 10_000) return `${(n / 1_000).toFixed(1)}K`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(2)}K`;
  return n.toLocaleString();
}

function formatExactTokens(n: number): string {
  if (!Number.isFinite(n)) return "0";
  return Math.round(n).toLocaleString();
}

function formatUSD(usd: number): string {
  if (!Number.isFinite(usd) || usd < 0) return "$0.00";
  if (usd === 0) return "$0.00";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  if (usd < 1) return `$${usd.toFixed(3)}`;
  return `$${usd.toFixed(2)}`;
}

function formatPercent(part: number, whole: number): string {
  if (whole <= 0 || part <= 0) return "0%";
  const pct = (part / whole) * 100;
  return pct >= 10 ? `${pct.toFixed(0)}%` : `${pct.toFixed(1)}%`;
}

function formatAttemptTime(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const mm = String(d.getMonth() + 1).padStart(2, "0");
  const dd = String(d.getDate()).padStart(2, "0");
  const hh = String(d.getHours()).padStart(2, "0");
  const mi = String(d.getMinutes()).padStart(2, "0");
  return `${mm}-${dd} ${hh}:${mi}`;
}

function executorLabel(raw: string): string {
  switch (raw) {
    case "claude":
      return "Claude";
    case "codex":
      return "Codex";
    case "gemini":
      return "Gemini";
    case "pi":
      return "Pi";
    case "opencode":
      return "OpenCode";
    case "":
    case "unknown":
      return t("未知 CLI", "Unknown CLI");
    default:
      return raw;
  }
}

function statusPill(status: string): { label: string; cls: string } {
  switch (status) {
    case "succeeded":
      return { label: t("已完成", "Succeeded"), cls: "usage-status-ok" };
    case "failed":
      return { label: t("失败", "Failed"), cls: "usage-status-fail" };
    case "canceled":
      return { label: t("已停止", "Canceled"), cls: "usage-status-warn" };
    case "interrupted":
      return { label: t("已中断", "Interrupted"), cls: "usage-status-warn" };
    case "running":
      return { label: t("运行中", "Running"), cls: "usage-status-neutral" };
    case "blocked":
      return { label: t("已阻塞", "Blocked"), cls: "usage-status-warn" };
    default:
      return { label: status || "—", cls: "usage-status-neutral" };
  }
}

function UsageChart({
  series,
  granularity,
}: {
  series: UsageBucket[];
  granularity: string;
}) {
  const [hovered, setHovered] = useState<number | null>(null);
  const maxTotal = useMemo(
    () => Math.max(1, ...series.map((b) => b.total_tokens)),
    [series],
  );
  const activeIndex =
    hovered !== null && hovered < series.length ? hovered : null;
  const activeBucket = activeIndex !== null ? series[activeIndex] : null;

  const width = 960;
  const height = 220;
  const padLeft = 54;
  const padRight = 16;
  const padTop = 14;
  const padBottom = 26;
  const plotW = width - padLeft - padRight;
  const plotH = height - padTop - padBottom;
  const count = Math.max(series.length, 1);
  const slotW = plotW / count;
  const barW = Math.max(5, Math.min(36, slotW * 0.56));
  const labelEvery =
    granularity === "hour"
      ? 3
      : count <= 10
        ? 1
        : count <= 20
          ? 2
          : Math.ceil(count / 8);

  const ticks = [0, 0.5, 1];

  return (
    <>
      <div className={cx("usage-chart-readout")} aria-live="polite">
        {activeBucket ? (
          <>
            <strong>{activeBucket.key}</strong>
            <span>
              {t("总计", "Total")}:{" "}
              <strong>{formatExactTokens(activeBucket.total_tokens)}</strong>
            </span>
            <span>
              {t("新输入", "Input")}: {formatExactTokens(activeBucket.input_tokens)}
            </span>
            <span>
              {t("缓存读取", "Cache read")}:{" "}
              {formatExactTokens(activeBucket.cache_read_tokens)}
            </span>
            {activeBucket.cache_creation_tokens > 0 && (
              <span>
                {t("缓存写入", "Cache write")}:{" "}
                {formatExactTokens(activeBucket.cache_creation_tokens)}
              </span>
            )}
            <span>
              {t("输出", "Output")}: {formatExactTokens(activeBucket.output_tokens)}
            </span>
            <span>
              {activeBucket.attempts} {t("次执行", "attempts")}
            </span>
            {activeBucket.reported_cost_usd > 0 && (
              <span>
                {t("实报费用", "Reported")}:{" "}
                <strong>{formatUSD(activeBucket.reported_cost_usd)}</strong>
              </span>
            )}
          </>
        ) : (
          <span>
            {t(
              "悬停或聚焦柱状条查看该时段精确 Token 明细",
              "Hover or focus a bar to inspect exact tokens for that period",
            )}
          </span>
        )}
      </div>
      <div className={cx("usage-chart-wrap")}>
        <svg
          className={cx("usage-chart-svg")}
          viewBox={`0 0 ${width} ${height}`}
          role="img"
          aria-label={t("Tokens 消耗趋势图", "Token usage trend chart")}
        >
          {ticks.map((ratio) => {
            const y = padTop + plotH * (1 - ratio);
            const value = Math.round(maxTotal * ratio);
            return (
              <g key={ratio}>
                <line
                  className={cx("usage-grid-line")}
                  x1={padLeft}
                  x2={width - padRight}
                  y1={y}
                  y2={y}
                />
                <text
                  className={cx("usage-axis-label")}
                  x={padLeft - 8}
                  y={y + 4}
                  textAnchor="end"
                >
                  {formatTokens(value)}
                </text>
              </g>
            );
          })}
          {series.map((bucket, idx) => {
            const xSlot = padLeft + idx * slotW;
            const xBar = xSlot + (slotW - barW) / 2;
            const baseBottom = padTop + plotH;
            const hInput = (bucket.input_tokens / maxTotal) * plotH;
            const hCacheRead = (bucket.cache_read_tokens / maxTotal) * plotH;
            const hCacheWrite =
              (bucket.cache_creation_tokens / maxTotal) * plotH;
            const hOutput = (bucket.output_tokens / maxTotal) * plotH;
            let cursorY = baseBottom;
            const yInput = (cursorY -= hInput);
            const yCacheRead = (cursorY -= hCacheRead);
            const yCacheWrite = (cursorY -= hCacheWrite);
            const yOutput = cursorY - hOutput;
            const showLabel =
              idx % labelEvery === 0 || idx === series.length - 1;
            const isActive = activeIndex === idx;
            return (
              <g
                key={bucket.key}
                tabIndex={0}
                onMouseEnter={() => setHovered(idx)}
                onMouseLeave={() => setHovered(null)}
                onFocus={() => setHovered(idx)}
                onBlur={() => setHovered(null)}
              >
                <rect
                  className={cx(
                    isActive ? "usage-bar-hit-active" : "usage-bar-hit",
                  )}
                  x={xSlot}
                  y={padTop}
                  width={slotW}
                  height={plotH}
                  rx={4}
                />
                {hInput > 0 && (
                  <rect
                    className={cx("usage-seg-input")}
                    x={xBar}
                    y={yInput}
                    width={barW}
                    height={Math.max(1, hInput)}
                  />
                )}
                {hCacheRead > 0 && (
                  <rect
                    className={cx("usage-seg-cache-read")}
                    x={xBar}
                    y={yCacheRead}
                    width={barW}
                    height={Math.max(1, hCacheRead)}
                  />
                )}
                {hCacheWrite > 0 && (
                  <rect
                    className={cx("usage-seg-cache-write")}
                    x={xBar}
                    y={yCacheWrite}
                    width={barW}
                    height={Math.max(1, hCacheWrite)}
                  />
                )}
                {hOutput > 0 && (
                  <rect
                    className={cx("usage-seg-output")}
                    x={xBar}
                    y={yOutput}
                    width={barW}
                    height={Math.max(1, hOutput)}
                  />
                )}
                {showLabel && (
                  <text
                    className={cx("usage-axis-label")}
                    x={xSlot + slotW / 2}
                    y={height - 6}
                    textAnchor="middle"
                  >
                    {bucket.label}
                  </text>
                )}
              </g>
            );
          })}
        </svg>
      </div>
    </>
  );
}

/** Global Tokens & natively reported cost dashboard reachable from the far-left rail. */
export function UsagePage({
  onOpenThread,
  onOpenTask,
}: {
  onOpenThread: (workspaceID: string, threadID: string) => void;
  onOpenTask: (workspaceID: string, taskID: string) => void;
}) {
  useLanguage();
  const [range, setRange] = useState<UsageRange>("7d");
  const [workspaceID, setWorkspaceID] = useState("");
  const [executor, setExecutor] = useState("");
  const [agentID, setAgentID] = useState("");
  const [tab, setTab] = useState<BreakdownTab>("model");
  const [report, setReport] = useState<UsageReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await usageReport({
        range,
        workspaceID: workspaceID || undefined,
        executor: executor || undefined,
        agentID: agentID || undefined,
      });
      setReport(data);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setLoading(false);
    }
  }, [range, workspaceID, executor, agentID]);

  useEffect(() => {
    void load();
  }, [load]);

  const hasFilter = !!(workspaceID || executor || agentID);
  const totals = report?.totals;
  const totalInput =
    (totals?.input_tokens ?? 0) +
    (totals?.cache_read_tokens ?? 0) +
    (totals?.cache_creation_tokens ?? 0);

  const breakdownRows: UsageBreakdown[] = useMemo(() => {
    if (!report) return [];
    switch (tab) {
      case "model":
        return report.by_model;
      case "executor":
        return report.by_executor;
      case "agent":
        return report.by_agent;
      case "workspace":
        return report.by_workspace;
    }
  }, [report, tab]);

  const workspaceOptions = useMemo(
    () =>
      Object.entries(report?.workspaces ?? {}).sort((a, b) =>
        a[1].localeCompare(b[1]),
      ),
    [report],
  );

  const memberOptions = useMemo(
    () =>
      Object.entries(report?.members ?? {}).sort((a, b) =>
        a[1].localeCompare(b[1]),
      ),
    [report],
  );

  const ranges: { value: UsageRange; label: string }[] = [
    { value: "today", label: t("今天", "Today") },
    { value: "7d", label: t("近 7 天", "7d") },
    { value: "30d", label: t("近 30 天", "30d") },
    { value: "all", label: t("全部", "All") },
  ];

  return (
    <section
      className={cx("usage-page")}
      aria-label={t("Tokens 用量统计", "Token usage dashboard")}
    >
      <div className={cx("usage-head")}>
        <p className={cx("usage-subtitle")}>
          {t(
            "统计各项目、成员与 CLI 的真实 Token 消耗（含缓存命中与失败尝试）；仅当 CLI 原生回报费用时展示金额。",
            "Tracks real token usage across projects, members and CLIs (including cache hits and failed attempts); USD cost is shown only when reported natively by a CLI.",
          )}
        </p>
        <div className={cx("usage-controls")}>
          <div
            className={cx("usage-range-group")}
            role="group"
            aria-label={t("时间范围", "Time range")}
          >
            {ranges.map((item) => (
              <button
                key={item.value}
                type="button"
                className={cx("usage-range-btn")}
                aria-pressed={range === item.value}
                onClick={() => setRange(item.value)}
              >
                {item.label}
              </button>
            ))}
          </div>
          <button
            type="button"
            className={cx("ghost small")}
            onClick={() => void load()}
            disabled={loading}
          >
            <Icon name="refresh" size={14} /> {t("刷新", "Refresh")}
          </button>
        </div>
      </div>

      <div className={cx("usage-filters")}>
        <select
          className={cx("usage-select")}
          aria-label={t("按项目筛选", "Filter by project")}
          value={workspaceID}
          onChange={(e) => setWorkspaceID(e.target.value)}
        >
          <option value="">{t("全部项目", "All projects")}</option>
          {workspaceOptions.map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
        </select>
        <select
          className={cx("usage-select")}
          aria-label={t("按 CLI 筛选", "Filter by CLI")}
          value={executor}
          onChange={(e) => setExecutor(e.target.value)}
        >
          <option value="">{t("全部 CLI", "All CLIs")}</option>
          {(report?.executors ?? []).map((execName) => (
            <option key={execName} value={execName}>
              {executorLabel(execName)}
            </option>
          ))}
        </select>
        <select
          className={cx("usage-select")}
          aria-label={t("按成员筛选", "Filter by member")}
          value={agentID}
          onChange={(e) => setAgentID(e.target.value)}
        >
          <option value="">{t("全部成员", "All members")}</option>
          {memberOptions.map(([id, name]) => (
            <option key={id} value={id}>
              {name}
            </option>
          ))}
        </select>
        {hasFilter && (
          <button
            type="button"
            className={cx("ghost small")}
            onClick={() => {
              setWorkspaceID("");
              setExecutor("");
              setAgentID("");
            }}
          >
            {t("重置筛选", "Reset filters")}
          </button>
        )}
      </div>

      {error && (
        <div className={cx("usage-error")} role="alert">
          <span>{error}</span>
          <button
            type="button"
            className={cx("ghost small")}
            onClick={() => void load()}
          >
            {t("重试", "Retry")}
          </button>
        </div>
      )}

      {loading && !report ? (
        <div className={cx("usage-card")}>
          <div className={cx("usage-empty")}>
            {t("正在加载 Token 用量数据…", "Loading token usage…")}
          </div>
        </div>
      ) : report && totals ? (
        <>
          <div className={cx("usage-kpi-grid")}>
            <div className={cx("usage-kpi")}>
              <span className={cx("usage-kpi-label")}>
                {t("总 Tokens", "Total tokens")}
              </span>
              <strong
                className={cx("usage-kpi-value")}
                title={formatExactTokens(totals.total_tokens)}
              >
                {formatTokens(totals.total_tokens)}
              </strong>
              <div className={cx("usage-kpi-meta")}>
                <span>
                  {t("新输入", "Input")} {formatTokens(totals.input_tokens)}
                </span>
                <span>
                  {t("输出", "Output")} {formatTokens(totals.output_tokens)}
                </span>
              </div>
            </div>

            <div className={cx("usage-kpi")}>
              <span className={cx("usage-kpi-label")}>
                {t("缓存读取命中", "Cache read hits")}
              </span>
              <strong
                className={cx("usage-kpi-value")}
                title={formatExactTokens(totals.cache_read_tokens)}
              >
                {formatTokens(totals.cache_read_tokens)}
              </strong>
              <div className={cx("usage-kpi-meta")}>
                <span>
                  {t("命中率", "Hit rate")}{" "}
                  {totalInput > 0
                    ? formatPercent(totals.cache_read_tokens, totalInput)
                    : "—"}
                </span>
                {totals.cache_creation_tokens > 0 && (
                  <span>
                    {t("写入", "Write")}{" "}
                    {formatTokens(totals.cache_creation_tokens)}
                  </span>
                )}
              </div>
            </div>

            <div className={cx("usage-kpi")}>
              <span className={cx("usage-kpi-label")}>
                {t("执行尝试与完整度", "Attempts & coverage")}
              </span>
              <strong className={cx("usage-kpi-value")}>
                {totals.attempts_total.toLocaleString()}
              </strong>
              <div className={cx("usage-kpi-meta")}>
                <span>
                  {t("已上报", "Reported")} {totals.attempts_known}/
                  {totals.attempts_total}
                </span>
                {totals.attempts_failed > 0 && (
                  <span>
                    {t("失败/停止", "Failed/stopped")} {totals.attempts_failed}
                  </span>
                )}
                {totals.attempts_unknown > 0 && (
                  <span className={cx("usage-warn-pill")}>
                    {totals.attempts_unknown}{" "}
                    {t("次未上报", "unreported")}
                  </span>
                )}
              </div>
            </div>

            <div className={cx("usage-kpi")}>
              <span className={cx("usage-kpi-label")}>
                {t("CLI 实报费用", "CLI reported cost")}
              </span>
              <strong className={cx("usage-kpi-value")}>
                {totals.attempts_with_cost > 0
                  ? formatUSD(totals.reported_cost_usd)
                  : "—"}
              </strong>
              <div className={cx("usage-kpi-meta")}>
                {totals.attempts_with_cost > 0 ? (
                  <span>
                    {t("来自", "From")} {totals.attempts_with_cost}/
                    {totals.attempts_total}{" "}
                    {t("次原生回报", "native reports")}
                  </span>
                ) : (
                  <span>
                    {t(
                      "当前记录无原生金额（仅统计真实 Tokens）",
                      "No native USD reported (real tokens only)",
                    )}
                  </span>
                )}
              </div>
            </div>
          </div>

          {totals.attempts_total === 0 ? (
            <div className={cx("usage-card")}>
              <div className={cx("usage-empty")}>
                <strong>
                  {hasFilter
                    ? t(
                        "当前筛选条件下暂无执行记录",
                        "No attempts match the current filters",
                      )
                    : t("当前时间范围暂无 Token 消耗记录", "No token usage yet in this range")}
                </strong>
                <span>
                  {hasFilter
                    ? t(
                        "可以切换时间范围或重置筛选条件查看其他记录。",
                        "Try switching the time range or resetting filters.",
                      )
                    : t(
                        "在任意项目中发起协作会话或任务后，真实 Token 消耗与缓存命中会自动记录在这里。",
                        "Real token counts and prompt cache hits will appear here automatically once you run conversations or tasks.",
                      )}
                </span>
              </div>
            </div>
          ) : (
            <>
              <div className={cx("usage-card")}>
                <div className={cx("usage-card-head")}>
                  <h2 className={cx("usage-card-title")}>
                    {report.granularity === "hour"
                      ? t("今日分时消耗趋势", "Today hourly trend")
                      : t("每日消耗趋势", "Daily token trend")}
                  </h2>
                  <div className={cx("usage-legend")}>
                    <span className={cx("usage-legend-item")}>
                      <i className={cx("usage-swatch usage-swatch-input")} />
                      {t("新输入", "Input")}
                    </span>
                    <span className={cx("usage-legend-item")}>
                      <i
                        className={cx("usage-swatch usage-swatch-cache-read")}
                      />
                      {t("缓存读取", "Cache read")}
                    </span>
                    <span className={cx("usage-legend-item")}>
                      <i
                        className={cx("usage-swatch usage-swatch-cache-write")}
                      />
                      {t("缓存写入", "Cache write")}
                    </span>
                    <span className={cx("usage-legend-item")}>
                      <i className={cx("usage-swatch usage-swatch-output")} />
                      {t("输出", "Output")}
                    </span>
                  </div>
                </div>
                <UsageChart
                  series={report.series}
                  granularity={report.granularity}
                />
              </div>

              <div className={cx("usage-two-col")}>
                <div className={cx("usage-card")}>
                  <div className={cx("usage-card-head")}>
                    <h2 className={cx("usage-card-title")}>
                      {t("维度分布", "Breakdown")}
                    </h2>
                    <div
                      className={cx("usage-tabs")}
                      role="tablist"
                      aria-label={t("分布维度", "Breakdown dimension")}
                    >
                      {(
                        [
                          ["model", t("按模型", "Model")],
                          ["executor", t("按 CLI", "CLI")],
                          ["agent", t("按成员", "Member")],
                          ["workspace", t("按项目", "Project")],
                        ] as [BreakdownTab, string][]
                      ).map(([key, label]) => (
                        <button
                          key={key}
                          type="button"
                          role="tab"
                          className={cx("usage-tab")}
                          aria-selected={tab === key}
                          onClick={() => setTab(key)}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                  </div>
                  <div className={cx("usage-breakdown-list")}>
                    {breakdownRows.map((row) => {
                      const share =
                        totals.total_tokens > 0
                          ? (row.total_tokens / totals.total_tokens) * 100
                          : 0;
                      const rowTotal = Math.max(1, row.total_tokens);
                      const wIn = (row.input_tokens / rowTotal) * share;
                      const wCR = (row.cache_read_tokens / rowTotal) * share;
                      const wCW =
                        (row.cache_creation_tokens / rowTotal) * share;
                      const wOut = (row.output_tokens / rowTotal) * share;
                      const displayLabel =
                        tab === "executor"
                          ? executorLabel(row.label)
                          : row.label;
                      return (
                        <div key={row.key} className={cx("usage-breakdown-row")}>
                          <div className={cx("usage-breakdown-top")}>
                            <span className={cx("usage-breakdown-name")}>
                              <span
                                className={cx("usage-breakdown-label")}
                                title={displayLabel}
                              >
                                {displayLabel}
                              </span>
                              {row.secondary && (
                                <span className={cx("usage-badge")}>
                                  {executorLabel(row.secondary)}
                                </span>
                              )}
                            </span>
                            <span className={cx("usage-breakdown-val")}>
                              {formatTokens(row.total_tokens)}
                              <span className={cx("usage-pct")}>
                                {formatPercent(
                                  row.total_tokens,
                                  totals.total_tokens,
                                )}
                              </span>
                            </span>
                          </div>
                          <div className={cx("usage-bar-track")}>
                            {wIn > 0 && (
                              <span
                                className={cx(
                                  "usage-bar-seg usage-swatch-input",
                                )}
                                style={{ width: `${wIn}%` }}
                              />
                            )}
                            {wCR > 0 && (
                              <span
                                className={cx(
                                  "usage-bar-seg usage-swatch-cache-read",
                                )}
                                style={{ width: `${wCR}%` }}
                              />
                            )}
                            {wCW > 0 && (
                              <span
                                className={cx(
                                  "usage-bar-seg usage-swatch-cache-write",
                                )}
                                style={{ width: `${wCW}%` }}
                              />
                            )}
                            {wOut > 0 && (
                              <span
                                className={cx(
                                  "usage-bar-seg usage-swatch-output",
                                )}
                                style={{ width: `${wOut}%` }}
                              />
                            )}
                          </div>
                          <div className={cx("usage-breakdown-meta")}>
                            <span>
                              {t("输入", "In")} {formatTokens(row.input_tokens)}
                            </span>
                            <span>
                              {t("缓存", "Cache")}{" "}
                              {formatTokens(row.cache_read_tokens)}
                            </span>
                            <span>
                              {t("输出", "Out")} {formatTokens(row.output_tokens)}
                            </span>
                            <span>
                              {row.attempts} {t("次", "runs")}
                            </span>
                            {row.attempts_with_cost > 0 && (
                              <span>{formatUSD(row.reported_cost_usd)}</span>
                            )}
                          </div>
                        </div>
                      );
                    })}
                  </div>
                </div>

                <div className={cx("usage-card")}>
                  <div className={cx("usage-card-head")}>
                    <h2 className={cx("usage-card-title")}>
                      {t("高消耗会话与任务", "Top conversations & tasks")}
                    </h2>
                  </div>
                  <div className={cx("usage-target-list")}>
                    {report.top_targets.map((target) => {
                      const title =
                        target.title ||
                        (target.task_id
                          ? t("未命名任务", "Untitled task")
                          : t("未命名会话", "Untitled conversation"));
                      const key = target.task_id
                        ? `task:${target.task_id}`
                        : `thread:${target.thread_id}`;
                      return (
                        <button
                          key={key}
                          type="button"
                          className={cx("usage-target-item")}
                          disabled={target.deleted}
                          onClick={() => {
                            if (target.deleted) return;
                            if (target.task_id) {
                              onOpenTask(target.workspace_id, target.task_id);
                            } else {
                              onOpenThread(target.workspace_id, target.thread_id);
                            }
                          }}
                        >
                          <div className={cx("usage-target-top")}>
                            <span
                              className={cx("usage-target-title")}
                              title={title}
                            >
                              {title}
                            </span>
                            <span className={cx("usage-breakdown-val")}>
                              {formatTokens(target.total_tokens)}
                            </span>
                          </div>
                          <div className={cx("usage-target-meta")}>
                            <span className={cx("usage-badge")}>
                              {target.workspace_name}
                            </span>
                            <span className={cx("usage-badge")}>
                              {target.task_id
                                ? t("任务", "Task")
                                : t("会话", "Thread")}
                            </span>
                            {target.deleted && (
                              <span className={cx("usage-warn-pill")}>
                                {t("已在回收站", "In trash")}
                              </span>
                            )}
                            <span>
                              {target.attempts} {t("次", "runs")}
                            </span>
                            {target.cache_read_tokens > 0 && (
                              <span>
                                {t("缓存", "Cache")}{" "}
                                {formatTokens(target.cache_read_tokens)}
                              </span>
                            )}
                            {target.attempts_with_cost > 0 && (
                              <span>{formatUSD(target.reported_cost_usd)}</span>
                            )}
                          </div>
                        </button>
                      );
                    })}
                  </div>
                </div>
              </div>

              <div className={cx("usage-card")}>
                <div className={cx("usage-card-head")}>
                  <h2 className={cx("usage-card-title")}>
                    {t("最近执行明细", "Recent attempts")}
                  </h2>
                  <span className={cx("usage-muted")}>
                    {t(
                      `展示最近 ${report.recent.length} 条记录`,
                      `Showing latest ${report.recent.length} attempts`,
                    )}
                  </span>
                </div>
                <div className={cx("usage-table-wrap")}>
                  <table className={cx("usage-table")}>
                    <thead>
                      <tr>
                        <th>{t("时间", "Time")}</th>
                        <th>{t("项目 / 会话", "Project / Target")}</th>
                        <th>{t("成员", "Member")}</th>
                        <th>{t("CLI / 模型", "CLI / Model")}</th>
                        <th className={cx("usage-num")}>
                          {t("新输入", "Input")}
                        </th>
                        <th className={cx("usage-num")}>
                          {t("缓存读", "Cache")}
                        </th>
                        <th className={cx("usage-num")}>
                          {t("输出", "Output")}
                        </th>
                        <th className={cx("usage-num")}>
                          {t("实报费用", "Cost")}
                        </th>
                        <th>{t("状态", "Status")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {report.recent.map((entry) => {
                        const pill = statusPill(entry.status);
                        const title =
                          entry.title ||
                          (entry.task_id
                            ? t("未命名任务", "Untitled task")
                            : t("未命名会话", "Untitled conversation"));
                        const unreported =
                          !entry.usage_known && entry.total_tokens === 0;
                        return (
                          <tr key={entry.attempt_id}>
                            <td className={cx("usage-time")}>
                              {formatAttemptTime(entry.started_at)}
                            </td>
                            <td>
                              {entry.deleted ? (
                                <span className={cx("usage-muted")}>
                                  {entry.workspace_name} · {title} (
                                  {t("回收站", "Trash")})
                                </span>
                              ) : (
                                <button
                                  type="button"
                                  className={cx("usage-link-btn")}
                                  onClick={() =>
                                    entry.task_id
                                      ? onOpenTask(
                                          entry.workspace_id,
                                          entry.task_id,
                                        )
                                      : onOpenThread(
                                          entry.workspace_id,
                                          entry.thread_id,
                                        )
                                  }
                                >
                                  {entry.workspace_name} · {title}
                                </button>
                              )}
                            </td>
                            <td>{entry.agent_name || "—"}</td>
                            <td>
                              <span className={cx("usage-badge")}>
                                {executorLabel(entry.executor ?? "")}
                              </span>{" "}
                              <span className={cx("usage-muted")}>
                                {entry.model || "—"}
                              </span>
                            </td>
                            {unreported ? (
                              <td
                                colSpan={3}
                                className={cx("usage-num usage-muted")}
                              >
                                {t("未上报用量", "Unreported")}
                              </td>
                            ) : (
                              <>
                                <td className={cx("usage-num")}>
                                  {formatExactTokens(entry.input_tokens)}
                                </td>
                                <td className={cx("usage-num")}>
                                  {entry.cache_read_tokens > 0
                                    ? formatExactTokens(entry.cache_read_tokens)
                                    : "—"}
                                </td>
                                <td className={cx("usage-num")}>
                                  {formatExactTokens(entry.output_tokens)}
                                </td>
                              </>
                            )}
                            <td className={cx("usage-num")}>
                              {entry.cost_known
                                ? formatUSD(entry.reported_cost_usd ?? 0)
                                : "—"}
                            </td>
                            <td>
                              <span className={cx(`usage-status ${pill.cls}`)}>
                                {pill.label}
                              </span>
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              </div>
            </>
          )}
        </>
      ) : null}
    </section>
  );
}
