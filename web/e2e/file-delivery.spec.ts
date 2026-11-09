import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";
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

test("keeps a picked file local until the first message is sent", async ({ page }) => {
  const status = await (await page.request.get(baseURL + "/auth/status")).json();
  const auth = await page.request.post(
    baseURL + (status.initialized ? "/auth/login" : "/auth/setup"),
    { data: { password: "browser-test-password" } },
  );
  expect(auth.ok()).toBeTruthy();
  const session = await auth.json();
  const headers = { "X-CSRF-Token": session.csrf_token };
  const workspace = await (
    await page.request.post(`${baseURL}/api/v1/workspaces`, {
      headers,
      data: { name: "附件草稿验收" },
    })
  ).json();
  await page.goto(`${baseURL}/w/${workspace.id}`);
  await page.getByLabel("输入问题").waitFor();
  const threadRequests: string[] = [];
  const sourceRequests: string[] = [];
  page.on("request", (request) => {
    if (request.method() !== "POST") return;
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/threads")) threadRequests.push(path);
    if (/\/threads\/[^/]+\/sources$/.test(path)) sourceRequests.push(path);
  });
  await page
    .locator('input[type="file"]')
    .last()
    .setInputFiles({
      name: "draft-note.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("draft attachment"),
    });
  await expect(page.getByText(/资料：draft-note\.txt/)).toBeVisible();
  await page.waitForTimeout(400);
  expect(threadRequests).toHaveLength(0);
  expect(sourceRequests).toHaveLength(0);
  await page.getByLabel("输入问题").fill("发送前不要创建会话。");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => threadRequests.length).toBe(1);
  await expect.poll(() => sourceRequests.length).toBe(1);
  await expect(page).toHaveURL(new RegExp(`/w/${workspace.id}/t/`));
});

test("uploads an arbitrary file and downloads exact processed bytes", async ({ page }) => {
  const status = await (await page.request.get(baseURL+"/auth/status")).json();
  const auth = await page.request.post(baseURL+(status.initialized ? "/auth/login" : "/auth/setup"), { data: { password: "browser-test-password" } });
  expect(auth.ok()).toBeTruthy();
  const session = await auth.json();
  const headers = { "X-CSRF-Token": session.csrf_token };
  const created = await page.request.post(baseURL+"/api/v1/workspaces", {headers, data: {name: "文件交付验收"}});
  const workspace = await created.json();
  // The workspace `execution-policy`/`file-execution` routes are retired. The
  // permission is one global field-merge setting shared by Composer and settings.
  const settings = await (await page.request.get(`${baseURL}/api/v1/settings`)).json();
  const configured = await page.request.patch(`${baseURL}/api/v1/settings`, {
    headers: { ...headers, "If-Match": String(settings.revision) },
    data: { execution_mode: "full_access" },
  });
  expect(configured.ok()).toBeTruthy();
  expect((await configured.json()).execution_mode).toBe("full_access");
  const {items: members} = await (await page.request.get(`${baseURL}/api/v1/workspaces/${workspace.id}/agents`)).json();
  const member = members[0];
  const thread = await (await page.request.post(`${baseURL}/api/v1/workspaces/${workspace.id}/threads`, {headers,data:{title:"附件处理",workflow:"free",agent_ids:[member.id],coordinator_id:member.id}})).json();
  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  const attachment = page.locator('input[type="file"]').last();
  await attachment.setInputFiles({name:"input.unlisted",mimeType:"application/octet-stream",buffer:Buffer.from([0,1,2,255])});
  await expect(page.getByText(/资料：input.unlisted/)).toBeVisible();
  await page.getByLabel("输入问题").fill("文件交付浏览器验收：处理附件并交付成品文件。");
  await page.getByRole("button",{name:"发送",exact:true}).click();
  await expect(page.getByText("已处理附件并交付成品。").last()).toBeVisible({timeout:30000});
  const result = page.getByRole("button",{name:/文件处理完成/});
  if (!(await result.isVisible())) {
    const panel = page.getByRole("button",{name:"打开协作面板",exact:true});
    if (await panel.isVisible()) await panel.click();
    await page.getByRole("tab",{name:"产物",exact:true}).click();
  }
  await result.first().click();
  const download = page.waitForEvent("download");
  await page.getByRole("link",{name:/下载 result.custom/}).click();
  const file = await download;
  expect(file.suggestedFilename()).toBe("result.custom");
  expect(await readFile((await file.path())!)).toEqual(Buffer.concat([Buffer.from("processed:"),Buffer.from([0,1,2,255])]));
  await page.getByRole("button",{name:"关闭窗口"}).click();
  await page.setViewportSize({width:390,height:844});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBeTruthy();
});
