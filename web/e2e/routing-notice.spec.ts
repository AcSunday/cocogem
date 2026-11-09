import { expect, test } from "@playwright/test";
import setup from "./setup";

let baseURL = "";
let cleanup: (() => Promise<void>) | undefined;
test.beforeAll(async () => {
  const previous = process.env.COCOGEM_TEST_BASE;
  cleanup = await setup();
  baseURL = process.env.COCOGEM_TEST_BASE!;
  process.env.COCOGEM_TEST_BASE = previous;
});
test.afterAll(async () => cleanup?.());

test("explicit recipients override the workflow and notices open above the input", async ({ page }, testInfo) => {
  const auth = await page.request.post(`${baseURL}/auth/setup`, {
    data: { password: "routing-notice-password" },
  });
  expect(auth.ok()).toBeTruthy();
  const { csrf_token: csrf } = await auth.json();
  const headers = { "X-CSRF-Token": csrf };
  const created = await page.request.post(`${baseURL}/api/v1/workspaces`, {
    headers,
    data: { name: "明确指派验收" },
  });
  const workspace = await created.json();
  const { items: members } = await (await page.request.get(`${baseURL}/api/v1/workspaces/${workspace.id}/agents`)).json();
  const orange = members.find((member: { handle: string }) => member.handle === "orange");
  const thread = await (await page.request.post(`${baseURL}/api/v1/workspaces/${workspace.id}/threads`, {
    headers,
    data: { title: "文档比较", agent_ids: [orange.id], coordinator_id: orange.id, workflow: "research" },
  })).json();
  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  const input = page.getByLabel("输入问题");
  await input.fill("@orange 新文档对比原文档有什么变化？");
  const previewResponse = page.waitForResponse((response) => response.url().endsWith("/routing-preview"));
  await page.getByRole("button", { name: "发送", exact: true }).click();
  const preview = await (await previewResponse).json();
  expect(preview.proposal_id).toBeTruthy();
  expect(preview.routing).toMatchObject({ decision: "assign", workflow: "free", targets: [orange.id], clarification: "" });
  await expect(page.getByTestId("run-status")).toHaveText("已完成", { timeout: 30000 });
  const current = await (await page.request.get(`${baseURL}/api/v1/threads/${thread.id}`)).json();
  expect(current.thread.workflow).toBe("research");
  expect(current.runs).toHaveLength(1);
  const view = await (await page.request.get(`${baseURL}/api/v1/runs/${current.runs[0].id}`)).json();
  expect(view.steps).toHaveLength(1);
  expect(view.steps[0]).toMatchObject({ agent_id: orange.id, role: "responder" });

  for (const { width, height, appearance } of [
    { width: 1280, height: 900, appearance: "light" },
    { width: 390, height: 844, appearance: "dark" },
  ]) {
    await page.setViewportSize({ width, height });
    await page.evaluate((value) => { document.documentElement.dataset.appearance = value; }, appearance);
    await input.fill("@missing 比较两份文档");
    await page.getByRole("button", { name: "发送", exact: true }).click();
    const notice = page.getByRole("status").filter({ hasText: "成员 @missing 不可用" });
    await expect(notice).toBeVisible();
    const form = input.locator("xpath=ancestor::form");
    const box = (await notice.boundingBox())!;
    const composer = (await form.boundingBox())!;
    expect(box.y + box.height).toBeLessThanOrEqual(composer.y);
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    const close = notice.getByRole("button", { name: "关闭提示", exact: true });
    await expect(close).toHaveCSS("border-radius", "50%");
    await expect(close.locator("svg")).toBeVisible();
    const noticeBox = (await notice.boundingBox())!;
    const closeBox = (await close.boundingBox())!;
    expect(Math.abs(closeBox.width - closeBox.height)).toBeLessThanOrEqual(1);
    expect(closeBox.x).toBeGreaterThan(noticeBox.x + noticeBox.width / 2);
    expect(closeBox.y).toBeLessThan(noticeBox.y + noticeBox.height / 2);
    await page.screenshot({ path: testInfo.outputPath(`notice-${appearance}.png`) });
    await close.click();
    await expect(notice).toHaveCount(0);
    await expect(input).toHaveValue("@missing 比较两份文档");
    await expect(input).toBeFocused();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  }

  await page.route("**/routing-preview", (route) => route.fulfill({
    status: 400,
    contentType: "application/json",
    body: JSON.stringify({ error: { code: "invalid_request", message: "无法提交，请稍后重试。" } }),
  }));
  await input.fill("保留这份草稿");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  const failure = page.getByRole("alert");
  await expect(failure).toContainText("无法提交，请稍后重试。");
  expect(await failure.evaluate((node) => !!node.closest("form"))).toBeTruthy();
  await failure.getByRole("button", { name: "关闭错误提示" }).click();
  await expect(failure).toHaveCount(0);
  await expect(input).toHaveValue("保留这份草稿");
});
