import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

test("persisted task handoffs, skill settings and whole-run cancellation", async ({
  page,
}) => {
  const agents = ["coordinator", "researcher", "reviewer"].map((id, index) => ({
    id,
    workspace_id: "",
    display_name: ["协调成员", "研究成员", "复核成员"][index],
    handle: id,
    aliases: [],
    role_description: ["按任务组织协作", "检索并核对资料", "独立复核证据"][
      index
    ],
    capabilities: [id],
    executor: ["claude", "codex", "gemini"][index],
    model: "default",
    tools: [],
    disabled: false,
    revision: 1,
  }));
  const workspace = {
    id: "collaboration-ui",
    name: "协作验收空间",
    description: "",
    root: "",
    revision: 1,
    skill_ids: null as string[] | null,
    created_at: "2026-10-24T00:00:00Z",
  };
  const thread = {
    id: "collaboration-thread",
    workspace_id: workspace.id,
    title: "研究与独立复核",
    agent_ids: agents.map((agent) => agent.id),
    coordinator_id: "coordinator",
    workflow: "free",
    revision: 1,
    updated_at: "2026-10-24T00:00:00Z",
  };
  const run = {
    id: "collaboration-run",
    workspace_id: workspace.id,
    thread_id: thread.id,
    workspace,
    agents,
    input: { workflow: "free", message: "研究两种方案并进行独立复核" },
    status: "running",
    error: "",
    created_at: "2026-10-24T00:00:00Z",
    finished_at: "",
  };
  const steps = [
    {
      id: "plan",
      run_id: run.id,
      agent_id: "coordinator",
      agent_name: "协调成员",
      instruction: "根据任务选择合适成员",
      role: "coordinator",
      depends_on: [],
      status: "succeeded",
      error: "",
    },
    {
      id: "research",
      run_id: run.id,
      agent_id: "researcher",
      agent_name: "研究成员",
      instruction: "调查两种方案，提供来源并保留不确定性。",
      role: "researcher",
      kind: "handoff",
      from_agent_id: "coordinator",
      from_step_id: "plan",
      depends_on: ["plan"],
      context_refs: ["task-message"],
      status: "succeeded",
      error: "",
    },
    {
      id: "review",
      run_id: run.id,
      agent_id: "reviewer",
      agent_name: "复核成员",
      instruction: "独立检查研究结果的来源与推理。",
      role: "reviewer",
      kind: "handoff",
      from_agent_id: "coordinator",
      from_step_id: "plan",
      depends_on: ["research"],
      context_refs: ["research-message"],
      status: "running",
      error: "",
    },
    {
      id: "summary",
      run_id: run.id,
      agent_id: "coordinator",
      agent_name: "协调成员",
      instruction: "收到复核结果后汇总，保留异议。",
      role: "coordinator",
      kind: "continuation",
      depends_on: ["research", "review"],
      status: "pending",
      error: "",
    },
  ];
  const messages = [
    {
      id: "task-message",
      run_id: run.id,
      step_id: "",
      role: "user",
      status: "completed",
      content: "研究两种方案并进行独立复核",
      created_at: "2026-10-24T00:00:00Z",
    },
    {
      id: "research-message",
      run_id: run.id,
      step_id: "research",
      agent_id: "researcher",
      agent_name: "研究成员",
      role: "assistant",
      status: "completed",
      content: "方案一更易部署；方案二需要补充兼容性证据。",
      created_at: "2026-10-24T00:00:01Z",
    },
  ];
  const skills = [
    {
      id: "task-orchestration",
      name: "task-orchestration",
      description: "按任务组织分工与验收。",
      version: "skill-version-one",
    },
    {
      id: "independent-review",
      name: "independent-review",
      description: "由非作者独立核对证据。",
      version: "skill-version-two",
    },
  ];
  let cancellations = 0;
  // These fixtures exercise the browser contract, not real CLI execution.
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body: unknown = { items: [] };
    if (path === "/api/v1/system")
      body = {
        csrf_token: "browser-test",
        models: ["default"],
        executors: [],
        roots: [],
        verified: {},
        limits: {},
        model_status: "configured",
        search_status: "not_configured",
        skills,
      };
    else if (path === "/api/v1/workspaces") body = { items: [workspace] };
    else if (
      path === `/api/v1/workspaces/${workspace.id}` &&
      request.method() === "PATCH"
    ) {
      Object.assign(workspace, request.postDataJSON());
      workspace.revision++;
      body = workspace;
    } else if (path === `/api/v1/workspaces/${workspace.id}/agents`)
      body = { items: agents };
    else if (path === `/api/v1/workspaces/${workspace.id}/threads`)
      body = { items: [thread], next_cursor: "" };
    else if (path === `/api/v1/threads/${thread.id}`)
      body = {
        thread,
        runs: [run],
        messages: { items: messages, next_cursor: "" },
        sources: [],
      };
    else if (path === `/api/v1/runs/${run.id}`) body = { run, steps };
    else if (path === `/api/v1/runs/${run.id}/cancel`) {
      cancellations++;
      run.status = "canceled";
      run.finished_at = "2026-10-24T00:00:10Z";
      for (const step of steps)
        if (step.status !== "succeeded") step.status = "canceled";
      body = run;
    } else if (path.endsWith("/events")) {
      await route.fulfill({
        contentType: "text/event-stream",
        body: ": connected\n\n",
      });
      return;
    }
    await route.fulfill({ json: body });
  });
  await page.goto(`/w/${workspace.id}/t/${thread.id}`);
  // Running collaborations keep their process visible without a click.
  await expect(page.getByTestId("run-process")).toHaveAttribute("open");
  const tasks = page.getByRole("list", { name: "协作任务" });
  await expect(tasks.getByText("等待结果", { exact: true })).toBeVisible();
  const review = tasks
    .locator("details")
    .filter({ has: page.getByText("复核成员", { exact: true }) });
  await review.locator("summary").click();
  await expect(review).toContainText("独立检查研究结果的来源与推理。");
  await expect(review).toContainText(
    "方案一更易部署；方案二需要补充兼容性证据。",
  );

  for (const mode of ["light", "dark"]) {
    await page.getByLabel("外观模式", { exact: true }).click();
    await page
      .getByRole("option", {
        name: mode === "light" ? "浅色" : "深色",
        exact: true,
      })
      .click();
    for (const width of [1125, 390]) {
      await page.setViewportSize({ width, height: 844 });
      await expect(review).toHaveAttribute("open", "");
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      await page.screenshot({
        path: test.info().outputPath(`collaboration-${mode}-${width}.png`),
      });
    }
    await page.setViewportSize({ width: 1125, height: 844 });
  }
  const accessibility = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa"])
    .analyze();
  expect(accessibility.violations).toEqual([]);
  await page.getByRole("button", { name: "停止协作", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已停止");
  expect(cancellations).toBe(1);
  await page.reload();
  await expect(page.getByTestId("run-status")).toHaveText("已停止");
  await expect(page.getByTestId("run-process")).toHaveAttribute("open");
  await expect(tasks.getByText("已停止", { exact: true })).toHaveCount(2);

  await page.getByRole("button", { name: "编辑工作空间", exact: true }).click();
  await page.getByRole("checkbox", { name: /独立复核/ }).uncheck();
  await page.getByRole("button", { name: "保存工作空间", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByRole("button", { name: "编辑工作空间", exact: true }).click();
  await expect(page.getByRole("checkbox", { name: /任务编排/ })).toBeChecked();
  await expect(
    page.getByRole("checkbox", { name: /独立复核/ }),
  ).not.toBeChecked();
});
