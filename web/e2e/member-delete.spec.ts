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

test("deletes a member only after an icon-only confirmation", async ({ page }, testInfo) => {
  const auth = await page.request.post(`${baseURL}/auth/setup`, {
    data: { password: "member-delete-password" },
  });
  expect(auth.ok()).toBeTruthy();
  const { csrf_token: csrf } = await auth.json();
  const headers = { "X-CSRF-Token": csrf };
  const workspace = await (
    await page.request.post(`${baseURL}/api/v1/workspaces`, {
      headers,
      data: { name: "成员删除验收" },
    })
  ).json();
  await page.goto(`${baseURL}/w/${workspace.id}`);
  await page.getByRole("button", { name: "设置", exact: true }).click();
  const settings = page.getByRole("dialog", { name: "设置" });
  await settings.getByRole("tab", { name: "成员", exact: true }).click();
  const remove = settings.getByRole("button", {
    name: "删除成员 布偶猫",
    exact: true,
  });
  await expect(remove).toBeVisible();
  expect(await remove.evaluate((node) => node.textContent)).toBe("");
  await remove.click();
  const confirm = page.getByRole("dialog", { name: "删除成员" });
  await expect(confirm).toContainText("布偶猫");
  await expect(confirm).toContainText("无法撤销");
  await page.screenshot({ path: testInfo.outputPath("member-delete.png") });
  await confirm.getByRole("button", { name: "取消" }).click();
  await expect(confirm).toHaveCount(0);
  await expect(remove).toBeVisible();
  const deleted = page.waitForResponse(
    (response) =>
      response.request().method() === "DELETE" &&
      /\/api\/v1\/agents\/[^/]+$/.test(new URL(response.url()).pathname),
  );
  await remove.click();
  await page.getByRole("dialog", { name: "删除成员" })
    .getByRole("button", { name: "确认删除" })
    .click();
  await deleted;
  await expect(remove).toHaveCount(0);
  const { items: globals } = await (
    await page.request.get(`${baseURL}/api/v1/agents`)
  ).json();
  expect(globals).toHaveLength(2);
  const { items: members } = await (
    await page.request.get(
      `${baseURL}/api/v1/workspaces/${workspace.id}/agents`,
    )
  ).json();
  expect(members).toHaveLength(2);
});
