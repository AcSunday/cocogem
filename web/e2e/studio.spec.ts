import { expect, test } from "@playwright/test";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

test("shows workspace cats in Cat Studio and enforces unique built-in cat selection", async ({
  page,
}) => {
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

  await page.goto(`/w/${ws.id}?view=studio`);
  await expect(page.getByTestId("studio-page")).toBeVisible();
  await expect(page.getByText("研发室成员")).toBeVisible();
  await expect(page.getByRole("button", { name: "布偶猫", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "缅因猫", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "橘猫", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /布偶猫.*待机/ })).toBeVisible();

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
});
