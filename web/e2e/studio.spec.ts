import { expect, test } from "@playwright/test";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

test("shows workspace cats in Cat Studio and enforces unique built-in cat selection", async ({
  page,
}, testInfo) => {
  await page.goto("/");
  const status = await (await page.request.get("/auth/status")).json();
  const auth = await page.request.post(
    status.initialized ? "/auth/login" : "/auth/setup",
    { data: { password: "studio-e2e-password" } },
  );
  const { csrf_token: csrf } = await auth.json();
  await page.context().addCookies((await page.request.storageState()).cookies);

  const wsRes = await page.request.post("/api/v1/workspaces", {
    headers: { "X-CSRF-Token": csrf },
    data: { name: "猫猫研发室验收" },
  });
  const ws = await wsRes.json();

  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await page.goto(`/w/${ws.id}?view=studio`);
  await expect(page.getByTestId("studio-page")).toBeVisible();
  await expect(page.getByText("研发室成员")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "布偶猫", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "缅因猫", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "橘猫", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: /布偶猫.*待机/ }),
  ).toBeVisible();
  const ragdoll = page
    .getByRole("region", { name: "研发室场景" })
    .getByRole("button", { name: "布偶猫", exact: true });
  await expect(ragdoll).toHaveAttribute("data-seat", "review");
  await expect(ragdoll.locator('[data-pose="work"]')).toBeVisible();
  const mark = page
    .getByRole("button", { name: "Cocogem", exact: true })
    .locator("img");
  await expect(mark).toHaveAttribute("src", "/favicon.svg");
  await expect
    .poll(() =>
      mark.evaluate((node) => (node as HTMLImageElement).naturalWidth),
    )
    .toBeGreaterThan(0);

  const [desktopScene, desktopPanel] = await Promise.all([
    page.getByTestId("studio-scene-scroll").boundingBox(),
    page.getByTestId("studio-overlay-rail").boundingBox(),
  ]);
  expect(desktopScene).not.toBeNull();
  expect(desktopPanel).not.toBeNull();
  expect(desktopScene!.x + desktopScene!.width).toBeLessThanOrEqual(
    desktopPanel!.x,
  );
  await page.screenshot({ path: testInfo.outputPath("studio-desktop.png") });
  await page.setViewportSize({ width: 390, height: 844 });
  const viewport = page.getByTestId("studio-viewport");
  const scene = page.getByTestId("studio-scene-scroll");
  const topbar = page.getByTestId("studio-topbar");
  const command = page.getByTestId("studio-command-bar");
  const panel = page.getByTestId("studio-overlay-rail");
  const [viewportRect, commandRect, panelRect, sceneScrolls, topbarFits] =
    await Promise.all([
      viewport.boundingBox(),
      command.boundingBox(),
      panel.boundingBox(),
      scene.evaluate((node) => node.scrollWidth > node.clientWidth),
      topbar.evaluate((node) => node.scrollWidth <= node.clientWidth),
    ]);
  expect(viewportRect).not.toBeNull();
  expect(commandRect).not.toBeNull();
  expect(panelRect).not.toBeNull();
  const mobileLayout = {
    sceneScrolls,
    topbarFits,
    commandFits:
      commandRect!.x >= viewportRect!.x &&
      commandRect!.x + commandRect!.width <=
        viewportRect!.x + viewportRect!.width,
    panelFits:
      panelRect!.x >= viewportRect!.x &&
      panelRect!.x + panelRect!.width <= viewportRect!.x + viewportRect!.width,
  };
  expect(mobileLayout).toEqual({
    sceneScrolls: true,
    topbarFits: true,
    commandFits: true,
    panelFits: true,
  });
  await page.screenshot({ path: testInfo.outputPath("studio-mobile.png") });
  await page.setViewportSize({ width: 390, height: 540 });

  // Open Settings -> Members -> New member and verify 10-cat uniqueness.
  await page.getByRole("button", { name: "设置", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "设置" });
  await dialog.getByRole("tab", { name: "成员", exact: true }).click();
  await dialog.getByRole("button", { name: "＋ 新成员" }).click();

  const picker = dialog.getByRole("group", { name: "选择猫猫形象" });
  await expect(picker.getByRole("button")).toHaveCount(10);
  await expect(
    picker.getByRole("button", { name: /布偶猫.*使用/ }),
  ).toBeDisabled();
  const siamese = picker.getByRole("button", { name: "暹罗猫", exact: true });
  await expect(siamese).toBeEnabled();
  await expect(siamese).toHaveAttribute("aria-pressed", "true");
  await picker.getByRole("button").last().scrollIntoViewIfNeeded();
  await expect(picker.getByRole("button").last()).toBeInViewport();
  const close = dialog.getByRole("button", { name: "关闭窗口" });
  await expect(close).toBeInViewport();
  expect(
    await dialog.evaluate((node) => node.scrollWidth <= node.clientWidth),
  ).toBe(true);
  await close.click();
  await expect(dialog).toHaveCount(0);
});
