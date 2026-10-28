import { chooseOption } from "../test/select";
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type {
  Agent,
  Artifact,
  Attempt,
  Message,
  Run,
  RunInput,
  Step,
  System,
} from "../api/client";
import { App } from "./App";

const system: System = {
  models: ["default"],
  roots: [],
  model_status: "configured",
  search_status: "not_configured",
  verified: {},
  csrf_token: "test",
  limits: {},
  executors: [
    {
      id: "claude",
      installed: true,
      version: "1",
      supported: true,
      reason: "",
      adapter: "claude-stream-json",
      isolation: "enforced",
      protocol: "runtime",
    },
    {
      id: "codex",
      installed: true,
      version: "1",
      supported: true,
      reason: "只读工具范围由 Cocogem 管理",
      adapter: "codex-exec-json",
      isolation: "enforced",
      protocol: "runtime",
    },
    {
      id: "gemini",
      installed: true,
      version: "old",
      supported: false,
      reason: "缺少安全工具模式",
      adapter: "gemini-stream-json",
      isolation: "unavailable",
      protocol: "runtime",
    },
  ],
};
HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute("open", "");
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute("open");
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  delete document.documentElement.dataset.appearance;
  history.replaceState({}, "", "/");
});

it("applies and saves the appearance, including when browser storage is unavailable", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) =>
      Response.json(path === "/api/v1/system" ? system : { items: [] }),
    ),
  );
  render(<App />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("外观模式");
  expect(select).toHaveValue("system");
  await chooseOption(user, select, "dark");
  expect(document.documentElement).toHaveAttribute("data-appearance", "dark");
  expect(localStorage.getItem("cocogem-appearance")).toBe("dark");
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new DOMException("Storage is disabled", "SecurityError");
  });
  await chooseOption(user, select, "light");
  expect(select).toHaveValue("light");
  expect(document.documentElement).toHaveAttribute("data-appearance", "light");
  await chooseOption(user, select, "system");
  expect(document.documentElement).toHaveAttribute("data-appearance", "system");
});

it("keeps linked drafts and exact artifact versions through theme and stream updates", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "持续协作",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const agent: Agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "布偶猫",
    handle: "ragdoll",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-20T00:00:00Z",
  } as Run;
  const artifacts: Artifact[] = [1, 2].map((version) => ({
    id: "report",
    run_id: "run",
    title: "协作笔记",
    type: "note",
    version,
    body: `第 ${version} 版正文`,
    feedback: version === 1 ? "dismissed" : "accepted",
    source_ids: [],
    findings: [],
    limitations: [],
    created_at: "2026-10-20T00:00:00Z",
    feedback_at: "",
    revision: 1,
    step_id: "step",
    thread_id: "thread",
  }));
  let content = "第一段回复";
  const streams: { onmessage?: (event: MessageEvent) => void }[] = [];
  vi.stubGlobal(
    "EventSource",
    class {
      constructor() {
        streams.push(this);
      }
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "研究室" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?"))
        return Response.json({
          thread,
          messages: {
            items: [
              {
                id: "message",
                run_id: "run",
                agent_id: "one",
                agent_name: "布偶猫",
                role: "assistant",
                status: "completed",
                content,
                created_at: "2026-10-20T00:00:00Z",
              },
            ],
            next_cursor: "",
          },
          runs: [run],
          sources: [],
        });
      if (path.endsWith("/artifacts"))
        return Response.json({ items: artifacts });
      if (path === "/api/v1/runs/run") return Response.json({ run, steps: [] });
      return Response.json({ items: [] });
    }),
  );
  const { unmount } = render(<App />);
  const user = userEvent.setup();
  await screen.findByText("第一段回复");
  await user.type(
    screen.getByLabelText("输入问题"),
    "保留我的草稿 https://example.com/research",
  );
  await chooseOption(user, screen.getByLabelText("外观模式"), "dark");
  await waitFor(() => expect(streams.at(-1)).toBeTruthy());
  content = "第二段流式回复";
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({ run_id: "run", seq: 1, type: "message_delta" }),
    } as MessageEvent),
  );
  await screen.findByText(content);
  expect(screen.getByLabelText("输入问题")).toHaveValue(
    "保留我的草稿 https://example.com/research",
  );
  expect(screen.queryByLabelText("资料链接")).not.toBeInTheDocument();
  const results = within(screen.getByLabelText("第 1 次协作产物"));
  await user.click(results.getByRole("button", { name: /v1/ }));
  expect(screen.getByRole("dialog")).toHaveTextContent("第 1 版正文");
  expect(screen.getByRole("button", { name: "已搁置" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await user.click(screen.getByRole("button", { name: "关闭窗口" }));
  await user.click(results.getByRole("button", { name: /v2/ }));
  expect(screen.getByRole("dialog")).toHaveTextContent("第 2 版正文");
  expect(screen.getByRole("button", { name: "已接受" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  expect(sessionStorage.getItem("cocogem-drafts")).toContain("保留我的草稿");
  unmount();
  render(<App />);
  expect(await screen.findByLabelText("输入问题")).toHaveValue(
    "保留我的草稿 https://example.com/research",
  );
});

it("verifies the selected local CLI and exact model, with unsupported versions disabled", async () => {
  const info = structuredClone(system);
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/v1/system/verify") {
      const input = JSON.parse(String(options?.body));
      info.verified[`${input.executor}:${input.model}`] =
        "2026-10-19T00:00:00Z";
      return Response.json({ ...input, verified_at: "2026-10-19T00:00:00Z" });
    }
    return Response.json(path === "/api/v1/system" ? info : { items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "设置" }));
  await user.click(screen.getByRole("tab", { name: "服务" }));
  expect(screen.getByText(/已安装不代表连接或模型可用/)).toBeInTheDocument();
  await chooseOption(user, screen.getByLabelText("验证 CLI"), "codex");
  expect(
    screen.getByText(/说明：只读工具范围由 Cocogem 管理/),
  ).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "验证模型连接（可能产生用量）" }),
  ).toBeEnabled();
  expect(
    screen.getByLabelText("验证模型（留空使用 CLI 默认）"),
  ).not.toBeRequired();
  await user.click(
    screen.getByRole("button", { name: "验证模型连接（可能产生用量）" }),
  );
  await waitFor(() =>
    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/system/verify",
      expect.objectContaining({
        body: JSON.stringify({ executor: "codex", model: "default" }),
      }),
    ),
  );
  await screen.findByText(/该 CLI 与模型上次已验证/);
  await user.type(
    screen.getByLabelText("验证模型（留空使用 CLI 默认）"),
    "chosen-model",
  );
  await user.click(
    screen.getByRole("button", { name: "验证模型连接（可能产生用量）" }),
  );
  await waitFor(() =>
    expect(fetcher).toHaveBeenCalledWith(
      "/api/v1/system/verify",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ executor: "codex", model: "chosen-model" }),
      }),
    ),
  );
  expect(
    await screen.findByText(/该 CLI 与模型上次已验证/),
  ).toBeInTheDocument();
  await chooseOption(user, screen.getByLabelText("验证 CLI"), "gemini");
  expect(
    screen.getByRole("button", { name: "验证模型连接（可能产生用量）" }),
  ).toBeDisabled();
  expect(screen.getByText(/不支持：缺少安全工具模式/)).toBeInTheDocument();
});

it("applies edited collaboration defaults to a new conversation without losing the draft", async () => {
  const agents: Agent[] = ["one", "two", "three"].map((id) => ({
    id,
    workspace_id: "ws",
    display_name: id,
    handle: id,
    aliases: [],
    role_description: "",
    model: "default",
    executor: "codex",
    tools: [],
    disabled: false,
    revision: 1,
  }));
  let settings = {
    revision: 1,
    default_coordinator_id: "one",
    assignments: {
      research: { researcher: "one", critic: "two", coordinator: "one" },
    } as Record<string, Record<string, string>>,
  };
  const thread = {
    id: "new-thread",
    workspace_id: "ws",
    title: "新会话",
    agent_ids: ["three", "one", "two"],
    coordinator_id: "three",
    workflow: "free",
    revision: 1,
    updated_at: "",
  };
  let createBody: Record<string, unknown> | undefined;
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [{ id: "ws", name: "工作空间" }] });
    if (path === "/api/v1/agents") return Response.json({ items: agents });
    if (path === "/api/v1/workspaces/ws/agents")
      return Response.json({ items: agents });
    if (path === "/api/v1/settings" && options?.method === "PATCH") {
      const input = JSON.parse(String(options.body));
      settings = { revision: settings.revision + 1, ...input };
      return Response.json(settings);
    }
    if (path === "/api/v1/settings") return Response.json(settings);
    if (
      path === "/api/v1/workspaces/ws/threads" &&
      options?.method === "POST"
    ) {
      createBody = JSON.parse(String(options.body));
      return Response.json(thread);
    }
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [], next_cursor: "" });
    if (path.startsWith("/api/v1/threads/new-thread?"))
      return Response.json({
        thread,
        messages: { items: [], next_cursor: "" },
        runs: [],
        sources: [],
      });
    if (path === "/api/v1/threads/new-thread/routing-preview")
      return Response.json({
        routing: { decision: "discuss", clarification: "等待确认", targets: [] },
      });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  const draft = "先写好的问题，不能因打开设置丢失";
  await user.type(await screen.findByLabelText("输入问题"), draft);
  await screen.findByText("协调者：one");
  await user.click(screen.getByRole("button", { name: "设置" }));
  await user.click(screen.getByRole("tab", { name: "协作" }));
  await chooseOption(user, screen.getByLabelText("默认协调者"), "three");
  await chooseOption(user, screen.getByLabelText("研究"), "three");
  await user.click(screen.getByRole("button", { name: "保存协作默认" }));
  await screen.findByText("协作默认已保存，新会话会使用这些选择。");
  await screen.findByText("协调者：three");
  const saved = fetcher.mock.calls.find(
    ([path, options]) =>
      path === "/api/v1/settings" &&
      (options as RequestInit | undefined)?.method === "PATCH",
  );
  expect(JSON.parse(String((saved?.[1] as RequestInit).body))).toMatchObject({
    default_coordinator_id: "three",
    assignments: { research: { researcher: "three" } },
  });
  await user.click(screen.getByRole("button", { name: "关闭窗口" }));
  expect(screen.getByLabelText("输入问题")).toHaveValue(draft);
  await user.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() => expect(createBody).toBeTruthy());
  expect(createBody?.agent_ids).toEqual(["three", "one", "two"]);
  // Omitting the coordinator lets the server apply the saved default.
  expect(createBody).not.toHaveProperty("coordinator_id");
});

it("shows a visible coordinator fallback when the global default is unavailable", async () => {
  const agents: Agent[] = ["one", "two"].map((id) => ({
    id,
    workspace_id: "ws",
    display_name: id,
    handle: id,
    aliases: [],
    role_description: "",
    model: "default",
    executor: "codex",
    tools: ["publish_artifact"],
    disabled: false,
    revision: 1,
  }));
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "工作空间" }] });
      if (path === "/api/v1/workspaces/ws/agents")
        return Response.json({ items: agents });
      if (path === "/api/v1/agents") return Response.json({ items: agents });
      if (path === "/api/v1/settings")
        return Response.json({
          revision: 1,
          default_coordinator_id: "gone",
          assignments: {},
        });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [], next_cursor: "" });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  expect(
    await screen.findByText("默认协调者不可用，使用two"),
  ).toBeVisible();
});

it.each([
  { search: "configured", question: "比较协作的取舍" },
  { search: "not_configured", question: "比较协作的取舍" },
  { search: "configured", question: "研究 https://example.org/topic" },
  { search: "not_configured", question: "研究 https://example.org/topic" },
])(
  "sends automatic research with saved conversation roles ($search, $question)",
  async ({ search, question }) => {
    history.replaceState({}, "", "/w/ws/t/two");
    const agents: Agent[] = ["disabled", "one", "two", "outside"].map((id) => ({
      id,
      workspace_id: "ws",
      display_name: id,
      handle: id,
      aliases: [],
      role_description: "",
      model: "default",
      executor: "codex",
      tools: ["publish_artifact"],
      disabled: id === "disabled",
      revision: 1,
    }));
    const thread = {
      id: "two",
      workspace_id: "ws",
      title: "双成员研究",
      agent_ids: ["one", "two"],
      coordinator_id: "two",
      workflow: "research",
      revision: 1,
      updated_at: "",
    };
    const previewInputs: RunInput[] = [];
    const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
      if (path === "/api/v1/system")
        return Response.json({ ...system, search_status: search });
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "工作空间" }] });
      if (path === "/api/v1/workspaces/ws/agents")
        return Response.json({ items: agents });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/two?"))
        return Response.json({
          thread,
          messages: { items: [], next_cursor: "" },
          runs: [],
          sources: [],
        });
      if (path === "/api/v1/threads/two/routing-preview") {
        previewInputs.push(JSON.parse(String(options?.body)));
        return Response.json({
          routing: {
            decision: "assign",
            workflow: "research",
            targets: [],
            assignments: {
              researcher: "one",
              critic: "two",
              coordinator: "two",
            },
            clarification: "保留问题等待确认",
          },
        });
      }
      return Response.json({ items: [] });
    });
    vi.stubGlobal("fetch", fetcher);
    render(<App />);
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "双成员研究" });
    await waitFor(() =>
      expect(screen.getByLabelText("研究")).toHaveValue("one"),
    );
    expect(screen.getByLabelText("工作方式")).toHaveValue("research");
    expect(screen.getByLabelText("质疑")).toHaveValue("two");
    expect(screen.getByLabelText("汇总")).toHaveValue("two");
    await user.type(screen.getByLabelText("输入问题"), question);
    await user.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(previewInputs).toHaveLength(1));
    const previewInput = previewInputs[0];
    expect(previewInput.targets).toEqual([]);
    expect(previewInput.research_scope).toBe("auto");
    expect(previewInput.message).toBe(question);
    expect(previewInput.source_urls).toEqual([]);
    expect(screen.queryByLabelText("资料链接")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("研究范围")).not.toBeInTheDocument();
    // Saved defaults are not sent as manual overrides; the server resolves them.
    expect(previewInput.assignments).toEqual({});
    // The accepted preview's effective roles are what the composer reports.
    expect(screen.getByLabelText(/^成员与分工/)).toHaveAccessibleName(
      "成员与分工：研究：one · 质疑：two · 汇总：two",
    );

    // Manual role overrides still travel; omitted roles stay server-resolved.
    await chooseOption(user, screen.getByLabelText("研究"), "two");
    await user.click(screen.getByRole("button", { name: "发送" }));
    await waitFor(() => expect(previewInputs).toHaveLength(2));
    expect(previewInputs[1].assignments).toEqual({ researcher: "two" });
    expect(Object.values(previewInputs[1].assignments ?? {})).not.toContain(
      "disabled",
    );
    expect(Object.values(previewInputs[1].assignments ?? {})).not.toContain(
      "outside",
    );
  },
);

it("loads persisted handoffs and stops their whole collaboration with the composer control", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agents: Agent[] = ["coordinator", "researcher"].map((id) => ({
    id,
    workspace_id: "",
    display_name: id === "coordinator" ? "协调成员" : "研究成员",
    handle: id,
    aliases: [],
    role_description: "",
    model: "default",
    executor: "codex",
    tools: [],
    disabled: false,
    revision: 1,
  }));
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "持续交接",
    agent_ids: agents.map((agent) => agent.id),
    coordinator_id: "coordinator",
    workflow: "free",
    revision: 1,
    updated_at: "",
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents,
    status: "running",
    created_at: "2026-10-24T00:00:00Z",
  } as Run;
  const steps: Step[] = [
    {
      id: "parent",
      run_id: "run",
      agent_id: "coordinator",
      agent_name: "协调成员",
      instruction: "安排工作",
      role: "coordinator",
      depends_on: [],
      status: "succeeded",
      error: "",
    },
    {
      id: "child",
      run_id: "run",
      agent_id: "researcher",
      agent_name: "研究成员",
      instruction: "检索当前资料并记录来源。",
      role: "researcher",
      kind: "handoff",
      from_agent_id: "coordinator",
      from_step_id: "parent",
      depends_on: ["parent"],
      status: "running",
      error: "",
    },
    {
      id: "return",
      run_id: "run",
      agent_id: "coordinator",
      agent_name: "协调成员",
      instruction: "接收研究结果后汇总",
      role: "coordinator",
      kind: "continuation",
      depends_on: ["child"],
      status: "pending",
      error: "",
    },
  ];
  vi.stubGlobal(
    "EventSource",
    class {
      close() {}
    },
  );
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [{ id: "ws", name: "协作空间" }] });
    if (path === "/api/v1/workspaces/ws/agents")
      return Response.json({ items: agents });
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [thread], next_cursor: "" });
    if (path.startsWith("/api/v1/threads/thread?"))
      return Response.json({
        thread,
        messages: { items: [], next_cursor: "" },
        runs: [run],
        sources: [],
      });
    if (path === "/api/v1/runs/run") return Response.json({ run, steps });
    if (path === "/api/v1/runs/run/cancel" && options?.method === "POST") {
      run.status = "canceled";
      for (const step of steps)
        if (step.status !== "succeeded") step.status = "canceled";
      return Response.json(run);
    }
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  const { unmount } = render(<App />);
  const user = userEvent.setup();
  await userEvent.click(
    await screen.findByText("协作过程", { selector: "summary" }),
  );
  const tasks = within(await screen.findByRole("list", { name: "协作任务" }));
  expect(tasks.getByText("协调成员 →")).toBeVisible();
  expect(tasks.getByText("等待结果")).toBeVisible();
  await user.click(tasks.getByText("研究成员", { selector: "strong" }));
  expect(tasks.getByText("检索当前资料并记录来源。")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "停止协作" }));
  await waitFor(() =>
    expect(screen.getByTestId("run-status")).toHaveTextContent("已停止"),
  );
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/runs/run/cancel",
    expect.objectContaining({ method: "POST" }),
  );
  expect(tasks.queryByText("等待结果")).not.toBeInTheDocument();
  unmount();
  render(<App />);
  expect(await screen.findByTestId("run-status")).toHaveTextContent("已停止");
  expect(
    await screen.findByRole("list", { name: "协作任务" }),
  ).toHaveTextContent("研究成员");
});

it("uses the run's member snapshot for message avatars, not the current profile", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const current: Agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "布偶猫",
    handle: "ragdoll",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "codex",
    tools: [],
    disabled: false,
    revision: 2,
    avatar: "🐱",
  };
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "头像快照",
    agent_ids: ["one"],
    coordinator_id: "one",
    workflow: "free",
    revision: 1,
    updated_at: "",
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [{ ...current, avatar: "🦊", revision: 1 }],
    status: "succeeded",
    created_at: "2026-10-24T00:00:00Z",
  } as Run;
  const message = {
    id: "message",
    thread_id: "thread",
    run_id: "run",
    step_id: "step",
    agent_id: "one",
    agent_name: "布偶猫",
    role: "assistant",
    content: "历史回复",
    status: "completed",
    created_at: "2026-10-24T00:00:01Z",
  } as Message;
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [{ id: "ws", name: "协作空间" }] });
    if (path === "/api/v1/workspaces/ws/agents")
      return Response.json({ items: [current] });
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [thread], next_cursor: "" });
    if (path.startsWith("/api/v1/threads/thread?"))
      return Response.json({
        thread,
        messages: { items: [message], next_cursor: "" },
        runs: [run],
        sources: [],
      });
    if (path === "/api/v1/runs/run")
      return Response.json({ run, steps: [], attempts: [] });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const timeline = await screen.findByTestId("conversation-timeline");
  expect(await within(timeline).findByText("历史回复")).toBeVisible();
  expect(within(timeline).getByText("🦊")).toBeVisible();
  expect(within(timeline).queryByText("🐱")).toBeNull();
});

it("sends workspace skill selections to the API when editing", async () => {
  history.replaceState({}, "", "/w/ws");
  const workspace = {
    id: "ws",
    name: "技能空间",
    description: "",
    root: "",
    skill_ids: ["task-handoff"],
    revision: 2,
    created_at: "",
  };
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/v1/system")
      return Response.json({
        ...system,
        skills: [
          {
            id: "task-handoff",
            name: "task-handoff",
            description: "按任务交接",
            version: "version-one",
          },
        ],
      });
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [workspace] });
    if (path === "/api/v1/workspaces/ws" && options?.method === "PATCH")
      return Response.json({
        ...workspace,
        ...JSON.parse(String(options.body)),
      });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "编辑工作空间" }));
  await user.click(screen.getByRole("checkbox", { name: /任务交接/ }));
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/workspaces/ws",
    expect.objectContaining({
      method: "PATCH",
      body: JSON.stringify({
        name: "技能空间",
        description: "",
        root: "",
        skill_ids: [],
      }),
    }),
  );
});

it("preserves the current conversation while saving coordinator and workspace directory changes", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  let workspace = {
    id: "ws",
    name: "研究室",
    description: "",
    root: "",
    skill_ids: null,
    revision: 1,
    created_at: "",
  };
  let thread = {
    id: "thread",
    workspace_id: "ws",
    title: "保留原会话",
    agent_ids: ["one", "two"],
    coordinator_id: "one",
    workflow: "free",
    revision: 1,
    updated_at: "2026-10-24T00:00:00Z",
  };
  const agents: Agent[] = ["one", "two"].map((id) => ({
    id,
    workspace_id: "ws",
    display_name: id === "one" ? "研究成员" : "协调成员",
    handle: id,
    aliases: [],
    role_description: "",
    model: "default",
    executor: "codex",
    tools: [],
    disabled: false,
    revision: 1,
  }));
  const run = {
    id: "saved-run",
    input: { workflow: "free" },
    agents,
    status: "succeeded",
    created_at: "2026-10-24T00:00:00Z",
  } as Run;
  const requests: string[] = [];
  vi.stubGlobal(
    "EventSource",
    class {
      close() {}
    },
  );
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => {
    requests.push(`${options?.method ?? "GET"} ${path}`);
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [workspace] });
    if (path === "/api/v1/workspaces/ws" && options?.method === "PATCH") {
      workspace = {
        ...workspace,
        ...JSON.parse(String(options.body)),
        revision: workspace.revision + 1,
      };
      return Response.json(workspace);
    }
    if (path.endsWith("/agents")) return Response.json({ items: agents });
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [thread], next_cursor: "" });
    if (path === "/api/v1/threads/thread" && options?.method === "PATCH") {
      thread = {
        ...thread,
        ...JSON.parse(String(options.body)),
        revision: thread.revision + 1,
      };
      return Response.json(thread);
    }
    if (path.startsWith("/api/v1/threads/thread?"))
      return Response.json({
        thread,
        messages: {
          items: [
            {
              id: "retained-message",
              run_id: run.id,
              agent_id: "one",
              agent_name: "研究成员",
              role: "assistant",
              status: "completed",
              content: "之前的研究结论必须一直保留",
              created_at: "2026-10-24T00:00:00Z",
            },
          ],
          next_cursor: "",
        },
        runs: [run],
        sources: [],
      });
    if (path.endsWith("/artifacts")) return Response.json({ items: [] });
    if (path === "/api/v1/runs/saved-run")
      return Response.json({ run, steps: [] });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  const user = userEvent.setup();
  render(<App />);
  await screen.findByText("之前的研究结论必须一直保留");
  await user.type(
    screen.getByLabelText("输入问题"),
    "还没发送的草稿 https://example.com/retained",
  );
  await user.click(screen.getByRole("button", { name: "会话设置" }));
  await chooseOption(
    user,
    screen.getByRole("combobox", { name: "协调者" }),
    "two",
  );
  expect(screen.getByText("之前的研究结论必须一直保留")).toBeInTheDocument();
  expect(requests.some((request) => request.startsWith("PATCH"))).toBe(false);
  const push = vi.spyOn(history, "pushState");
  await user.click(screen.getByRole("button", { name: "保存会话" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(screen.getByText("之前的研究结论必须一直保留")).toBeInTheDocument();
  expect(screen.getByLabelText("输入问题")).toHaveValue(
    "还没发送的草稿 https://example.com/retained",
  );
  expect(screen.queryByLabelText("资料链接")).not.toBeInTheDocument();
  expect(screen.getByTestId("run-status")).toHaveTextContent("已完成");
  expect(location.pathname).toBe("/w/ws/t/thread");
  expect(push).not.toHaveBeenCalled();
  expect(requests.filter((request) => request.startsWith("PATCH"))).toEqual([
    "PATCH /api/v1/threads/thread",
  ]);
  expect(requests.some((request) => request.startsWith("POST"))).toBe(false);
  await user.click(screen.getByRole("button", { name: "会话设置" }));
  expect(screen.getByRole("combobox", { name: "协调者" })).toHaveValue("two");
  await user.click(screen.getByRole("button", { name: "关闭窗口" }));
  await user.click(screen.getByRole("button", { name: "编辑工作空间" }));
  await chooseOption(
    user,
    screen.getByRole("combobox", { name: "本地资料目录" }),
    "custom-root",
  );
  await user.type(screen.getByLabelText("目录路径"), "/public/sources");
  await user.click(screen.getByRole("button", { name: "保存工作空间" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(workspace.root).toBe("/public/sources");
  expect(screen.getByText("之前的研究结论必须一直保留")).toBeInTheDocument();
  expect(screen.getByLabelText("输入问题")).toHaveValue(
    "还没发送的草稿 https://example.com/retained",
  );
  expect(screen.queryByLabelText("资料链接")).not.toBeInTheDocument();
  expect(location.pathname).toBe("/w/ws/t/thread");
  expect(push).not.toHaveBeenCalled();
});

it("renames a conversation with If-Match, keeps its members and retains a failed draft", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const workspace = {
    id: "ws",
    name: "研究室",
    description: "",
    root: "",
    skill_ids: null,
    revision: 1,
    created_at: "",
  };
  let thread = {
    id: "thread",
    workspace_id: "ws",
    title: "保留原会话",
    agent_ids: ["one", "two"],
    coordinator_id: "two",
    workflow: "research",
    revision: 4,
    updated_at: "2026-10-24T00:00:00Z",
  };
  const agents: Agent[] = ["one", "two"].map((id) => ({
    id,
    workspace_id: "ws",
    display_name: id,
    handle: id,
    aliases: [],
    role_description: "",
    model: "default",
    executor: "codex",
    tools: [],
    disabled: false,
    revision: 1,
  }));
  const patches: { body: Record<string, unknown>; options?: RequestInit }[] =
    [];
  let conflict = true;
  vi.stubGlobal(
    "EventSource",
    class {
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options?: RequestInit) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [workspace] });
      if (path.endsWith("/agents")) return Response.json({ items: agents });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path === "/api/v1/threads/thread" && options?.method === "PATCH") {
        const body = JSON.parse(String(options.body));
        patches.push({ body, options });
        if (conflict) {
          conflict = false;
          return Response.json(
            { error: { message: "会话已在别处更新，请重新打开后再试" } },
            { status: 412 },
          );
        }
        thread = { ...thread, ...body, revision: thread.revision + 1 };
        return Response.json(thread);
      }
      if (path.startsWith("/api/v1/threads/thread?"))
        return Response.json({
          thread,
          messages: { items: [], next_cursor: "" },
          runs: [],
          sources: [],
        });
      return Response.json({ items: [] });
    }),
  );
  const user = userEvent.setup();
  render(<App />);
  const trigger = await screen.findByRole("button", {
    name: "更多会话操作：保留原会话",
  });
  await user.click(trigger);
  await user.click(screen.getByRole("menuitem", { name: "重命名" }));
  const input = screen.getByLabelText("会话标题");
  expect(input).toHaveValue("保留原会话");
  await user.clear(input);
  await user.type(input, "新的标题");
  await user.click(screen.getByRole("button", { name: "保存名称" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "会话已在别处更新，请重新打开后再试",
  );
  expect(input).toHaveValue("新的标题");
  await user.click(screen.getByRole("button", { name: "保存名称" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(patches).toHaveLength(2);
  for (const patch of patches) {
    expect(patch.options?.headers).toEqual(
      expect.objectContaining({ "If-Match": "4" }),
    );
    expect(patch.body).toEqual({ title: "新的标题" });
  }
  expect(
    screen.getByRole("button", { name: "更多会话操作：新的标题" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "新的标题" })).toBeInTheDocument();
});

it("clears a stale tool-wait warning after confirmed progress", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "合成进度测试",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  let content = "工具执行前";
  const streams: { onmessage?: (event: MessageEvent) => void }[] = [];
  vi.stubGlobal(
    "EventSource",
    class {
      constructor() {
        streams.push(this);
      }
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?"))
        return Response.json({
          thread,
          messages: {
            items: [
              {
                id: "message",
                run_id: "run",
                agent_id: "one",
                agent_name: "合成成员",
                role: "assistant",
                status: "processing",
                content,
                created_at: "2026-10-26T00:00:00Z",
              },
            ],
            next_cursor: "",
          },
          runs: [run],
          sources: [],
        });
      if (path.endsWith("/artifacts")) return Response.json({ items: [] });
      if (path === "/api/v1/runs/run")
        return Response.json({ run, steps: [], attempts: [] });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  await screen.findByText("工具执行前");
  await waitFor(() => expect(streams.at(-1)).toBeTruthy());
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 1,
        type: "liveness_warning",
        data: { state: "waiting_tool", silence_seconds: 1 },
      }),
    } as MessageEvent),
  );
  await screen.findByText("等待工具 · 已 1 秒");
  content = "工具已完成且模型继续回复";
  act(() => {
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 2,
        type: "tool_finished",
        data: { status: "ok" },
      }),
    } as MessageEvent);
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 3,
        type: "message_delta",
        data: { message_id: "message", mode: "append", text: content },
      }),
    } as MessageEvent);
  });
  await screen.findByText(content);
  expect(screen.queryByText("等待工具 · 已 1 秒")).toBeNull();
});

it("pages conversation history one page at a time without accumulating it", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "长会话",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const agent: Agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "布偶猫",
    handle: "ragdoll",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  };
  const reply = (id: string, content: string) => ({
    id,
    thread_id: "thread",
    run_id: "run",
    step_id: "step",
    agent_id: "one",
    agent_name: "布偶猫",
    role: "assistant",
    content,
    status: "completed",
    created_at: "2026-10-20T00:00:00Z",
  });
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "succeeded",
    created_at: "2026-10-20T00:00:00Z",
  } as Run;
  vi.stubGlobal(
    "EventSource",
    class {
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [{ id: "ws", name: "研究室" }] });
    if (path === "/api/v1/workspaces/ws/agents")
      return Response.json({ items: [agent] });
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [thread], next_cursor: "" });
    if (path.startsWith("/api/v1/threads/thread?")) {
      if (path.includes("cursor=older"))
        return Response.json({
          thread,
          messages: { items: [reply("old", "更早的回复")], next_cursor: "" },
          runs: [run],
          sources: [],
        });
      return Response.json({
        thread,
        messages: {
          items: [reply("latest", "最新的回复")],
          next_cursor: "older",
        },
        runs: [run],
        sources: [],
      });
    }
    if (path === "/api/v1/runs/run")
      return Response.json({ run, steps: [], attempts: [], cursor: 0 });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  expect(await screen.findByText("最新的回复")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "加载更早的消息" }));
  expect(await screen.findByText("更早的回复")).toBeVisible();
  expect(screen.queryByText("最新的回复")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "回到最新消息" }));
  expect(await screen.findByText("最新的回复")).toBeVisible();
  expect(screen.queryByText("更早的回复")).not.toBeInTheDocument();
  const threadCalls = fetcher.mock.calls.filter((call) =>
    String(call[0]).startsWith("/api/v1/threads/thread?"),
  );
  // Initial load, one bounded seed refresh, then the two explicit page loads.
  expect(threadCalls).toHaveLength(4);
  expect(String(threadCalls.at(-1)?.[0])).toContain("limit=100");
});

it("keeps a liveness warning after activity events evict it from the window", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "合成进度测试",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  const streams: { onmessage?: (event: MessageEvent) => void }[] = [];
  vi.stubGlobal(
    "EventSource",
    class {
      constructor() {
        streams.push(this);
      }
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?"))
        return Response.json({
          thread,
          messages: {
            items: [
              {
                id: "message",
                run_id: "run",
                agent_id: "one",
                agent_name: "合成成员",
                role: "assistant",
                status: "processing",
                content: "正在准备回复",
                created_at: "2026-10-26T00:00:00Z",
              },
            ],
            next_cursor: "",
          },
          runs: [run],
          sources: [],
        });
      if (path === "/api/v1/runs/run")
        return Response.json({ run, steps: [], attempts: [], cursor: 0 });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  await screen.findByText("正在准备回复");
  await waitFor(() => expect(streams.at(-1)).toBeTruthy());
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 1,
        type: "liveness_warning",
        data: { state: "waiting_tool", silence_seconds: 5 },
      }),
    } as MessageEvent),
  );
  await screen.findByText("等待工具 · 已 5 秒");
  act(() => {
    for (let seq = 2; seq <= 540; seq++)
      streams.at(-1)?.onmessage?.({
        data: JSON.stringify({
          run_id: "run",
          seq,
          type: "native_notice",
          data: { kind: "status" },
        }),
      } as MessageEvent);
  });
  expect(screen.getByText("等待工具 · 已 5 秒")).toBeVisible();

  // Once confirmed progress clears it, replaying the evicted warning must not
  // bring it back.
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 541,
        type: "tool_finished",
        data: { tool_call_id: "call", name: "read_file", status: "ok" },
      }),
    } as MessageEvent),
  );
  await waitFor(() =>
    expect(screen.queryByText("等待工具 · 已 5 秒")).toBeNull(),
  );
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 1,
        type: "liveness_warning",
        data: { state: "waiting_tool", silence_seconds: 5 },
      }),
    } as MessageEvent),
  );
  expect(screen.queryByText("等待工具 · 已 5 秒")).toBeNull();
});

it("restores a warning outside the 500-event window from the attempt snapshot and clears a nil snapshot", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "持久告警测试",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  let warning: Attempt["liveness_warning"] = {
    state: "stalled",
    silence_seconds: 900,
    last_event_type: "usage",
    process_alive: true,
    tool_active: false,
    observed_at: "2026-10-26T00:15:00Z",
  };
  const attempts = () => [
    {
      id: "attempt",
      run_id: "run",
      step_id: "step",
      number: 1,
      status: "running",
      started_at: "2026-10-26T00:00:00Z",
      liveness_warning: warning,
    },
  ];
  // The bounded window holds 500 ordinary events and no warning at all.
  const windowEvents = Array.from({ length: 500 }, (_, index) => ({
    schema_version: 1,
    run_id: "run",
    seq: index + 101,
    type: "native_notice",
    occurred_at: "",
    data: { kind: "status" },
  }));
  const streams: { onmessage?: (event: MessageEvent) => void }[] = [];
  vi.stubGlobal(
    "EventSource",
    class {
      constructor() {
        streams.push(this);
      }
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?"))
        return Response.json({
          thread,
          messages: { items: [], next_cursor: "" },
          runs: [run],
          sources: [],
        });
      if (path === "/api/v1/runs/run")
        return Response.json({
          run,
          steps: [],
          attempts: attempts(),
          cursor: 600,
        });
      if (path.startsWith("/api/v1/runs/run/event-history"))
        return Response.json({ items: windowEvents, next_cursor: "" });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  expect(await screen.findByText("长时间无进展 · 已 900 秒")).toBeVisible();
  await waitFor(() => expect(streams.at(-1)).toBeTruthy());

  // A non-clearing frame triggers a refresh; the nil snapshot is authoritative.
  warning = null;
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 601,
        type: "usage_updated",
        data: { known: false },
      }),
    } as MessageEvent),
  );
  await waitFor(() =>
    expect(screen.queryByText("长时间无进展 · 已 900 秒")).toBeNull(),
  );
});

it("loads source bodies from the source endpoint instead of the metadata list", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "来源测试",
    agent_ids: [],
    workflow: "free",
    revision: 1,
  };
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [{ id: "ws", name: "研究室" }] });
    if (path === "/api/v1/workspaces/ws/agents")
      return Response.json({ items: [] });
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [thread], next_cursor: "" });
    if (path.startsWith("/api/v1/threads/thread?"))
      return Response.json({
        thread,
        messages: { items: [], next_cursor: "" },
        runs: [],
        sources: [
          {
            id: "src",
            kind: "web",
            title: "公开资料",
            url: "https://example.com/source",
            path: "",
            content: "",
            captured_at: "2026-10-20T00:00:00Z",
            truncated: false,
          },
        ],
      });
    if (path === "/api/v1/sources/src")
      return Response.json({
        id: "src",
        kind: "web",
        title: "公开资料",
        url: "https://example.com/source",
        path: "",
        content: "捕获的完整正文",
        captured_at: "2026-10-20T00:00:00Z",
        truncated: false,
      });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "来源测试" });
  await user.click(screen.getByRole("button", { name: "打开协作面板" }));
  expect(screen.getByRole("tab", { name: /产物/ })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(
    screen.queryByRole("tab", { name: "成员" }),
  ).not.toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: /来源/ }));
  await user.click(screen.getByRole("button", { name: /公开资料/ }));
  expect(await screen.findByText("捕获的完整正文")).toBeVisible();
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/sources/src",
    expect.objectContaining({ method: "GET" }),
  );
});

it("loads one page of run event history only when asked", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "历史事件测试",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  vi.stubGlobal(
    "EventSource",
    class {
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
    if (path.endsWith("/agents")) return Response.json({ items: [agent] });
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [thread], next_cursor: "" });
    if (path.startsWith("/api/v1/threads/thread?"))
      return Response.json({
        thread,
        messages: { items: [], next_cursor: "" },
        runs: [run],
        sources: [],
      });
    if (path === "/api/v1/runs/run")
      return Response.json({ run, steps: [], attempts: [], cursor: 600 });
    if (path.startsWith("/api/v1/runs/run/event-history")) {
      // The initial seed only covers the recent window; older events stay on
      // the explicit history page.
      if (path.includes("limit=500"))
        return Response.json({ items: [], next_cursor: "" });
      return Response.json({
        items: [
          {
            schema_version: 1,
            run_id: "run",
            seq: 2,
            type: "tool_started",
            occurred_at: "",
            agent_id: "one",
            step_id: "step",
            data: { tool_call_id: "call", name: "read_file", input: {} },
          },
          {
            schema_version: 1,
            run_id: "run",
            seq: 3,
            type: "tool_finished",
            occurred_at: "",
            agent_id: "one",
            step_id: "step",
            data: {
              tool_call_id: "call",
              status: "ok",
              duration_ms: 5,
              output: "历史结果",
            },
          },
        ],
        next_cursor: "",
      });
    }
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "历史事件测试" });
  await user.click(screen.getByRole("button", { name: "打开协作面板" }));
  await user.click(screen.getByRole("tab", { name: "过程" }));
  await user.click(screen.getByRole("button", { name: "浏览历史事件" }));
  expect(await screen.findByText("读取文件")).toBeVisible();
  expect(screen.getByText("完成")).toBeVisible();
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/runs/run/event-history?limit=100",
    expect.objectContaining({ method: "GET" }),
  );
});

it("seeds the recent event window and resumes live from the snapshot cursor", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "种子测试",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  const streams: string[] = [];
  vi.stubGlobal(
    "EventSource",
    class {
      constructor(url: string) {
        streams.push(url);
      }
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  const fetcher = vi.fn(async (path: string) => {
    if (path === "/api/v1/system") return Response.json(system);
    if (path === "/api/v1/workspaces")
      return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
    if (path.endsWith("/agents")) return Response.json({ items: [agent] });
    if (path.startsWith("/api/v1/workspaces/ws/threads"))
      return Response.json({ items: [thread], next_cursor: "" });
    if (path.startsWith("/api/v1/threads/thread?"))
      return Response.json({
        thread,
        messages: { items: [], next_cursor: "" },
        runs: [run],
        sources: [],
      });
    if (path === "/api/v1/runs/run")
      return Response.json({ run, steps: [], attempts: [], cursor: 620 });
    if (path.startsWith("/api/v1/runs/run/event-history"))
      return Response.json({
        items: [
          {
            schema_version: 1,
            run_id: "run",
            seq: 600,
            type: "tool_started",
            occurred_at: "",
            agent_id: "one",
            step_id: "step",
            data: { tool_call_id: "call", name: "read_file", input: {} },
          },
        ],
        next_cursor: "",
      });
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "种子测试" });
  await waitFor(() => expect(streams).toHaveLength(1));
  expect(screen.getByTestId("latest-tool")).toHaveTextContent(
    "最近工具：读取文件 · 执行中",
  );
  expect(streams[0]).toBe("/api/v1/runs/run/events?after=620");
  expect(fetcher).toHaveBeenCalledWith(
    "/api/v1/runs/run/event-history?limit=500&after=120",
    expect.objectContaining({ method: "GET" }),
  );
  await user.click(screen.getByRole("button", { name: "打开协作面板" }));
  await user.click(screen.getByRole("tab", { name: "过程" }));
  expect(await screen.findByText("读取文件")).toBeVisible();
});

it("merges a bounded seed history into the run view without repeating or dropping steps", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "合并去重",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "succeeded",
    created_at: "2026-10-26T00:00:00Z",
    finished_at: "2026-10-26T00:05:00Z",
    error: "",
  } as Run;
  const names = ["成员一", "成员二", "成员三", "成员四", "成员五"];
  const steps = names.map(
    (name, index) =>
      ({
        id: `step-${index}`,
        run_id: "run",
        agent_id: "one",
        agent_name: name,
        instruction: `${name}的任务`,
        role: "researcher",
        depends_on: [],
        status: "succeeded",
        error: "",
      }) as Step,
  );
  const attempts = steps.map(
    (step, index) =>
      ({
        id: `attempt-${index}`,
        run_id: "run",
        step_id: step.id,
        number: 1,
        status: "succeeded",
        executor: "claude",
      }) as Attempt,
  );
  const event = (seq: number, type: string, data: unknown, step: number) => ({
    schema_version: 1,
    run_id: "run",
    seq,
    type,
    occurred_at: "",
    agent_id: "one",
    step_id: steps[step].id,
    data,
  });
  // The recent window only covers the last two steps; earlier steps must
  // survive from the run snapshot instead of being swallowed by the merge.
  const recent = [
    event(1, "step_started", { ...steps[3], status: "running" }, 3),
    event(2, "attempt_started", attempts[3], 3),
    event(3, "attempt_finished", attempts[3], 3),
    event(4, "step_finished", steps[3], 3),
    event(5, "step_started", { ...steps[4], status: "running" }, 4),
    event(6, "attempt_started", attempts[4], 4),
    event(7, "attempt_finished", attempts[4], 4),
    event(8, "step_finished", steps[4], 4),
    // task_submitted carries a Step body, and a malformed step event may miss
    // its id; neither may become an extra task card.
    event(9, "task_submitted", { ...steps[0], id: "task-child" }, 0),
    event(10, "step_started", { ...steps[0], id: "" }, 0),
  ];
  const detail = vi.fn(async () =>
    Response.json({ run, steps, attempts, cursor: 8 }),
  );
  vi.stubGlobal(
    "EventSource",
    class {
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?"))
        return Response.json({
          thread,
          messages: { items: [], next_cursor: "" },
          runs: [run],
          sources: [],
        });
      if (path === "/api/v1/runs/run") return detail();
      if (path.startsWith("/api/v1/runs/run/event-history"))
        return Response.json({ items: recent, next_cursor: "" });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  await userEvent.click(
    await screen.findByText("协作过程", { selector: "summary" }),
  );
  const list = await screen.findByRole("list", { name: "协作任务" });
  const tasks = within(list);
  await waitFor(() => expect(detail).toHaveBeenCalled());
  await waitFor(() => expect(tasks.getAllByText(/1 次尝试/)).toHaveLength(5));
  expect(Array.from(list.children)).toHaveLength(5);
  expect(tasks.getAllByText(/第 1 次/)).toHaveLength(5);
  for (const name of names) expect(tasks.getByText(name)).toBeVisible();
});

it("applies a terminal seed snapshot before one bounded refresh replaces the page", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "终态种子",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const runningRun = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  const finishedRun = {
    ...runningRun,
    status: "succeeded",
    finished_at: "2026-10-26T00:05:00Z",
    error: "",
  } as Run;
  const step = {
    id: "step",
    run_id: "run",
    agent_id: "one",
    agent_name: "合成成员",
    instruction: "完成收尾",
    role: "researcher",
    depends_on: [],
    status: "succeeded",
    error: "",
    started_at: "2026-10-26T00:00:00Z",
    deadline_at: "2026-10-26T00:10:00Z",
  } as Step;
  const message = (content: string, status: string) => ({
    id: "message",
    thread_id: "thread",
    run_id: "run",
    step_id: "step",
    agent_id: "one",
    agent_name: "合成成员",
    role: "assistant",
    status,
    content,
    created_at: "2026-10-26T00:00:00Z",
  });
  let threadCalls = 0;
  let releaseRefresh = () => {};
  const refreshGate = new Promise<void>((resolve) => {
    releaseRefresh = resolve;
  });
  let streams = 0;
  vi.stubGlobal(
    "EventSource",
    class {
      constructor() {
        streams++;
      }
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?")) {
        threadCalls++;
        if (threadCalls > 1) await refreshGate;
        const terminal = threadCalls > 1;
        return Response.json({
          thread,
          messages: {
            items: [
              message(
                terminal ? "新的完整回复" : "旧的回复",
                terminal ? "completed" : "processing",
              ),
            ],
            next_cursor: "",
          },
          runs: [terminal ? finishedRun : runningRun],
          sources: [],
        });
      }
      if (path.endsWith("/artifacts")) return Response.json({ items: [] });
      if (path === "/api/v1/runs/run")
        return Response.json({
          run: finishedRun,
          steps: [step],
          attempts: [],
          cursor: 2,
        });
      if (path.startsWith("/api/v1/runs/run/event-history"))
        return Response.json({
          items: [
            {
              schema_version: 1,
              run_id: "run",
              seq: 1,
              type: "message_delta",
              occurred_at: "",
              agent_id: "one",
              step_id: "step",
              data: {
                message_id: "message",
                mode: "snapshot",
                message: message("新的完整回复", "completed"),
              },
            },
            {
              schema_version: 1,
              run_id: "run",
              seq: 2,
              type: "run_finished",
              occurred_at: "",
              data: finishedRun,
            },
          ],
          next_cursor: "",
        });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  const user = userEvent.setup();
  await waitFor(() => expect(threadCalls).toBe(2));
  expect((await screen.findAllByText("新的完整回复")).length).toBeGreaterThan(
    0,
  );
  expect(screen.getByTestId("run-status")).toHaveTextContent("已完成");
  expect(screen.queryByText("生成中")).toBeNull();
  expect(streams).toBe(0);

  releaseRefresh();
  await act(async () => {
    await refreshGate;
  });
  expect(screen.getAllByText("新的完整回复").length).toBeGreaterThan(0);
  expect(screen.getByTestId("run-status")).toHaveTextContent("已完成");
  await userEvent.click(
    await screen.findByText("协作过程", { selector: "summary" }),
  );
  const tasks = within(screen.getByRole("list", { name: "协作任务" }));
  expect(tasks.getByText("已完成")).toBeVisible();
  await user.click(tasks.getByText("合成成员", { selector: "strong" }));
  expect(tasks.getByText(/开始：/)).toBeVisible();
  expect(tasks.getByText(/截止：/)).toBeVisible();
});

it("keeps newer streamed text when an earlier thread refresh resolves late", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "延迟快照",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  const message = {
    id: "message",
    thread_id: "thread",
    run_id: "run",
    step_id: "step",
    agent_id: "one",
    agent_name: "合成成员",
    role: "assistant",
    status: "processing",
    content: "旧回复",
    created_at: "2026-10-26T00:00:00Z",
  };
  const page = (content: string, status: string) =>
    Response.json({
      thread,
      messages: { items: [{ ...message, content, status }], next_cursor: "" },
      runs: [run],
      sources: [],
    });
  let threadCalls = 0;
  let releaseStale = () => {};
  const staleGate = new Promise<void>((resolve) => {
    releaseStale = resolve;
  });
  const streams: { onmessage?: (event: MessageEvent) => void }[] = [];
  vi.stubGlobal(
    "EventSource",
    class {
      constructor() {
        streams.push(this);
      }
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?")) {
        threadCalls++;
        if (threadCalls === 1) return page("旧回复", "processing");
        if (threadCalls === 2) {
          // This response snapshot predates the delta it will deliver after.
          const stale = page("旧回复", "processing");
          await staleGate;
          return stale;
        }
        return page("最新预览", "completed");
      }
      if (path.endsWith("/artifacts")) return Response.json({ items: [] });
      if (path === "/api/v1/runs/run")
        return Response.json({ run, steps: [], attempts: [], cursor: 0 });
      if (path.startsWith("/api/v1/runs/run/event-history"))
        return Response.json({ items: [], next_cursor: "" });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  expect(await screen.findByText("旧回复")).toBeVisible();
  await waitFor(() => expect(threadCalls).toBe(2));
  await waitFor(() => expect(streams.at(-1)).toBeTruthy());
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 1,
        type: "message_delta",
        data: {
          message_id: "message",
          mode: "snapshot",
          message: {
            id: "message",
            content: "最新预览",
            content_bytes: 12,
            status: "completed",
          },
        },
      }),
    } as MessageEvent),
  );
  expect(await screen.findByText("最新预览")).toBeVisible();
  expect(screen.queryByText("生成中")).toBeNull();

  releaseStale();
  await act(async () => {
    await staleGate;
  });
  expect(screen.getByText("最新预览")).toBeVisible();
  expect(screen.queryByText("旧回复")).toBeNull();
  expect(screen.queryByText("生成中")).toBeNull();
});

it("discards steps loaded by a refresh generation that started earlier", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "旧代次",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const runningRun = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  const finishedRun = {
    ...runningRun,
    status: "succeeded",
    finished_at: "2026-10-26T00:05:00Z",
    error: "",
  } as Run;
  const step = (instruction: string, status: string) =>
    ({
      id: "step",
      run_id: "run",
      agent_id: "one",
      agent_name: "合成成员",
      instruction,
      role: "researcher",
      depends_on: [],
      status,
      error: "",
    }) as Step;
  let detailCalls = 0;
  let releaseOldDetail = () => {};
  const detailGate = new Promise<void>((resolve) => {
    releaseOldDetail = resolve;
  });
  let threadCalls = 0;
  vi.stubGlobal(
    "EventSource",
    class {
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?")) {
        threadCalls++;
        const terminal = threadCalls > 1;
        return Response.json({
          thread,
          messages: { items: [], next_cursor: "" },
          runs: [terminal ? finishedRun : runningRun],
          sources: [],
        });
      }
      if (path.endsWith("/artifacts")) return Response.json({ items: [] });
      if (path === "/api/v1/runs/run") {
        detailCalls++;
        if (detailCalls === 1) {
          const stale = Response.json({
            run: runningRun,
            steps: [step("旧版任务说明", "running")],
            attempts: [],
            cursor: 0,
          });
          await detailGate;
          return stale;
        }
        return Response.json({
          run: finishedRun,
          steps: [step("新版任务说明", "succeeded")],
          attempts: [],
          cursor: 2,
        });
      }
      if (path.startsWith("/api/v1/runs/run/event-history"))
        return Response.json({ items: [], next_cursor: "" });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  await waitFor(() => expect(threadCalls).toBe(2));
  releaseOldDetail();
  await act(async () => {
    await detailGate;
  });
  await userEvent.click(
    await screen.findByText("协作过程", { selector: "summary" }),
  );
  const tasks = within(await screen.findByRole("list", { name: "协作任务" }));
  await userEvent.click(tasks.getByText("合成成员", { selector: "strong" }));
  expect(await tasks.findByText("新版任务说明")).toBeVisible();
  expect(tasks.queryByText("旧版任务说明")).toBeNull();
});

it("ignores replayed and cross-run SSE frames without regressing state", async () => {
  history.replaceState({}, "", "/w/ws/t/thread");
  const agent = {
    id: "one",
    workspace_id: "ws",
    display_name: "合成成员",
    handle: "synthetic",
    aliases: [],
    role_description: "",
    model: "default",
    executor: "claude",
    tools: [],
    disabled: false,
    revision: 1,
  } as Agent;
  const thread = {
    id: "thread",
    workspace_id: "ws",
    title: "过期帧测试",
    agent_ids: ["one"],
    workflow: "free",
    revision: 1,
  };
  const run = {
    id: "run",
    input: { workflow: "free" },
    agents: [agent],
    status: "running",
    created_at: "2026-10-26T00:00:00Z",
  } as Run;
  let content = "现有回复";
  const streams: { onmessage?: (event: MessageEvent) => void }[] = [];
  vi.stubGlobal(
    "EventSource",
    class {
      constructor() {
        streams.push(this);
      }
      onmessage?: (event: MessageEvent) => void;
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/api/v1/system") return Response.json(system);
      if (path === "/api/v1/workspaces")
        return Response.json({ items: [{ id: "ws", name: "合成工作区" }] });
      if (path.endsWith("/agents")) return Response.json({ items: [agent] });
      if (path.startsWith("/api/v1/workspaces/ws/threads"))
        return Response.json({ items: [thread], next_cursor: "" });
      if (path.startsWith("/api/v1/threads/thread?"))
        return Response.json({
          thread,
          messages: {
            items: [
              {
                id: "message",
                run_id: "run",
                agent_id: "one",
                agent_name: "合成成员",
                role: "assistant",
                status: "processing",
                content,
                created_at: "2026-10-26T00:00:00Z",
              },
            ],
            next_cursor: "",
          },
          runs: [run],
          sources: [],
        });
      if (path === "/api/v1/runs/run")
        return Response.json({ run, steps: [], attempts: [], cursor: 0 });
      if (path.startsWith("/api/v1/runs/run/event-history"))
        return Response.json({ items: [], next_cursor: "" });
      return Response.json({ items: [] });
    }),
  );
  render(<App />);
  await screen.findByText("现有回复");
  await waitFor(() => expect(streams.at(-1)).toBeTruthy());
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 10,
        type: "liveness_warning",
        data: { state: "waiting_tool", silence_seconds: 5 },
      }),
    } as MessageEvent),
  );
  await screen.findByText("等待工具 · 已 5 秒");
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 11,
        type: "tool_finished",
        data: { tool_call_id: "call", name: "read_file", status: "ok" },
      }),
    } as MessageEvent),
  );
  await waitFor(() =>
    expect(screen.queryByText("等待工具 · 已 5 秒")).toBeNull(),
  );

  // Replayed warning and an outdated snapshot must not roll state back.
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 10,
        type: "liveness_warning",
        data: { state: "waiting_tool", silence_seconds: 5 },
      }),
    } as MessageEvent),
  );
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 9,
        type: "message_delta",
        data: {
          message_id: "message",
          mode: "snapshot",
          message: {
            id: "message",
            content: "过期回复",
            content_bytes: 12,
            status: "completed",
          },
        },
      }),
    } as MessageEvent),
  );
  expect(screen.queryByText("等待工具 · 已 5 秒")).toBeNull();
  expect(screen.queryByText("过期回复")).toBeNull();

  // A frame belonging to another run is ignored as well.
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "other",
        seq: 12,
        type: "liveness_warning",
        data: { state: "stalled", silence_seconds: 30 },
      }),
    } as MessageEvent),
  );
  expect(screen.queryByText(/长时间无进展/)).toBeNull();

  content = "现有回复新正文";
  act(() =>
    streams.at(-1)?.onmessage?.({
      data: JSON.stringify({
        run_id: "run",
        seq: 12,
        type: "message_delta",
        data: { message_id: "message", mode: "append", text: "新正文" },
      }),
    } as MessageEvent),
  );
  expect(await screen.findByText(/现有回复新正文/)).toBeVisible();
});
