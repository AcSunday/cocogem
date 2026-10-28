import { expect, test } from "@playwright/test";
import type {
  Agent,
  Artifact,
  Event,
  Run,
  Step,
  ThreadView,
  Workspace,
} from "../src/api/client";
import setup from "./setup";

let baseURL = "";
let cleanup: (() => Promise<void>) | undefined;

// Keep first-password setup and global profile edits isolated from other specs.
test.beforeAll(async () => {
  const previous = process.env.COCOGEM_TEST_BASE;
  cleanup = await setup();
  baseURL = process.env.COCOGEM_TEST_BASE!;
  process.env.COCOGEM_TEST_BASE = previous;
});
test.afterAll(async () => cleanup?.());

test("browser, server and scheduler execute research, independent review and result return", async ({
  page,
}) => {
  await page.goto(baseURL);
  await page
    .getByLabel("设置密码", { exact: true })
    .fill("runtime-browser-password");
  await page
    .getByLabel("确认密码", { exact: true })
    .fill("runtime-browser-password");
  await page.getByRole("button", { name: "设置密码并进入" }).click();
  await page.getByRole("button", { name: "创建第一个工作空间" }).click();
  await page.getByLabel("名称", { exact: true }).fill("动态编排验收空间");
  await page.getByRole("checkbox", { name: /结果汇总/ }).uncheck();
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const workspaceID = new URL(page.url()).pathname.split("/w/")[1];
  await page.reload();
  await page.getByRole("button", { name: "编辑工作空间", exact: true }).click();
  await expect(
    page.getByRole("checkbox", { name: /结果汇总/ }),
  ).not.toBeChecked();
  await page.getByRole("checkbox", { name: /结果汇总/ }).check();
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // Global profiles live in Settings → Members; the top entry only manages
  // this workspace's grants.
  await page.getByRole("button", { name: "设置", exact: true }).click();
  const settingsDialog = page.getByRole("dialog", { name: "设置" });
  await settingsDialog.getByRole("tab", { name: "成员" }).click();
  for (const [handle, name, capability] of [
    ["ragdoll", "调度员", "fixture-coordinate"],
    ["maine", "资料员", "fixture-research"],
    ["orange", "核对员", "fixture-review"],
  ]) {
    await settingsDialog
      .getByRole("button", { name: new RegExp(`\\(@${handle}\\)`) })
      .click();
    await settingsDialog.getByLabel("显示名", { exact: true }).fill(name);
    await settingsDialog.getByLabel("能力标签（用逗号分隔）").fill(capability);
    await settingsDialog
      .getByRole("button", { name: "保存成员", exact: true })
      .click();
    await expect(settingsDialog.getByRole("status")).toContainText("已保存");
  }
  await page.getByRole("button", { name: "关闭窗口" }).click();
  await page.reload();
  await page.getByRole("button", { name: "设置", exact: true }).click();
  await settingsDialog.getByRole("tab", { name: "成员" }).click();
  await settingsDialog
    .getByRole("button", { name: /^资料员 \(@maine\)/ })
    .click();
  await expect(settingsDialog.getByLabel("能力标签（用逗号分隔）")).toHaveValue(
    "fixture-research",
  );
  await page.getByRole("button", { name: "关闭窗口" }).click();
  const members: { items: Agent[] } = await (
    await page.request.get(`${baseURL}/api/v1/agents`)
  ).json();
  expect(members.items).toHaveLength(3);
  expect(members.items.every((member) => member.workspace_id === "")).toBe(
    true,
  );
  const coordinator = members.items.find((member) =>
    member.capabilities?.includes("fixture-coordinate"),
  )!;
  const researcher = members.items.find((member) =>
    member.capabilities?.includes("fixture-research"),
  )!;
  const reviewer = members.items.find((member) =>
    member.capabilities?.includes("fixture-review"),
  )!;
  const spaces: { items: Workspace[] } = await (
    await page.request.get(`${baseURL}/api/v1/workspaces`)
  ).json();
  const savedWorkspace = spaces.items.find(
    (space) => space.id === workspaceID,
  )!;
  expect(savedWorkspace.skill_ids?.sort()).toEqual([
    "independent-review",
    "synthesize-results",
    "task-handoff",
    "task-orchestration",
  ]);

  await page
    .getByRole("button", { name: "新建会话", exact: false })
    .first()
    .click();
  await page.getByLabel("会话标题").fill("动态研究与独立复核");
  await page.getByRole("combobox", { name: "协调者", exact: true }).click();
  await page
    .getByRole("option", { name: "调度员 (@ragdoll)", exact: true })
    .click();
  await page.getByRole("button", { name: "保存会话", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "动态研究与独立复核", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("工作方式", { exact: true })).toHaveText(
    "自由协作",
  );
  const accepted = page.waitForResponse(
    (response) =>
      response.url().endsWith("/runs") &&
      response.request().method() === "POST",
  );
  await page
    .getByLabel("输入问题")
    .fill(
      "动态协作验收：请先研究给定资料，再找非作者独立复核，最后汇总并保留异议。 https://example.org/cocogem/research",
    );
  await expect(
    page.getByRole("button", { name: "发送", exact: true }),
  ).toBeEnabled();
  await page.getByLabel("输入问题").press("Enter");
  const run: Run = await (await accepted).json();
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 15000,
  });
  const process = page.getByTestId("run-process");
  await expect(process).not.toHaveAttribute("open");
  await expect(process.locator(":scope > summary")).toHaveText(
    /协作已完成 · \d+ 条过程/,
  );
  await process.locator(":scope > summary").click();
  await expect(process).toHaveAttribute("open");
  const tasks = page.getByRole("list", { name: "协作任务" });
  const researchTask = tasks.locator("details").filter({
    has: page.locator("summary").getByText("资料员", { exact: true }),
  });
  const reviewTask = tasks.locator("details").filter({
    has: page.locator("summary").getByText("核对员", { exact: true }),
  });
  await expect(researchTask.locator("summary")).toContainText("调度员 →");
  await expect(reviewTask.locator("summary")).toContainText("调度员 →");
  await reviewTask.locator("summary").click();
  await expect(reviewTask).toContainText(
    "独立核对研究消息、来源与研究笔记的确切版本。",
  );
  await expect(reviewTask).toContainText("资料员");
  await expect(
    reviewTask.getByRole("button", { name: "来源：测试资料", exact: true }),
  ).toBeVisible();
  await expect(reviewTask).toContainText("动态研究结果");
  await expect(reviewTask).toContainText("动态复核结果");
  await expect(
    page
      .getByRole("article")
      .getByText(
        "动态协作最终汇总：研究与独立复核已完成，保留证据不足；最终纪要已保存。",
        { exact: true },
      ),
  ).toBeVisible();

  const threadID = new URL(page.url()).pathname.split("/t/")[1];
  const detail: { run: Run; steps: Step[] } = await (
    await page.request.get(`${baseURL}/api/v1/runs/${run.id}`)
  ).json();
  const thread: ThreadView = await (
    await page.request.get(`${baseURL}/api/v1/threads/${threadID}`)
  ).json();
  const artifacts: { items: Artifact[] } = await (
    await page.request.get(`${baseURL}/api/v1/threads/${threadID}/artifacts`)
  ).json();
  const handoffs = detail.steps.filter((step) => step.kind === "handoff");
  expect(handoffs).toHaveLength(2);
  expect(handoffs.map((step) => [step.agent_id, step.role])).toEqual([
    [researcher.id, "researcher"],
    [reviewer.id, "reviewer"],
  ]);
  expect(
    handoffs.every(
      (step) =>
        step.from_agent_id === coordinator.id && step.status === "succeeded",
    ),
  ).toBe(true);
  expect(
    detail.steps.filter((step) => step.kind === "continuation"),
  ).toHaveLength(2);
  expect(detail.steps.every((step) => step.status === "succeeded")).toBe(true);
  const researchMessage = thread.messages.items?.find(
    (message) => message.step_id === handoffs[0].id,
  )!;
  // Process handoffs reference committed messages and sources; only the final
  // coordinator continuation publishes a formal artifact.
  expect(artifacts.items).toHaveLength(1);
  expect(artifacts.items[0]).toMatchObject({
    title: "动态协作验收纪要",
    type: "note",
    version: 1,
    step_id: detail.steps.filter((item) => item.kind === "continuation").at(-1)
      ?.id,
  });
  expect(handoffs[1].context_refs).toHaveLength(2);
  expect(handoffs[1].context_refs).toEqual(
    expect.arrayContaining([researchMessage.id, thread.sources[0].id]),
  );
  expect(detail.run.delivery?.message_ids.length).toBeGreaterThan(0);
  expect(detail.run.delivery?.artifact_refs).toEqual([
    expect.objectContaining({
      id: artifacts.items[0].id,
      version: 1,
    }),
  ]);
  expect(Object.keys(detail.run.skill_versions ?? {}).sort()).toEqual(
    savedWorkspace.skill_ids?.sort(),
  );
  const eventResponse = await page.request.get(
    `${baseURL}/api/v1/runs/${run.id}/events`,
  );
  const events: Event[] = (await eventResponse.text())
    .split("\n")
    .filter((line) => line.startsWith("data: "))
    .map((line) => JSON.parse(line.slice(6)));
  expect(
    events.filter((event) => event.type === "task_submitted"),
  ).toHaveLength(2);
  const skillReads = events
    .filter((event) => event.type === "skill_read")
    .map((event) => (event.data as { skill_id: string }).skill_id);
  expect(skillReads).toEqual(
    expect.arrayContaining([
      "task-handoff",
      "task-orchestration",
      "independent-review",
      "synthesize-results",
    ]),
  );
  const finished = events
    .filter((event) => event.type === "tool_finished")
    .map((event) => event.data as { name: string; status: string });
  expect(finished.every((event) => event.status === "ok")).toBe(true);
  expect(
    finished.filter((event) => event.name === "handoff_task"),
  ).toHaveLength(2);
  await page
    .getByLabel("第 1 次协作产物")
    .getByRole("button", { name: /动态协作验收纪要/ })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "保留异议：资料不能证明质量提升",
  );
  await expect(page.getByRole("dialog")).toContainText("未调用真实模型");
  await page.getByRole("button", { name: "关闭窗口" }).click();
  await page.reload();
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
  // The reader expanded the process before reloading; that choice survives.
  await expect(page.getByTestId("run-process")).toHaveAttribute("open");
  await expect(tasks.locator("details")).toHaveCount(5);
  await page.screenshot({
    path: test.info().outputPath("dynamic-collaboration-runtime.png"),
  });
});
