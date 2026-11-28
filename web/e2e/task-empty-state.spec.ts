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

test("an empty task list keeps a single new-task action in the header", async ({ page }) => {
  const status = await (await page.request.get(baseURL + "/auth/status")).json();
  const auth = await (await page.request.post(baseURL + (status.initialized ? "/auth/login" : "/auth/setup"), { data: { password: "task-empty-password" } })).json();
  const headers = { "X-CSRF-Token": auth.csrf_token };
  const workspace = await (await page.request.post(baseURL + "/api/v1/workspaces", { headers, data: { name: "空任务清单验收" } })).json();
  await page.goto(`${baseURL}/w/${workspace.id}?view=tasks`);
  await expect(page.getByRole("heading", { name: "任务清单", exact: true })).toBeVisible();

  // The empty state points at the header action instead of repeating a second
  // button in the middle of the page.
  const create = page.getByRole("button", { name: "新建任务", exact: true });
  await expect(create).toHaveCount(1);
  await expect(create).toBeVisible();
  await expect(
    page.getByText("还没有任务，用右上角的「新建任务」创建第一个任务。"),
  ).toBeVisible();
  await mkdir("/tmp/cocogem-task-redesign", { recursive: true });
  await page.screenshot({ path: "/tmp/cocogem-task-redesign/task-empty.png", fullPage: true });

  await create.click();
  await expect(page.getByRole("dialog", { name: "新建任务" })).toBeVisible();
});

test("exits selection mode and restores header new-task action when external deletion empties the list", async ({ page }) => {
  const status = await (await page.request.get(baseURL + "/auth/status")).json();
  const auth = await (await page.request.post(baseURL + (status.initialized ? "/auth/login" : "/auth/setup"), { data: { password: "task-empty-password" } })).json();
  const headers = { "X-CSRF-Token": auth.csrf_token };
  const workspace = await (await page.request.post(baseURL + "/api/v1/workspaces", { headers, data: { name: "跨标签页删除验收" } })).json();
  const created = await (await page.request.post(`${baseURL}/api/v1/workspaces/${workspace.id}/tasks`, {
    headers,
    data: { request_id: crypto.randomUUID(), title: "将被另一端删除的任务", goal: "验证轮询清空后退出选择态" },
  })).json();

  await page.goto(`${baseURL}/w/${workspace.id}?view=tasks`);
  await expect(page.getByRole("heading", { name: "任务清单", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "选择", exact: true }).click();
  await page.getByRole("checkbox", { name: "选择任务：将被另一端删除的任务" }).check();
  await expect(page.getByText("已选 1 项")).toBeVisible();
  await expect(page.getByRole("button", { name: /新建任务/ })).toHaveCount(0);

  await page.request.post(`${baseURL}/api/v1/tasks/delete`, {
    headers,
    data: { task_ids: [created.task.id] },
  });

  await expect(
    page.getByText("还没有任务，用右上角的「新建任务」创建第一个任务。"),
  ).toBeVisible({ timeout: 10000 });
  await expect(page.getByText(/已选 \d+ 项/)).toHaveCount(0);
  await expect(page.getByRole("button", { name: "退出选择", exact: true })).toHaveCount(0);
  const create = page.getByRole("button", { name: "新建任务", exact: true });
  await expect(create).toHaveCount(1);
  await expect(create).toBeVisible();
});
