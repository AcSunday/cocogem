import { expect, test } from "@playwright/test";
import type { Workspace } from "../src/api/client";
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

test("sets and edits a local directory path with server validation and persistence", async ({
  page,
}, testInfo) => {
  await page.goto(baseURL);
  await page
    .getByLabel("设置密码", { exact: true })
    .fill("directory-browser-password");
  await page
    .getByLabel("确认密码", { exact: true })
    .fill("directory-browser-password");
  await page.getByRole("button", { name: "设置密码并进入" }).click();
  await expect(
    page.getByRole("button", { name: "创建第一个工作空间" }),
  ).toBeVisible();
  const system = await (
    await page.request.get(`${baseURL}/api/v1/system`)
  ).json();
  const root: string = system.roots[0];
  expect(root).toBeTruthy();
  await page.getByRole("button", { name: "创建第一个工作空间" }).click();
  await page.getByLabel("名称", { exact: true }).fill("目录设置验收");
  await page
    .getByRole("combobox", { name: "本地资料目录", exact: true })
    .click();
  await page
    .getByRole("option", { name: "输入目录路径…", exact: true })
    .click();
  await page
    .getByLabel("目录路径", { exact: true })
    .fill(`${root}/not-existing`);
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(page.getByRole("alert")).toContainText("目录不存在或无法访问");
  await expect(page.getByLabel("名称", { exact: true })).toHaveValue(
    "目录设置验收",
  );
  await expect(page.getByLabel("目录路径", { exact: true })).toHaveValue(
    `${root}/not-existing`,
  );
  await page.getByLabel("目录路径", { exact: true }).fill(`${root}/README.md`);
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(page.getByRole("alert")).toContainText("请选择文件夹");
  await page.getByLabel("目录路径", { exact: true }).fill(`${root}/.`);
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const workspaceURL = page.url();
  const workspaceID = new URL(workspaceURL).pathname.split("/w/")[1];
  const getWorkspace = async () => {
    const data: { items: Workspace[] } = await (
      await page.request.get(`${baseURL}/api/v1/workspaces`)
    ).json();
    return data.items.find((workspace) => workspace.id === workspaceID)!;
  };
  expect((await getWorkspace()).root).toBe(root);
  await page.reload();
  await page.getByRole("button", { name: "编辑工作空间", exact: true }).click();
  await expect(
    page.getByRole("combobox", { name: "本地资料目录", exact: true }),
  ).toHaveText(root);
  await page
    .getByRole("combobox", { name: "本地资料目录", exact: true })
    .click();
  await page
    .getByRole("option", { name: "输入目录路径…", exact: true })
    .click();
  await page
    .getByLabel("目录路径", { exact: true })
    .fill(root.replace(/\/fixtures$/, ""));
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "目录不在服务端允许范围内",
  );
  expect((await getWorkspace()).root).toBe(root);
  await page.getByRole("button", { name: "浏览目录", exact: true }).click();
  const picker = page.getByRole("region", { name: "选择本机目录" });
  await picker.getByRole("button", { name: "允许的目录", exact: true }).click();
  await picker.getByRole("button", { name: root, exact: true }).click();
  await expect(picker.getByRole("button", { name: "返回上级" })).toBeDisabled();
  await expect(picker.getByRole("button", { name: "README.md" })).toHaveCount(
    0,
  );
  await picker.getByRole("button", { name: "项目 空间", exact: true }).click();
  await picker.getByRole("button", { name: "空目录", exact: true }).click();
  await expect(picker.getByText("没有可选的子目录")).toBeVisible();
  await picker.getByRole("button", { name: "返回上级" }).click();
  await expect(
    picker.getByRole("button", { name: "空目录", exact: true }),
  ).toBeVisible();
  await picker.getByRole("button", { name: "取消选择" }).click();
  expect((await getWorkspace()).root).toBe(root);
  await expect(page.getByLabel("目录路径", { exact: true })).toHaveValue(
    root.replace(/\/fixtures$/, ""),
  );
  await page.getByRole("button", { name: "浏览目录", exact: true }).click();
  await picker.getByRole("button", { name: "允许的目录", exact: true }).click();
  await picker.getByRole("button", { name: root, exact: true }).click();
  await picker.getByRole("button", { name: "项目 空间", exact: true }).click();
  await expect(
    picker.getByRole("button", { name: "选择此目录" }),
  ).toBeEnabled();
  await page.emulateMedia({ colorScheme: "dark" });
  await picker.screenshot({
    path: testInfo.outputPath("directory-picker-dark.png"),
  });
  await page.emulateMedia({ colorScheme: "light" });
  await picker.screenshot({
    path: testInfo.outputPath("directory-picker-light.png"),
  });
  await page.setViewportSize({ width: 390, height: 732 });
  await expect(
    picker.getByRole("button", { name: "选择此目录" }),
  ).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  const pickerWidth = await picker.boundingBox();
  expect(pickerWidth!.width).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1280, height: 720 });
  await picker.getByRole("button", { name: "选择此目录" }).click();
  await expect(picker).toHaveCount(0);
  await expect(page.getByLabel("目录路径", { exact: true })).toHaveValue(
    `${root}/项目 空间`,
  );
  expect((await getWorkspace()).root).toBe(root);
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect((await getWorkspace()).root).toBe(`${root}/项目 空间`);
  await page.reload();
  await page.getByRole("button", { name: "编辑工作空间", exact: true }).click();
  await expect(page.getByLabel("目录路径", { exact: true })).toHaveValue(
    `${root}/项目 空间`,
  );
  await page
    .getByRole("combobox", { name: "本地资料目录", exact: true })
    .click();
  await page.getByRole("option", { name: "暂不绑定", exact: true }).click();
  await page.getByRole("button", { name: "保存工作空间" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page).toHaveURL(workspaceURL);
  expect((await getWorkspace()).root).toBe("");
});
