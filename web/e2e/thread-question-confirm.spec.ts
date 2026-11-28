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

test("displays task confirmation questions inside the conversation and resumes on answer", async ({
  page,
}) => {
  test.setTimeout(120000);
  const status = await (await page.request.get(`${baseURL}/auth/status`)).json();
  const auth = await (
    await page.request.post(
      `${baseURL}${status.initialized ? "/auth/login" : "/auth/setup"}`,
      { data: { password: "question-confirm-password" } },
    )
  ).json();
  const headers = { "X-CSRF-Token": auth.csrf_token };

  const ws = await (
    await page.request.post(`${baseURL}/api/v1/workspaces`, {
      headers,
      data: { name: "会话确认框验收空间" },
    })
  ).json();
  await page.request.post(`${baseURL}/api/v1/workspaces/${ws.id}/tasks`, {
    headers,
    data: {
      request_id: crypto.randomUUID(),
      title: "核对修复收件人",
      goal: "COCOGEM_ASK_QUESTION_E2E 请审查问题并在不确定时向用户确认",
    },
  });

  await page.goto(`${baseURL}/w/${ws.id}?view=tasks`);
  await page.getByRole("button", { name: /核对修复收件人/ }).click();
  await page.getByRole("button", { name: "开始执行", exact: true }).click();
  await expect(page).toHaveURL(/\/t\//);

  // Inside the conversation view, the yielded ask_question prompt appears live above the composer.
  const questionPanel = page.getByTestId("conversation-questions");
  await expect(questionPanel).toBeVisible({ timeout: 30000 });
  await expect(questionPanel).toContainText("是否将修复任务交给本轮名册中的「狸花猫」");
  await expect(
    questionPanel.getByRole("button", { name: /确认继续：交由狸花猫修复.*建议/ }),
  ).toHaveAttribute("aria-pressed", "true");

  // Confirm right inside the conversation; card clears and continuation run completes here.
  await questionPanel.getByRole("button", { name: "确认并继续" }).click();
  await expect(questionPanel).toHaveCount(0);
  await expect(
    page.getByText("QUESTION_RESUMED_DONE: 已按你的确认继续执行并完成").last(),
  ).toBeVisible({ timeout: 30000 });
});
