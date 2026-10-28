import { expect, test, type Locator } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

test("create a team, run research, inspect evidence, export, follow up and cancel", async ({
  page,
}) => {
  const selectOption = async (
    control: Locator,
    value: string | { index: number },
  ) => {
    await control.click();
    const menu = page.getByRole("listbox");
    if (typeof value === "string")
      await menu.locator(`[role="option"][data-value="${value}"]`).click();
    else await menu.getByRole("option").nth(value.index).click();
  };
  const openPanel = async (name: string) => {
    const toggle = page.getByRole("button", {
      name: "打开协作面板",
      exact: true,
    });
    if (await toggle.isVisible()) await toggle.click();
    await page.getByRole("tab", { name }).click();
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
  await page.getByRole("button", { name: "创建第一个工作空间" }).click();
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
  await page.getByRole("button", { name: "成员 3", exact: true }).click();
  await page.getByLabel("别名（用逗号分隔）").fill("布布");
  await page.getByRole("button", { name: "保存成员" }).click();
  await expect(page.getByRole("status")).toContainText("已保存");
  await page.getByLabel("职责").fill("研究资料并核对证据。");
  await page.getByRole("button", { name: "保存成员" }).click();
  await expect(page.getByRole("status")).toContainText("已保存");
  await page.getByRole("button", { name: "关闭窗口" }).click();
  await page
    .getByRole("button", { name: "做一次 Research", exact: false })
    .click();
  const messageInput = page.getByLabel("输入问题");
  await messageInput.fill(
    `${await messageInput.inputValue()}\n${[
      "https://example.org/cocogem/research",
      "https://example.org/cocogem/limits",
      "https://example.org/cocogem/cases",
    ].join("\n")}`,
  );
  await expect(page.getByLabel("资料链接")).toHaveCount(0);
  await expect(page.getByLabel("研究范围")).toHaveCount(0);
  await messageInput.press("Shift+Enter");
  await expect(messageInput).toHaveValue(/\n$/);
  await messageInput.press("Enter");
  await expect(page.getByText("已完成", { exact: true }).first()).toBeVisible({
    timeout: 15000,
  });
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
  const researchAppearance = page.getByLabel("外观模式", { exact: true });
  for (const mode of ["light", "dark"] as const) {
    await selectOption(researchAppearance, mode);
    await page.screenshot({
      path: test.info().outputPath(`conversation-${mode}.png`),
    });
  }
  await selectOption(researchAppearance, "light");
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
  await selectOption(
    page.getByLabel("工作方式", { exact: true }),
    "discussion",
  );
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
  await openPanel("过程");
  await selectOption(page.getByLabel("历史协作"), { index: 0 });
  await expect(page.getByText("读取网页").first()).toBeVisible();

  await page
    .getByRole("button", { name: "新建会话", exact: false })
    .first()
    .click();
  await page.getByLabel("会话标题").fill("只读代码分析");
  await page.getByRole("button", { name: "保存会话" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await selectOption(
    page.getByLabel("工作方式", { exact: true }),
    "code_analysis",
  );
  await page
    .getByLabel("输入问题")
    .fill("分析示例仓库如何区分身份和角色，不要声称执行测试。");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
  await openPanel("产物");
  await page
    .getByRole("complementary", { name: "协作资料" })
    .getByRole("button", { name: /代码分析报告 · v1/ })
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
  await selectOption(
    page.getByLabel("工作方式", { exact: true }),
    "discussion",
  );
  await page
    .getByLabel("输入问题")
    .fill("分别讨论协作的收益和成本，保留不同观点。");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
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
  await selectOption(page.getByLabel("工作方式", { exact: true }), "free");
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
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
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
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
  const after = (
    await (await page.request.get(`/api/v1/threads/${threadPath}`)).json()
  ).runs.length;
  expect(after).toBe(before + 1);
  expect(postCount).toBe(2);
  await page.unroute("**/runs");
  await expect(
    page.getByRole("article", { name: "你的消息" }).last(),
  ).toContainText("布布帮我研究一下幂等重试");
  await expect(
    page.locator("summary").filter({ hasText: /^协作过程/ }),
  ).toHaveCount(after);
  // Theme and panel changes keep the linked draft and workflow configuration in place.
  await selectOption(page.getByLabel("工作方式", { exact: true }), "research");
  await page.getByLabel(/^成员与分工/).click();
  await page
    .getByLabel("输入问题")
    .fill("主题切换和打开资料后仍在的草稿 https://example.org/draft");
  const appearance = page.getByLabel("外观模式", { exact: true });
  const timeline = page.getByTestId("conversation-timeline");
  await timeline.evaluate((element) => {
    element.scrollTop = 0;
    element.dispatchEvent(new Event("scroll"));
  });
  for (const mode of ["light", "dark"] as const) {
    const readingPosition = await timeline.evaluate(
      (element) => element.scrollTop,
    );
    await selectOption(appearance, mode);
    expect(await timeline.evaluate((element) => element.scrollTop)).toBe(
      readingPosition,
    );
    await openPanel("过程");
    await expect(page.getByLabel("输入问题")).toHaveValue(
      "主题切换和打开资料后仍在的草稿 https://example.org/draft",
    );
    await expect(page.getByLabel(/^成员与分工/).locator("..")).toHaveAttribute(
      "open",
      "",
    );
    await appearance.click();
    await expect(page.getByRole("listbox")).toBeVisible();
    await page.screenshot({
      path: test.info().outputPath(`select-${mode}.png`),
    });
    const menuAccessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa"])
      .analyze();
    expect(menuAccessibility.violations).toEqual([]);
    await appearance.press("Escape");
    const accessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
      .analyze();
    expect(accessibility.violations).toEqual([]);
    await page
      .getByRole("button", { name: "关闭协作面板", exact: true })
      .click();
    await expect(
      page.getByRole("complementary", { name: "协作资料" }),
    ).toBeHidden();
    await expect(page.locator("#context-toggle")).toBeFocused();
  }
  await page.reload();
  await expect(appearance).toHaveText(/深色/);
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(30, 30, 30)",
  );
  await selectOption(appearance, "system");
  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(250, 247, 242)",
  );
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(30, 30, 30)",
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
  await expect(page.getByLabel("工作方式", { exact: true })).toHaveText(
    /协作讨论/,
  );
  const twoMemberURL = page.url();
  await page.reload();
  await expect(page.getByLabel("工作方式", { exact: true })).toHaveText(
    /协作讨论/,
  );
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
  await expect(page.getByTestId("run-status")).toHaveText("已完成");

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
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
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

  // Renaming does not relabel an already captured tool execution.
  await openPanel("成员");
  await page.getByRole("button", { name: "管理成员与称呼" }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: /^布偶猫 \(@ragdoll\)/ })
    .click();
  await page.getByLabel("显示名").fill("新布偶猫");
  await page.getByRole("button", { name: "保存成员" }).click();
  await expect(page.getByRole("dialog").getByRole("status")).toContainText(
    "已保存",
  );
  await page.getByRole("button", { name: "关闭窗口" }).click();
  await page.goto(researchURL);
  await openPanel("过程");
  await selectOption(page.getByLabel("历史协作"), { index: 0 });
  await expect(
    page.getByText("布偶猫 (@ragdoll)", { exact: true }).first(),
  ).toBeVisible();
  await page.goto(twoMemberURL);
  await expect(page.getByLabel("工作方式", { exact: true })).toHaveText(
    /协作讨论/,
  );

  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await expect(page.getByLabel("切换工作空间")).toBeVisible();
  await page
    .getByRole("button", { name: "只读代码分析", exact: false })
    .click();
  await expect(
    page.getByRole("heading", { name: "只读代码分析", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  // The context panel is a drawer on phones and Escape closes it.
  await page.getByRole("button", { name: "打开协作面板" }).click();
  await expect(page.getByRole("tab", { name: /产物/ })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tab", { name: /产物/ })).toBeHidden();
  await expect(page.locator("#context-toggle")).toBeFocused();
  await page.getByLabel("输入问题").fill("手机上的草稿");
  for (const mode of ["light", "dark"] as const) {
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await selectOption(appearance, mode);
    await page.getByRole("button", { name: "关闭导航", exact: true }).click();
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

  // Links live in the message; workflow roles stay in a floating panel.
  await selectOption(page.getByLabel("工作方式", { exact: true }), "free");
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
  await selectOption(page.getByLabel("工作方式", { exact: true }), "research");
  const assignmentToggle = page.getByLabel(/^成员与分工/);
  const workflowControl = page.getByLabel("工作方式", { exact: true });
  const beforeAssignment = await workflowControl.boundingBox();
  await assignmentToggle.click();
  await expect(page.getByLabel("研究", { exact: true })).toBeVisible();
  expect((await workflowControl.boundingBox())?.y).toBe(beforeAssignment?.y);
  await assignmentToggle.press("Escape");
  await selectOption(page.getByLabel("工作方式", { exact: true }), "free");

  // @ mentions filter by what is typed and are chosen with the keyboard.
  const input = page.getByLabel("输入问题");
  await input.fill("请 @ma");
  await expect(
    page.getByRole("listbox", { name: "选择成员" }).getByRole("option"),
  ).toHaveCount(1);
  await page.keyboard.press("Enter");
  await expect(input).toHaveValue("请 @maine ");
  await input.fill("");

  // Deleting a conversation asks first and then removes it from the list.
  const threadList = page.getByRole("navigation", { name: "会话列表" });
  await expect(
    threadList.getByRole("button", { name: "并行讨论" }),
  ).toBeVisible();
  await threadList
    .locator("div")
    .filter({ hasText: "并行讨论" })
    .getByRole("button", { name: "删除会话" })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "删除会话" })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    threadList.getByRole("button", { name: "并行讨论" }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "服务与设置" }).click();
  await page.getByRole("button", { name: "锁定工作空间" }).click();
  await expect(page.getByLabel("密码", { exact: true })).toBeVisible();
  const unauthorized = await page.request.get("/api/v1/workspaces");
  expect(unauthorized.status()).toBe(401);
  await page.getByLabel("密码", { exact: true }).fill("wrong-test-password");
  await page.getByRole("button", { name: "解锁工作空间" }).click();
  await expect(page.getByRole("alert")).toContainText("密码不正确");
  await page.getByLabel("密码", { exact: true }).fill("browser-test-password");
  await page.getByRole("button", { name: "解锁工作空间" }).click();
  await expect(page.getByRole("button", { name: "服务与设置" })).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("密码", { exact: true })).toHaveCount(0);
});
