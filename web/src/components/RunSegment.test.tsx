import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Agent, Artifact, Event, Message, Run, Step } from "../api/client";
import { RunSegment } from "./RunSegment";

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.unstubAllGlobals();
});

const agent: Agent = {
  id: "one",
  workspace_id: "",
  display_name: "布偶猫",
  handle: "ragdoll",
  aliases: [],
  role_description: "",
  model: "default",
  executor: "claude",
  avatar: "🐱",
  tools: [],
  disabled: false,
  revision: 1,
};

const baseRun = {
  id: "run",
  workspace_id: "ws",
  thread_id: "thread",
  input: { workflow: "free" },
  agents: [agent],
  status: "running",
  created_at: "2026-10-20T00:00:00Z",
  finished_at: "",
  error: "",
  routing: {
    usage_known: false,
    model_call: false,
    input_tokens: 0,
    output_tokens: 0,
  },
} as unknown as Run;

const step = {
  id: "step",
  run_id: "run",
  agent_id: "one",
  agent_name: "布偶猫",
  role: "researcher",
  status: "succeeded",
} as Step;

const message = (id: string, content: string): Message => ({
  id,
  run_id: "run",
  thread_id: "thread",
  step_id: "step",
  agent_id: "one",
  agent_name: "布偶猫",
  role: "assistant",
  status: "completed",
  content,
  created_at: "2026-10-20T00:00:00Z",
});

const artifact = (version: number, title = "最终纪要"): Artifact => ({
  id: "note",
  version,
  revision: 1,
  thread_id: "thread",
  run_id: "run",
  step_id: "step",
  type: "note",
  title,
  body: `第 ${version} 版正文`,
  findings: [],
  source_ids: [],
  limitations: [],
  feedback: "unreviewed",
  feedback_at: "",
  created_at: "2026-10-20T00:00:00Z",
});

const props = {
  index: 1,
  latest: true,
  busy: false,
  onSnapshot: () => {},
  onRetry: () => {},
  onResume: () => {},
};

const contentPage = (
  messageID: string,
  content: string,
  extra: Record<string, unknown> = {},
) =>
  Response.json({
    message_id: messageID,
    content_format: "inline",
    content,
    content_bytes: content.length,
    content_truncated: false,
    has_more: false,
    ...extra,
  });

/** MessageCards are articles; collaboration task results are not. */
const articles = (text: string) =>
  screen
    .getAllByRole("article")
    .filter((article) => article.textContent?.includes(text));

it("renders every research and read-only analysis step role", () => {
  const renderRoles = (roles: string[]) => {
    const steps = roles.map(
      (role, index) =>
        ({
          ...step,
          id: `step-${index}`,
          role,
          status: "succeeded",
        }) as Step,
    );
    const messages = steps.map((item, index) => ({
      ...message(`message-${index}`, `步骤输出 ${index}`),
      step_id: item.id,
    }));
    return render(
      <RunSegment
        {...props}
        run={{ ...baseRun, status: "running" }}
        steps={steps}
        messages={messages}
      />,
    );
  };
  const expectRoles = (labels: string[]) => {
    labels.forEach((label, index) => {
      expect(
        within(articles(`步骤输出 ${index}`)[0]).getByText(label),
      ).toBeVisible();
    });
  };
  const research = renderRoles([
    "researcher",
    "critic",
    "researcher",
    "critic",
    "coordinator",
  ]);
  expectRoles(["研究", "质疑", "研究", "质疑", "汇总"]);
  research.unmount();
  renderRoles(["analyst", "critic", "analyst", "final_reviewer"]);
  expectRoles(["分析", "质疑", "分析", "最终复核"]);
});

it("folds intermediate replies but keeps every explicit final message", () => {
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "succeeded",
        delivery: {
          message_ids: ["final-a", "final-b"],
          artifact_refs: [],
        },
      }}
      steps={[step]}
      messages={[
        message("middle", "中间过程"),
        message("final-a", "最终甲"),
        message("final-b", "最终乙"),
      ]}
    />,
  );
  const process = screen.getByTestId("run-process");
  expect(process).not.toHaveAttribute("open");
  expect(process.querySelector("summary")).toHaveTextContent(
    "协作已完成 · 1 条过程",
  );
  expect(articles("中间过程")[0].closest("details")).toBe(process);
  expect(articles("最终甲")[0].closest("details")).toBeNull();
  expect(articles("最终乙")[0].closest("details")).toBeNull();
});

it("collapses an artifact-only delivery without inventing an empty final message", () => {
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "succeeded",
        delivery: {
          message_ids: [],
          artifact_refs: [{ id: "note", version: 2 }],
        },
      }}
      steps={[step]}
      messages={[message("empty", "   ")]}
      artifacts={[artifact(1), artifact(2)]}
    />,
  );
  const process = screen.getByTestId("run-process");
  expect(process).not.toHaveAttribute("open");
  expect(screen.queryByText("查看最终结果")).not.toBeInTheDocument();
  const cards = within(screen.getByLabelText("第 1 次协作产物"));
  expect(cards.getAllByText("最终纪要")).toHaveLength(1);
  expect(cards.getByText("讨论纪要 · v2")).toBeVisible();
  expect(cards.queryByText("讨论纪要 · v1")).toBeNull();
});

it("keeps failed and legacy runs fully open", () => {
  const { rerender } = render(
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "failed", error: "执行失败" }}
      steps={[step]}
      messages={[message("mid", "失败过程")]}
    />,
  );
  expect(screen.getByTestId("run-process")).toHaveAttribute("open");
  expect(articles("失败过程")[0].closest("details")).toBeNull();
  rerender(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "succeeded",
        delivery: null,
      }}
      steps={[step]}
      messages={[message("legacy", "旧历史回复")]}
      artifacts={[artifact(1, "旧产物")]}
    />,
  );
  expect(screen.getByTestId("run-process")).toHaveAttribute("open");
  expect(articles("旧历史回复")[0].closest("details")).toBeNull();
  expect(screen.getByText("旧产物")).toBeVisible();
});

it("keeps a reader's expand or collapse across rerenders and remounts", async () => {
  const delivered = {
    ...baseRun,
    status: "succeeded",
    delivery: { message_ids: ["final"], artifact_refs: [] },
  } as Run;
  const view = () => (
    <RunSegment
      {...props}
      run={delivered}
      steps={[step]}
      messages={[message("final", "最终结果")]}
    />
  );
  const first = render(view());
  const process = screen.getByTestId("run-process");
  expect(process).not.toHaveAttribute("open");
  await userEvent.click(process.querySelector("summary")!);
  expect(process).toHaveAttribute("open");
  first.rerender(
    <RunSegment
      {...props}
      run={delivered}
      steps={[{ ...step, status: "succeeded" }]}
      messages={[message("final", "最终结果")]}
    />,
  );
  expect(screen.getByTestId("run-process")).toHaveAttribute("open");
  first.unmount();
  render(view());
  expect(screen.getByTestId("run-process")).toHaveAttribute("open");
  await userEvent.click(
    screen.getByTestId("run-process").querySelector("summary")!,
  );
  expect(screen.getByTestId("run-process")).not.toHaveAttribute("open");
  cleanup();
  render(view());
  expect(screen.getByTestId("run-process")).not.toHaveAttribute("open");
});

it("opens the terminal run summary details by click and closes them on Escape or outside press", async () => {
  const user = userEvent.setup();
  const run = {
    ...baseRun,
    status: "succeeded",
    finished_at: "2026-10-20T00:01:02Z",
    routing: {
      usage_known: true,
      model_call: true,
      input_tokens: 12,
      output_tokens: 34,
    },
  } as Run;
  render(<RunSegment {...props} run={run} messages={[]} />);
  const badge = screen.getByRole("button", { name: "查看协作摘要详情" });
  expect(badge).toHaveAttribute("aria-expanded", "false");
  expect(badge.parentElement).not.toHaveAttribute("data-open");
  expect(within(badge).getByText("1 分 2 秒")).toBeVisible();
  expect(within(badge).getByText("12 / 34 tokens")).toBeVisible();
  expect(within(badge).getByText("12 / 34 tokens")).toHaveAttribute(
    "title",
    "12 / 34 tokens",
  );

  await user.click(badge);
  expect(badge).toHaveAttribute("aria-expanded", "true");
  expect(badge.parentElement).toHaveAttribute("data-open", "true");

  await user.keyboard("{Escape}");
  expect(badge).toHaveAttribute("aria-expanded", "false");
  expect(badge.parentElement).not.toHaveAttribute("data-open");

  await user.click(badge);
  expect(badge).toHaveAttribute("aria-expanded", "true");
  await user.click(document.body);
  expect(badge).toHaveAttribute("aria-expanded", "false");
});

it("reads a final message missing from the page by its exact id", async () => {
  const fetchMock = vi.fn(async (path: string) => {
    if (path.startsWith("/api/v1/runs/run/messages/final-missing/content"))
      return Response.json({
        message_id: "final-missing",
        content_format: "inline",
        content: "分页外的最终正文",
        content_bytes: 24,
        content_truncated: false,
        has_more: false,
      });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "succeeded",
        delivery: {
          message_ids: ["final-missing"],
          artifact_refs: [],
        },
      }}
      steps={[step]}
      messages={[message("middle", "过程回复")]}
    />,
  );
  const process = screen.getByTestId("run-process");
  expect(process).not.toHaveAttribute("open");
  expect(articles("过程回复")[0].closest("details")).toBe(process);
  await userEvent.click(screen.getByRole("button", { name: "查看最终结果" }));
  expect(await screen.findByText("分页外的最终正文")).toBeVisible();
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock.mock.calls[0][0]).toContain(
    "/api/v1/runs/run/messages/final-missing/content",
  );
});

it("labels an exact final message from its own author and model snapshot", async () => {
  const avatar =
    "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==";
  const snapshot = {
    ...message("final-missing", ""),
    agent_name: "快照成员",
    model: "requested-x",
    resolved_model: "resolved-y",
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      contentPage("final-missing", "最终正文", { message: snapshot }),
    ),
  );
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        agents: [{ ...agent, avatar, model: "profile-model" }],
        status: "succeeded",
        delivery: {
          message_ids: ["final-missing"],
          artifact_refs: [],
        },
      }}
      steps={[{ ...step, role: "reviewer" }]}
      messages={[message("middle", "过程回复")]}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "查看最终结果" }));
  expect(await screen.findByText("最终正文")).toBeVisible();
  const result = screen.getByLabelText("最终结果");
  expect(within(result).getByText("快照成员")).toBeVisible();
  expect(within(result).getByText("（resolved-y）")).toBeVisible();
  expect(within(result).getByText("复核")).toBeVisible();
  expect(result.querySelector("img")).toHaveAttribute("src", avatar);
  expect(within(result).queryByText("布偶猫")).toBeNull();
  expect(within(result).queryByText(/profile-model/)).toBeNull();
});

it("marks a final message without metadata instead of inventing a model", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => contentPage("final-missing", "历史正文")),
  );
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        agents: [{ ...agent, model: "profile-model" }],
        status: "succeeded",
        delivery: {
          message_ids: ["final-missing"],
          artifact_refs: [],
        },
      }}
      steps={[step]}
      messages={[message("middle", "过程回复")]}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "查看最终结果" }));
  expect(await screen.findByText("历史正文")).toBeVisible();
  const result = screen.getByLabelText("最终结果");
  expect(within(result).getByText("历史最终回复")).toBeVisible();
  expect(within(result).queryByText(/profile-model/)).toBeNull();
  expect(within(result).queryByText(/^（.*）$/)).toBeNull();
});

it("retries only the final result that failed", async () => {
  let failures = 0;
  const fetchMock = vi.fn(async (path: string) => {
    if (path.includes("/final-a/content"))
      return contentPage("final-a", "最终甲");
    failures++;
    if (failures <= 2)
      return Response.json({ error: { message: "加载失败" } }, { status: 500 });
    return contentPage("final-b", "最终乙");
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "succeeded",
        delivery: {
          message_ids: ["final-a", "final-b"],
          artifact_refs: [],
        },
      }}
      steps={[step]}
      messages={[message("middle", "过程回复")]}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: /查看最终结果/ }));
  expect(await screen.findByText("最终甲")).toBeVisible();
  expect(await screen.findByText("加载失败")).toBeVisible();
  expect(screen.getByRole("button", { name: "重试" })).toBeVisible();
  // Reopening retries the failed target and leaves the loaded one alone.
  await userEvent.click(screen.getByRole("button", { name: /收起最终结果/ }));
  await userEvent.click(screen.getByRole("button", { name: /查看最终结果/ }));
  expect(await screen.findByText("加载失败")).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "重试" }));
  expect(await screen.findByText("最终乙")).toBeVisible();
  const calls = (needle: string) =>
    fetchMock.mock.calls.filter(([path]) => (path as string).includes(needle))
      .length;
  expect(calls("/final-a/content")).toBe(1);
  expect(calls("/final-b/content")).toBe(3);
});

it("pages a long final body one bounded page at a time", async () => {
  const fetchMock = vi.fn(async (path: string) => {
    if (path.includes("cursor=cursor-2"))
      return contentPage("final-long", "第二页正文");
    return contentPage("final-long", "第一页正文", {
      has_more: true,
      next_cursor: "cursor-2",
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "succeeded",
        delivery: {
          message_ids: ["final-long"],
          artifact_refs: [],
        },
      }}
      steps={[step]}
      messages={[message("middle", "过程回复")]}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "查看最终结果" }));
  expect(await screen.findByText("第一页正文")).toBeVisible();
  await userEvent.click(
    screen.getByRole("button", { name: "查看下一页最终结果" }),
  );
  expect(await screen.findByText("第二页正文")).toBeVisible();
  expect(screen.queryByText("第一页正文")).toBeNull();
  expect(screen.getByText("第 2 页")).toBeVisible();
  await userEvent.click(
    screen.getByRole("button", { name: "查看上一页最终结果" }),
  );
  expect(await screen.findByText("第一页正文")).toBeVisible();
  expect(screen.queryByText("第二页正文")).toBeNull();
  const urls = fetchMock.mock.calls.map(([path]) => path as string);
  expect(urls.filter((url) => url.includes("cursor=cursor-2"))).toHaveLength(1);
});

it("retries the exact final page that failed", async () => {
  let pageTwoCalls = 0;
  const fetchMock = vi.fn(async (path: string) => {
    if (path.includes("cursor=cursor-2")) {
      pageTwoCalls++;
      if (pageTwoCalls === 1)
        return Response.json(
          { error: { message: "第二页加载失败" } },
          { status: 500 },
        );
      return contentPage("final-long", "第二页正文");
    }
    return contentPage("final-long", "第一页正文", {
      has_more: true,
      next_cursor: "cursor-2",
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "succeeded",
        delivery: {
          message_ids: ["final-long"],
          artifact_refs: [],
        },
      }}
      steps={[step]}
      messages={[message("middle", "过程回复")]}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "查看最终结果" }));
  expect(await screen.findByText("第一页正文")).toBeVisible();
  await userEvent.click(
    screen.getByRole("button", { name: "查看下一页最终结果" }),
  );
  expect(await screen.findByRole("alert")).toHaveTextContent("第二页加载失败");
  expect(screen.getByText("第一页正文")).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "重试" }));
  expect(await screen.findByText("第二页正文")).toBeVisible();
  expect(screen.queryByText("第一页正文")).toBeNull();
  expect(screen.getByText("第 2 页")).toBeVisible();
  expect(
    fetchMock.mock.calls.filter(([path]) =>
      (path as string).includes("cursor=cursor-2"),
    ),
  ).toHaveLength(2);
  expect(fetchMock).toHaveBeenCalledTimes(3);
});

it("does not let a previous run's response reach the new run", async () => {
  const pending: Record<string, (response: Response) => void> = {};
  vi.stubGlobal(
    "fetch",
    vi.fn(
      (path: string) =>
        new Promise<Response>((resolve) => {
          pending[path] = resolve;
        }),
    ),
  );
  const delivered = (id: string, messageID: string): Run =>
    ({
      ...baseRun,
      id,
      status: "succeeded",
      delivery: { message_ids: [messageID], artifact_refs: [] },
    }) as Run;
  const { rerender } = render(
    <RunSegment
      {...props}
      run={delivered("run-old", "old-final")}
      steps={[step]}
      messages={[]}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "查看最终结果" }));
  const oldPath = Object.keys(pending).find((path) =>
    path.includes("old-final"),
  )!;
  rerender(
    <RunSegment
      {...props}
      run={delivered("run-new", "new-final")}
      steps={[step]}
      messages={[]}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "查看最终结果" }));
  const newPath = Object.keys(pending).find((path) =>
    path.includes("new-final"),
  )!;
  await act(async () => {
    pending[oldPath](contentPage("old-final", "旧run正文"));
  });
  await act(async () => {
    pending[newPath](contentPage("new-final", "新run正文"));
  });
  expect(await screen.findByText("新run正文")).toBeVisible();
  expect(screen.queryByText("旧run正文")).toBeNull();
});

it("keeps the member retry beside its failed reply instead of the process", async () => {
  const retry = vi.fn();
  const failedStep = {
    ...step,
    id: "failed-step",
    agent_name: "橘猫",
    status: "failed",
  } as Step;
  const failedMessage = {
    ...message("failed-message", "未完成的草稿"),
    step_id: failedStep.id,
    agent_name: "橘猫",
    status: "failed",
  };
  render(
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "failed", error: "provider failed" }}
      steps={[failedStep]}
      messages={[failedMessage]}
      retryableSteps={[{ runID: "run", step: failedStep }]}
      onRetry={retry}
    />,
  );
  const article = articles("未完成的草稿")[0];
  const button = within(article).getByRole("button", { name: "重试 橘猫" });
  await userEvent.click(button);
  expect(retry).toHaveBeenCalledWith("run", "failed-step");
  expect(
    within(screen.getByTestId("run-process")).queryByRole("button", {
      name: "重试 橘猫",
    }),
  ).toBeNull();
});

it("replaces the failed member reply with the retry in the original turn", () => {
  const peerStep = {
    ...step,
    id: "peer-step",
    agent_name: "布偶猫",
    status: "succeeded",
  } as Step;
  const failedStep = {
    ...step,
    id: "failed-step",
    agent_name: "橘猫",
    status: "failed",
  } as Step;
  const retryStep = {
    ...failedStep,
    id: "retry-step",
    status: "succeeded",
    retry_of_step_id: failedStep.id,
  } as Step;
  const retryRun = {
    ...baseRun,
    id: "retry",
    status: "succeeded",
    input: { ...baseRun.input, retry_of_run_id: baseRun.id },
    retry_of_step_id: failedStep.id,
    delivery: { message_ids: ["retry-message"], artifact_refs: [] },
  } as Run;
  render(
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "failed", error: "provider failed" }}
      retries={[{ run: retryRun, steps: [retryStep] }]}
      steps={[peerStep, failedStep]}
      messages={[
        {
          ...message("peer-message", "同伴回复"),
          step_id: peerStep.id,
        },
        {
          ...message("old-failed", "未完成的草稿"),
          step_id: failedStep.id,
          agent_name: "橘猫",
          status: "failed",
        },
        {
          ...message("retry-message", "重试后的完整回复"),
          run_id: retryRun.id,
          step_id: retryStep.id,
          status: "completed",
        },
      ]}
    />,
  );
  expect(screen.queryByText("未完成的草稿")).toBeNull();
  expect(articles("同伴回复")).toHaveLength(1);
  expect(articles("重试后的完整回复")).toHaveLength(1);
  expect(screen.getAllByLabelText("第 1 次协作")).toHaveLength(1);
  expect(screen.getByTestId("run-status")).toHaveTextContent("已完成");
});

it("keeps a delivery-only artifact target visible until the page catches up", async () => {
  const onRefresh = vi.fn();
  const view = (artifacts: Artifact[]) => (
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "succeeded",
        delivery: {
          message_ids: [],
          artifact_refs: [{ id: "note", version: 2 }],
        },
      }}
      steps={[step]}
      messages={[message("middle", "过程回复")]}
      artifacts={artifacts}
      onRefresh={onRefresh}
    />
  );
  const { rerender } = render(view([]));
  expect(screen.getByTestId("run-process")).not.toHaveAttribute("open");
  expect(screen.getByText("最终产物正在加载…")).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "重新加载" }));
  expect(onRefresh).toHaveBeenCalledTimes(1);
  rerender(view([artifact(1), artifact(2)]));
  expect(screen.queryByText("最终产物正在加载…")).toBeNull();
  expect(screen.getByText("讨论纪要 · v2")).toBeVisible();
});

it("uses the run member snapshot for mention names, aliases and roles", () => {
  const historical = {
    ...agent,
    display_name: "历史布偶猫",
    aliases: ["布布"],
    role_description: "当时的独立复核职责",
  };
  const run = {
    ...baseRun,
    status: "succeeded",
    agents: [historical],
    delivery: { message_ids: ["final"], artifact_refs: [] },
  } as unknown as Run;
  render(
    <RunSegment
      {...props}
      run={run}
      steps={[step]}
      messages={[message("final", "请 @布布 复核这条结论。")]}
    />,
  );
  const mention = screen.getByText("@布布");
  fireEvent.focus(mention);
  const tooltip = screen.getByRole("tooltip");
  expect(tooltip).toHaveTextContent("历史布偶猫");
  expect(tooltip).toHaveTextContent("@ragdoll");
  expect(tooltip).toHaveTextContent("当时的独立复核职责");
});

const toolEvent = (
  seq: number,
  type: string,
  data: Record<string, unknown>,
  extra: Partial<Event> = {},
): Event => ({
  schema_version: 1,
  run_id: "run",
  seq,
  type,
  occurred_at: "",
  step_id: "step",
  agent_id: "one",
  data,
  ...extra,
});

it("shows each parallel member's tool activity beside that member's reply", () => {
  const peer = {
    ...agent,
    id: "two",
    display_name: "橘猫",
    handle: "orange",
  };
  const stepOne = { ...step, id: "step-one", agent_id: "one" } as Step;
  const stepTwo = {
    ...step,
    id: "step-two",
    agent_id: "two",
    agent_name: "橘猫",
  } as Step;
  render(
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "running", agents: [agent, peer] }}
      steps={[stepOne, stepTwo]}
      messages={[
        { ...message("message-one", "甲成员的结论"), step_id: stepOne.id },
        {
          ...message("message-two", "乙成员的结论"),
          step_id: stepTwo.id,
          agent_id: "two",
          agent_name: "橘猫",
        },
      ]}
      events={[
        toolEvent(
          1,
          "tool_started",
          { tool_call_id: "a", name: "read_file", input: { path: "a.go" } },
          { step_id: stepOne.id },
        ),
        toolEvent(
          2,
          "tool_finished",
          { tool_call_id: "a", name: "read_file", status: "ok", output: "a" },
          { step_id: stepOne.id },
        ),
        toolEvent(
          3,
          "tool_started",
          { tool_call_id: "b", name: "web_search", input: { query: "文档" } },
          { step_id: stepTwo.id, agent_id: "two" },
        ),
        toolEvent(
          4,
          "tool_finished",
          {
            tool_call_id: "b",
            name: "web_search",
            status: "error",
            output: "blocked",
          },
          { step_id: stepTwo.id, agent_id: "two" },
        ),
      ]}
    />,
  );
  const one = articles("甲成员的结论")[0];
  const two = articles("乙成员的结论")[0];
  expect(within(one).getByTestId("tool-activity")).toHaveTextContent(
    "读取 1 个文件",
  );
  expect(within(one).queryByText(/联网搜索/)).toBeNull();
  expect(within(two).getByTestId("tool-activity")).toHaveTextContent(
    "联网搜索 1 次 · 1 个失败",
  );
  expect(within(two).queryByText(/读取 1 个文件/)).toBeNull();
});

it("ignores another run's tool events in the shared live window", () => {
  render(
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "running" }}
      steps={[step]}
      messages={[message("reply", "本轮回合")]}
      events={[
        toolEvent(
          1,
          "tool_started",
          { tool_call_id: "a", name: "read_file", input: { path: "a.go" } },
          { run_id: "other-run" },
        ),
        toolEvent(
          2,
          "tool_finished",
          { tool_call_id: "a", name: "read_file", status: "ok" },
          { run_id: "other-run" },
        ),
      ]}
    />,
  );
  expect(screen.queryByTestId("tool-activity")).not.toBeInTheDocument();
  expect(screen.queryByText(/读取 1 个文件/)).not.toBeInTheDocument();
});

it("does not pair a retried native-1 finish with the original attempt in the body", async () => {
  const user = userEvent.setup();
  render(
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "running" }}
      steps={[step]}
      messages={[message("reply", "重试后的回复")]}
      events={[
        toolEvent(1, "attempt_started", { id: "attempt-a", number: 1 }),
        toolEvent(2, "tool_started", {
          tool_call_id: "native-1",
          name: "Read",
          origin: "native",
          input: { file_path: "first.txt" },
        }),
        toolEvent(3, "attempt_finished", {
          id: "attempt-a",
          number: 1,
          status: "interrupted",
        }),
        toolEvent(4, "attempt_started", { id: "attempt-b", number: 2 }),
        toolEvent(5, "tool_started", {
          tool_call_id: "native-1",
          name: "Read",
          origin: "native",
          input: { file_path: "second.txt" },
        }),
        toolEvent(6, "tool_finished", {
          tool_call_id: "native-1",
          name: "Read",
          origin: "native",
          status: "ok",
          output: "second attempt output",
        }),
      ]}
    />,
  );
  const reply = articles("重试后的回复")[0];
  const block = within(reply).getByTestId("tool-activity");
  expect(block).toHaveTextContent("读取 2 个文件 · 1 个结果未知");
  await user.click(within(block).getByRole("button", { name: /读取 2 个文件/ }));
  const items = within(block).getAllByRole("listitem");
  expect(items[0]).toHaveTextContent("first.txt");
  expect(items[0]).toHaveTextContent("结果未知");
  expect(items[0]).not.toHaveTextContent("second attempt output");
  expect(items[1]).toHaveTextContent("second.txt");
  expect(items[1]).toHaveTextContent("second attempt output");
});

it("shows awaiting authorization outside the run process disclosure", () => {
  const slot = vi.fn(() => (
    <button type="button">本会话持续允许</button>
  ));
  render(
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "running" }}
      steps={[step]}
      messages={[message("reply", "等待授权的回复")]}
      permissionSlot={slot}
      events={[
        toolEvent(1, "tool_started", {
          tool_call_id: "native-1",
          name: "Bash",
          origin: "native",
          status: "awaiting_permission",
          permission_request_id: "request-1",
          normalized_resource_scope: "command: npm publish",
          input: { command: "npm publish" },
        }),
      ]}
    />,
  );
  const layer = screen.getByLabelText("待处理授权");
  expect(layer).toHaveTextContent("等待授权 · Bash（npm publish）");
  expect(
    within(layer).getByRole("button", { name: "本会话持续允许" }),
  ).toBeVisible();
  expect(slot).toHaveBeenCalledWith(
    expect.objectContaining({ requestID: "request-1" }),
  );
  // The member block still marks the call as blocked, but the decision lives
  // in the separate layer, outside the run process disclosure.
  const reply = articles("等待授权的回复")[0];
  expect(within(reply).getByTestId("tool-activity")).toHaveTextContent(
    "等待授权",
  );
  expect(screen.getByTestId("run-process")).not.toContainElement(layer);
});

it("keeps pending authorization visible and operable after the process log is folded", async () => {
  const decide = vi.fn();
  const slot = vi.fn((prompt) => (
    <button
      type="button"
      onClick={() => decide(prompt.requestID, "allow_once")}
    >
      仅本次允许
    </button>
  ));
  const view = (events: Event[]) => (
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "running" }}
      steps={[{ ...step, status: "running" }]}
      messages={[]}
      events={events}
      permissionSlot={slot}
    />
  );
  const { rerender } = render(view([]));
  const process = screen.getByTestId("run-process");
  expect(process).toHaveAttribute("open");
  await userEvent.click(process.querySelector("summary")!);
  expect(process).not.toHaveAttribute("open");

  // A pending authorization arrives only after the reader folded the log.
  rerender(
    view([
      toolEvent(1, "tool_started", {
        tool_call_id: "native-1",
        name: "Bash",
        origin: "native",
        status: "awaiting_permission",
        permission_request_id: "request-1",
        normalized_resource_scope: "command: npm publish",
        input: { command: "npm publish" },
      }),
    ]),
  );
  const folded = screen.getByTestId("run-process");
  expect(folded).not.toHaveAttribute("open");
  // The member log really is inside the folded region...
  expect(folded).toContainElement(screen.getByTestId("tool-activity"));
  // ...while the authorization layer and its action stay outside and usable.
  const layer = screen.getByLabelText("待处理授权");
  expect(folded).not.toContainElement(layer);
  expect(within(layer).getByRole("status")).toHaveTextContent("等待授权");
  const action = within(layer).getByRole("button", {
    name: "仅本次允许",
  });
  expect(action).toBeVisible();
  await userEvent.click(action);
  expect(decide).toHaveBeenCalledWith("request-1", "allow_once");
});

it("shows one host invocation from pending through success", async () => {
  const user = userEvent.setup();
  const pendingEvents = [
    toolEvent(1, "tool_started", {
      tool_call_id: "call",
      name: "run_command",
      input: { command: "go test ./..." },
    }),
    toolEvent(2, "tool_finished", {
      tool_call_id: "call",
      name: "run_command",
      status: "awaiting_permission",
      permission_request_id: "request-1",
    }),
  ];
  const view = (events: Event[]) => (
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "running" }}
      steps={[step]}
      messages={[message("reply", "执行的回复")]}
      events={events}
      permissionSlot={() => <button type="button">允许</button>}
    />
  );
  const { rerender } = render(view(pendingEvents));
  const pendingLayer = screen.getByLabelText("待处理授权");
  expect(within(pendingLayer).getAllByRole("status")).toHaveLength(1);
  expect(pendingLayer).toHaveTextContent("等待授权");
  expect(
    within(articles("执行的回复")[0]).getByTestId("tool-activity"),
  ).toHaveTextContent("等待授权");

  rerender(
    view([
      ...pendingEvents,
      toolEvent(3, "tool_finished", {
        tool_call_id: "call",
        name: "run_command",
        status: "ok",
        output: "ok  internal/workflow",
        duration_ms: 5,
      }),
    ]),
  );
  expect(screen.queryByLabelText("待处理授权")).not.toBeInTheDocument();
  const block = within(articles("执行的回复")[0]).getByTestId(
    "tool-activity",
  );
  expect(block).toHaveTextContent("执行 1 条命令");
  await user.click(
    within(block).getByRole("button", { name: /执行 1 条命令/ }),
  );
  expect(within(block).getAllByRole("listitem")).toHaveLength(1);
  expect(within(block).getByText(/ok\s+internal\/workflow/)).toBeVisible();
});

it("shows one native invocation when approval precedes the start", async () => {
  const user = userEvent.setup();
  const pendingEvents = [
    toolEvent(1, "tool_finished", {
      tool_call_id: "native-1",
      name: "Bash",
      origin: "native",
      status: "awaiting_permission",
      permission_request_id: "request-1",
    }),
    toolEvent(2, "tool_started", {
      tool_call_id: "native-1",
      name: "Bash",
      origin: "native",
      input: { command: "npm publish" },
    }),
  ];
  const view = (events: Event[]) => (
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "running" }}
      steps={[step]}
      messages={[message("reply", "原生执行的回复")]}
      events={events}
      permissionSlot={() => <button type="button">允许</button>}
    />
  );
  const { rerender } = render(view(pendingEvents));
  expect(
    within(screen.getByLabelText("待处理授权")).getAllByRole("status"),
  ).toHaveLength(1);
  expect(
    within(articles("原生执行的回复")[0]).getByTestId("tool-activity"),
  ).toHaveTextContent("等待授权");

  rerender(
    view([
      ...pendingEvents,
      toolEvent(3, "tool_finished", {
        tool_call_id: "native-1",
        name: "Bash",
        origin: "native",
        status: "ok",
        output: "published",
      }),
    ]),
  );
  expect(screen.queryByLabelText("待处理授权")).not.toBeInTheDocument();
  const block = within(articles("原生执行的回复")[0]).getByTestId(
    "tool-activity",
  );
  expect(block).toHaveTextContent("执行 1 条命令");
  await user.click(
    within(block).getByRole("button", { name: /执行 1 条命令/ }),
  );
  expect(within(block).getAllByRole("listitem")).toHaveLength(1);
  expect(within(block).getByText("published")).toBeVisible();
});

it("loads the exact run history from a tool activity link", async () => {
  const fetcher = vi.fn(async (path: string) => {
    if (path.startsWith("/api/v1/runs/run/event-history"))
      return Response.json({
        items: [
          toolEvent(600, "tool_finished", {
            tool_call_id: "call",
            name: "read_file",
            status: "ok",
            output: "历史回执",
          }),
        ],
        next_cursor: "",
      });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  const events = [
    toolEvent(1, "attempt_started", { id: "attempt-a", number: 1 }),
    toolEvent(2, "tool_started", {
      tool_call_id: "call",
      name: "read_file",
      input: { path: "a.go" },
    }),
    toolEvent(3, "attempt_finished", {
      id: "attempt-a",
      number: 1,
      status: "interrupted",
    }),
  ];
  render(
    <RunSegment
      {...props}
      run={{ ...baseRun, status: "running" }}
      steps={[step]}
      messages={[message("reply", "历史回执的回复")]}
      events={events}
    />,
  );
  const reply = articles("历史回执的回复")[0];
  expect(within(reply).getByTestId("tool-activity")).toHaveTextContent(
    "结果未知",
  );
  await userEvent.click(within(reply).getByRole("button", { name: "查看历史" }));
  expect(await screen.findByText("读取文件")).toBeVisible();
  expect(screen.getByRole("region", { name: "历史事件" })).toBeVisible();
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/runs/run/event-history?limit=100&after=1",
    expect.objectContaining({ method: "GET" }),
  );
});

it("keeps intake and completed upstream steps inside the process drawer while an A2A run is active and displays converged review badges", () => {
  const intakeStep = {
    ...step,
    id: "step-intake",
    role: "task_intake",
    agent_name: "协调者",
    status: "succeeded" as const,
  };
  const relayExec = {
    ...step,
    id: "step-exec",
    kind: "relay",
    from_step_id: "step-intake",
    from_agent_id: "agent-coord",
    role: "engineer",
    agent_name: "执行者",
    status: "succeeded" as const,
  };
  const relayRev = {
    ...step,
    id: "step-rev",
    kind: "relay",
    from_step_id: "step-exec",
    from_agent_id: "agent-exec",
    depends_on: ["step-exec"],
    role: "reviewer",
    agent_name: "质疑者",
    review_verdict: "approved",
    status: "running" as const,
  };
  const skippedRework = {
    ...step,
    id: "step-skip",
    depends_on: ["step-rev"],
    role: "engineer",
    agent_name: "执行者",
    status: "skipped" as const,
    converged: true,
  };
  render(
    <RunSegment
      {...props}
      run={{
        ...baseRun,
        status: "running",
        task_promotion_step_id: "step-intake",
      }}
      steps={[intakeStep, relayExec, relayRev, skippedRework]}
      messages={[
        { ...message("m-intake", "任务已建立，正在继续执行。"), step_id: "step-intake" },
        { ...message("m-exec", "执行者已完成修复"), step_id: "step-exec" },
        {
          ...message("m-rev", "质疑者正在独立复核"),
          step_id: "step-rev",
          status: "processing",
        },
      ]}
    />,
  );
  const processDrawer = screen.getByTestId("run-process");
  expect(within(processDrawer).getAllByText("任务已建立，正在继续执行。").length).toBeGreaterThan(0);
  expect(articles("任务已建立，正在继续执行。")[0].closest("details")).toBe(processDrawer);
  expect(articles("执行者已完成修复")[0].closest("details")).toBe(processDrawer);
  expect(articles("质疑者正在独立复核")[0].closest("details")).toBeNull();
  expect(within(processDrawer).getByText("复核通过")).toBeVisible();
  expect(within(processDrawer).getByText("已收敛跳过")).toBeVisible();
});

