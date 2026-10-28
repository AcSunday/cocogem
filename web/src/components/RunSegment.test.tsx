import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { Agent, Artifact, Message, Run, Step } from "../api/client";
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
        agents: [{ ...agent, model: "profile-model" }],
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
  expect(within(result).getByText("🐱")).toBeVisible();
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
      return Response.json(
        { error: { message: "加载失败" } },
        { status: 500 },
      );
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
