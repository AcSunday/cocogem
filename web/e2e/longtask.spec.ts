import { expect, test, type Locator, type Page } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:net";

const PASSWORD = "browser-test-password";

// This suite runs on its own backend so it neither depends on nor changes the
// shared browser server's password, workspace or run history.
let baseURL = "";
let stopServer: (() => Promise<void>) | undefined;

test.beforeAll(async () => {
  const dir = await mkdtemp(join(tmpdir(), "cocogem-longtask-e2e-"));
  const binary = join(dir, "httpapi.test");
  try {
    execFileSync("go", ["test", "-c", "-o", binary, "./internal/httpapi"], {
      cwd: join(process.cwd(), ".."),
      stdio: "pipe",
    });
  } catch (error) {
    await rm(dir, { recursive: true, force: true });
    throw error;
  }
  const probe = createServer();
  await new Promise<void>((resolve) => probe.listen(0, "127.0.0.1", resolve));
  const address = probe.address();
  if (!address || typeof address === "string")
    throw new Error("no long-task test port");
  const port = address.port;
  await new Promise<void>((resolve) => probe.close(() => resolve()));
  const child = spawn(
    binary,
    ["-test.run=^TestBrowserServer$", "-test.timeout=0"],
    {
      cwd: process.cwd(),
      env: {
        ...process.env,
        COCOGEM_E2E: "1",
        COCOGEM_DATA_DIR: dir,
        COCOGEM_E2E_LISTEN: `127.0.0.1:${port}`,
        COCOGEM_E2E_WEB_DIR: join(process.cwd(), "dist"),
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let stderr = "";
  child.stderr.on("data", (data) => {
    stderr += data.toString();
  });
  stopServer = async () => {
    if (child.exitCode === null && child.signalCode === null) {
      const exited = new Promise((resolve) => child.once("exit", resolve));
      child.kill("SIGTERM");
      const force = setTimeout(() => child.kill("SIGKILL"), 5000);
      await exited;
      clearTimeout(force);
    }
    await rm(dir, { recursive: true, force: true });
  };
  baseURL = await new Promise<string>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error("long-task server startup timeout: " + stderr)),
      30000,
    );
    child.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`long-task server exited: ${code} ${stderr}`));
    });
    child.stdout.on("data", (data) => {
      const match = data.toString().match(/Cocogem: (http:\/\/[^\s]+)/);
      if (match) {
        clearTimeout(timer);
        resolve(match[1]);
      }
    });
  });
  for (let count = 0; count < 50; count++) {
    try {
      await fetch(baseURL);
      break;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
}, 120000);

test.afterAll(async () => {
  if (stopServer) await stopServer();
  stopServer = undefined;
});

async function authenticate(page: Page): Promise<string> {
  const status = await (await page.request.get(`${baseURL}/auth/status`)).json();
  const path = status.initialized ? "/auth/login" : "/auth/setup";
  let response = await page.request.post(`${baseURL}${path}`, {
    data: { password: PASSWORD },
  });
  if (response.status() === 409 && !status.initialized) {
    response = await page.request.post(`${baseURL}/auth/login`, {
      data: { password: PASSWORD },
    });
  }
  if (!response.ok()) {
    throw new Error(
      `authentication failed: ${response.status()} ${await response.text()}`,
    );
  }
  return (await response.json()).csrf_token as string;
}

async function api(
  page: Page,
  csrf: string,
  method: string,
  path: string,
  data?: unknown,
): Promise<any> {
  const response = await page.request.fetch(`${baseURL}${path}`, {
    method,
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    data: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!response.ok()) {
    throw new Error(
      `${method} ${path}: ${response.status()} ${await response.text()}`,
    );
  }
  return response.json();
}

async function waitRun(page: Page, csrf: string, id: string): Promise<any> {
  for (let attempt = 0; attempt < 600; attempt++) {
    const view = await api(page, csrf, "GET", `/api/v1/runs/${id}`);
    if (
      ["succeeded", "failed", "canceled", "interrupted", "uncertain"].includes(
        view.run.status,
      )
    )
      return view;
    await page.waitForTimeout(100);
  }
  throw new Error(`run ${id} did not settle`);
}

test("long task preview pages to tail evidence and bounds live events", async ({
  page,
}) => {
  test.setTimeout(180000);
  const csrf = await authenticate(page);
  const directories = await api(page, csrf, "GET", "/api/v1/directories");
  const root = directories.items[0].path as string;
  const workspace = await api(page, csrf, "POST", "/api/v1/workspaces", {
    name: "长任务浏览器验收",
    description: "",
    root,
    skill_ids: [],
  });
  const members = await api(
    page,
    csrf,
    "GET",
    `/api/v1/workspaces/${workspace.id}/agents`,
  );
  const ids = members.items.map((member: { id: string }) => member.id);
  const thread = await api(
    page,
    csrf,
    "POST",
    `/api/v1/workspaces/${workspace.id}/threads`,
    {
      title: "长任务分页",
      agent_ids: ids,
      coordinator_id: ids[2],
      workflow: "free",
    },
  );
  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  await expect(page.getByRole("heading", { name: "长任务分页" })).toBeVisible();

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
  const submit = async (text: string) => {
    await page.getByLabel("输入问题").fill(text);
    const accepted = page.waitForResponse(
      (response) =>
        response.url().endsWith("/runs") &&
        response.request().method() === "POST" &&
        response.status() === 202,
    );
    await page.getByRole("button", { name: "发送", exact: true }).click();
    return (await accepted).json();
  };

  const run1 = await submit("长任务浏览器验收：读取资料并保留尾部反证。");
  await waitRun(page, csrf, run1.id);
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 60000,
  });

  // The timeline keeps a bounded preview; expanding loads one page at a time.
  const longMessage = page
    .getByRole("article")
    .filter({ has: page.getByRole("button", { name: /全文/ }) });
  await expect(longMessage.getByText(/当前为部分预览/)).toBeVisible();
  await longMessage.getByRole("button", { name: "展开全文" }).click();
  await expect(longMessage.getByText("长任务正文起始标记")).toBeVisible();
  await expect(longMessage.getByText("第 1 页", { exact: true })).toBeVisible();
  await longMessage.getByRole("button", { name: "查看下一页全文" }).click();
  await expect(longMessage.getByText("第 2 页", { exact: true })).toBeVisible();
  await expect(longMessage.getByText(/末页反证/)).toBeVisible();
  await expect(longMessage.getByText("长任务正文起始标记")).toHaveCount(0);
  await expect(
    longMessage.getByRole("button", { name: "查看下一页全文" }),
  ).toBeDisabled();

  // More than 500 live events must be capped, not accumulated forever.
  await openPanel("过程");
  const panel = page.getByRole("complementary", { name: "协作资料" });
  await expect(page.getByText("仅保留最近 500 条事件")).toBeVisible();

  // A second, ordinary run has a distinct tool record for the stale-response check.
  const run2 = await submit("请用一句话总结协作成本。");
  await waitRun(page, csrf, run2.id);
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 60000,
  });
  await expect(page.getByLabel("历史协作")).toBeVisible();

  let releaseRun1!: () => void;
  const held = new Promise<void>((resolve) => {
    releaseRun1 = resolve;
  });
  let heldRequests = 0;
  const historyRoute = `**/api/v1/runs/${run1.id}/event-history*`;
  await page.route(historyRoute, async (route) => {
    heldRequests++;
    await held;
    await route.continue();
  });
  await selectOption(page.getByLabel("历史协作"), { index: 0 });
  await page.waitForTimeout(200);
  await selectOption(page.getByLabel("历史协作"), { index: 1 });
  await expect(panel.getByText("读取网页").first()).toBeVisible();
  releaseRun1();
  await page.waitForTimeout(500);
  // The delayed response for run 1 must not replace run 2's committed events.
  await expect(panel.getByText("读取网页").first()).toBeVisible();
  await expect(panel.getByText("重读来源")).toHaveCount(0);
  expect(heldRequests).toBeGreaterThan(0);
  await page.unroute(historyRoute);

  // The long run's full history pages beyond the bounded live window.
  await selectOption(page.getByLabel("历史协作"), { index: 0 });
  await expect(panel.getByText("重读来源").first()).toBeVisible({
    timeout: 20000,
  });
  await panel.getByRole("button", { name: "浏览历史事件" }).click();
  const history = page.getByRole("region", { name: "历史事件" });
  await expect(history.getByText("第 1 页", { exact: true })).toBeVisible();
  let pages = 1;
  while (pages < 10) {
    const next = history.getByRole("button", {
      name: "查看下一页历史事件",
    });
    if (await next.isDisabled()) break;
    await next.click();
    pages++;
    await expect(
      history.getByText(`第 ${pages} 页`, { exact: true }),
    ).toBeVisible();
  }
  expect(pages).toBeGreaterThanOrEqual(6);
});
