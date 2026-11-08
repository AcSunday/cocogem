import { expect, test, type Locator, type Page } from "@playwright/test";
import setup, { PERMISSION_OUTSIDE_CONTENT } from "./setup";

/**
 * Real host approval journey.
 *
 * Nothing here mocks the permission HTTP API or fabricates a request. A normal
 * Task is created and started; `internal/httpapi/browser_test.go`
 * (`runPermissionFixture`) then calls the ordinary host `read_file` tool with a
 * real path OUTSIDE the bound project root, so the server itself creates the
 * `ToolPermissionRequest` and blocks the step until the UI decides.
 *
 * Every `setup()` call creates a fresh temp dir, so each test is independent.
 */

let baseURL = "";
let cleanup: (() => Promise<void>) | undefined;
test.beforeAll(async () => {
  const previous = process.env.COCOGEM_TEST_BASE;
  cleanup = await setup();
  baseURL = process.env.COCOGEM_TEST_BASE!;
  process.env.COCOGEM_TEST_BASE = previous;
});
test.afterAll(async () => await cleanup?.());

/** The runner fixture only triggers for a task goal with this exact prefix. */
const MARKER = "权限审批浏览器验收：";

async function authenticate(page: Page) {
  const status = await (await page.request.get(baseURL + "/auth/status")).json();
  const response = await page.request.post(
    baseURL + (status.initialized ? "/auth/login" : "/auth/setup"),
    { data: { password: "permission-journey-password" } },
  );
  expect(response.ok()).toBeTruthy();
  const session = await response.json();
  await page.context().addCookies((await page.request.storageState()).cookies);
  return { "X-CSRF-Token": session.csrf_token as string };
}

/**
 * Creates and starts a Task whose goal triggers the host permission fixture.
 * A new Task per test keeps the chat-promotion path out of the way.
 */
async function startTask(
  page: Page,
  headers: Record<string, string>,
  workspace: string,
  goal: string,
) {
  const created = await page.request.post(
    `${baseURL}/api/v1/workspaces/${workspace}/tasks`,
    {
      headers,
      data: {
        request_id: crypto.randomUUID(),
        title: goal.slice(MARKER.length).slice(0, 40) || "权限审批",
        goal,
      },
    },
  );
  expect(created.ok()).toBeTruthy();
  // The create reply is already `{task, revision, budget, completions}`.
  const created_ = await created.json();
  const taskID = created_.task.id as string;
  const started = await page.request.post(
    `${baseURL}/api/v1/tasks/${taskID}/start`,
    { headers, data: { request_id: crypto.randomUUID() } },
  );
  expect(started.ok()).toBeTruthy();
  // The start reply is the launched Run; the task's thread is on the Task.
  const detail = await (
    await page.request.get(`${baseURL}/api/v1/tasks/${taskID}`)
  ).json();
  return {
    id: taskID,
    thread_id: detail.task.thread_id as string,
    workspace_id: detail.task.workspace_id as string,
  };
}

async function createWorkspace(page: Page, headers: Record<string, string>) {
  const workspace = await (
    await page.request.post(baseURL + "/api/v1/workspaces", {
      headers,
      data: { name: "权限审批验收" },
    })
  ).json();
  return workspace;
}

async function pendingRequest(
  page: Page,
  threadID: string,
  timeout = 30000,
) {
  let found: Record<string, unknown> | undefined;
  await expect
    .poll(
      async () => {
        const response = await page.request.get(
          `${baseURL}/api/v1/threads/${threadID}/permission-requests`,
        );
        if (!response.ok()) return 0;
        const body = await response.json();
        const pending = (body.items ?? []).filter(
          (item: { status: string }) => item.status === "pending",
        );
        found = pending[0];
        return pending.length;
      },
      { timeout, intervals: [250, 500, 1000] },
    )
    .toBeGreaterThan(0);
  return found!;
}

/**
 * The blocked operation legitimately renders twice: in the member's own reply
 * activity and in the Composer queue. Both are outside the collapsed process
 * log, so any of them is a valid place for the user to decide; the tests use the
 * first (the reply-body one) unless they specifically exercise the queue.
 */
const pendingCard = (page: Page) =>
  page.getByRole("region", { name: "待处理授权" }).first();

/** The visible approval card gives the three fixed actions. */
async function expectThreeActions(card: Locator) {
  await expect(card.getByRole("button", { name: "仅本次允许" })).toBeVisible();
  await expect(
    card.getByRole("button", { name: "本会话持续允许" }),
  ).toBeVisible();
  await expect(card.getByRole("button", { name: "拒绝" })).toBeVisible();
}

async function decidedStatus(page: Page, threadID: string, id: string) {
  const response = await page.request.get(
    `${baseURL}/api/v1/threads/${threadID}/permission-requests`,
  );
  const body = await response.json();
  return (body.items ?? []).find((item: { id: string }) => item.id === id)
    ?.status as string | undefined;
}

test("allow once reads the real outside file exactly once", async ({ page }) => {
  test.setTimeout(180000);
  const headers = await authenticate(page);
  const workspace = await createWorkspace(page, headers);
  const task = await startTask(
    page,
    headers,
    workspace.id,
    `${MARKER}读取项目外文件并交付审批结果。`,
  );
  await page.goto(`${baseURL}/w/${task.workspace_id}/t/${task.thread_id}`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();

  // The server created the request through the real host tool path.
  const request = await pendingRequest(page, task.thread_id);
  expect(request.tool_name).toBe("read_file");
  expect(request.origin).toBe("host");
  expect(request.status).toBe("pending");

  // The blocked call is visible outside every collapsed disclosure.
  const card = pendingCard(page);
  await expect(card).toBeVisible();
  await expectThreeActions(card);
  await expect(card).toContainText("等待授权");

  await card.getByRole("button", { name: "仅本次允许" }).click();
  // The persisted state is the truth: the request settles server-side.
  await expect
    .poll(() => decidedStatus(page, task.thread_id, String(request.id)), {
      timeout: 20000,
    })
    .toBe("allowed_once");

  // The real file bytes reach the delivery, proving the tool actually executed.
  await expect
    .poll(
      async () => {
        const deliveries = await page.request.get(
          `${baseURL}/api/v1/tasks/${task.id}/deliveries`,
        );
        if (!deliveries.ok()) return "";
        return JSON.stringify(await deliveries.json());
      },
      { timeout: 60000 },
    )
    .toContain(PERMISSION_OUTSIDE_CONTENT);

  // A settled request cannot be decided again from the UI.
  await expect(
    page.getByRole("button", { name: "仅本次允许" }),
  ).toHaveCount(0);
});

test("deny produces no read and reports the refusal", async ({ page }) => {
  test.setTimeout(180000);
  const headers = await authenticate(page);
  const workspace = await createWorkspace(page, headers);
  const task = await startTask(
    page,
    headers,
    workspace.id,
    `${MARKER}拒绝后不应读取项目外文件。`,
  );
  await page.goto(`${baseURL}/w/${task.workspace_id}/t/${task.thread_id}`);

  const request = await pendingRequest(page, task.thread_id);
  const card = pendingCard(page);
  await expectThreeActions(card);
  await card.getByRole("button", { name: "拒绝" }).click();

  await expect
    .poll(() => decidedStatus(page, task.thread_id, String(request.id)), {
      timeout: 20000,
    })
    .toBe("denied");

  // The member reports the real refusal instead of a fabricated success, and the
  // outside bytes never appear in the delivery.
  await expect
    .poll(
      async () => {
        const deliveries = await page.request.get(
          `${baseURL}/api/v1/tasks/${task.id}/deliveries`,
        );
        if (!deliveries.ok()) return "";
        return JSON.stringify(await deliveries.json());
      },
      { timeout: 60000 },
    )
    .toContain("授权未通过");
  const body = await (
    await page.request.get(`${baseURL}/api/v1/tasks/${task.id}/deliveries`)
  ).json();
  expect(JSON.stringify(body)).not.toContain(PERMISSION_OUTSIDE_CONTENT);

  // The refusal is delivered as the member's final reply and renders outside
  // every collapsed disclosure. It arrives with the run's final delivery, so
  // poll until the page reflects the settled run.
  await expect
    .poll(
      async () =>
        page
          .getByRole("article")
          .filter({ hasText: "授权未通过" })
          .first()
          .isVisible()
          .catch(() => false),
      { timeout: 60000 },
    )
    .toBe(true);
  // The card sits in the reply, not inside a folded process log.
  const refusal = page.getByRole("article").filter({ hasText: "授权未通过" });
  await expect(refusal.first()).toBeVisible();
  await expect(
    refusal.first().evaluate((node) => !!node.closest("details")),
  ).resolves.toBe(false);
});

test("a conversation grant is shared with another member and not asked again", async ({
  page,
}) => {
  test.setTimeout(240000);
  const headers = await authenticate(page);
  const workspace = await createWorkspace(page, headers);
  // The host staffs the launched thread from the workspace roster (three seeded
  // members), so the grant must hold across member boundaries.
  const members = await (
    await page.request.get(
      `${baseURL}/api/v1/workspaces/${workspace.id}/agents`,
    )
  ).json();
  expect((members.items ?? []).length).toBeGreaterThanOrEqual(2);

  const task = await startTask(
    page,
    headers,
    workspace.id,
    `${MARKER}同会话持续授权后不再重复询问。`,
  );
  const thread = await (
    await page.request.get(`${baseURL}/api/v1/threads/${task.thread_id}`)
  ).json();
  // The launched conversation really carries more than one member.
  expect((thread.thread.agent_ids ?? []).length).toBeGreaterThanOrEqual(2);
  await page.goto(`${baseURL}/w/${task.workspace_id}/t/${task.thread_id}`);

  const request = await pendingRequest(page, task.thread_id);
  const card = pendingCard(page);
  await expectThreeActions(card);
  await card.getByRole("button", { name: "本会话持续允许" }).click();

  await expect
    .poll(() => decidedStatus(page, task.thread_id, String(request.id)), {
      timeout: 20000,
    })
    .toBe("allowed_conversation");

  // The host persisted a thread-scoped grant for exactly this tool/scope.
  await expect
    .poll(
      async () => {
        const grants = await page.request.get(
          `${baseURL}/api/v1/threads/${task.thread_id}/permission-grants`,
        );
        if (!grants.ok()) return 0;
        const body = await grants.json();
        return (body.items ?? []).filter(
          (grant: { status: string }) => grant.status === "active",
        ).length;
      },
      { timeout: 20000 },
    )
    .toBeGreaterThan(0);

  const grantBody = await (
    await page.request.get(
      `${baseURL}/api/v1/threads/${task.thread_id}/permission-grants`,
    )
  ).json();
  const grant = (grantBody.items ?? [])[0];
  expect(grant.thread_id).toBe(task.thread_id);
  expect(grant.tool_matcher || grant.action).toBeTruthy();

  // No second prompt for the same operation: the run never asks again.
  const stillPending = await (
    await page.request.get(
      `${baseURL}/api/v1/threads/${task.thread_id}/permission-requests`,
    )
  ).json();
  const repeated = (stillPending.items ?? []).filter(
    (item: { status: string; id: string }) =>
      item.status === "pending" && item.id !== request.id,
  );
  expect(repeated).toEqual([]);
});

test("dismissing keeps the request pending and the draft intact", async ({
  page,
}) => {
  test.setTimeout(180000);
  const headers = await authenticate(page);
  const workspace = await createWorkspace(page, headers);
  const task = await startTask(
    page,
    headers,
    workspace.id,
    `${MARKER}稍后处理仍然保留请求与草稿。`,
  );
  await page.goto(`${baseURL}/w/${task.workspace_id}/t/${task.thread_id}`);

  const draft = "等一下再决定的草稿内容";
  const input = page.getByLabel("输入问题");
  await input.fill(draft);
  const request = await pendingRequest(page, task.thread_id);
  // The queue above the Composer owns the view-only "稍后处理" control.
  const queue = page.getByRole("region", { name: "待处理授权" }).last();
  await expect(queue.getByRole("button", { name: "稍后处理" })).toBeVisible();
  await expectThreeActions(pendingCard(page));

  // Closing the notice is not a decision.
  await queue.getByRole("button", { name: "稍后处理" }).click();
  await expect(page.getByText(/个操作等待授权，已折叠但未决定/)).toBeVisible();
  await expect(page.getByRole("button", { name: "重新打开" })).toBeVisible();
  // Still pending on the server, and the draft is untouched.
  expect(await decidedStatus(page, task.thread_id, String(request.id))).toBe(
    "pending",
  );
  await expect(input).toHaveValue(draft);

  // Reopening restores the actions without re-requesting anything.
  await page.getByRole("button", { name: "重新打开" }).click();
  await expectThreeActions(pendingCard(page));
});

test("the pending request stays visible after folding the process log", async ({
  page,
}) => {
  test.setTimeout(180000);
  const headers = await authenticate(page);
  const workspace = await createWorkspace(page, headers);
  const task = await startTask(
    page,
    headers,
    workspace.id,
    `${MARKER}折叠过程后仍能看到待授权。`,
  );
  await page.goto(`${baseURL}/w/${task.workspace_id}/t/${task.thread_id}`);
  await pendingRequest(page, task.thread_id);

  const card = pendingCard(page);
  await expect(card).toBeVisible();
  const disclosure = page.getByTestId("run-process").first();
  if (await disclosure.count()) {
    await disclosure.evaluate((node) => {
      const details = node as HTMLDetailsElement;
      details.open = false;
      details.dispatchEvent(new Event("toggle"));
    });
    await expect(disclosure).not.toHaveAttribute("open", "");
  }
  // The blocked operation is outside every run/process disclosure.
  await expectThreeActions(pendingCard(page));
});
