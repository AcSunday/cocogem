import { mkdir } from "node:fs/promises";
import { crc32, deflateSync } from "node:zlib";
import { expect, test, type Locator, type Page } from "@playwright/test";
import setup from "./setup";

const PASSWORD = "member-profiles-password";
const SHOTS = "/tmp/cocogem-supervision";
const CUSTOM_COLOR = "#b239fa";
const PNG_WIDTH = 4;
const PNG_HEIGHT = 3;
let baseURL = "";
let cleanup: (() => Promise<void>) | undefined;

test.beforeAll(async () => {
  const previous = process.env.COCOGEM_TEST_BASE;
  cleanup = await setup();
  baseURL = process.env.COCOGEM_TEST_BASE!;
  process.env.COCOGEM_TEST_BASE = previous;
  await mkdir(SHOTS, { recursive: true });
});
test.afterAll(async () => cleanup?.());

/** Builds a complete PNG: signature, IHDR, IDAT scanlines and IEND with valid CRCs. */
function pngFixture(width: number, height: number) {
  const chunk = (type: string, data: Buffer) => {
    const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
    const out = Buffer.alloc(12 + data.length);
    out.writeUInt32BE(data.length, 0);
    body.copy(out, 4);
    out.writeUInt32BE(crc32(body) >>> 0, 8 + data.length);
    return out;
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // truecolour RGB
  const stride = width * 3 + 1;
  const pixels = Buffer.alloc(stride * height);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const at = y * stride + 1 + x * 3;
      pixels[at] = 0xb2;
      pixels[at + 1] = 0x39;
      pixels[at + 2] = 0xfa;
    }
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(pixels)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

async function authenticate(page: Page) {
  const status = await (await page.request.get(`${baseURL}/auth/status`)).json();
  const path = status.initialized ? "/auth/login" : "/auth/setup";
  const response = await page.request.post(`${baseURL}${path}`, {
    data: { password: PASSWORD },
  });
  if (!response.ok())
    throw new Error(`authentication failed: ${response.status()}`);
  const system = await (await page.request.get(`${baseURL}/api/v1/system`)).json();
  await page.goto(baseURL);
  return system as { csrf_token: string; models: string[] };
}

async function api(
  page: Page,
  csrf: string,
  method: string,
  path: string,
  data?: unknown,
) {
  const response = await page.request.fetch(`${baseURL}${path}`, {
    method,
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    data: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!response.ok())
    throw new Error(
      `${method} ${path}: ${response.status()} ${await response.text()}`,
    );
  return response.json();
}

test("member profiles: avatar, arbitrary color, exact verify options and stable identity", async ({
  page,
}) => {
  test.setTimeout(240000);
  const system = await authenticate(page);
  const csrf = system.csrf_token;

  const directories = await api(page, csrf, "GET", "/api/v1/directories");
  const workspace = await api(page, csrf, "POST", "/api/v1/workspaces", {
    name: "成员档案验收",
    description: "",
    root: directories.items[0].path,
    skill_ids: [],
  });
  const members = await api(
    page,
    csrf,
    "GET",
    `/api/v1/workspaces/${workspace.id}/agents`,
  );
  expect(members.items).toHaveLength(3);
  const ids = members.items.map((member: { id: string }) => member.id);
  const target = members.items.find(
    (member: { handle: string }) => member.handle === "ragdoll",
  );
  expect(target).toBeTruthy();
  const thread = await api(
    page,
    csrf,
    "POST",
    `/api/v1/workspaces/${workspace.id}/threads`,
    {
      title: "成员档案验收会话",
      agent_ids: ids,
      coordinator_id: ids[0],
      workflow: "free",
    },
  );
  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  await expect(
    page.getByRole("heading", { name: "成员档案验收会话", exact: true }),
  ).toBeVisible();

  const openMembers = async () => {
    await page.getByRole("button", { name: "设置", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "设置" });
    await dialog.getByRole("tab", { name: "成员", exact: true }).click();
    const list = dialog.getByLabel("成员列表");
    await expect(list).toBeVisible();
    return { dialog, list };
  };
  const cards = (list: Locator) =>
    list.getByRole("button").filter({ hasNotText: "新成员" });

  const { dialog, list } = await openMembers();
  await expect(cards(list)).toHaveCount(3);
  await list.getByRole("button", { name: /@ragdoll/ }).click();
  await expect(
    dialog.getByRole("heading", { name: "编辑成员", exact: true }),
  ).toBeVisible();

  const model = dialog.getByLabel("模型（留空使用 CLI 默认）");
  const context = dialog.getByLabel("上下文大小限制");
  const thinking = dialog.getByLabel("思考等级（留空保持 CLI 默认）");
  const verifiedText = dialog.getByText(
    "已验证当前执行 CLI、模型、上下文大小限制和思考等级。",
  );
  // Empty controls keep the CLI defaults: the request must omit both overrides.
  await expect(dialog.getByLabel("执行 CLI")).toHaveAttribute("value", "claude");
  await expect(model).toHaveValue("");
  await expect(context).toHaveValue("");
  await expect(thinking).toHaveAttribute("value", "");
  const verify = async () => {
    const [request, response] = await Promise.all([
      page.waitForRequest(
        (item) =>
          item.method() === "POST" &&
          new URL(item.url()).pathname === "/api/v1/system/verify",
      ),
      page.waitForResponse(
        (item) =>
          item.request().method() === "POST" &&
          new URL(item.url()).pathname === "/api/v1/system/verify",
      ),
      dialog.getByRole("button", { name: "验证模型连接" }).click(),
    ]);
    expect(response.ok()).toBe(true);
    return request.postDataJSON();
  };
  expect(await verify()).toEqual({ executor: "claude", model: "default" });
  await expect(verifiedText).toBeVisible();
  expect(system.models).toEqual(["default"]);

  // The fixture only advertises the default model; the optional options still
  // travel with it before an explicit model identifier is chosen.
  await context.fill("90000");
  await thinking.click();
  await page
    .getByRole("listbox")
    .locator('[role="option"][data-value="high"]')
    .click();
  expect(await verify()).toEqual({
    executor: "claude",
    model: "default",
    context_size: 90000,
    thinking_level: "high",
  });
  await expect(verifiedText).toBeVisible();

  // An explicit model is forwarded verbatim and invalidates older success.
  await model.fill("public-model");
  await expect(verifiedText).toHaveCount(0);
  expect(await verify()).toEqual({
    executor: "claude",
    model: "public-model",
    context_size: 90000,
    thinking_level: "high",
  });
  await expect(verifiedText).toBeVisible();

  // The uploaded avatar is a complete PNG whose decoded size is valid.
  const png = pngFixture(PNG_WIDTH, PNG_HEIGHT);
  for (const marker of ["IHDR", "IDAT", "IEND"])
    expect(png.includes(Buffer.from(marker, "ascii"))).toBe(true);
  await dialog.locator('input[type="file"]').setInputFiles({
    name: "member-avatar.png",
    mimeType: "image/png",
    buffer: png,
  });
  const preview = dialog.locator('span[aria-hidden="true"] img').first();
  await expect(preview).toBeVisible();
  await expect(preview).toHaveAttribute("src", /^data:image\/png;base64,/);
  await expect
    .poll(() => preview.evaluate((image) => (image as HTMLImageElement).naturalWidth))
    .toBe(PNG_WIDTH);
  await expect
    .poll(() => preview.evaluate((image) => (image as HTMLImageElement).naturalHeight))
    .toBe(PNG_HEIGHT);
  await expect(
    dialog.locator('span[aria-hidden="true"]').first(),
  ).toHaveText("");

  await dialog.getByLabel("显示名").fill("布偶猫验收");
  await dialog.getByLabel("职责").fill("研究资料，整理证据与不确定性。验收归档。");
  await dialog.getByLabel("能力标签（用逗号分隔）").fill("avatar-color-verify");
  await dialog.getByLabel("专属颜色").fill(CUSTOM_COLOR);

  const [saveRequest, saveResponse] = await Promise.all([
    page.waitForRequest(
      (item) =>
        item.method() === "PATCH" &&
        new URL(item.url()).pathname === `/api/v1/agents/${target.id}`,
    ),
    page.waitForResponse(
      (item) =>
        item.request().method() === "PATCH" &&
        new URL(item.url()).pathname === `/api/v1/agents/${target.id}`,
    ),
    dialog.getByRole("button", { name: "保存成员" }).click(),
  ]);
  expect(saveResponse.ok()).toBe(true);
  expect(saveRequest.postDataJSON()).toMatchObject({
    id: target.id,
    display_name: "布偶猫验收",
    handle: "ragdoll",
    executor: "claude",
    model: "public-model",
    context_size: 90000,
    thinking_level: "high",
    avatar: expect.stringMatching(/^data:image\/png;base64,/),
  });
  await expect(
    dialog.getByText("已保存，可以继续编辑此成员。"),
  ).toBeVisible();
  const colors = await page.evaluate(() =>
    JSON.parse(localStorage.getItem("cocogem.member-colors") ?? "{}"),
  );
  expect(colors[target.id]).toBe(CUSTOM_COLOR);
  await dialog.getByLabel("专属颜色").scrollIntoViewIfNeeded();
  await page.waitForTimeout(150);
  await page.screenshot({ path: `${SHOTS}/member-editor-desktop.png` });

  // Member cards render in the narrow viewport with the saved identity.
  await dialog.getByRole("button", { name: "← 返回列表" }).click();
  await expect(list).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(250);
  await page.screenshot({ path: `${SHOTS}/member-cards-mobile.png` });
  await page.setViewportSize({ width: 1280, height: 900 });

  // Returning to the editor shows every saved field and the decoded avatar.
  await list.getByRole("button", { name: /@ragdoll/ }).click();
  await expect(
    dialog.getByRole("heading", { name: "编辑成员", exact: true }),
  ).toBeVisible();
  await expect(dialog.getByLabel("显示名")).toHaveValue("布偶猫验收");
  await expect(dialog.getByLabel("Handle")).toHaveValue("ragdoll");
  await expect(dialog.getByLabel("职责")).toHaveValue(
    "研究资料，整理证据与不确定性。验收归档。",
  );
  await expect(dialog.getByLabel("能力标签（用逗号分隔）")).toHaveValue(
    "avatar-color-verify",
  );
  await expect(dialog.getByLabel("专属颜色")).toHaveValue(CUSTOM_COLOR);
  await expect(dialog.getByLabel("模型（留空使用 CLI 默认）")).toHaveValue(
    "public-model",
  );
  await expect(
    dialog.getByLabel("上下文大小限制"),
  ).toHaveValue("90000");
  await expect(thinking).toHaveAttribute("value", "high");
  const reopenedAvatar = dialog.locator('span[aria-hidden="true"] img').first();
  await expect(reopenedAvatar).toBeVisible();
  await expect
    .poll(() =>
      reopenedAvatar.evaluate((image) => (image as HTMLImageElement).naturalWidth),
    )
    .toBe(PNG_WIDTH);
  await expect
    .poll(() =>
      reopenedAvatar.evaluate((image) => (image as HTMLImageElement).naturalHeight),
    )
    .toBe(PNG_HEIGHT);
  await expect(dialog.locator('span[aria-hidden="true"]').first()).toHaveText("");

  // Closing and reloading keeps the arbitrary color and avatar.
  await dialog.getByRole("button", { name: "← 返回列表" }).click();
  await dialog.getByRole("button", { name: "关闭窗口" }).click();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "成员档案验收会话", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      (id) =>
        JSON.parse(localStorage.getItem("cocogem.member-colors") ?? "{}")[id],
      target.id,
    ),
  ).toBe(CUSTOM_COLOR);
  {
    const reopened = await openMembers();
    await reopened.list.getByRole("button", { name: /@ragdoll/ }).click();
    await expect(
      reopened.dialog.getByLabel("专属颜色"),
    ).toHaveValue(CUSTOM_COLOR);
    const reloadedAvatar = reopened.dialog
      .locator('span[aria-hidden="true"] img')
      .first();
    await expect(reloadedAvatar).toBeVisible();
    await expect(reloadedAvatar).toHaveAttribute(
      "src",
      /^data:image\/png;base64,/,
    );
    await expect
      .poll(() =>
        reloadedAvatar.evaluate(
          (image) => (image as HTMLImageElement).naturalWidth,
        ),
      )
      .toBe(PNG_WIDTH);
    await expect
      .poll(() =>
        reloadedAvatar.evaluate(
          (image) => (image as HTMLImageElement).naturalHeight,
        ),
      )
      .toBe(PNG_HEIGHT);
    await expect(
      reopened.dialog.locator('span[aria-hidden="true"]').first(),
    ).toHaveText("");
    await reopened.dialog
      .getByRole("button", { name: "关闭窗口" })
      .click();
  }

  // A direct mention reaches the edited member; its message uses the stored color.
  const input = page.getByLabel("输入问题");
  await input.fill("请 @ragdoll 核对成员档案。");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 60000,
  });
  const reply = page.locator("article").filter({ hasText: "布偶猫验收" }).last();
  await expect(reply).toBeVisible({ timeout: 30000 });
  expect(
    await reply.evaluate((element) =>
      getComputedStyle(element).getPropertyValue("--member-color").trim(),
    ),
  ).toBe(CUSTOM_COLOR);

  // Clearing the avatar falls back to the first character of the display name.
  {
    const reopened = await openMembers();
    await reopened.list.getByRole("button", { name: /@ragdoll/ }).click();
    await expect(
      reopened.dialog.getByRole("button", { name: "移除头像" }),
    ).toBeVisible();
    await reopened.dialog.getByRole("button", { name: "移除头像" }).click();
    const [request, response] = await Promise.all([
      page.waitForRequest(
        (item) =>
          item.method() === "PATCH" &&
          new URL(item.url()).pathname === `/api/v1/agents/${target.id}`,
      ),
      page.waitForResponse(
        (item) =>
          item.request().method() === "PATCH" &&
          new URL(item.url()).pathname === `/api/v1/agents/${target.id}`,
      ),
      reopened.dialog.getByRole("button", { name: "保存成员" }).click(),
    ]);
    expect(response.ok()).toBe(true);
    expect(request.postDataJSON()).toMatchObject({ id: target.id, avatar: "" });
    await expect(
      reopened.dialog.getByText("已保存，可以继续编辑此成员。"),
    ).toBeVisible();
    await reopened.dialog.getByRole("button", { name: "← 返回列表" }).click();
    await reopened.list.getByRole("button", { name: /@ragdoll/ }).click();
    const fallback = reopened.dialog
      .locator('span[aria-hidden="true"]')
      .first();
    await expect(fallback).toHaveText("布");
    await expect(fallback.locator("img")).toHaveCount(0);
    const profiles = await api(page, csrf, "GET", "/api/v1/agents");
    expect(
      profiles.items.find((agent: { id: string }) => agent.id === target.id)
        .avatar,
    ).toBeFalsy();
    await reopened.dialog
      .getByRole("button", { name: "← 返回列表" })
      .click();
  }

  // Creating a profile and renaming it keeps one stable identity and does not
  // add a workspace grant or a duplicate profile.
  const profilesBefore = await api(page, csrf, "GET", "/api/v1/agents");
  const workspaceBefore = await api(
    page,
    csrf,
    "GET",
    `/api/v1/workspaces/${workspace.id}/agents`,
  );
  const createDialog = dialog;
  const createList = list;
  await expect(createList).toBeVisible();
  await createList.getByRole("button", { name: /新成员/ }).click();
  await expect(
    createDialog.getByRole("heading", { name: "新建成员", exact: true }),
  ).toBeVisible();
  await createDialog.getByLabel("显示名").fill("验收新成员");
  await createDialog.getByLabel("Handle").fill("acceptance-member");
  await createDialog.getByLabel("职责").fill("负责成员档案验收。");
  const [createRequest, createResponse] = await Promise.all([
    page.waitForRequest(
      (item) =>
        item.method() === "POST" &&
        new URL(item.url()).pathname === "/api/v1/agents",
    ),
    page.waitForResponse(
      (item) =>
        item.request().method() === "POST" &&
        new URL(item.url()).pathname === "/api/v1/agents",
    ),
    createDialog.getByRole("button", { name: "保存成员" }).click(),
  ]);
  expect(createResponse.ok()).toBe(true);
  const created = await createResponse.json();
  expect(createRequest.postDataJSON()).toMatchObject({
    display_name: "验收新成员",
    handle: "acceptance-member",
  });
  expect(created.id).toBeTruthy();
  await expect(
    createDialog.getByText("已保存，可以继续编辑此成员。"),
  ).toBeVisible();
  await expect(
    createDialog.getByRole("heading", { name: "编辑成员", exact: true }),
  ).toBeVisible();

  await createDialog.getByLabel("显示名").fill("验收新成员改名");
  const [renameRequest, renameResponse] = await Promise.all([
    page.waitForRequest(
      (item) =>
        item.method() === "PATCH" &&
        new URL(item.url()).pathname === `/api/v1/agents/${created.id}`,
    ),
    page.waitForResponse(
      (item) =>
        item.request().method() === "PATCH" &&
        new URL(item.url()).pathname === `/api/v1/agents/${created.id}`,
    ),
    createDialog.getByRole("button", { name: "保存成员" }).click(),
  ]);
  expect(renameResponse.ok()).toBe(true);
  expect(renameRequest.postDataJSON()).toMatchObject({
    id: created.id,
    display_name: "验收新成员改名",
  });
  expect((await renameResponse.json()).id).toBe(created.id);
  await expect(
    createDialog.getByText("已保存，可以继续编辑此成员。"),
  ).toBeVisible();

  await createDialog.getByRole("button", { name: "← 返回列表" }).click();
  await expect(createList).toBeVisible();
  await expect(
    createList.getByRole("button", { name: /@acceptance-member/ }),
  ).toHaveCount(1);
  await expect(
    createList.getByText("@acceptance-member", { exact: true }),
  ).toHaveCount(1);
  const profilesAfter = await api(page, csrf, "GET", "/api/v1/agents");
  expect(profilesAfter.items).toHaveLength(profilesBefore.items.length + 1);
  expect(
    profilesAfter.items.filter(
      (agent: { handle: string }) => agent.handle === "acceptance-member",
    ),
  ).toHaveLength(1);
  expect(
    profilesAfter.items.find(
      (agent: { handle: string }) => agent.handle === "acceptance-member",
    ).id,
  ).toBe(created.id);
  const workspaceAfter = await api(
    page,
    csrf,
    "GET",
    `/api/v1/workspaces/${workspace.id}/agents`,
  );
  expect(workspaceAfter.items).toHaveLength(workspaceBefore.items.length);
  expect(
    workspaceAfter.items.map((member: { id: string }) => member.id).sort(),
  ).toEqual(
    workspaceBefore.items.map((member: { id: string }) => member.id).sort(),
  );

  // The renamed profile still opens with all edited content.
  await createList.getByRole("button", { name: /@acceptance-member/ }).click();
  await expect(createDialog.getByLabel("显示名")).toHaveValue("验收新成员改名");
  await expect(createDialog.getByLabel("Handle")).toHaveValue(
    "acceptance-member",
  );
  await expect(createDialog.getByLabel("职责")).toHaveValue(
    "负责成员档案验收。",
  );
  await createDialog.getByRole("button", { name: "← 返回列表" }).click();
  await expect(createList).toBeVisible();
});
