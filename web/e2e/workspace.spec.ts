import { expect, test } from "@playwright/test";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

test("create a team, run research, inspect evidence, export, follow up and cancel", async ({
  page,
}) => {
  await page.goto("/#bootstrap=" + process.env.COCOGEM_TEST_TOKEN);
  await expect(
    page.getByRole("heading", { name: "把问题交给一个团队。" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "创建第一个工作空间" }).click();
  await page.getByLabel("名称", { exact: true }).fill("猫猫研究室");
  await page.getByLabel("本地资料目录").selectOption({ index: 1 });
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(
    page.getByRole("button", { name: "管理成员与称呼" }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "管理成员与称呼" }).click();
  await page.getByLabel("别名（用逗号分隔）").fill("布布");
  await page.getByRole("button", { name: "保存成员" }).click();
  await expect(page.getByRole("status")).toContainText("已保存");
  await page.getByRole("button", { name: "关闭窗口" }).click();
  await page
    .getByRole("button", { name: "做一次 Research", exact: false })
    .click();
  await page.getByRole("button", { name: "发送 ↑", exact: true }).click();
  await expect(page.getByText("已完成", { exact: true }).first()).toBeVisible({
    timeout: 15000,
  });
  await page.getByRole("tab", { name: "来源" }).click();
  await expect(
    page.getByRole("button", { name: /WEB · 已捕获原文/ }).first(),
  ).toBeVisible();
  await page
    .getByRole("button", { name: /WEB · 已捕获原文/ })
    .first()
    .click();
  await expect(page.getByTestId("source-detail")).toContainText("仅为离线演示");
  await page.getByRole("tab", { name: "产物" }).click();
  await page
    .getByRole("button", { name: /research_report · v1/ })
    .last()
    .click();
  await expect(page.getByTestId("artifact-detail")).toContainText("分歧与限制");
  await page.getByRole("button", { name: "接受", exact: true }).click();
  await expect(page.getByRole("button", { name: /已接受/ })).toBeVisible();
  const download = page.waitForEvent("download");
  await page.getByRole("link", { name: "下载 Markdown" }).click();
  expect((await download).suggestedFilename()).toBe("cocogem-report.md");
  await page.getByRole("button", { name: "引用后追问" }).click();
  await page.getByLabel("输入问题").fill("补充讨论这个方案的限制");
  await page.getByLabel("工作方式", { exact: true }).selectOption("discussion");
  await page.getByRole("button", { name: "发送 ↑", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "停止协作", exact: false }),
  ).toBeVisible();
  await page.getByRole("button", { name: "停止协作", exact: false }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已停止");
  await page.reload();
  await expect(page.getByTestId("run-status")).toHaveText("已停止");
  await page.getByRole("tab", { name: "工具" }).click();
  await page.getByLabel("历史协作").selectOption({ index: 0 });
  await expect(page.getByText(/fetch_url · 完成/).first()).toBeVisible();

  await page
    .getByRole("button", { name: "新建会话", exact: false })
    .first()
    .click();
  await page.getByLabel("会话标题").fill("只读代码分析");
  await page.getByRole("button", { name: "保存会话" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByLabel("工作方式", { exact: true })
    .selectOption("code_analysis");
  await page
    .getByLabel("输入问题")
    .fill("分析示例仓库如何区分身份和角色，不要声称执行测试。");
  await page.getByRole("button", { name: "发送 ↑", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
  await page.getByRole("tab", { name: "产物" }).click();
  await page.getByRole("button", { name: /code_analysis · v1/ }).click();
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
  await page.getByLabel("工作方式", { exact: true }).selectOption("discussion");
  await page
    .getByLabel("输入问题")
    .fill("分别讨论协作的收益和成本，保留不同观点。");
  await page.getByRole("button", { name: "发送 ↑", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
  await page.getByRole("tab", { name: "产物" }).click();
  await expect(page.getByRole("button", { name: /note · v1/ })).toBeVisible();

  // An unresolved name preserves the new-thread draft instead of silently dispatching.
  await page
    .getByRole("button", { name: "新建会话", exact: false })
    .first()
    .click();
  await page.getByLabel("会话标题").fill("路由澄清");
  await page.getByRole("button", { name: "保存会话" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByLabel("工作方式", { exact: true }).selectOption("free");
  await page.getByLabel("输入问题").fill("@missing 帮我调查");
  await page.getByRole("button", { name: "发送 ↑", exact: true }).click();
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
  await page.getByRole("button", { name: "发送 ↑", exact: true }).click();
  await page.getByLabel("输入问题").fill("下一条草稿，不能被清除");
  await expect(page.getByTestId("run-status")).toHaveText("已完成");
  await expect(page.getByLabel("输入问题")).toHaveValue(
    "下一条草稿，不能被清除",
  );
  await page.unroute("**/runs");

  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "导航", exact: true }).click();
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
});
