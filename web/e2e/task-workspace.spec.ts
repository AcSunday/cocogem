import { expect, test } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import setup from "./setup";

let baseURL = "";
let cleanup: (() => Promise<void>) | undefined;
test.beforeAll(async () => {
  const previous = process.env.COCOGEM_TEST_BASE;
  cleanup = await setup(); baseURL = process.env.COCOGEM_TEST_BASE!;
  process.env.COCOGEM_TEST_BASE = previous;
});
test.afterAll(async () => cleanup?.());

test("tasks create conversations, chat promotes in place and navigation stays consistent", async ({ page }) => {
  test.setTimeout(120000);
  const status = await (await page.request.get(baseURL + "/auth/status")).json();
  const auth = await (await page.request.post(baseURL + (status.initialized ? "/auth/login" : "/auth/setup"), { data: { password: "task-workspace-password" } })).json();
  const headers = { "X-CSRF-Token": auth.csrf_token };
  const workspace = await (await page.request.post(baseURL + "/api/v1/workspaces", { headers, data: { name: "Cocogem 产品迭代" } })).json();
  const create = async (title: string, goal: string) => (await page.request.post(`${baseURL}/api/v1/workspaces/${workspace.id}/tasks`, { headers, data: { request_id: crypto.randomUUID(), title, goal } })).json();
  await create("整理资料与需求", "整理任务交互的需求，保留用户约束并形成验收条目。");
  const paused = await create("验证原生执行", "等待测试环境准备好后，验证真实执行链路。");
  await page.request.post(`${baseURL}/api/v1/tasks/${paused.task.id}/actions`, { headers, data: { action: "pause", request_id: crypto.randomUUID() } });
  await page.goto(`${baseURL}/w/${workspace.id}?view=tasks`);
  await expect(page.getByRole("heading", { name: "任务清单", exact: true })).toBeVisible();
  const rail = page.getByRole("complementary", { name: "项目视图" });
  await expect(rail.getByRole("button", { name: "任务清单" })).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("navigation", { name: "会话列表" })).toHaveCount(0);
  await page.getByRole("button", { name: /新建任务/ }).click();
  await page.getByLabel("任务标题", { exact: true }).fill("改造任务工作区");
  await page.getByLabel("目标与交付要求").fill("整理任务驱动会话的交互方案，交付可以核对的设计记录。");
  await page.getByRole("button", { name: "保存任务", exact: true }).click();
  await expect(page.getByRole("heading", { name: "改造任务工作区", exact: true })).toBeVisible();
  await expect(page.getByText("开始执行时创建")).toBeVisible();
  await page.getByRole("button", { name: "开始执行", exact: true }).click();
  await expect(page).toHaveURL(/\/t\//);
  const threadURL = page.url();
  await expect(rail.getByRole("button", { name: "项目首页" })).toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("run-status")).toHaveText("已完成", { timeout: 30000 });
  await page.getByRole("button", { name: /查看任务 →/ }).click();
  await expect(page.getByRole("heading", { name: "改造任务工作区", exact: true })).toBeVisible();
  await expect(page.getByLabel("任务属性")).toContainText("待验收");
  await page.getByRole("button", { name: /返回任务清单/ }).click();
  await page.getByRole("button", { name: /筛选项目/ }).click();
  await page.getByRole("option", { name: workspace.name }).click();
  await expect(page.getByRole("region", { name: "待验收任务" })).toContainText("改造任务工作区");
  await page.getByRole("button", { name: "看板", exact: true }).click();
  await expect(page.getByRole("button", { name: "清单", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "待验收任务" })).toContainText("改造任务工作区");
  await page.screenshot({ path: "/tmp/cocogem-task-redesign/task-board-view.png", fullPage: true });
  await page.getByRole("button", { name: "清单", exact: true }).click();
  await mkdir("/tmp/cocogem-task-redesign", { recursive: true });
  await page.screenshot({ path: "/tmp/cocogem-task-redesign/task-board.png", fullPage: true });
  await page.getByRole("button", { name: /改造任务工作区/ }).click();
  await expect(page.getByRole("heading", { name: "改造任务工作区", exact: true })).toBeVisible();
  await page.screenshot({ path: "/tmp/cocogem-task-redesign/task-detail.png", fullPage: true });
  // P7: acceptance is a dedicated dialog reached from the "去验收" action, not
  // an inline form.
  await expect(
    page.getByRole("button", { name: "去验收", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "去验收", exact: true }).click();
  const acceptance = page.getByRole("dialog", { name: "人工验收" });
  await expect(acceptance).toBeVisible();
  const judgement = acceptance.getByRole("combobox", { name: /^判断/ }).first();
  await judgement.selectOption("passed");
  await acceptance.getByRole("checkbox", { name: /会话交付/ }).first().check();
  // The dialog's submit is "提交验收"; it stays disabled until every required
  // criterion is judged and passing ones carry evidence.
  const submit = acceptance.getByRole("button", {
    name: "提交验收",
    exact: true,
  });
  await expect(submit).toBeEnabled();
  await submit.click();
  await expect(page.getByLabel("任务属性")).toContainText("已验收");
  await page.goBack();
  await expect(page.getByRole("button", { name: /筛选项目/ })).toContainText(
    workspace.name,
  );
  await page.getByRole("button", { name: "选择", exact: true }).click();
  await page.getByRole("button", { name: "全选当前", exact: true }).click();
  await expect(page.getByText("已选 2 项")).toBeVisible();
  await page.screenshot({ path: "/tmp/cocogem-task-redesign/task-selection.png", fullPage: true });
  await page.getByRole("button", { name: "永久删除所选" }).click();
  const confirmDelete = page.getByRole("dialog", {
    name: "确认永久删除任务",
  });
  await expect(confirmDelete).toContainText("无法恢复");
  await page.screenshot({ path: "/tmp/cocogem-task-redesign/task-delete-confirm.png", fullPage: true });
  await confirmDelete
    .getByRole("button", { name: "永久删除", exact: true })
    .click();
  await expect(page.getByText(/已永久删除/)).toBeVisible();
  await expect(
    page.getByRole("button", { name: /整理资料与需求/ }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: /验证原生执行/ }),
  ).toHaveCount(0);
  await page.goto(threadURL);
  await expect(rail.getByRole("button", { name: "项目首页" })).toHaveAttribute("aria-current", "page");
  const members = await (await page.request.get(`${baseURL}/api/v1/workspaces/${workspace.id}/agents`)).json();
  const member = members.items[0];
  const chat = await (await page.request.post(`${baseURL}/api/v1/workspaces/${workspace.id}/threads`, { headers, data: { title: "先聊想法", agent_ids: [member.id], coordinator_id: member.id, workflow: "free" } })).json();
  await page.goto(`${baseURL}/w/${workspace.id}/t/${chat.id}`);
  await page.getByLabel("输入问题").fill("解释一下目标和验收的区别。");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成", { timeout: 30000 });
  let current = await (await page.request.get(`${baseURL}/api/v1/threads/${chat.id}`)).json();
  expect(current.thread.task_id).toBeFalsy();
  await page.getByLabel("输入问题").fill("任务触发浏览器验收：按照讨论结果交付一份方案报告。");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByRole("button", { name: /查看任务 →/ })).toBeVisible({ timeout: 30000 });
  expect(page.url()).toContain(`/t/${chat.id}`);
  current = await (await page.request.get(`${baseURL}/api/v1/threads/${chat.id}`)).json();
  expect(current.thread.task_id).toBeTruthy();
  expect(current.runs[0].task_id).toBeFalsy();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "任务清单", exact: true }).click();
  await expect(page.getByRole("heading", { name: "任务清单", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
});
