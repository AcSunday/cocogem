import { expect, test } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import setup from "./setup";

const SHOTS = "/tmp/cocogem-supervision";
let baseURL = "";
let cleanup: (() => Promise<void>) | undefined;
test.beforeAll(async () => {
  const previous = process.env.COCOGEM_TEST_BASE;
  cleanup = await setup();
  baseURL = process.env.COCOGEM_TEST_BASE!;
  process.env.COCOGEM_TEST_BASE = previous;
  await mkdir(SHOTS, { recursive: true });
});
test.afterAll(async () => cleanup?.());

test("the mobile drawer shows a tappable new-conversation + control", async ({ page }) => {
  const status = await (await page.request.get(`${baseURL}/auth/status`)).json();
  const auth = await (
    await page.request.post(`${baseURL}${status.initialized ? "/auth/login" : "/auth/setup"}`, {
      data: { password: "sidebar-mobile-password" },
    })
  ).json();
  const headers = { "Content-Type": "application/json", "X-CSRF-Token": auth.csrf_token };
  const directories = await (
    await page.request.get(`${baseURL}/api/v1/directories`)
  ).json();
  const workspace = await (
    await page.request.post(`${baseURL}/api/v1/workspaces`, {
      headers,
      data: {
        name: "移动侧栏验收",
        description: "",
        root: directories.items[0].path,
        skill_ids: [],
      },
    })
  ).json();
  const members = await (
    await page.request.get(`${baseURL}/api/v1/workspaces/${workspace.id}/agents`)
  ).json();
  const member = members.items[0];
  const thread = await (
    await page.request.post(`${baseURL}/api/v1/workspaces/${workspace.id}/threads`, {
      headers,
      data: { title: "移动侧栏会话", agent_ids: [member.id], coordinator_id: member.id },
    })
  ).json();

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  await page.getByRole("button", { name: "打开导航" }).click();
  const sidebar = page.getByRole("complementary", { name: "导航" });
  const add = sidebar.getByRole("button", { name: "新建会话", exact: true });
  await expect(add).toBeVisible();
  // The drawer slides in over 0.2s; measure once the transform has settled.
  await expect
    .poll(async () => (await add.boundingBox())?.x ?? -1)
    .toBeGreaterThanOrEqual(0);
  const box = await add.boundingBox();
  expect(box).not.toBeNull();
  // The icon target must stay at least 24x24 inside the open drawer.
  expect(box!.width).toBeGreaterThanOrEqual(24);
  expect(box!.height).toBeGreaterThanOrEqual(24);
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(390);
  await page.screenshot({ path: `${SHOTS}/sidebar-mobile-drawer.png` });
});
