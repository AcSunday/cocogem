import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { ToolTimeline } from "./ToolTimeline";
import type { Agent, Event, Source } from "../api/client";

afterEach(cleanup);
it("shows the committed upstream replies, exact artifact versions and captured sources", async () => {
  const agent: Agent = {
    id: "critic",
    workspace_id: "ws",
    display_name: "旧缅因猫",
    handle: "maine",
    aliases: [],
    role_description: "",
    model: "default",
    tools: [],
    disabled: false,
    revision: 1,
  };
  const event: Event = {
    schema_version: 1,
    run_id: "run",
    seq: 1,
    type: "handoff_prepared",
    occurred_at: "",
    agent_id: "critic",
    data: {
      instruction: "核对上游结论",
      original_task: "比较两种协作方案",
      from_steps: ["internal-step-id"],
      messages: [
        {
          id: "message",
          agent_name: "布偶猫",
          content: "方案一节省时间，但证据仍有不足。",
        },
      ],
      artifacts: [{ id: "artifact", version: 2, title: "方案分析" }],
      source_ids: ["source"],
    },
  };
  const source = { id: "source", title: "公开研究资料" } as Source;
  const onSource = vi.fn();
  render(
    <ToolTimeline
      agents={[agent]}
      events={[event]}
      sources={[source]}
      onSource={onSource}
    />,
  );
  expect(screen.getByText("旧缅因猫 (@maine)")).toBeVisible();
  await userEvent.click(screen.getByText("收到工作包"));
  expect(screen.getByText("方案一节省时间，但证据仍有不足。")).toBeVisible();
  expect(screen.getByText("方案分析 · v2")).toBeVisible();
  expect(screen.queryByText(/internal-step-id/)).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "公开研究资料" }));
  expect(onSource).toHaveBeenCalledWith("source");
});
