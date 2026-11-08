import { expect, test, type Page } from "@playwright/test";
import setup from "./setup";

/**
 * Real-browser layout and computed-style regressions for the P5/P6 findings.
 * These replace the CSS-source regex tests: they measure the rendered document
 * instead of matching a stylesheet string.
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
      { data: { password: "layout-regression-password" } },
    )
  ).json();
  // Session cookie from the API request also applies to page navigations.
  await page.context().addCookies(
    (await page.request.storageState()).cookies,
  );
  return { "X-CSRF-Token": auth.csrf_token as string };
}

async function seedTasks(page: Page, headers: Record<string, string>) {
  const workspace = await (
    await page.request.post(baseURL + "/api/v1/workspaces", {
      headers,
      data: { name: "布局回归" },
    })
  ).json();
  const create = async (title: string, goal: string) => {
    const response = await page.request.post(
      `${baseURL}/api/v1/workspaces/${workspace.id}/tasks`,
      { headers, data: { request_id: crypto.randomUUID(), title, goal } },
    );
    return response.json();
  };
  // A long same-column stack proves the lane scrolls instead of cropping cards.
  for (let index = 0; index < 12; index++)
    await create(`待办任务 ${index + 1}`, `第 ${index + 1} 个目标，用于验证列内滚动。`);
  return workspace;
}

test("composer hint button renders 24×24 with a transparent background", async ({
  page,
}) => {
  const headers = await signIn(page);
  const workspace = await seedTasks(page, headers);
  await page.goto(`${baseURL}/w/${workspace.id}`);
  const hint = page.getByRole("button", {
    name: "输入提示：/ 选择协作模式，@ 指定成员",
  });
  await expect(hint).toBeVisible();

  const measure = async () =>
    hint.evaluate((node) => {
      const style = getComputedStyle(node);
      const rect = node.getBoundingClientRect();
      return {
        width: rect.width,
        height: rect.height,
        background: style.backgroundColor,
        boxShadow: style.boxShadow,
        borderRadius: style.borderRadius,
        boxSizing: style.boxSizing,
      };
    });

  const normal = await measure();
  expect(normal.width).toBe(24);
  expect(normal.height).toBe(24);
  expect(normal.boxSizing).toBe("border-box");
  // Fully transparent: the global button fill must not leak through.
  expect(["rgba(0, 0, 0, 0)", "transparent"]).toContain(normal.background);
  expect(normal.boxShadow).toBe("none");
  // A square control, never an ellipse.
  expect(normal.borderRadius).not.toBe("50%");

  await hint.hover();
  const hovered = await measure();
  expect(hovered.width).toBe(24);
  expect(hovered.height).toBe(24);
  expect(["rgba(0, 0, 0, 0)", "transparent"]).toContain(hovered.background);
  expect(hovered.boxShadow).toBe("none");

  await page.keyboard.press("Tab");
  await hint.focus();
  const focused = await measure();
  expect(focused.width).toBe(24);
  expect(focused.height).toBe(24);
  expect(["rgba(0, 0, 0, 0)", "transparent"]).toContain(focused.background);
  expect(focused.boxShadow).toBe("none");
  // Keyboard focus stays visible.
  const outline = await hint.evaluate(
    (node) => getComputedStyle(node).outlineStyle,
  );
  expect(outline).not.toBe("none");

  // Hovering only reveals the tooltip; it never submits or clears the draft.
  await hint.hover();
  await expect(page.getByRole("tooltip")).toBeVisible();
  await expect(page.getByLabel("输入问题")).toHaveValue("");
});

test("board header controls stay inside a 360px viewport", async ({ page }) => {
  const headers = await signIn(page);
  const workspace = await seedTasks(page, headers);
  await page.setViewportSize({ width: 360, height: 780 });
  await page.goto(`${baseURL}/w/${workspace.id}?view=tasks`);
  await page.getByRole("button", { name: "看板", exact: true }).click();
  await expect(page.getByRole("button", { name: "清单", exact: true })).toBeVisible();

  // The header action group is the ancestor element that contains the view
  // switch and the create button (class names are hashed by CSS modules).
  const switchGroup = page.getByRole("group", { name: "任务视图" });
  const actions = switchGroup.locator("xpath=..");
  for (const part of [actions, switchGroup]) {
    const box = await part.boundingBox();
    expect(box).not.toBeNull();
    // The action group never spills past the viewport edge.
    expect(box!.x).toBeGreaterThanOrEqual(-0.5);
    expect(box!.x + box!.width).toBeLessThanOrEqual(360 + 0.5);
  }

  for (const name of ["清单", "看板", "新建任务"]) {
    const control = page.getByRole("button", { name, exact: true });
    const rect = await control.boundingBox();
    expect(rect).not.toBeNull();
    expect(rect!.x).toBeGreaterThanOrEqual(-0.5);
    expect(rect!.x + rect!.width).toBeLessThanOrEqual(360 + 0.5);
    // Touch sizing is uniform at this breakpoint.
    expect(Math.round(rect!.height)).toBe(40);
  }

  // The document itself must not develop a horizontal scrollbar.
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
});

test("a long board column scrolls internally and keeps card height", async ({
  page,
}) => {
  const headers = await signIn(page);
  const workspace = await seedTasks(page, headers);
  // Short viewport: 12 cards cannot fit in the lane.
  await page.setViewportSize({ width: 1440, height: 620 });
  await page.goto(`${baseURL}/w/${workspace.id}?view=tasks`);
  await page.getByRole("button", { name: "看板", exact: true }).click();

  // CSS modules hash class names, so select the lane structurally: the column's
  // card area is its only scrollable child div.
  const lane = page
    .getByRole("region", { name: "待办任务" })
    .locator("div")
    .first();
  await expect(lane).toBeVisible();
  const metrics = await lane.evaluate((node) => ({
    scrollHeight: node.scrollHeight,
    clientHeight: node.clientHeight,
    overflowY: getComputedStyle(node).overflowY,
  }));
  expect(metrics.scrollHeight).toBeGreaterThan(metrics.clientHeight);
  expect(["auto", "scroll"]).toContain(metrics.overflowY);

  // Cards keep their natural height instead of being squeezed to fit.
  const heights = await lane.locator("article").evaluateAll((nodes) =>
    nodes.map((node) => node.getBoundingClientRect().height),
  );
  expect(heights.length).toBeGreaterThan(4);
  const shortest = Math.min(...heights);
  expect(shortest).toBeGreaterThan(80);

  // Scrolling the lane actually reveals later cards.
  await lane.evaluate((node) => node.scrollTo({ top: node.scrollHeight }));
  const scrollTop = await lane.evaluate((node) => node.scrollTop);
  expect(scrollTop).toBeGreaterThan(0);

  // The page body itself does not grow a horizontal scrollbar.
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
});
