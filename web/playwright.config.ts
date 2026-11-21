import { existsSync } from "node:fs";
import { chromium, defineConfig, devices } from "@playwright/test";

// Prefer the bundled Playwright browser. When it is not installed (for example
// an offline workspace) fall back to the system Chrome channel so the documented
// `make e2e` still runs instead of failing on a download.
const bundled = chromium.executablePath();
const channel =
  process.env.PLAYWRIGHT_CHANNEL ?? (existsSync(bundled) ? undefined : "chrome");
export default defineConfig({
  testDir: "./e2e",
  timeout: 60000,
  workers: 1,
  globalSetup: "./e2e/setup.ts",
  use: {
    baseURL: "http://127.0.0.1:18741",
    trace: "retain-on-failure",
    channel,
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
