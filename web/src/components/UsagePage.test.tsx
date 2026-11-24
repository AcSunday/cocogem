import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { UsageReport } from "../api/client";
import { setLanguage } from "../lib/i18n";
import { UsagePage } from "./UsagePage";

const sampleReport: UsageReport = {
  range: "7d",
  granularity: "day",
  totals: {
    input_tokens: 1500,
    output_tokens: 500,
    cache_read_tokens: 8000,
    cache_creation_tokens: 500,
    total_tokens: 10500,
    reported_cost_usd: 0.0245,
    attempts_total: 4,
    attempts_known: 3,
    attempts_unknown: 1,
    attempts_with_cost: 2,
    attempts_failed: 1,
  },
  series: [
    {
      key: "2026-11-21",
      label: "11-21",
      input_tokens: 500,
      output_tokens: 200,
      cache_read_tokens: 3000,
      cache_creation_tokens: 0,
      total_tokens: 3700,
      reported_cost_usd: 0.01,
      attempts: 1,
    },
    {
      key: "2026-11-22",
      label: "11-22",
      input_tokens: 1000,
      output_tokens: 300,
      cache_read_tokens: 5000,
      cache_creation_tokens: 500,
      total_tokens: 6800,
      reported_cost_usd: 0.0145,
      attempts: 3,
    },
  ],
  by_model: [
    {
      key: "claude:claude-sonnet-4-6",
      label: "claude-sonnet-4-6",
      secondary: "claude",
      input_tokens: 1000,
      output_tokens: 300,
      cache_read_tokens: 6000,
      cache_creation_tokens: 500,
      total_tokens: 7800,
      reported_cost_usd: 0.0245,
      attempts: 3,
      attempts_known: 2,
      attempts_with_cost: 2,
    },
    {
      key: "codex:gpt-5.4",
      label: "gpt-5.4",
      secondary: "codex",
      input_tokens: 500,
      output_tokens: 200,
      cache_read_tokens: 2000,
      cache_creation_tokens: 0,
      total_tokens: 2700,
      reported_cost_usd: 0,
      attempts: 1,
      attempts_known: 1,
      attempts_with_cost: 0,
    },
  ],
  by_executor: [
    {
      key: "claude",
      label: "claude",
      input_tokens: 1000,
      output_tokens: 300,
      cache_read_tokens: 6000,
      cache_creation_tokens: 500,
      total_tokens: 7800,
      reported_cost_usd: 0.0245,
      attempts: 3,
      attempts_known: 2,
      attempts_with_cost: 2,
    },
  ],
  by_agent: [
    {
      key: "ag-1",
      label: "Architect",
      input_tokens: 1500,
      output_tokens: 500,
      cache_read_tokens: 8000,
      cache_creation_tokens: 500,
      total_tokens: 10500,
      reported_cost_usd: 0.0245,
      attempts: 4,
      attempts_known: 3,
      attempts_with_cost: 2,
    },
  ],
  by_workspace: [
    {
      key: "ws-1",
      label: "Alpha",
      input_tokens: 1500,
      output_tokens: 500,
      cache_read_tokens: 8000,
      cache_creation_tokens: 500,
      total_tokens: 10500,
      reported_cost_usd: 0.0245,
      attempts: 4,
      attempts_known: 3,
      attempts_with_cost: 2,
    },
  ],
  top_targets: [
    {
      workspace_id: "ws-1",
      workspace_name: "Alpha",
      thread_id: "th-1",
      task_id: "task-1",
      title: "Build usage dashboard",
      input_tokens: 1000,
      output_tokens: 300,
      cache_read_tokens: 6000,
      cache_creation_tokens: 500,
      total_tokens: 7800,
      reported_cost_usd: 0.0245,
      attempts: 3,
      attempts_with_cost: 2,
      last_at: "2026-11-22T02:00:00Z",
    },
    {
      workspace_id: "ws-1",
      workspace_name: "Alpha",
      thread_id: "th-2",
      title: "Quick chat",
      input_tokens: 500,
      output_tokens: 200,
      cache_read_tokens: 2000,
      cache_creation_tokens: 0,
      total_tokens: 2700,
      reported_cost_usd: 0,
      attempts: 1,
      attempts_with_cost: 0,
      last_at: "2026-11-21T02:00:00Z",
    },
    {
      workspace_id: "ws-1",
      workspace_name: "Alpha",
      thread_id: "th-3",
      title: "Deleted thread",
      deleted: true,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      total_tokens: 0,
      reported_cost_usd: 0,
      attempts: 1,
      attempts_with_cost: 0,
      last_at: "2026-11-21T01:00:00Z",
    },
  ],
  recent: [
    {
      attempt_id: "att-1",
      run_id: "run-1",
      workspace_id: "ws-1",
      workspace_name: "Alpha",
      thread_id: "th-1",
      task_id: "task-1",
      title: "Build usage dashboard",
      agent_id: "ag-1",
      agent_name: "Architect",
      executor: "claude",
      model: "claude-sonnet-4-6",
      status: "succeeded",
      usage_known: true,
      usage_completeness: "known",
      input_tokens: 1000,
      output_tokens: 300,
      cache_read_tokens: 6000,
      cache_creation_tokens: 500,
      total_tokens: 7800,
      reported_cost_usd: 0.0245,
      cost_known: true,
      started_at: "2026-11-22T02:00:00Z",
    },
    {
      attempt_id: "att-2",
      run_id: "run-2",
      workspace_id: "ws-1",
      workspace_name: "Alpha",
      thread_id: "th-2",
      title: "Quick chat",
      agent_id: "ag-1",
      agent_name: "Architect",
      executor: "codex",
      model: "gpt-5.4",
      status: "failed",
      usage_known: false,
      usage_completeness: "unknown",
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      total_tokens: 0,
      started_at: "2026-11-22T01:00:00Z",
    },
  ],
  workspaces: { "ws-1": "Alpha" },
  members: { "ag-1": "Architect" },
  executors: ["claude", "codex"],
};

describe("UsagePage", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    setLanguage("zh-CN");
    fetchMock.mockReset();
    fetchMock.mockImplementation(
      async () =>
        new Response(JSON.stringify(sampleReport), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    setLanguage("zh-CN");
  });

  it("renders KPIs, cache hit ratio, unreported coverage, native cost and navigates on click", async () => {
    const onOpenThread = vi.fn();
    const onOpenTask = vi.fn();
    render(
      <UsagePage onOpenThread={onOpenThread} onOpenTask={onOpenTask} />,
    );

    await waitFor(() =>
      expect(screen.getByText("10.5K")).toBeInTheDocument(),
    );
    expect(screen.getByText("8.00K")).toBeInTheDocument();
    expect(screen.getByText(/命中率 80%/)).toBeInTheDocument();
    expect(screen.getByText(/1 次未上报/)).toBeInTheDocument();
    expect(screen.getAllByText("$0.025").length).toBeGreaterThan(0);
    expect(screen.getByText("未上报用量")).toBeInTheDocument();

    // Clicking a task target opens task detail; clicking a thread target opens thread.
    fireEvent.click(
      screen.getAllByRole("button", { name: /Build usage dashboard/ })[0],
    );
    expect(onOpenTask).toHaveBeenCalledWith("ws-1", "task-1");

    fireEvent.click(screen.getAllByRole("button", { name: /Quick chat/ })[0]);
    expect(onOpenThread).toHaveBeenCalledWith("ws-1", "th-2");

    // Trashed thread target is rendered disabled.
    const trashed = screen.getByRole("button", { name: /Deleted thread/ });
    expect(trashed).toBeDisabled();
  });

  it("queries the backend with updated range and filters", async () => {
    render(<UsagePage onOpenThread={() => {}} onOpenTask={() => {}} />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByRole("button", { name: "今天" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls.at(-1)?.[0]).toContain("range=today"),
    );

    fireEvent.click(screen.getByLabelText("按 CLI 筛选"));
    fireEvent.click(screen.getByRole("option", { name: "Codex" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls.at(-1)?.[0]).toContain("executor=codex"),
    );

    // Switch breakdown tab to Member.
    fireEvent.click(screen.getByRole("tab", { name: "按成员" }));
    expect(screen.getAllByText("Architect").length).toBeGreaterThan(0);
  });

  it("shows honest no-cost message and English copy when no CLI reported USD", async () => {
    setLanguage("en");
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ...sampleReport,
          totals: {
            ...sampleReport.totals,
            reported_cost_usd: 0,
            attempts_with_cost: 0,
          },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    render(<UsagePage onOpenThread={() => {}} onOpenTask={() => {}} />);
    await waitFor(() =>
      expect(
        screen.getByText(/No native USD reported \(real tokens only\)/),
      ).toBeInTheDocument(),
    );
  });
});
