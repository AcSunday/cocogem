import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type {
  Artifact,
  Attempt,
  Message,
  Run,
  Source,
  Step,
} from "../api/client";
import { CollaborationTasks } from "./CollaborationTasks";

afterEach(cleanup);

const run = {
  id: "run",
  agents: [
    { id: "coordinator", display_name: "本轮协调成员" },
    { id: "researcher", display_name: "本轮研究成员" },
  ],
} as Run;
const parent: Step = {
  id: "parent",
  run_id: "run",
  agent_id: "coordinator",
  agent_name: "本轮协调成员",
  role: "coordinator",
  depends_on: [],
  instruction: "整理任务",
  status: "succeeded",
  error: "",
};
const child: Step = {
  id: "child",
  run_id: "run",
  agent_id: "researcher",
  agent_name: "本轮研究成员",
  role: "researcher",
  kind: "handoff",
  from_step_id: "parent",
  from_agent_id: "coordinator",
  context_refs: ["message", "source", "artifact@2"],
  depends_on: ["parent"],
  instruction: "查找可核对的资料，并说明证据限制。",
  status: "queued",
  error: "",
};
const messages = [
  {
    id: "message",
    step_id: "parent",
    agent_name: "本轮协调成员",
    content: "需要核对两种方案的证据。",
    status: "completed",
  },
  {
    id: "result",
    step_id: "child",
    agent_name: "本轮研究成员",
    content: "研究结果保留了未确认的问题。",
    status: "completed",
  },
] as Message[];
const sources = [{ id: "source", title: "来源原文" }] as Source[];
const artifacts = [1, 2].map((version) => ({
  id: "artifact",
  title: "研究笔记",
  version,
})) as Artifact[];

it("shows task sender, recipient, context, exact artifact version and committed result", async () => {
  const onSource = vi.fn();
  const onArtifact = vi.fn();
  render(
    <CollaborationTasks
      run={run}
      steps={[parent, child]}
      messages={messages}
      sources={sources}
      artifacts={artifacts}
      onSource={onSource}
      onArtifact={onArtifact}
    />,
  );
  const tasks = within(screen.getByRole("list", { name: "协作任务" }));
  expect(tasks.getByText("本轮协调成员 →")).toBeVisible();
  expect(tasks.getByText("排队中")).toBeVisible();
  const recipient = tasks.getByText("本轮研究成员", { selector: "strong" });
  await userEvent.click(recipient);
  const task = within(recipient.closest("details")!);
  expect(task.getByText(child.instruction)).toBeVisible();
  expect(task.getByText("需要核对两种方案的证据。")).toBeVisible();
  expect(task.getByText("研究结果保留了未确认的问题。")).toBeVisible();
  await userEvent.click(task.getByRole("button", { name: "来源：来源原文" }));
  expect(onSource).toHaveBeenCalledWith("source");
  await userEvent.click(task.getByRole("button", { name: "研究笔记 · v2" }));
  expect(onArtifact).toHaveBeenCalledWith(artifacts[1]);
  expect(tasks.queryByText("研究笔记 · v1")).not.toBeInTheDocument();
  expect(tasks.queryByText(/artifact@2/)).not.toBeInTheDocument();
});

it("keeps a task expanded while status and results change", async () => {
  const props = { run, messages: [], sources: [], artifacts: [] };
  const { rerender } = render(
    <CollaborationTasks {...props} steps={[parent, child]} />,
  );
  await userEvent.click(
    screen.getByText("本轮研究成员", { selector: "strong" }),
  );
  expect(screen.getByText(child.instruction)).toBeVisible();
  rerender(
    <CollaborationTasks
      {...props}
      messages={messages}
      steps={[
        parent,
        { ...child, status: "failed", error: "资料服务暂时不可用" },
      ]}
    />,
  );
  expect(screen.getByText("失败")).toBeVisible();
  expect(screen.getByText("资料服务暂时不可用")).toBeVisible();
  expect(screen.getByText(child.instruction)).toBeVisible();
  expect(screen.getByText("研究结果保留了未确认的问题。")).toBeVisible();
});

it("shows the recipients returning results to the waiting coordinator", () => {
  const continuation: Step = {
    ...parent,
    id: "return",
    kind: "continuation",
    from_agent_id: "coordinator",
    depends_on: ["child"],
    status: "pending",
    instruction: "处理成员返回的结果",
  };
  render(
    <CollaborationTasks
      run={run}
      steps={[parent, child, continuation]}
      messages={[]}
      sources={[]}
      artifacts={[]}
    />,
  );
  expect(screen.getByText("本轮研究成员 →")).toBeVisible();
  expect(screen.getByText("结果回传")).toBeVisible();
  expect(screen.getByText("等待结果")).toBeVisible();
});

it("labels the partial usage and liveness warning of the running attempt", async () => {
  const attempt: Attempt = {
    id: "attempt",
    run_id: "run",
    step_id: "child",
    number: 1,
    status: "running",
    started_at: "",
    usage_known: false,
    usage_completeness: "partial",
    input_tokens: 7,
    output_tokens: 0,
    liveness_warning: {
      state: "stalled",
      silence_seconds: 900,
      last_event_type: "usage",
      process_alive: true,
      tool_active: false,
      observed_at: "2026-10-27T00:00:00Z",
    },
  };
  render(
    <CollaborationTasks
      run={run}
      steps={[parent, child]}
      attempts={[attempt]}
      messages={[]}
      sources={[]}
      artifacts={[]}
    />,
  );
  await userEvent.click(
    screen.getByText("本轮研究成员", { selector: "strong" }),
  );
  expect(screen.getByText(/已知部分 7 \/ 0 tokens/)).toBeVisible();
  expect(screen.getByText(/告警：长时间无进展/)).toBeVisible();
  expect(screen.getByText(/已 900 秒/)).toBeVisible();
});
