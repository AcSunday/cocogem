import { expect, test } from "@playwright/test";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

test("removes legacy member colors while preserving unrelated preferences", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem("cocogem.member-colors", '{"member":"#123456"}');
    localStorage.setItem("cocogem-appearance", "dark");
    localStorage.setItem("unrelated-preference", "keep");
  });
  await page.route("**/assets/*.js", (route) =>
    route.request().url().endsWith("/appearance.js")
      ? route.continue()
      : route.abort(),
  );
  await page.goto("/");
  expect(
    await page.evaluate(() => localStorage.getItem("cocogem.member-colors")),
  ).toBeNull();
  expect(
    await page.evaluate(() => localStorage.getItem("unrelated-preference")),
  ).toBe("keep");
  await expect(page.locator("html")).toHaveAttribute("data-appearance", "dark");
});

test("restores explicit appearance before React loads", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.addInitScript(() =>
    localStorage.setItem("cocogem-appearance", "dark"),
  );
  await page.route("**/assets/*.js", (route) =>
    route.request().url().endsWith("/appearance.js")
      ? route.continue()
      : route.abort(),
  );
  await page.goto("/");
  await expect(page.locator("html")).toHaveAttribute("data-appearance", "dark");
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(11, 13, 16)",
  );
  await expect(page.locator("#root")).toBeEmpty();
});

test("restores the warm appearance before React loads", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.addInitScript(() =>
    localStorage.setItem("cocogem-appearance", "warm"),
  );
  await page.route("**/assets/*.js", (route) =>
    route.request().url().endsWith("/appearance.js")
      ? route.continue()
      : route.abort(),
  );
  await page.goto("/");
  await expect(page.locator("html")).toHaveAttribute("data-appearance", "warm");
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(250, 247, 242)",
  );
});

test("drops a removed theme preference and falls back to system", async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.addInitScript(() =>
    localStorage.setItem("cocogem-appearance", "deepseek"),
  );
  await page.route("**/assets/*.js", (route) =>
    route.request().url().endsWith("/appearance.js")
      ? route.continue()
      : route.abort(),
  );
  await page.goto("/");
  await expect(page.locator("html")).not.toHaveAttribute("data-appearance");
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(248, 250, 252)",
  );
});

test("ignores invalid preferences and works without browser storage", async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await page.route("**/assets/*.js", (route) =>
    route.request().url().endsWith("/appearance.js")
      ? route.continue()
      : route.abort(),
  );
  await page.addInitScript(() => {
    try {
      localStorage.setItem("cocogem-appearance", "invalid");
    } catch {
      /* Storage can be disabled below. */
    }
  });
  await page.goto("/");
  await expect(page.locator("html")).not.toHaveAttribute("data-appearance");
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(11, 13, 16)",
  );
  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(248, 250, 252)",
  );
  await page.addInitScript(() => {
    Object.defineProperty(window, "localStorage", {
      get() {
        throw new DOMException("Disabled", "SecurityError");
      },
    });
  });
  await page.reload();
  await expect(page.locator("html")).not.toHaveAttribute("data-appearance");
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(248, 250, 252)",
  );
});
