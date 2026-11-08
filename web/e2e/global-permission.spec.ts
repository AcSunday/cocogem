import { expect, test, type Page } from "@playwright/test";
import setup from "./setup";

/**
 * The global permission is one server-owned value. Composer and 设置 → 权限
 * read and write it through the same field-merge PATCH, and two visible windows
 * converge within 2 seconds. Saving a non-permission field never resets it.
 */

let baseURL = "";
let cleanup: (() => Promise<void>) | undefined;
test.beforeAll(async () => {
  const previous = process.env.COCOGEM_TEST_BASE;
  cleanup = await setup();
  baseURL = process.env.COCOGEM_TEST_BASE!;
  process.env.COCOGEM_TEST_BASE = previous;
});
test.afterAll(async () => await cleanup?.());

async function signIn(page: Page) {
  const status = await (await page.request.get(baseURL + "/auth/status")).json();
  const auth = await (
    await page.request.post(
      baseURL + (status.initialized ? "/auth/login" : "/auth/setup"),
      { data: { password: "global-permission-password" } },
    )
  ).json();
  await page.context().addCookies((await page.request.storageState()).cookies);
  return { "X-CSRF-Token": auth.csrf_token as string };
}

async function seed(page: Page, headers: Record<string, string>) {
  const workspace = await (
    await page.request.post(baseURL + "/api/v1/workspaces", {
      headers,
      data: { name: "全局权限验收" },
    })
  ).json();
  return workspace;
}

const permissionTab = (page: Page) =>
  page.getByRole("dialog", { name: "设置" }).getByRole("tab", { name: "权限", exact: true });

test("the permission page shows three modes and no runtime internals", async ({
  page,
}) => {
  const headers = await signIn(page);
  const workspace = await seed(page, headers);
  await page.goto(`${baseURL}/w/${workspace.id}`);
  await page.getByLabel("输入问题").waitFor();

  await page.getByRole("button", { name: "设置", exact: true }).click();
  const settings = page.getByRole("dialog", { name: "设置" });
  await expect(permissionTab(page)).toBeVisible();
  await permissionTab(page).click();
  const panel = settings.getByRole("tabpanel", { name: "权限" });
  for (const name of ["受控", "项目内自主", "完全访问"]) {
    await expect(
      panel.getByRole("radio", { name: new RegExp(name) }),
    ).toBeVisible();
  }
  // Internal terms never reach the page.
  const text = await panel.innerText();
  expect(text).not.toContain("epoch");
  expect(text).not.toContain("代次");
  expect(text).not.toContain("permission_epoch");
  // The plain consequence is stated instead.
  expect(text).toContain("都会失效");
});

test("saving the permission from settings updates the composer and survives a reload", async ({
  page,
}) => {
  const headers = await signIn(page);
  const workspace = await seed(page, headers);
  await page.goto(`${baseURL}/w/${workspace.id}`);
  const composerMode = page.getByRole("combobox", { name: "权限模式" });
  await expect(composerMode).toBeVisible();

  const before = await (
    await page.request.get(baseURL + "/api/v1/settings")
  ).json();
  // Only the permission is patched: collaboration defaults must be preserved.
  const patched = await page.request.patch(baseURL + "/api/v1/settings", {
    headers: { ...headers, "If-Match": String(before.revision) },
    data: { execution_mode: "full_access" },
  });
  expect(patched.ok()).toBeTruthy();
  const after = await patched.json();
  expect(after.execution_mode).toBe("full_access");
  expect(after.collaboration_budget).toEqual(before.collaboration_budget);
  expect(after.default_coordinator_id).toBe(before.default_coordinator_id);
  expect(after.permission_epoch).toBeGreaterThan(before.permission_epoch ?? 0);

  // The visible composer follows the server within the bounded refresh.
  await expect
    .poll(() => composerMode.getAttribute("value"), { timeout: 4000 })
    .toBe("full_access");

  // Saving collaboration must not reset the permission.
  const collaboration = await page.request.patch(
    baseURL + "/api/v1/settings",
    {
      headers: { ...headers, "If-Match": String(after.revision) },
      data: {
        collaboration_budget: {
          auto_continuations: 12,
          parallel_branches: 3,
          writer_branches: 1,
          fix_rounds: 2,
          no_progress_failures: 1,
        },
      },
    },
  );
  expect(collaboration.ok()).toBeTruthy();
  const saved = await collaboration.json();
  expect(saved.execution_mode).toBe("full_access");
  expect(saved.collaboration_budget.auto_continuations).toBe(12);

  await page.reload();
  await expect(
    page.getByRole("combobox", { name: "权限模式" }),
  ).toHaveAttribute("value", "full_access");
});

test("changing the permission in one window reaches another within 2 seconds", async ({
  browser,
}) => {
  const first = await browser.newContext();
  const pageA = await first.newPage();
  const headers = await signIn(pageA);
  // The second window is an independent context that shares only the session
  // cookie, so the two windows converge through the server, not local state.
  const second = await browser.newContext();
  await second.addCookies(await first.cookies());
  const pageB = await second.newPage();
  const workspace = await seed(pageA, headers);

  await pageA.goto(`${baseURL}/w/${workspace.id}`);
  await pageB.goto(`${baseURL}/w/${workspace.id}`);
  const modeA = pageA.getByRole("combobox", { name: "权限模式" });
  const modeB = pageB.getByRole("combobox", { name: "权限模式" });
  await expect(modeA).toBeVisible();
  await expect(modeB).toBeVisible();
  // Both start from the stored value, so the later change is a real transition.
  const startA = await modeA.getAttribute("value");
  await expect
    .poll(() => modeB.getAttribute("value"), { timeout: 4000 })
    .toBe(startA);
  expect(startA).not.toBe("controlled");

  // Change it from window A through the settings page.
  await pageA.getByRole("button", { name: "设置", exact: true }).click();
  const settingsA = pageA.getByRole("dialog", { name: "设置" });
  await settingsA.getByRole("tab", { name: "权限", exact: true }).click();
  const panelA = settingsA.getByRole("tabpanel", { name: "权限" });
  // The radio is controlled by the async save, so click and await the result
  // instead of asserting the DOM state synchronously.
  await panelA.getByRole("radio", { name: /受控/ }).click();
  await expect(panelA.getByText(/已切换为受控/)).toBeVisible({ timeout: 5000 });
  await expect(panelA.getByRole("radio", { name: /受控/ })).toBeChecked();

  // Both windows converge within the 2-second bound.
  await expect
    .poll(() => modeA.getAttribute("value"), { timeout: 2000 })
    .toBe("controlled");
  await expect
    .poll(() => modeB.getAttribute("value"), { timeout: 2000 })
    .toBe("controlled");

  await first.close();
  await second.close();
});
