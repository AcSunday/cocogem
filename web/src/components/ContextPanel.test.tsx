import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, it } from "vitest";
import type { Source } from "../api/client";
import { ContextPanel, type PanelTab } from "./ContextPanel";

afterEach(cleanup);

function Panel({ initial = "artifacts" }: { initial?: PanelTab }) {
  const [tab, setTab] = useState<PanelTab>(initial);
  return (
    <ContextPanel
      open
      close={() => {}}
      tab={tab}
      setTab={setTab}
      sources={[]}
      onSource={() => {}}
      artifacts={[]}
      onArtifact={() => {}}
      runs={[]}
      onInspect={() => {}}
      events={[]}
    />
  );
}

it("renders a passed test source as exit 0 when exit_code is omitted", () => {
  const source: Source = {
    captured_at: "2026-10-28T10:00:00Z",
    content: "$ go test -count=1 ./...\nok",
    end_line: 0,
    id: "test-source",
    kind: "test",
    path: ".",
    run_id: "run-1",
    sha256: "",
    start_line: 0,
    thread_id: "thread-1",
    title: "go test -count=1 ./...",
    truncated: false,
    url: "",
    workspace_id: "workspace-1",
  };
  const panel = (shown: Source) => (
    <ContextPanel
      open
      close={() => {}}
      tab="sources"
      setTab={() => {}}
      sources={[shown]}
      source={shown}
      sourceID={shown.id}
      onSource={() => {}}
      artifacts={[]}
      onArtifact={() => {}}
      runs={[]}
      onInspect={() => {}}
      events={[]}
    />
  );
  const { rerender } = render(panel(source));
  const detail = () => screen.getByTestId("source-detail");
  expect(detail()).toHaveTextContent("exit 0");
  expect(detail()).not.toHaveTextContent("exit undefined");
  rerender(panel({ ...source, timed_out: true }));
  expect(detail()).toHaveTextContent("exit 超时");
  rerender(panel({ ...source, exit_code: 1 }));
  expect(detail()).toHaveTextContent("exit 1");
});

it("shows only source, artifact and process tabs without member management", () => {
  render(<Panel />);
  expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
    "来源",
    "产物",
    "过程",
  ]);
  expect(screen.getByRole("tab", { name: "产物" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(screen.queryByRole("tab", { name: "成员" })).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "管理成员与称呼" }),
  ).not.toBeInTheDocument();
});

it("wraps and clamps keyboard navigation across the three tabs", async () => {
  const user = userEvent.setup();
  render(<Panel />);
  const tab = (name: string) => screen.getByRole("tab", { name });

  tab("产物").focus();
  await user.keyboard("{ArrowRight}");
  expect(tab("过程")).toHaveAttribute("aria-selected", "true");
  expect(tab("过程")).toHaveFocus();
  await user.keyboard("{ArrowRight}");
  expect(tab("来源")).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{ArrowLeft}");
  expect(tab("过程")).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{Home}");
  expect(tab("来源")).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{End}");
  expect(tab("过程")).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{ArrowRight}");
  expect(tab("来源")).toHaveAttribute("aria-selected", "true");
  expect(tab("来源")).toHaveFocus();
});
