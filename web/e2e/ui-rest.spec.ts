import { mkdir } from "node:fs/promises";
import { expect, test, type Locator, type Page } from "@playwright/test";
import setup from "./setup";

const PASSWORD = "ui-rest-password";
const SHOTS = "/tmp/cocogem-supervision";
const STREAM_FIRST = "流式第一段：可见的阶段性正文。";
const STREAM_SECOND = "流式第二段：持续追加的正文。";
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

async function selectOption(page: Page, control: Locator, value: string) {
  await control.click();
  await page
    .getByRole("listbox")
    .locator(`[role="option"][data-value="${value}"]`)
    .click();
}

/** Resolves a theme token the same way the browser would paint it. */
async function themeColor(page: Page, token: string) {
  return page.evaluate((name) => {
    const probe = document.createElement("div");
    probe.style.backgroundColor = `var(${name})`;
    document.body.append(probe);
    const color = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return color;
  }, token);
}

async function authenticate(page: Page) {
  const status = await (
    await page.request.get(`${baseURL}/auth/status`)
  ).json();
  const path = status.initialized ? "/auth/login" : "/auth/setup";
  const response = await page.request.post(`${baseURL}${path}`, {
    data: { password: PASSWORD },
  });
  if (!response.ok())
    throw new Error(`authentication failed: ${response.status()}`);
  const system = await (
    await page.request.get(`${baseURL}/api/v1/system`)
  ).json();
  await page.goto(baseURL);
  return system.csrf_token as string;
}

async function api(
  page: Page,
  csrf: string,
  method: string,
  path: string,
  data?: unknown,
) {
  const response = await page.request.fetch(`${baseURL}${path}`, {
    method,
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    data: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!response.ok())
    throw new Error(
      `${method} ${path}: ${response.status()} ${await response.text()}`,
    );
  return response.json();
}

test("run status, stop control, mentions, trash lifecycle and settings screenshots", async ({
  page,
}) => {
  test.setTimeout(180000);
  const csrf = await authenticate(page);
  await expect(
    page.getByRole("heading", { name: "把问题交给一个团队。" }),
  ).toBeVisible();
  const directories = await api(page, csrf, "GET", "/api/v1/directories");
  const workspace = await api(page, csrf, "POST", "/api/v1/workspaces", {
    name: "UI 剩余验收",
    description: "",
    root: directories.items[0].path,
    skill_ids: [],
  });
  const members = await api(
    page,
    csrf,
    "GET",
    `/api/v1/workspaces/${workspace.id}/agents`,
  );
  expect(members.items.length).toBeGreaterThan(1);
  const ids = members.items.map((member: { id: string }) => member.id);
  const member = members.items[0];
  const mentionMember = members.items[1];
  const thread = await api(
    page,
    csrf,
    "POST",
    `/api/v1/workspaces/${workspace.id}/threads`,
    {
      title: "UI 状态验收",
      agent_ids: ids,
      coordinator_id: member.id,
      workflow: "free",
    },
  );
  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  await expect(
    page.getByRole("heading", { name: "UI 状态验收", exact: true }),
  ).toBeVisible();

  const sidebar = page.getByRole("complementary", { name: "导航" });
  const workspaceSwitch = sidebar.getByRole("combobox", {
    name: "切换工作空间",
  });
  const newThread = sidebar.getByRole("button", {
    name: "新建会话",
    exact: true,
  });
  const search = sidebar.getByLabel("搜索会话");
  const searchField = search.locator("xpath=..");
  const placeholderOpacity = () => search.evaluate(
    (element) => getComputedStyle(element, "::placeholder").opacity,
  );
  const surface2 = await themeColor(page, "--surface-2");
  const border = await themeColor(page, "--border");
  const transparent = "rgba(0, 0, 0, 0)";

  // The three top controls stay quiet until hover or keyboard focus; the new
  // conversation action shows a chat icon and the switch label stays visual-free.
  await expect(workspaceSwitch).toBeEnabled();
  await expect(newThread).toBeEnabled();
  await page.mouse.move(1, 1);
  await expect.poll(placeholderOpacity).toBe("1");
  for (const control of [workspaceSwitch, newThread, searchField]) {
    await expect(control).toHaveCSS("background-color", transparent);
    await expect(control).toHaveCSS("border-top-color", transparent);
  }
  await expect(sidebar.getByText("切换工作空间", { exact: true })).toHaveCount(
    0,
  );
  await expect(newThread.locator('svg path[d^="M21 12a8 8"]')).toHaveCount(1);
  await page.screenshot({ path: `${SHOTS}/final-sidebar-desktop.png` });

  for (const control of [workspaceSwitch, newThread, searchField]) {
    await control.hover();
    await expect(control).toHaveCSS("background-color", surface2);
    await expect(control).toHaveCSS("border-top-color", border);
  }

  // Keyboard focus must show the same feedback with the pointer far away.
  await page.mouse.move(1, 1);
  await sidebar.locator("a").first().focus();
  await page.keyboard.press("Tab");
  await expect(workspaceSwitch).toBeFocused();
  await expect(workspaceSwitch).toHaveCSS("background-color", surface2);
  await expect(workspaceSwitch).toHaveCSS("border-top-color", border);
  await page.keyboard.press("Tab");
  await expect(newThread).toBeFocused();
  await expect(newThread).toHaveCSS("background-color", surface2);
  await expect(newThread).toHaveCSS("border-top-color", border);
  await page.keyboard.press("Tab");
  await expect(search).toBeFocused();
  await expect(searchField).toHaveCSS("background-color", surface2);
  await expect(searchField).toHaveCSS("border-top-color", border);
  await expect.poll(placeholderOpacity).toBe("0");
  await search.fill("不存在的会话标题");
  await expect(sidebar.getByText("没有匹配的会话。")).toBeVisible();
  await search.clear();
  await expect(sidebar.getByRole("button", { name: "UI 状态验收", exact: true }))
    .toBeVisible();
  await expect.poll(placeholderOpacity).toBe("0");
  await search.press("Tab");
  await expect.poll(placeholderOpacity).toBe("1");
  await search.click();
  await expect(search).toBeFocused();
  await expect.poll(placeholderOpacity).toBe("0");

  const input = page.getByLabel("输入问题");
  const send = page.getByRole("button", { name: "发送", exact: true });
  await input.fill(`请 @${mentionMember.handle} 复核协作成本。`);
  await page.mouse.move(1, 1);
  const sendColor = await send.evaluate(
    (element) => getComputedStyle(element).backgroundColor,
  );
  await send.click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 30000,
  });
  await expect(
    sidebar.getByRole("status", { name: "运行状态：已完成" }),
  ).toBeVisible();

  // Real render data resolves the @ text to the member's avatar and profile.
  const mention = page
    .getByText(`@${mentionMember.handle}`, { exact: true })
    .first();
  await mention.focus();
  const tooltip = page.getByRole("tooltip");
  await expect(tooltip).toContainText(mentionMember.display_name);
  await expect(tooltip).toContainText(`@${mentionMember.handle}`);
  // The card is fixed to the viewport and hits above the top bar, so its
  // top-left corner must not be covered by another surface.
  const box = await tooltip.boundingBox();
  expect(box).not.toBeNull();
  const viewport = page.viewportSize()!;
  expect(box!.x).toBeGreaterThanOrEqual(8);
  expect(box!.y).toBeGreaterThanOrEqual(8);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewport.width - 8);
  expect(
    await page.evaluate(
      ({ x, y }) => {
        const tip = document.querySelector('[role="tooltip"]');
        const hit = document.elementFromPoint(x, y);
        return !!tip && !!hit && tip.contains(hit);
      },
      { x: box!.x + 4, y: box!.y + 4 },
    ),
  ).toBe(true);
  await page.keyboard.press("Escape");
  await page.screenshot({ path: `${SHOTS}/ui-rest-conversation-light.png` });

  // The stop control carries the same theme background as send and is a
  // filled square; cancelling still settles the real run.
  await input.fill("等待取消：请保持运行直到我停止。");
  await send.click();
  const stop = page.getByRole("button", { name: "停止协作" });
  await expect(stop).toBeVisible({ timeout: 15000 });
  await expect(
    sidebar.getByRole("status", { name: "运行状态：执行中" }),
  ).toBeVisible({ timeout: 15000 });
  await page.mouse.move(1, 1);
  await expect
    .poll(() =>
      stop.evaluate((element) => getComputedStyle(element).backgroundColor),
    )
    .toBe(sendColor);
  await expect(stop.locator("rect")).toHaveAttribute("fill", "currentColor");
  await stop.click();
  await expect(page.getByTestId("run-status")).toHaveText("已停止", {
    timeout: 30000,
  });
  await expect(
    sidebar.getByRole("status", { name: "运行状态：已停止" }),
  ).toBeVisible({ timeout: 30000 });

  // Service tab restores install/connection diagnostics and the lock entry.
  await page.getByRole("button", { name: "设置", exact: true }).click();
  const settings = page.getByRole("dialog", { name: "设置" });
  await settings.getByRole("tab", { name: "服务" }).click();
  await expect(settings.getByText(/已安装不代表连接或模型可用/)).toBeVisible();
  await expect(settings.getByText(/连接未验证/).first()).toBeVisible();
  await expect(
    settings.getByRole("button", { name: "锁定工作空间" }),
  ).toBeVisible();
  await page.screenshot({
    path: `${SHOTS}/ui-rest-settings-service-light.png`,
  });
  await settings.getByRole("button", { name: "关闭窗口" }).click();
  await selectOption(page, page.getByLabel("外观模式"), "dark");

  // Dark keeps the same quiet idle controls; capture the final sidebar too.
  await page.mouse.move(1, 1);
  for (const control of [workspaceSwitch, newThread, searchField]) {
    await expect(control).toHaveCSS("background-color", transparent);
    await expect(control).toHaveCSS("border-top-color", transparent);
  }
  await page.screenshot({
    path: `${SHOTS}/final-sidebar-desktop-dark.png`,
    animations: "disabled",
  });

  // Seed one soft-deleted conversation, verify the trash labels, then save a
  // retention change that preserves the existing collaboration defaults.
  const doomed = await api(
    page,
    csrf,
    "POST",
    `/api/v1/workspaces/${workspace.id}/threads`,
    {
      title: "回收站验收",
      agent_ids: ids,
      coordinator_id: member.id,
      workflow: "free",
    },
  );
  await api(page, csrf, "DELETE", `/api/v1/threads/${doomed.id}`);
  const before = await api(page, csrf, "GET", "/api/v1/settings");
  await page.getByRole("button", { name: "设置", exact: true }).click();
  await settings.getByRole("tab", { name: "回收站" }).click();
  await expect(settings.getByText("回收站验收")).toBeVisible();
  await expect(
    settings.getByText("UI 剩余验收", { exact: true }).first(),
  ).toBeVisible();
  await expect(settings.getByText(/删除于 /)).toBeVisible();
  await expect(settings.getByText(/计划于 .* 自动清理/)).toBeVisible();
  await page.screenshot({
    path: `${SHOTS}/ui-rest-settings-trash-dark.png`,
  });
  const saved = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/settings") &&
      response.request().method() === "PATCH",
  );
  await settings.getByLabel("回收站保留天数").fill("45");
  await settings.getByRole("button", { name: "保存保留周期" }).click();
  const request = (await saved).request();
  expect(request.headers()["if-match"]).toBe(String(before.revision));
  const body = request.postDataJSON();
  expect(body).toMatchObject({
    default_coordinator_id: before.default_coordinator_id,
    assignments: before.assignments,
    retention_days: 45,
  });
  await expect(
    settings.getByText("保留周期已保存，回收站中的清理时间已同步更新。"),
  ).toBeVisible();

  // Restore navigates back to the conversation and refreshes the sidebar.
  await settings.getByRole("button", { name: "恢复会话：回收站验收" }).click();
  await settings.getByRole("button", { name: "确认恢复" }).click();
  await expect(
    page.getByRole("heading", { name: "回收站验收", exact: true }),
  ).toBeVisible();
  await expect(settings).toHaveCount(0);
  await expect(
    sidebar.getByRole("button", { name: "回收站验收", exact: true }),
  ).toBeVisible();

  // Delete again and purge only after the explicit destructive confirmation.
  await sidebar
    .getByRole("button", { name: "回收站验收", exact: true })
    .hover();
  await page.getByRole("button", { name: "更多会话操作：回收站验收" }).click();
  await page.getByRole("menuitem", { name: "删除" }).click();
  await page
    .getByRole("dialog", { name: "删除会话" })
    .getByRole("button", { name: "删除会话" })
    .click();
  await expect(
    sidebar.getByRole("button", { name: "回收站验收", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "设置", exact: true }).click();
  await settings.getByRole("tab", { name: "回收站" }).click();
  await settings
    .getByRole("button", { name: "永久删除会话：回收站验收" })
    .click();
  await expect(
    settings.getByRole("alertdialog", { name: "确认永久删除 回收站验收" }),
  ).toContainText("无法恢复");
  await settings.getByRole("button", { name: "确认永久删除" }).click();
  await expect(settings.getByText("回收站为空。")).toBeVisible();

  // Narrow screens keep the tab rail usable without a fixed 200px column.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(settings.getByRole("tab", { name: "服务" })).toBeVisible();
  expect(
    await settings.getByRole("tab", { name: "服务" }).evaluate((tab) => {
      const box = tab.getBoundingClientRect();
      return box.width > 0 && box.left >= 0 && box.right <= innerWidth;
    }),
  ).toBe(true);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await settings.getByRole("tab", { name: "服务" }).click();
  await page.screenshot({ path: `${SHOTS}/ui-rest-settings-mobile.png` });
  const settingsBox = await settings.boundingBox();
  expect(settingsBox).not.toBeNull();
  expect(settingsBox!.x).toBeGreaterThanOrEqual(0);
  expect(settingsBox!.x + settingsBox!.width).toBeLessThanOrEqual(390);

  // The phone drawer keeps every top control inside the viewport.
  await settings.getByRole("button", { name: "关闭窗口" }).click();
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await expect(sidebar).toBeVisible();
  // Wait out the drawer slide before measuring its page bounds.
  await expect(sidebar).toHaveCSS("transform", "none");
  const sidebarBox = await sidebar.boundingBox();
  expect(sidebarBox).not.toBeNull();
  expect(sidebarBox!.x).toBeGreaterThanOrEqual(0);
  expect(sidebarBox!.x + sidebarBox!.width).toBeLessThanOrEqual(390);
  for (const control of [workspaceSwitch, newThread, search]) {
    const box = await control.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(390);
  }
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

test("streams staged assistant text while the run is active", async ({
  page,
}) => {
  test.setTimeout(120000);
  const csrf = await authenticate(page);
  const directories = await api(page, csrf, "GET", "/api/v1/directories");
  const workspace = await api(page, csrf, "POST", "/api/v1/workspaces", {
    name: "UI 流式验收",
    description: "",
    root: directories.items[0].path,
    skill_ids: [],
  });
  const members = await api(
    page,
    csrf,
    "GET",
    `/api/v1/workspaces/${workspace.id}/agents`,
  );
  const member = members.items[0];
  const thread = await api(
    page,
    csrf,
    "POST",
    `/api/v1/workspaces/${workspace.id}/threads`,
    {
      title: "流式界面验收",
      agent_ids: [member.id],
      coordinator_id: member.id,
      workflow: "free",
    },
  );
  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  await expect(
    page.getByRole("heading", { name: "流式界面验收", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("输入问题")).toBeVisible();

  // Sample the DOM in-page so the first 400ms frame cannot slip between
  // Playwright assertions; the text itself is persisted by the real fixture.
  await page.evaluate(
    ({ firstText, secondText }) => {
      const samples: {
        at: number;
        first: boolean;
        second: boolean;
        status: string;
      }[] = [];
      const sample = () => {
        // Task details can repeat the text inside a collapsed <details>; only
        // the rendered message cards count as user-visible stream progress.
        const text = [
          ...document.querySelectorAll(
            '[data-testid="conversation-timeline"] article',
          ),
        ]
          .map((element) => (element as HTMLElement).innerText)
          .join("\n");
        samples.push({
          at: Math.round(performance.now()),
          first: text.includes(firstText),
          second: text.includes(secondText),
          status:
            document
              .querySelector('[data-testid="run-status"]')
              ?.textContent?.trim() ?? "",
        });
      };
      const observer = new MutationObserver(sample);
      observer.observe(document.body, {
        subtree: true,
        childList: true,
        characterData: true,
        attributes: true,
      });
      Object.assign(window, {
        __streamSamples: samples,
        __streamObserver: observer,
      });
      sample();
    },
    { firstText: STREAM_FIRST, secondText: STREAM_SECOND },
  );

  await page
    .getByLabel("输入问题")
    .fill(`流式界面验收：请 @${member.handle} 分段输出阶段正文。`);
  await page.getByRole("button", { name: "发送", exact: true }).click();

  // The task detail under the collapsed process repeats the text; the message
  // card is the visible delivery and must receive both staged parts itself.
  const streamedMessage = page
    .getByRole("article")
    .filter({ hasText: STREAM_FIRST })
    .last();
  await expect(streamedMessage).toBeVisible({ timeout: 20000 });
  await expect(streamedMessage).toContainText(STREAM_SECOND, {
    timeout: 20000,
  });
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 30000,
  });

  const samples = await page.evaluate(() => {
    const state = window as unknown as {
      __streamSamples?: {
        at: number;
        first: boolean;
        second: boolean;
        status: string;
      }[];
      __streamObserver?: MutationObserver;
    };
    state.__streamObserver?.disconnect();
    return state.__streamSamples ?? [];
  });
  const firstOnly = samples.filter((sample) => sample.first && !sample.second);
  const secondSeen = samples.find((sample) => sample.second);
  expect(firstOnly.length).toBeGreaterThan(0);
  expect(secondSeen).toBeDefined();
  expect(firstOnly[0].at).toBeLessThan(secondSeen!.at);
  expect(firstOnly.some((sample) => sample.status === "执行中")).toBe(true);
  expect(
    samples.some((sample) => sample.second && sample.status === "执行中"),
  ).toBe(true);

  // The delivered message keeps both staged parts in order.
  const finalMessage = page
    .getByRole("article")
    .filter({ hasText: STREAM_FIRST })
    .last();
  await expect(finalMessage).toContainText(STREAM_FIRST);
  await expect(finalMessage).toContainText(STREAM_SECOND);
  const finalText = await finalMessage.innerText();
  expect(finalText.indexOf(STREAM_FIRST)).toBeGreaterThanOrEqual(0);
  expect(finalText.indexOf(STREAM_SECOND)).toBeGreaterThan(
    finalText.indexOf(STREAM_FIRST),
  );
});
