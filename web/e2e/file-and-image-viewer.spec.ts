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

test("opens files at exact lines and diffs, renders visual evidence thumbnails & lightbox, and admits full_access ordinary chat commands", async ({
  page,
}) => {
  test.setTimeout(120000);
  const status = await (await page.request.get(`${baseURL}/auth/status`)).json();
  const auth = await (
    await page.request.post(
      `${baseURL}${status.initialized ? "/auth/login" : "/auth/setup"}`,
      { data: { password: "p0-file-image-password" } },
    )
  ).json();
  const headers = { "X-CSRF-Token": auth.csrf_token };

  // Find seeded fixtures root and point workspace at fixtures/git-demo so real
  // source files, PNG evidence, and live git diffs all work end-to-end.
  const dirs = await (
    await page.request.get(`${baseURL}/api/v1/directories`)
  ).json();
  const fixturesRoot: string =
    dirs.path || dirs.items?.[0]?.path || "";
  expect(fixturesRoot).toBeTruthy();

  const ws = await (
    await page.request.post(`${baseURL}/api/v1/workspaces`, {
      headers,
      data: { name: "P0 验收空间", root: `${fixturesRoot}/git-demo` },
    })
  ).json();
  const th = await (
    await page.request.post(`${baseURL}/api/v1/workspaces/${ws.id}/threads`, {
      headers,
      data: { title: "P0 文件与视觉验收" },
    })
  ).json();

  await page.goto(`${baseURL}/w/${ws.id}/t/${th.id}`);

  // 1. Switch permission mode to Full access (完全访问) in Composer.
  const modeSelect = page.getByRole("combobox", { name: "权限模式" });
  await modeSelect.click();
  await page.getByRole("option", { name: "完全访问" }).click();

  const composer = page.locator("#composer-input");

  // 2. Member reads a file, captures PNG visual evidence via view_image, and cites line references.
  await composer.fill("COCOGEM_P0_FILE_IMAGE_E2E 请检查 src/demo.go 并截图验收");
  await page.getByRole("button", { name: "发送" }).click();

  await expect(page.getByText("已核查").last()).toBeVisible({ timeout: 30000 });

  // Visual evidence thumbnail strip & markdown image both load real PNG bytes cleanly.
  const evidenceStrip = page.getByTestId("visual-evidence-strip").last();
  await expect(evidenceStrip).toBeVisible();
  const evidenceThumbImg = evidenceStrip.locator("img").first();
  await expect
    .poll(async () =>
      evidenceThumbImg.evaluate(
        (img: HTMLImageElement) => img.complete && img.naturalWidth > 0,
      ),
    )
    .toBe(true);

  // Clicking the thumbnail opens the full-size ImageLightboxModal.
  await evidenceThumbImg.locator("..").click();
  const lightbox = page.getByTestId("image-lightbox-modal");
  await expect(lightbox).toBeVisible();
  const zoomBtn = lightbox.locator("button[aria-pressed]");
  await expect(zoomBtn).toHaveAttribute("aria-pressed", "false");
  await zoomBtn.click();
  await expect(zoomBtn).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("Escape");
  await expect(lightbox).toHaveCount(0);

  // Clicking `src/demo.go:3-5` opens FileViewerModal with lines 3-5 highlighted & Diff tab populated.
  await page.getByRole("button", { name: "src/demo.go:3-5" }).last().click();
  const fileModal = page.getByTestId("file-viewer-modal");
  await expect(fileModal).toBeVisible();
  const codeView = page.getByTestId("file-code-viewport");
  await expect(codeView.locator('[data-line="3"]')).toHaveAttribute(
    "data-highlighted",
    "true",
  );
  await expect(codeView.locator('[data-line="4"]')).toHaveAttribute(
    "data-highlighted",
    "true",
  );
  await expect(codeView.locator('[data-line="5"]')).toHaveAttribute(
    "data-highlighted",
    "true",
  );

  // Switch to Diff tab and verify git diff hunks are shown.
  await fileModal.getByRole("tab", { name: "变更 Diff" }).click();
  const diffView = page.getByTestId("file-diff-viewport");
  await expect(diffView).toContainText("after-dirty");
  await page.keyboard.press("Escape");
  await expect(fileModal).toHaveCount(0);

  // 3. Ordinary chat in full_access executes run_command directly with no blocks.
  await composer.fill("COCOGEM_P0_FULL_ACCESS_CMD 请在普通对话直接运行命令");
  await page.getByRole("button", { name: "发送" }).click();
  await expect(
    page.getByText("普通对话命令执行成功：FULL_ACCESS_COMMAND_OK").last(),
  ).toBeVisible({ timeout: 30000 });

  // 4. PlanOnly still locks write & command execution even when global mode is full_access.
  const planChip = page.getByRole("button", { name: "仅规划" });
  await planChip.click();
  await expect(planChip).toHaveAttribute("aria-pressed", "true");
  await composer.fill("COCOGEM_P0_PLAN_ONLY_CMD 仅规划模式验证只读约束");
  await page.getByRole("button", { name: "发送" }).click();
  await expect(
    page.getByText("PLAN_ONLY_VERIFIED: 只读模式已限制命令与写入").last(),
  ).toBeVisible({ timeout: 30000 });
});
