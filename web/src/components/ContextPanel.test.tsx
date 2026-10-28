import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, it } from "vitest";
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
