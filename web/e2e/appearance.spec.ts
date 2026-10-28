import { expect, test } from "@playwright/test";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

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
    "rgb(30, 30, 30)",
  );
  await expect(page.locator("#root")).toBeEmpty();
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
    "rgb(30, 30, 30)",
  );
  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("html")).toHaveCSS(
    "background-color",
    "rgb(250, 247, 242)",
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
    "rgb(250, 247, 242)",
  );
});
