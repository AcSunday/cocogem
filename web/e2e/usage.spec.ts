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

test("far-left rail opens token usage dashboard, persists on reload, and clicks through to threads", async ({
  page,
}) => {
  test.setTimeout(120000);
  const status = await (await page.request.get(baseURL + "/auth/status")).json();
  const auth = await (
    await page.request.post(
      baseURL + (status.initialized ? "/auth/login" : "/auth/setup"),
      { data: { password: "usage-e2e-password" } },
    )
  ).json();
  const headers = { "X-CSRF-Token": auth.csrf_token };

  const workspace = await (
    await page.request.post(baseURL + "/api/v1/workspaces", {
      headers,
      data: { name: "用量测试项目" },
    })
  ).json();
  const thread = await (
    await page.request.post(
      `${baseURL}/api/v1/workspaces/${workspace.id}/threads`,
      { headers, data: { title: "统计 Token 会话" } },
    )
  ).json();

  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  const composer = page.locator("#composer-input");
  await expect(composer).toBeVisible();
  await composer.fill("帮我快速验证 Token 消耗统计。");
  await composer.press("Enter");
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 30000,
  });

  // Switch to Token Usage view via far-left rail button.
  const rail = page.getByRole("complementary", { name: "项目视图" });
  const usageBtn = rail.getByRole("button", { name: "Tokens 用量" });
  await usageBtn.click();

  await expect(page).toHaveURL(/\?view=usage$/);
  await expect(usageBtn).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("navigation", { name: "会话列表" })).toHaveCount(0);

  const dashboard = page.getByRole("region", { name: "Tokens 用量统计" });
  await expect(dashboard).toBeVisible();
  await expect(dashboard.getByText("总 Tokens")).toBeVisible();
  await expect(dashboard.getByText("缓存读取命中")).toBeVisible();
  await expect(dashboard.getByText("统计 Token 会话").first()).toBeVisible();

  // Reloading preserves ?view=usage.
  await page.reload();
  await expect(page).toHaveURL(/\?view=usage$/);
  await expect(
    page.getByRole("region", { name: "Tokens 用量统计" }),
  ).toBeVisible();

  // Switching time range and breakdown tabs works cleanly.
  await page.getByRole("button", { name: "今天", exact: true }).click();
  await expect(page.getByText("今日分时消耗趋势")).toBeVisible();
  await page.getByRole("tab", { name: "按项目", exact: true }).click();
  await expect(
    dashboard.locator("span", { hasText: "用量测试项目" }).first(),
  ).toBeVisible();

  // Clicking the top conversation navigates directly to the thread.
  await dashboard
    .getByRole("button", { name: /统计 Token 会话/ })
    .first()
    .click();
  await expect(page).toHaveURL(new RegExp(`/w/${workspace.id}/t/${thread.id}$`));
});
