import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import type { Artifact, Source } from "../api/client";
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

it("keeps only the source and artifact tabs", () => {
  render(<Panel />);
  expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
    "来源",
    "产物",
  ]);
  expect(screen.getByRole("tab", { name: "产物" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(screen.queryByRole("tab", { name: "过程" })).not.toBeInTheDocument();
  expect(screen.queryByRole("tab", { name: "成员" })).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "管理成员与称呼" }),
  ).not.toBeInTheDocument();
  // Tool activity is rendered in the conversation body, not the panel.
  expect(screen.queryByText("工具调用和交接记录会在协作期间实时出现。"))
    .not.toBeInTheDocument();
});

it("falls back to sources when a stale state still selects the removed process tab", () => {
  const legacy = "tools" as unknown as PanelTab;
  render(
    <ContextPanel
      open
      close={() => {}}
      tab={legacy}
      setTab={() => {}}
      sources={[]}
      onSource={() => {}}
      artifacts={[]}
      onArtifact={() => {}}
    />,
  );
  expect(screen.getByRole("tab", { name: "来源" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(screen.getByRole("tabpanel")).toHaveAttribute(
    "aria-labelledby",
    "tab-sources",
  );
  expect(screen.queryByRole("tab", { name: "过程" })).not.toBeInTheDocument();
});

it("wraps and clamps keyboard navigation across the two remaining tabs", async () => {
  const user = userEvent.setup();
  render(<Panel />);
  const tab = (name: string) => screen.getByRole("tab", { name });

  tab("产物").focus();
  await user.keyboard("{ArrowRight}");
  expect(tab("来源")).toHaveAttribute("aria-selected", "true");
  expect(tab("来源")).toHaveFocus();
  await user.keyboard("{ArrowRight}");
  expect(tab("产物")).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{ArrowLeft}");
  expect(tab("来源")).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{Home}");
  expect(tab("来源")).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{End}");
  expect(tab("产物")).toHaveAttribute("aria-selected", "true");
  await user.keyboard("{ArrowRight}");
  expect(tab("来源")).toHaveAttribute("aria-selected", "true");
  expect(tab("来源")).toHaveFocus();
});

const source: Source = {
  captured_at: "2026-11-08T10:00:00Z",
  content: "资料正文",
  end_line: 0,
  id: "source-1",
  kind: "web",
  path: "",
  run_id: "run",
  sha256: "",
  start_line: 0,
  thread_id: "thread",
  title: "公开研究资料",
  truncated: false,
  url: "https://example.test/doc",
  workspace_id: "ws",
};

const artifact: Artifact = {
  id: "note",
  version: 2,
  revision: 1,
  thread_id: "thread",
  run_id: "run",
  step_id: "step",
  type: "note",
  title: "最终纪要",
  body: "正文",
  findings: [],
  source_ids: [],
  limitations: [],
  feedback: "unreviewed",
  feedback_at: "",
  created_at: "2026-11-08T10:00:00Z",
};

it("keeps sources openable and the artifacts tab usable", async () => {
  const user = userEvent.setup();
  const onSource = vi.fn();
  const onArtifact = vi.fn();
  function Docked() {
    const [tab, setTab] = useState<PanelTab>("sources");
    return (
      <ContextPanel
        open
        close={() => {}}
        tab={tab}
        setTab={setTab}
        sources={[source]}
        onSource={onSource}
        artifacts={[artifact]}
        onArtifact={onArtifact}
      />
    );
  }
  render(<Docked />);
  await user.click(screen.getByRole("button", { name: /公开研究资料/ }));
  expect(onSource).toHaveBeenCalledWith(source);

  await user.click(screen.getByRole("tab", { name: /产物/ }));
  expect(screen.getByRole("tab", { name: /产物/ })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await user.click(screen.getByRole("button", { name: /最终纪要/ }));
  expect(onArtifact).toHaveBeenCalledWith(artifact);
});
