import { expect, test, type Locator } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

test("create a team, run research, inspect evidence, export, follow up and cancel", async ({
  page,
}) => {
  test.setTimeout(180000);
  const selectOption = async (
    control: Locator,
    value: string | { index: number },
  ) => {
    await control.click();
    const menu = page.getByRole("listbox");
    if (typeof value === "string")
      await menu.locator(`[role="option"][data-value="${value}"]`).click();
    else await menu.getByRole("option").nth(value.index).click();
    await page.evaluate(() => {
      for (const animation of document.getAnimations()) {
        if (animation instanceof CSSTransition) animation.finish();
      }
    });
  };
  const chooseMethod = async (value: string) => {
    const input = page.getByLabel("输入问题");
    const existing = await input.inputValue();
    await input.fill(`/${value}`);
    await page.keyboard.press("Enter");
    await expect(page.getByTestId("composer-method")).toHaveCount(
      value === "free" ? 0 : 1,
    );
    if (existing) await input.fill(existing);
  };
  const openPanel = async (name: string) => {
    const toggle = page.getByRole("button", {
      name: "打开协作面板",
      exact: true,
    });
    if (await toggle.isVisible()) await toggle.click();
    await page.getByRole("tab", { name }).click();
  };
  const setAppearance = async (value: string) => {
    await page.getByRole("button", { name: "设置", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "设置" });
    await dialog.getByRole("tab", { name: "外观", exact: true }).click();
    await selectOption(dialog.getByLabel("外观模式", { exact: true }), value);
    await dialog.getByRole("button", { name: "关闭窗口" }).click();
  };
  await page.goto("/");
  await page
    .getByLabel("设置密码", { exact: true })
    .fill("browser-test-password");
  await page
    .getByLabel("确认密码", { exact: true })
    .fill("browser-test-password");
  await page
    .getByLabel("确认密码", { exact: true })
    .fill("different-test-password");
  await page.getByRole("button", { name: "设置密码并进入" }).click();
  await expect(page.getByRole("alert")).toContainText("不一致");
  await page
    .getByLabel("确认密码", { exact: true })
    .fill("browser-test-password");
  await page.getByRole("button", { name: "设置密码并进入" }).click();
  await expect(
    page.getByRole("heading", { name: "把问题交给一个团队。" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "高级：手动创建项目" }).click();
  await page.getByLabel("名称", { exact: true }).fill("猫猫研究室");
  const directory = page.getByRole("combobox", {
    name: "本地资料目录",
    exact: true,
  });
  await directory.click();
  await expect(page.getByRole("listbox")).toBeVisible();
  await directory.press("ArrowDown");
  await directory.press("Enter");
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByRole("checkbox", { name: /我理解/ })).toHaveCount(0);
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(
    page.getByRole("button", { name: "成员 3", exact: true }),
  ).toBeEnabled();
  const workspaceID = page.url().split("/w/")[1];
  const memberInput = page.getByLabel("输入问题");
  await memberInput.fill("成员入口草稿");
  await page.getByRole("button", { name: "成员 3", exact: true }).click();
  const membersDialog = page.getByRole("dialog", { name: "工作空间成员" });
  await expect(membersDialog.getByLabel("显示名")).toHaveCount(0);
  await expect(membersDialog.getByLabel("Handle")).toHaveCount(0);
  await expect(membersDialog.getByLabel("职责")).toHaveCount(0);
  await expect(membersDialog.getByLabel("头像")).toHaveCount(0);
  await expect(membersDialog.getByLabel("执行 CLI")).toHaveCount(0);
  await expect(membersDialog.getByText("在所有工作空间停用此成员")).toHaveCount(
    0,
  );
  await expect(
    membersDialog.getByRole("button", { name: "＋ 新成员" }),
  ).toHaveCount(0);
  await expect(membersDialog.getByText(/全局档案请到设置/)).toBeVisible();
  // D04: no workspace tool grants or effective intersection in the member modal.
  await expect(membersDialog.getByText("本工作空间权限")).toHaveCount(0);
  await expect(membersDialog.getByText(/当前实际生效：/)).toHaveCount(0);
  await expect(membersDialog.getByLabel("read_file")).toHaveCount(0);
  await expect(
    membersDialog.getByRole("button", { name: "保存本空间权限" }),
  ).toHaveCount(0);
  await expect(
    membersDialog.getByText(/成员的工具与命令边界由设置中的全局权限决定。移出/),
  ).toBeVisible();
  await expect(membersDialog.getByLabel("在本工作空间启用")).toBeVisible();
  await page.getByRole("button", { name: "关闭窗口" }).click();
  await expect(memberInput).toHaveValue("成员入口草稿");

  // Global profiles stay in Settings → Members; the top entry only links them.
  await page.getByRole("button", { name: "设置", exact: true }).click();
  const settingsDialog = page.getByRole("dialog", { name: "设置" });
  await settingsDialog.getByRole("tab", { name: "成员" }).click();
  await settingsDialog
    .getByRole("button", { name: /布偶猫.*@ragdoll/ })
    .click();
  await settingsDialog.getByLabel("别名（用逗号分隔）").fill("布布");
  await settingsDialog.getByRole("button", { name: "保存成员" }).click();
  await expect(settingsDialog.getByRole("status")).toContainText("已保存");
  await settingsDialog.getByLabel("职责").fill("研究资料并核对证据。");
  await settingsDialog.getByRole("button", { name: "保存成员" }).click();
  await expect(settingsDialog.getByRole("status")).toContainText("已保存");
  await settingsDialog.getByRole("button", { name: "← 返回列表" }).click();
  const createdProfile = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/agents") &&
      response.request().method() === "POST",
  );
  await settingsDialog.getByRole("button", { name: "＋ 新成员" }).click();
  await settingsDialog.getByLabel("显示名").fill("访客猫");
  await settingsDialog.getByLabel("Handle").fill("visitor");
  await settingsDialog.getByRole("button", { name: "保存成员" }).click();
  await expect(settingsDialog.getByRole("status")).toContainText("已保存");
  const visitor = await (await createdProfile).json();
  // Role defaults are global; sessions only report the routing they receive.
  await settingsDialog.getByRole("tab", { name: "协作" }).click();
  const researchDefaults = settingsDialog.getByRole("group", {
    name: "研究协作",
  });
  for (const [role, member] of [
    ["研究", "缅因猫 (@maine)"],
    ["质疑", "布偶猫 (@ragdoll)"],
    ["汇总", "橘猫 (@orange)"],
  ] as const) {
    await researchDefaults
      .getByRole("combobox", { name: role, exact: true })
      .click();
    await settingsDialog
      .getByRole("option", { name: member, exact: true })
      .click();
  }
  await settingsDialog.getByRole("button", { name: "保存协作默认" }).click();
  await expect(settingsDialog.getByRole("status")).toContainText(
    "协作默认已保存",
  );
  await page.getByRole("button", { name: "关闭窗口" }).click();

  // Join, restrict, disable and remove only this workspace's relationship.
  await page.getByRole("button", { name: "成员 3", exact: true }).click();
  await membersDialog
    .getByRole("button", { name: /^访客猫 \(@visitor\) · 可加入$/ })
    .click();
  // D04: there is no tool checkbox to change; joining only manages membership.
  await expect(membersDialog.getByLabel("web_search")).toHaveCount(0);
  const joined = page.waitForResponse(
    (response) =>
      response
        .url()
        .endsWith(`/api/v1/workspaces/${workspaceID}/members/${visitor.id}`) &&
      response.request().method() === "PUT",
  );
  await membersDialog.getByRole("button", { name: "加入本工作空间" }).click();
  const joinRequest = (await joined).request();
  expect(joinRequest.headers()["if-match"]).toBeUndefined();
  // A fresh link starts empty; the boundary comes from the global permission.
  expect(joinRequest.postDataJSON()).toMatchObject({
    enabled: true,
    tools: null,
  });
  await expect(membersDialog.getByRole("status")).toContainText(
    "本空间成员设置已保存",
  );
  await membersDialog.getByLabel("在本工作空间启用").uncheck();
  const updated = page.waitForResponse(
    (response) =>
      response
        .url()
        .endsWith(`/api/v1/workspaces/${workspaceID}/members/${visitor.id}`) &&
      response.request().method() === "PATCH",
  );
  await membersDialog
    .getByRole("button", { name: "保存本空间设置" })
    .click();
  const updateRequest = (await updated).request();
  expect(updateRequest.headers()["if-match"]).toBe("1");
  // Enabling/disabling is the only membership field the UI edits.
  expect(updateRequest.postDataJSON()).toMatchObject({ enabled: false });
  const removed = page.waitForResponse(
    (response) =>
      response
        .url()
        .endsWith(`/api/v1/workspaces/${workspaceID}/members/${visitor.id}`) &&
      response.request().method() === "DELETE",
  );
  await membersDialog.getByRole("button", { name: "移出本工作空间" }).click();
  expect((await removed).request().headers()["if-match"]).toBe("2");
  await expect(
    membersDialog.getByRole("button", {
      name: /^访客猫 \(@visitor\) · 可加入$/,
    }),
  ).toBeVisible();
  await page.getByRole("button", { name: "关闭窗口" }).click();
  await expect(memberInput).toHaveValue("成员入口草稿");
  await memberInput.fill("");
  const messageInput = page.getByLabel("输入问题");
  await chooseMethod("research");
  await messageInput.fill(
    [
      "研究多 Agent 协作的适用场景，说明证据和限制。",
      "https://example.org/cocogem/research",
      "https://example.org/cocogem/limits",
      "https://example.org/cocogem/cases",
    ].join("\n"),
  );
  await expect(page.getByLabel("资料链接")).toHaveCount(0);
  await expect(page.getByLabel("研究范围")).toHaveCount(0);
  // Tab back into the composer so the caret is where a user typing would be.
  await messageInput.focus();
  await messageInput.evaluate((n) => n.setSelectionRange(n.value.length, n.value.length));
  await messageInput.press("Shift+Enter");
  await expect(messageInput).toHaveValue(/\n$/);
  const previewRequest = page.waitForRequest(
    (request) =>
      request.url().endsWith("/routing-preview") && request.method() === "POST",
  );
  await messageInput.press("Enter");
  const previewBody = (await previewRequest).postDataJSON();
  expect(previewBody.workflow).toBe("research");
  expect(previewBody.assignments).toEqual({});
  expect(previewBody.targets).toEqual([]);
  await expect(page.getByText("已完成", { exact: true }).first()).toBeVisible({
    timeout: 15000,
  });
  // The accepted routing carries the global defaults; no local editor exists.
  await expect(
    page.getByText("研究：缅因猫 · 质疑：布偶猫 · 汇总：橘猫"),
  ).toBeVisible();
  await expect(page.getByLabel(/^成员与分工/)).toHaveCount(0);
  const researchURL = page.url();
  await expect(
    page.getByRole("complementary", { name: "协作资料" }),
  ).toBeHidden();
  await expect(
    page
      .getByLabel("第 1 次协作产物")
      .getByRole("button", { name: /研究报告 · v1/ }),
  ).toBeVisible();
  expect((await messageInput.boundingBox())!.height).toBeLessThan(96);
  const researchTimeline = page.getByTestId("conversation-timeline");
  await researchTimeline.evaluate((element) => {
    element.scrollTop = 0;
  });
  for (const mode of ["light", "dark"] as const) {
    await setAppearance(mode);
    await page.screenshot({
      path: test.info().outputPath(`conversation-${mode}.png`),
    });
  }
  await setAppearance("light");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: test.info().outputPath("conversation-mobile.png"),
  });
  await page.setViewportSize({ width: 1280, height: 800 });
  await openPanel("来源");
  await expect(
    page.getByRole("button", { name: /网页 · 已读原文/ }).first(),
  ).toBeVisible();
  await page
    .getByRole("button", { name: /网页 · 已读原文/ })
    .first()
    .click();
  await expect(page.getByTestId("source-detail")).toContainText(
    "测试内容不能证明真实模型效果",
  );
  await openPanel("产物");
  await page
    .getByRole("complementary", { name: "协作资料" })
    .getByRole("button", { name: /研究报告 · v1/ })
    .last()
    .click();
  await expect(page.getByTestId("artifact-detail")).toContainText("分歧与限制");
  await page.getByRole("button", { name: "接受", exact: true }).click();
  await expect(
    page.getByRole("dialog").getByRole("button", { name: "已接受" }),
  ).toHaveAttribute("aria-pressed", "true");
  const download = page.waitForEvent("download");
  await page.getByRole("link", { name: "下载 Markdown" }).click();
  expect((await download).suggestedFilename()).toBe("cocogem-report.md");
  await page.getByRole("button", { name: "引用后追问" }).click();
  await page.getByLabel("输入问题").fill("等待取消：补充讨论这个方案的限制");
  await chooseMethod("discussion");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "停止协作", exact: false }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "停止协作", exact: true }),
  ).toHaveText("");
  await page.getByRole("button", { name: "停止协作", exact: false }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已停止");
  await page.reload();
  await expect(page.getByTestId("run-status")).toHaveText("已停止");
  // D08/A15: the panel keeps only 来源/产物; the process stays in the reply body.
  await expect(page.getByRole("tab", { name: "过程" })).toHaveCount(0);
  const process = page.getByText(/协作过程 · \d+ 条过程/).first();
  await expect(process).toBeVisible();
  // Run was cancelled before any tool produced a receipt: no fabricated result.
  await expect(page.getByTestId("tool-activity")).toHaveCount(0);

  await page
    .getByRole("button", { name: "新建会话", exact: false })
    .first()
    .click();
  await page.getByLabel("会话标题").fill("工程协作验收");
  await page.getByRole("button", { name: "保存会话" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await chooseMethod("engineering");
  await expect(page.getByLabel("工程动作")).toHaveCount(0);
  await expect(
    page.getByText(/按成员已获授权的能力执行/),
  ).toBeVisible();
  await page
    .getByLabel("输入问题")
    .fill("分析示例仓库如何区分身份和角色，不要声称执行测试。");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 20000,
  });
  await openPanel("产物");
  await page
    .getByRole("complementary", { name: "协作资料" })
    .getByRole("button", { name: /工程交付报告 · v1/ })
    .click();
  await page
    .getByRole("button", { name: /打开来源/ })
    .first()
    .click();
  await expect(page.getByTestId("source-detail")).toContainText(
    "Stable identity",
  );

  await page
    .getByRole("button", { name: "新建会话", exact: false })
    .first()
    .click();
  await page.getByLabel("会话标题").fill("并行讨论");
  await page.getByRole("button", { name: "保存会话" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await chooseMethod("discussion");
  await page
    .getByLabel("输入问题")
    .fill("分别讨论协作的收益和成本，保留不同观点。");
  const discussionPreview = page.waitForRequest(
    (request) =>
      request.url().endsWith("/routing-preview") && request.method() === "POST",
  );
  await page.getByRole("button", { name: "发送", exact: true }).click();
  const discussionBody = (await discussionPreview).postDataJSON();
  expect(discussionBody.workflow).toBe("discussion");
  expect(discussionBody.assignments).toEqual({});
  expect(discussionBody.targets).toEqual([]);
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 20000,
  });
  await openPanel("产物");
  await expect(
    page
      .getByRole("complementary", { name: "协作资料" })
      .getByRole("button", { name: /讨论纪要 · v1/ }),
  ).toBeVisible();

  // An unresolved name preserves the new-thread draft instead of silently dispatching.
  await page
    .getByRole("button", { name: "新建会话", exact: false })
    .first()
    .click();
  await page.getByLabel("会话标题").fill("路由澄清");
  await page.getByRole("button", { name: "保存会话" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await chooseMethod("free");
  await page.getByLabel("输入问题").fill("@missing 帮我调查");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByLabel("输入问题")).toHaveValue("@missing 帮我调查");
  await expect(
    page.getByRole("status").filter({ hasText: "不可用" }),
  ).toBeVisible();

  // Keep text typed while the accepted POST response is delayed.
  await page.route("**/runs", async (route) => {
    const response = await route.fetch();
    await new Promise((resolve) => setTimeout(resolve, 400));
    await route.fulfill({ response });
  });
  await page.getByLabel("输入问题").fill("布布帮我研究一下协作方法");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await page.getByLabel("输入问题").fill("下一条草稿，不能被清除");
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 20000,
  });
  await expect(page.getByLabel("输入问题")).toHaveValue(
    "下一条草稿，不能被清除",
  );
  await page.unroute("**/runs");

  const threadPath = new URL(page.url()).pathname.split("/t/")[1];
  const before = (
    await (await page.request.get(`/api/v1/threads/${threadPath}`)).json()
  ).runs.length;
  let postCount = 0;
  await page.route("**/runs", async (route) => {
    postCount++;
    if (postCount === 1) {
      await route.fetch();
      await route.abort("connectionfailed");
    } else await route.continue();
  });
  await page.getByLabel("输入问题").fill("布布帮我研究一下幂等重试");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "重试原请求", exact: true }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "重试原请求", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 20000,
  });
  const after = (
    await (await page.request.get(`/api/v1/threads/${threadPath}`)).json()
  ).runs.length;
  expect(after).toBe(before + 1);
  expect(postCount).toBe(2);
  await page.unroute("**/runs");
  await expect(
    page.getByRole("article", { name: "你的消息" }).last(),
  ).toContainText("布布帮我研究一下幂等重试");
  await expect(page.getByTestId("run-process")).toHaveCount(after);
  // Theme and panel changes keep the linked draft and workflow configuration in place.
  await chooseMethod("research");
  await expect(page.getByLabel(/^成员与分工/)).toHaveCount(0);
  await page
    .getByLabel("输入问题")
    .fill("主题切换和打开资料后仍在的草稿 https://example.org/draft");
  const timeline = page.getByTestId("conversation-timeline");
  await timeline.evaluate((element) => {
    element.scrollTop = 0;
    element.dispatchEvent(new Event("scroll"));
  });
  for (const mode of ["light", "dark"] as const) {
    const readingPosition = await timeline.evaluate(
      (element) => element.scrollTop,
    );
    await setAppearance(mode);
    expect(await timeline.evaluate((element) => element.scrollTop)).toBe(
      readingPosition,
    );
    await openPanel("来源");
    await expect(page.getByLabel("输入问题")).toHaveValue(
      "主题切换和打开资料后仍在的草稿 https://example.org/draft",
    );
    // The removed tool tab never comes back, and a stale selection falls back.
    await expect(page.getByRole("tab", { name: "过程" })).toHaveCount(0);
    await expect(page.getByLabel(/^成员与分工/)).toHaveCount(0);
    await page.getByRole("button", { name: "设置", exact: true }).click();
    const appearanceSettings = page.getByRole("dialog", { name: "设置" });
    await appearanceSettings.getByRole("tab", { name: "外观", exact: true }).click();
    const appearanceSelect = appearanceSettings.getByLabel("外观模式", {
      exact: true,
    });
    await appearanceSelect.click();
    await expect(page.getByRole("listbox")).toBeVisible();
    await page.screenshot({
      path: test.info().outputPath(`select-${mode}.png`),
    });
    const menuAccessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa"])
      .analyze();
    expect(menuAccessibility.violations).toEqual([]);
    await appearanceSelect.press("Escape");
    const accessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
      .analyze();
    expect(accessibility.violations).toEqual([]);
    await appearanceSettings.getByRole("button", { name: "关闭窗口" }).click();
    await page
      .getByRole("button", { name: "关闭协作面板", exact: true })
      .click();
    await expect(
      page.getByRole("complementary", { name: "协作资料" }),
    ).toBeHidden();
    await expect(page.locator("#context-toggle")).toBeFocused();
  }
  await page.reload();
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(11, 13, 16)",
  );
  await page.getByRole("button", { name: "设置", exact: true }).click();
  const persistedSettings = page.getByRole("dialog", { name: "设置" });
  await persistedSettings.getByRole("tab", { name: "外观", exact: true }).click();
  await expect(persistedSettings.getByLabel("外观模式")).toHaveText(/深色/);
  await persistedSettings.getByRole("button", { name: "关闭窗口" }).click();
  await setAppearance("system");
  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(248, 250, 252)",
  );
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(11, 13, 16)",
  );

  // A saved default applies after navigation and reload; ordinary discussion
  // uses this conversation's two members instead of inviting the third.
  await page
    .getByRole("button", { name: "新建会话", exact: false })
    .first()
    .click();
  await page.getByLabel("会话标题").fill("双成员讨论");
  await page
    .getByRole("checkbox", { name: "橘猫 (@orange)", exact: true })
    .uncheck();
  await expect(
    page.getByRole("combobox", { name: "协调者", exact: true }),
  ).toHaveText(/布偶猫/);
  await selectOption(page.getByLabel("默认工作方式"), "discussion");
  await page.getByRole("button", { name: "保存会话" }).click();
  await expect(page.getByTestId("composer-method")).toHaveText(/协作讨论/);
  const twoMemberURL = page.url();
  await page.reload();
  await expect(page.getByTestId("composer-method")).toHaveText(/协作讨论/);
  await page.getByLabel("输入问题").fill("独立讨论协作工作空间的优缺点。");
  const accepted = page.waitForResponse(
    (response) =>
      response.url().endsWith("/runs") &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "发送", exact: true }).click();
  const discussionRun = await (await accepted).json();
  expect(
    discussionRun.agents
      .map((agent: { handle: string }) => agent.handle)
      .sort(),
  ).toEqual(["maine", "ragdoll"]);
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 20000,
  });

  // Editing the current coordinator must keep its history and unsent state in place.
  const currentThreadID = new URL(page.url()).pathname.split("/t/")[1];
  const beforeSettings = await (
    await page.request.get(`/api/v1/threads/${currentThreadID}`)
  ).json();
  const linkedDraft = "切换协调者后仍保留的草稿 https://example.org/keep-draft";
  await page.getByLabel("输入问题").fill(linkedDraft);
  await timeline.evaluate((element) => {
    element.scrollTop = 0;
  });
  const settingsPosition = await timeline.evaluate(
    (element) => element.scrollTop,
  );
  const settingsWrites: string[] = [];
  const recordWrites = (request: import("@playwright/test").Request) => {
    if (["POST", "PATCH", "DELETE"].includes(request.method()))
      settingsWrites.push(
        `${request.method()} ${new URL(request.url()).pathname}`,
      );
  };
  page.on("request", recordWrites);
  await page.getByRole("button", { name: "会话设置", exact: true }).click();
  const nextCoordinator = discussionRun.agents.find(
    (agent: { handle: string }) => agent.handle === "maine",
  ).id;
  await selectOption(
    page.getByRole("combobox", { name: "协调者", exact: true }),
    nextCoordinator,
  );
  await expect(page.getByRole("dialog", { name: "会话设置" })).toBeVisible();
  expect(settingsWrites).toEqual([]);
  const settingsSaved = page.waitForResponse(
    (response) =>
      response.request().method() === "PATCH" &&
      response.url().endsWith(`/threads/${currentThreadID}`),
  );
  await page.getByRole("button", { name: "保存会话", exact: true }).click();
  await settingsSaved;
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page).toHaveURL(twoMemberURL);
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 20000,
  });
  await expect(page.getByLabel("输入问题")).toHaveValue(linkedDraft);
  await expect(page.getByLabel("资料链接")).toHaveCount(0);
  expect(await timeline.evaluate((element) => element.scrollTop)).toBe(
    settingsPosition,
  );
  page.off("request", recordWrites);
  expect(settingsWrites).toEqual([`PATCH /api/v1/threads/${currentThreadID}`]);
  const afterSettings = await (
    await page.request.get(`/api/v1/threads/${currentThreadID}`)
  ).json();
  expect(afterSettings.thread.coordinator_id).toBe(nextCoordinator);
  expect(afterSettings.messages.items).toEqual(beforeSettings.messages.items);
  expect(afterSettings.runs).toEqual(beforeSettings.runs);
  await page.getByRole("button", { name: "会话设置", exact: true }).click();
  await expect(
    page.getByRole("combobox", { name: "协调者", exact: true }),
  ).toHaveText(/缅因猫/);
  await page.getByRole("button", { name: "关闭窗口", exact: true }).click();

  // Renaming a global profile does not relabel an already captured tool execution.
  await page.getByRole("button", { name: "设置", exact: true }).click();
  await settingsDialog.getByRole("tab", { name: "成员" }).click();
  await settingsDialog
    .getByRole("button", { name: /布偶猫.*@ragdoll/ })
    .click();
  await settingsDialog.getByLabel("显示名").fill("新布偶猫");
  await settingsDialog.getByRole("button", { name: "保存成员" }).click();
  await expect(settingsDialog.getByRole("status")).toContainText("已保存");
  await page.getByRole("button", { name: "关闭窗口" }).click();
  await page.goto(researchURL);
  // Expand the collapsed process log: the historical record keeps the captured
  // member name, not the renamed one.
  const runProcess = page.getByTestId("run-process").first();
  await expect(runProcess).toBeVisible();
  await runProcess.evaluate((node) => {
    if (!node.hasAttribute("open")) (node as HTMLDetailsElement).open = true;
  });
  await expect(runProcess).toHaveAttribute("open", "");
  await expect(page.getByText("布偶猫", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("新布偶猫", { exact: true })).toHaveCount(0);
  await page.goto(twoMemberURL);
  await expect(page.getByTestId("composer-method")).toHaveText(/协作讨论/);

  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await expect(page.getByLabel("切换工作空间")).toBeVisible();
  // Touch and narrow layouts keep the row action reachable without hover.
  await expect(
    page.getByRole("button", { name: /更多会话操作：/ }).first(),
  ).toHaveCSS("opacity", "1");
  await page.getByRole("button", { name: "工程协作验收", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "工程协作验收", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  // The context panel is a drawer on phones and Escape closes it.
  await page.getByRole("button", { name: "打开协作面板" }).click();
  await expect(page.getByRole("tab", { name: /产物/ })).toBeVisible();
  await expect(page.getByRole("tab", { name: "成员" })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tab", { name: /产物/ })).toBeHidden();
  await expect(page.locator("#context-toggle")).toBeFocused();
  await page.getByLabel("输入问题").fill("手机上的草稿");
  for (const mode of ["light", "dark"] as const) {
    await setAppearance(mode);
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await expect(
      page.getByRole("complementary", { name: "导航" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "关闭导航", exact: true }).click();
    await expect(
      page.getByRole("complementary", { name: "导航" }),
    ).toBeHidden();
    await expect(page.getByLabel("输入问题")).toHaveValue("手机上的草稿");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    expect(
      (
        await new AxeBuilder({ page })
          .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
          .analyze()
      ).violations,
    ).toEqual([]);
  }
  await page.setViewportSize({ width: 1280, height: 800 });

  // Links live in the message; role defaults are global, not a composer panel.
  await chooseMethod("free");
  await expect(page.getByLabel(/^成员与分工/)).toHaveCount(0);
  await expect(page.getByText("自动指派", { exact: true })).toHaveCount(0);
  for (const width of [1125, 605, 390]) {
    await page.setViewportSize({ width, height: 732 });
    const input = page.getByLabel("输入问题");
    await input.fill("研究 https://example.org/public");
    await input.press("Shift+Enter");
    await expect(input).toHaveValue("研究 https://example.org/public\n");
    await expect(page.getByLabel("资料链接")).toHaveCount(0);
    await expect(page.getByLabel("研究范围")).toHaveCount(0);
    await page.screenshot({
      path: test.info().outputPath(`composer-links-${width}.png`),
    });
  }
  await page.setViewportSize({ width: 1280, height: 800 });
  await chooseMethod("research");
  await expect(page.getByLabel(/^成员与分工/)).toHaveCount(0);
  await expect(page.getByLabel("研究", { exact: true })).toHaveCount(0);
  await chooseMethod("free");

  // @ mentions filter by what is typed and are chosen with the keyboard.
  const input = page.getByLabel("输入问题");
  await input.fill("请 @ma");
  await expect(
    page.getByRole("listbox", { name: "选择成员" }).getByRole("option"),
  ).toHaveCount(1);
  await page.keyboard.press("Enter");
  await expect(input).toHaveValue("请 @maine ");
  await input.fill("");

  // Row actions hide until hover or focus; selection alone does not pin them.
  const threadList = page.getByRole("navigation", { name: "会话列表" });
  const currentRow = threadList
    .locator("div")
    .filter({ hasText: "工程协作验收" });
  const currentActions = threadList.getByRole("button", {
    name: "更多会话操作：工程协作验收",
  });
  await page.mouse.move(0, 0);
  await expect(currentActions).toHaveCSS("opacity", "0");
  await currentRow.hover();
  await expect(currentActions).toHaveCSS("opacity", "1");
  await currentActions.click();
  const currentMenu = page.getByRole("menu", {
    name: "工程协作验收 的会话操作",
  });
  await expect(currentMenu).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(currentMenu).toHaveCount(0);
  await expect(currentActions).toBeFocused();

  // Renaming edits only the title and keeps the conversation's members.
  const renamedThreadID = new URL(page.url()).pathname.split("/t/")[1];
  const beforeRename = await (
    await page.request.get(`/api/v1/threads/${renamedThreadID}`)
  ).json();
  await currentActions.click();
  await currentMenu.getByRole("menuitem", { name: "重命名" }).click();
  const renameDialog = page.getByRole("dialog", { name: "重命名会话" });
  await expect(renameDialog.getByLabel("会话标题")).toHaveValue("工程协作验收");
  await renameDialog.getByLabel("会话标题").fill("工程协作验收改名");
  await renameDialog.getByRole("button", { name: "保存名称" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "工程协作验收改名", exact: true }),
  ).toBeVisible();
  await expect(
    threadList.getByRole("button", {
      name: "更多会话操作：工程协作验收改名",
    }),
  ).toBeVisible();
  expect(page.url()).toContain(`/t/${renamedThreadID}`);
  const afterRename = await (
    await page.request.get(`/api/v1/threads/${renamedThreadID}`)
  ).json();
  expect(afterRename.thread.title).toBe("工程协作验收改名");
  expect(afterRename.thread.agent_ids).toEqual(beforeRename.thread.agent_ids);
  expect(afterRename.thread.coordinator_id).toBe(
    beforeRename.thread.coordinator_id,
  );
  expect(afterRename.thread.workflow).toBe(beforeRename.thread.workflow);

  // Deleting a conversation moves it to the trash immediately.
  const parallelRow = threadList.locator("div").filter({ hasText: "并行讨论" });
  await expect(
    threadList.getByRole("button", { name: "并行讨论", exact: true }),
  ).toBeVisible();
  await parallelRow.hover();
  await threadList
    .getByRole("button", { name: "更多会话操作：并行讨论" })
    .click();
  await page
    .getByRole("menu", { name: "并行讨论 的会话操作" })
    .getByRole("menuitem", { name: "删除" })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    threadList.getByRole("button", { name: "并行讨论", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "设置", exact: true }).click();
  await page.getByRole("tab", { name: "服务" }).click();
  await page.getByRole("button", { name: "锁定工作空间" }).click();
  await expect(page.getByLabel("密码", { exact: true })).toBeVisible();
  const unauthorized = await page.request.get("/api/v1/workspaces");
  expect(unauthorized.status()).toBe(401);
  await page.getByLabel("密码", { exact: true }).fill("wrong-test-password");
  await page.getByRole("button", { name: "解锁工作空间" }).click();
  await expect(page.getByRole("alert")).toContainText("密码不正确");
  await page.getByLabel("密码", { exact: true }).fill("browser-test-password");
  await page.getByRole("button", { name: "解锁工作空间" }).click();
  await expect(
    page.getByRole("button", { name: "设置", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("密码", { exact: true })).toHaveCount(0);
});
