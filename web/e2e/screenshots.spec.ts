import { test, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import setup from "./setup";

/**
 * Real-app screenshots for the P8 deliverable proof. These use the real built
 * app against the real test server (synthetic model, real code/UI) — not mock
 * HTML. Written outside the repo to the shared temp dir.
 */

const SHOTS =
  process.env.COCOGEM_QA_SCREENSHOTS ??
  "/var/folders/7_/srdsmgns77l_n36f3cb1r0xw0000gn/T/cocogem-redesign-20261108-gbvllu6h/screenshots";

let baseURL = "";
let cleanup: (() => Promise<void>) | undefined;
test.beforeAll(async () => {
  const previous = process.env.COCOGEM_TEST_BASE;
  cleanup = await setup();
  baseURL = process.env.COCOGEM_TEST_BASE!;
  process.env.COCOGEM_TEST_BASE = previous;
});
test.afterAll(async () => await cleanup?.());

async function authenticate(page: Page) {
  const status = await (await page.request.get(baseURL + "/auth/status")).json();
  const response = await page.request.post(
    baseURL + (status.initialized ? "/auth/login" : "/auth/setup"),
    { data: { password: "screenshot-password" } },
  );
  const session = await response.json();
  await page.context().addCookies((await page.request.storageState()).cookies);
  return { "X-CSRF-Token": session.csrf_token as string };
}

async function setAppearance(page: Page, value: string) {
  await page.evaluate((next) => {
    document.documentElement.dataset.appearance = next;
    try {
      localStorage.setItem("cocogem-appearance", next);
    } catch {
      // Storage can be unavailable; the dataset still applies for the shot.
    }
  }, value);
  await page.waitForTimeout(150);
}

async function seedTasks(page: Page, headers: Record<string, string>) {
  const workspace = await (
    await page.request.post(baseURL + "/api/v1/workspaces", {
      headers,
      data: { name: "界面验收" },
    })
  ).json();
  for (const [title, goal] of [
    ["整理资料与需求", "整理任务交互的需求，保留用户约束并形成验收条目。"],
    ["复核执行边界", "复核项目内自主的边界说明与授权记录。"],
    ["交付设计记录", "交付一份可以核对的设计记录。"],
  ] as const)
    await page.request.post(
      `${baseURL}/api/v1/workspaces/${workspace.id}/tasks`,
      { headers, data: { request_id: crypto.randomUUID(), title, goal } },
    );
  return workspace;
}

test("capture permission, board, acceptance and pending screenshots", async ({
  page,
}) => {
  test.setTimeout(240000);
  await mkdir(SHOTS, { recursive: true });
  const headers = await authenticate(page);
  const workspace = await seedTasks(page, headers);

  // --- Permission settings: desktop + mobile ---
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`${baseURL}/w/${workspace.id}`);
  await page.getByLabel("输入问题").waitFor();
  await page.getByRole("button", { name: "设置", exact: true }).click();
  const settings = page.getByRole("dialog", { name: "设置" });
  await settings.getByRole("tab", { name: "权限", exact: true }).click();
  await page.waitForTimeout(300);
  await page.screenshot({
    path: `${SHOTS}/permission-settings-desktop.png`,
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(300);
  await page.screenshot({
    path: `${SHOTS}/permission-settings-mobile.png`,
    fullPage: true,
  });
  await page.getByRole("button", { name: "关闭窗口" }).click();

  // --- Board in light / dark / warm, plus the mobile list ---
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(`${baseURL}/w/${workspace.id}?view=tasks`);
  await page.getByRole("button", { name: "看板", exact: true }).click();
  for (const theme of ["light", "dark", "warm"]) {
    await setAppearance(page, theme);
    await page.waitForTimeout(250);
    await page.screenshot({
      path: `${SHOTS}/board-${theme}.png`,
      fullPage: true,
    });
  }
  await setAppearance(page, "light");
  await page.getByRole("button", { name: "清单", exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(250);
  await page.screenshot({ path: `${SHOTS}/list-mobile.png`, fullPage: true });

  // --- Pending authorization + approved state, real host approval ---
  await page.setViewportSize({ width: 1440, height: 1000 });
  const goal = "权限审批浏览器验收：读取项目外文件并交付审批结果。";
  const created = await page.request.post(
    `${baseURL}/api/v1/workspaces/${workspace.id}/tasks`,
    { headers, data: { request_id: crypto.randomUUID(), title: "权限审批", goal } },
  );
  const task = (await created.json()).task;
  await page.request.post(`${baseURL}/api/v1/tasks/${task.id}/start`, {
    headers,
    data: { request_id: crypto.randomUUID() },
  });
  const detail = await (
    await page.request.get(`${baseURL}/api/v1/tasks/${task.id}`)
  ).json();
  await page.goto(
    `${baseURL}/w/${task.workspace_id}/t/${detail.task.thread_id}`,
  );
  const card = page.getByRole("region", { name: "待处理授权" }).first();
  await card.getByRole("button", { name: "仅本次允许" }).waitFor({
    timeout: 30000,
  });
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${SHOTS}/permission-pending.png`, fullPage: true });
  await card.getByRole("button", { name: "仅本次允许" }).click();
  await page.waitForTimeout(3000);
  await page.screenshot({ path: `${SHOTS}/permission-approved.png`, fullPage: true });

  // --- Acceptance dialog ---
  // Use the Task that actually finished the permission journey: it is in the
  // 待验收 phase, so 去验收 opens the real dialog instead of a loading screen.
  await page.goto(`${baseURL}/w/${task.workspace_id}/tasks/${task.id}`);
  const review = page.getByRole("button", { name: "去验收", exact: true });
  await review.waitFor({ timeout: 30000 });
  await review.click();
  const acceptanceDialog = page.getByRole("dialog", { name: "人工验收" });
  await acceptanceDialog.waitFor();
  await acceptanceDialog
    .getByRole("button", { name: "提交验收", exact: true })
    .waitFor();
  await page.waitForTimeout(300);
  await page.screenshot({
    path: `${SHOTS}/acceptance-dialog.png`,
    fullPage: true,
  });

  // --- Composer hint tooltip ---
  await page.goto(`${baseURL}/w/${workspace.id}`);
  await page.getByLabel("输入问题").waitFor();
  await page
    .getByRole("button", {
      name: "输入提示：/ 选择协作模式，@ 指定成员",
    })
    .hover();
  await page.getByRole("tooltip").waitFor();
  await page.waitForTimeout(200);
  await page.screenshot({ path: `${SHOTS}/composer-hint.png`, fullPage: true });

  console.log(`screenshots written to ${SHOTS}`);
});
