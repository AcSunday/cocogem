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

const MARKER = "权限审批浏览器验收：";

async function startPermissionTask(
  page: Page,
  headers: Record<string, string>,
  workspace: string,
  goal: string,
) {
  const created = await page.request.post(
    `${baseURL}/api/v1/workspaces/${workspace}/tasks`,
    {
      headers,
      data: {
        request_id: crypto.randomUUID(),
        title: goal.slice(MARKER.length).slice(0, 40) || "权限审批",
        goal,
      },
    },
  );
  expect(created.ok()).toBeTruthy();
  const created_ = await created.json();
  const taskID = created_.task.id as string;
  const started = await page.request.post(
    `${baseURL}/api/v1/tasks/${taskID}/start`,
    { headers, data: { request_id: crypto.randomUUID() } },
  );
  expect(started.ok()).toBeTruthy();
  const detail = await (
    await page.request.get(`${baseURL}/api/v1/tasks/${taskID}`)
  ).json();
  return {
    id: taskID,
    thread_id: detail.task.thread_id as string,
    workspace_id: detail.task.workspace_id as string,
  };
}

async function pendingRequest(
  page: Page,
  threadID: string,
  timeout = 30000,
) {
  let found: Record<string, unknown> | undefined;
  await expect
    .poll(
      async () => {
        const response = await page.request.get(
          `${baseURL}/api/v1/threads/${threadID}/permission-requests`,
        );
        if (!response.ok()) return 0;
        const body = await response.json();
        const pending = (body.items ?? []).filter(
          (item: { status: string }) => item.status === "pending",
        );
        found = pending[0];
        return pending.length;
      },
      { timeout, intervals: [250, 500, 1000] },
    )
    .toBeGreaterThan(0);
  return found!;
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

test("the permission queue notice is exactly as wide as the composer notice", async ({
  page,
}) => {
  test.setTimeout(60000);
  // The queue is the Composer's sibling, not a child of the Composer column.
  // Before the fix it stretched across the whole conversation pane while the
  // retry/failure notice above the Composer stayed at the Composer width.
  // We trigger the real host permission fixture and measure the rendered
  // PermissionQueue (.queue) and folded bar (.queueBar) against the rendered
  // Composer across both desktop and mobile (< 700px) viewports.
  const headers = await signIn(page);
  const workspace = await (
    await page.request.post(baseURL + "/api/v1/workspaces", {
      headers,
      data: { name: "权限提示布局回归" },
    })
  ).json();
  const task = await startPermissionTask(
    page,
    headers,
    workspace.id,
    `${MARKER}验证权限弹窗宽度与输入框及重试提示一致。`,
  );
  await page.goto(`${baseURL}/w/${task.workspace_id}/t/${task.thread_id}`);
  await pendingRequest(page, task.thread_id);

  const composer = page.locator("form[data-composer]");
  const queue = page.getByRole("region", { name: "待处理授权" }).last();
  await expect(composer).toBeVisible();
  await expect(queue).toBeVisible();

  // Desktop viewport: queue matches composer width and does not stretch across pane.
  await page.setViewportSize({ width: 1280, height: 720 });
  const desktopWidths = await page.evaluate(() => {
    const composer = document.querySelector("form[data-composer]");
    if (!composer) throw new Error("composer not rendered");
    const parent = composer.parentElement;
    if (!parent) throw new Error("composer has no parent");
    const queues = document.querySelectorAll("section[aria-label='待处理授权']");
    const queue = queues[queues.length - 1];
    if (!queue) throw new Error("queue not rendered");
    return {
      pane: parent.getBoundingClientRect().width,
      composer: composer.getBoundingClientRect().width,
      queue: queue.getBoundingClientRect().width,
    };
  });
  expect(desktopWidths.pane).toBeGreaterThan(0);
  expect(desktopWidths.queue).toBeCloseTo(desktopWidths.composer, 1);
  expect(desktopWidths.queue).toBeLessThan(desktopWidths.pane);

  // Mobile viewport (<= 700px): both queue and composer switch to calc(100% - 24px).
  await page.setViewportSize({ width: 390, height: 844 });
  const mobileWidths = await page.evaluate(() => {
    const composer = document.querySelector("form[data-composer]");
    if (!composer) throw new Error("composer not rendered");
    const parent = composer.parentElement;
    if (!parent) throw new Error("composer has no parent");
    const queues = document.querySelectorAll("section[aria-label='待处理授权']");
    const queue = queues[queues.length - 1];
    if (!queue) throw new Error("queue not rendered");
    return {
      pane: parent.getBoundingClientRect().width,
      composer: composer.getBoundingClientRect().width,
      queue: queue.getBoundingClientRect().width,
    };
  });
  expect(mobileWidths.queue).toBeCloseTo(mobileWidths.composer, 1);
  expect(mobileWidths.queue).toBeCloseTo(mobileWidths.pane - 24, 1);

  // Folded queue bar (.queueBar): dismissing keeps the same box on both mobile and desktop.
  await queue.getByRole("button", { name: "稍后处理" }).click();
  const reopen = page.getByRole("button", { name: "重新打开" });
  await expect(reopen).toBeVisible();

  const mobileBarWidths = await page.evaluate(() => {
    const composer = document.querySelector("form[data-composer]");
    if (!composer) throw new Error("composer not rendered");
    const parent = composer.parentElement;
    if (!parent) throw new Error("composer has no parent");
    const reopenBtn = Array.from(parent.querySelectorAll("button")).find((b) =>
      b.textContent?.includes("重新打开"),
    );
    const bar = reopenBtn?.parentElement;
    if (!bar) throw new Error("queueBar not rendered");
    return {
      pane: parent.getBoundingClientRect().width,
      composer: composer.getBoundingClientRect().width,
      bar: bar.getBoundingClientRect().width,
    };
  });
  expect(mobileBarWidths.bar).toBeCloseTo(mobileBarWidths.composer, 1);
  expect(mobileBarWidths.bar).toBeCloseTo(mobileBarWidths.pane - 24, 1);

  await page.setViewportSize({ width: 1280, height: 720 });
  const desktopBarWidths = await page.evaluate(() => {
    const composer = document.querySelector("form[data-composer]");
    if (!composer) throw new Error("composer not rendered");
    const parent = composer.parentElement;
    if (!parent) throw new Error("composer has no parent");
    const reopenBtn = Array.from(parent.querySelectorAll("button")).find((b) =>
      b.textContent?.includes("重新打开"),
    );
    const bar = reopenBtn?.parentElement;
    if (!bar) throw new Error("queueBar not rendered");
    return {
      pane: parent.getBoundingClientRect().width,
      composer: composer.getBoundingClientRect().width,
      bar: bar.getBoundingClientRect().width,
    };
  });
  expect(desktopBarWidths.bar).toBeCloseTo(desktopBarWidths.composer, 1);
  expect(desktopBarWidths.bar).toBeLessThan(desktopBarWidths.pane);
});

test("board controls stay inside a 360px viewport", async ({ page }) => {
  const headers = await signIn(page);
  const workspace = await seedTasks(page, headers);
  await page.setViewportSize({ width: 360, height: 780 });
  await page.goto(`${baseURL}/w/${workspace.id}?view=tasks`);
  await page.getByRole("button", { name: "看板", exact: true }).click();
  await expect(page.getByRole("button", { name: "清单", exact: true })).toBeVisible();

  const switchGroup = page.getByRole("group", { name: "任务视图" });
  const topbar = switchGroup.locator("xpath=ancestor::header");
  const switchBox = await switchGroup.boundingBox();
  const topbarBox = await topbar.boundingBox();
  expect(switchBox).not.toBeNull();
  expect(topbarBox).not.toBeNull();
  expect(switchBox!.x).toBeGreaterThanOrEqual(0);
  expect(switchBox!.x + switchBox!.width).toBeLessThanOrEqual(360);
  expect(switchBox!.y).toBeGreaterThanOrEqual(topbarBox!.y);
  expect(switchBox!.y + switchBox!.height).toBeLessThanOrEqual(
    topbarBox!.y + topbarBox!.height,
  );

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

test("the switch stays at the top right and the board is compact on wide screens", async ({ page }) => {
  const headers = await signIn(page);
  const workspace = await seedTasks(page, headers);
  await page.setViewportSize({ width: 1920, height: 1200 });
  await page.goto(`${baseURL}/w/${workspace.id}?view=tasks`);

  const switchGroup = page.getByRole("group", { name: "任务视图" });
  const topbar = switchGroup.locator("xpath=ancestor::header");
  const switchBox = await switchGroup.boundingBox();
  const topbarBox = await topbar.boundingBox();
  expect(switchBox).not.toBeNull();
  expect(topbarBox).not.toBeNull();
  expect(switchBox!.x + switchBox!.width).toBeGreaterThan(1920 - 36);
  expect(switchBox!.y).toBeGreaterThanOrEqual(topbarBox!.y);
  expect(switchBox!.y + switchBox!.height).toBeLessThanOrEqual(
    topbarBox!.y + topbarBox!.height,
  );

  await page.getByRole("button", { name: "看板", exact: true }).click();
  const columns = page.getByRole("region", { name: "待办任务" }).locator("xpath=..");
  const timeline = page.getByTestId("conversation-timeline");
  const columnsBox = await columns.boundingBox();
  const timelineBox = await timeline.boundingBox();
  expect(columnsBox).not.toBeNull();
  expect(timelineBox).not.toBeNull();
  expect(columnsBox!.width).toBeLessThanOrEqual(1280);
  expect(columnsBox!.height).toBeLessThanOrEqual(720);
  expect(columnsBox!.x - timelineBox!.x).toBeCloseTo(
    timelineBox!.x + timelineBox!.width - columnsBox!.x - columnsBox!.width,
    0,
  );

  await page.reload();
  await expect(page.getByRole("button", { name: "看板", exact: true })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await page.setViewportSize({ width: 1247, height: 1219 });
  const fit = await columns.evaluate((node) => ({
    clientWidth: node.clientWidth,
    scrollWidth: node.scrollWidth,
  }));
  expect(fit.scrollWidth - fit.clientWidth).toBeLessThanOrEqual(1);
  await expect(page.getByRole("region", { name: "已验收任务" })).toBeInViewport();

  await page.getByRole("button", { name: "清单", exact: true }).click();
  const listSwitchBox = await switchGroup.boundingBox();
  expect(listSwitchBox!.y).toBeCloseTo(switchBox!.y, 0);
  expect(listSwitchBox!.x + listSwitchBox!.width).toBeGreaterThan(1247 - 36);
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
