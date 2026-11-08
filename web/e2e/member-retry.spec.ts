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

test("retries only the failed member from a dismissible Composer notice", async ({
  page,
}, testInfo) => {
  const auth = await (
    await page.request.post(`${baseURL}/auth/setup`, {
      data: { password: "member-retry-password" },
    })
  ).json();
  const headers = { "X-CSRF-Token": auth.csrf_token };
  const workspace = await (
    await page.request.post(`${baseURL}/api/v1/workspaces`, {
      headers,
      data: { name: "成员重试验收" },
    })
  ).json();
  const { items: members } = await (
    await page.request.get(
      `${baseURL}/api/v1/workspaces/${workspace.id}/agents`,
    )
  ).json();
  const orange = members.find(
    (member: { handle: string }) => member.handle === "orange",
  );
  const thread = await (
    await page.request.post(
      `${baseURL}/api/v1/workspaces/${workspace.id}/threads`,
      {
        headers,
        data: {
          title: "成员重试",
          agent_ids: members.map((member: { id: string }) => member.id),
          coordinator_id: orange.id,
          workflow: "free",
        },
      },
    )
  ).json();
  await page.goto(`${baseURL}/w/${workspace.id}/t/${thread.id}`);
  const input = page.getByLabel("输入问题");
  await input.fill(
    "成员重试浏览器验收：请 @ragdoll @maine @orange 分别比较文档变化。",
  );
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByTestId("run-status")).toHaveText("失败", {
    timeout: 30000,
  });
  const original = await (
    await page.request.get(`${baseURL}/api/v1/threads/${thread.id}`)
  ).json();
  const run = original.runs.at(-1);
  const first = await (
    await page.request.get(`${baseURL}/api/v1/runs/${run.id}`)
  ).json();
  expect(
    first.steps.filter(
      (step: { status: string }) => step.status === "succeeded",
    ),
  ).toHaveLength(2);
  const failed = first.steps.find(
    (step: { status: string }) => step.status === "failed",
  );
  expect(failed.agent_id).toBe(orange.id);
  await input.fill("@rag");
  await input.press("End");
  await expect(page.getByRole("listbox", { name: "选择成员" })).toBeVisible();
  await input.press("Enter");
  await expect(input).toHaveValue("@ragdoll ");
  await input.fill("保留下一条问题的草稿");
  const form = input.locator("xpath=ancestor::form");
  const notice = form.getByRole("status").filter({ hasText: "成员回复未完成" });
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("Gemini 返回了空响应");
  const box = (await notice.boundingBox())!;
  expect(box.y + box.height).toBeLessThanOrEqual((await form.boundingBox())!.y);
  await page.screenshot({ path: testInfo.outputPath("member-retry.png") });
  await notice.getByRole("button", { name: "关闭提示", exact: true }).click();
  await expect(notice).toHaveCount(0);
  await expect(input).toHaveValue("保留下一条问题的草稿");
  // The member action stays beside the failed reply, never beside the process.
  const memberRetry = page.getByRole("button", {
    name: "重试 橘猫",
    exact: true,
  });
  expect(
    await memberRetry.evaluate((node) => !!node.closest("article")),
  ).toBeTruthy();
  expect(
    await memberRetry.evaluate(
      (node) => !!node.closest('[data-testid="run-process"]'),
    ),
  ).toBeFalsy();
  const replacedReply = original.messages.items.find(
    (message: { role: string; agent_id: string }) =>
      message.role === "assistant" && message.agent_id === orange.id,
  );
  expect(replacedReply).toBeTruthy();
  const response = page.waitForResponse((item) =>
    item.url().endsWith(`/runs/${run.id}/retry`),
  );
  await memberRetry.click();
  const accepted = await response;
  expect(accepted.request().postDataJSON()).toEqual({ step_id: failed.id });
  expect(accepted.request().headers()["idempotency-key"]).toBeTruthy();
  const retried = await accepted.json();
  expect(retried.retry_of_step_id).toBe(failed.id);
  await expect(page.getByTestId("run-status")).toHaveText("已完成", {
    timeout: 30000,
  });
  await page.screenshot({
    path: testInfo.outputPath("member-retry-replaced.png"),
  });
  const result = await (
    await page.request.get(`${baseURL}/api/v1/runs/${retried.id}`)
  ).json();
  expect(result.steps).toHaveLength(1);
  expect(result.steps[0]).toMatchObject({
    agent_id: orange.id,
    status: "succeeded",
  });
  const after = await (
    await page.request.get(`${baseURL}/api/v1/threads/${thread.id}`)
  ).json();
  expect(
    after.messages.items.filter(
      (message: { role: string }) => message.role === "user",
    ),
  ).toHaveLength(1);
  const originalPeers = original.messages.items.filter(
    (message: { role: string; agent_id: string; status: string }) =>
      message.role === "assistant" &&
      message.agent_id !== orange.id &&
      message.status === "completed",
  );
  for (const peer of originalPeers)
    expect(
      after.messages.items.find(
        (message: { id: string }) => message.id === peer.id,
      ),
    ).toMatchObject(peer);
  expect(
    after.messages.items.find(
      (message: { id: string }) => message.id === replacedReply!.id,
    ),
  ).toBeUndefined();
  expect(
    after.messages.items.filter(
      (message: { role: string; agent_id: string }) =>
        message.role === "assistant" && message.agent_id === orange.id,
    ),
  ).toHaveLength(1);
  await expect(page.getByLabel("第 1 次协作", { exact: true })).toHaveCount(1);
  await expect(input).toHaveValue("保留下一条问题的草稿");
  await expect(
    page.getByRole("button", { name: "重试 橘猫", exact: true }),
  ).toHaveCount(0);
});
