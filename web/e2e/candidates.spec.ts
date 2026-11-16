import { expect, test } from "@playwright/test";

test.use({ baseURL: process.env.COCOGEM_TEST_BASE });

const workspace = {
  id: "candidate-workspace",
  name: "候选变更空间",
  description: "",
  root: "/projects/cocogem",
  revision: 1,
  skill_ids: null as string[] | null,
  created_at: "2026-11-16T00:00:00Z",
};

const agent = {
  id: "candidate-member",
  workspace_id: workspace.id,
  display_name: "构建成员",
  handle: "builder",
  aliases: [],
  role_description: "实现并验证代码变更。",
  capabilities: [],
  executor: "claude",
  model: "default",
  tools: [],
  disabled: false,
  revision: 1,
};

const thread = {
  id: "candidate-thread",
  workspace_id: workspace.id,
  title: "候选变更验收",
  agent_ids: [agent.id],
  coordinator_id: agent.id,
  revision: 1,
  updated_at: "2026-11-16T00:00:00Z",
};

const execution = {
  id: "candidate-execution",
  workspace_id: workspace.id,
  thread_id: thread.id,
  kind: "git_worktree",
  path: "/data/worktrees/thread-run",
  canonical_root: "/projects/cocogem",
  base_revision: "abc123",
  tree_digest: "tree-digest",
  owner_member_id: agent.id,
  lease_owner: "server",
  lease_epoch: 1,
  lease_expires_at: "2026-11-16T10:00:00Z",
  status: "completed",
  observation_completeness: "candidate_tree_only",
  created_at: "2026-11-16T09:00:00Z",
  updated_at: "2026-11-16T09:30:00Z",
};

// These fixtures exercise the browser contract, not real CLI execution.
test("applies an unapplied candidate only after confirmation", async ({
  page,
}) => {
  let items: unknown[] = [
    {
      execution,
      verification: {
        state: "verified",
        changed_paths: [
          "internal/execution/manager.go",
          "internal/execution/worktree_test.go",
        ],
        diff_source_ids: ["diff-1"],
        test_source_ids: ["test-1"],
        tree_digest: "tree-digest",
      },
    },
  ];
  const applied: string[] = [];
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body: unknown = { items: [] };
    if (path === "/api/v1/system")
      body = {
        csrf_token: "browser-test",
        models: ["default"],
        executors: [],
        roots: [],
        verified: {},
        limits: {},
        model_status: "configured",
        search_status: "not_configured",
        skills: [],
      };
    else if (path === "/api/v1/workspaces") body = { items: [workspace] };
    else if (path === `/api/v1/workspaces/${workspace.id}/agents`)
      body = { items: [agent] };
    else if (path === `/api/v1/workspaces/${workspace.id}/threads`)
      body = { items: [thread], next_cursor: "" };
    else if (path === `/api/v1/threads/${thread.id}`)
      body = {
        thread,
        runs: [],
        messages: { items: [], next_cursor: "" },
        sources: [],
      };
    else if (path === `/api/v1/threads/${thread.id}/candidates`)
      body = { items };
    else if (
      path ===
        `/api/v1/threads/${thread.id}/candidates/${execution.id}/apply` &&
      request.method() === "POST"
    ) {
      applied.push(path);
      items = [];
      body = { ...execution, status: "applied" };
    }
    await route.fulfill({ json: body });
  });

  await page.goto(`/w/${workspace.id}/t/${thread.id}`);
  await expect(page.getByRole("region", { name: "候选变更" })).toBeVisible();
  await expect(page.getByText("已验证")).toBeVisible();
  await expect(
    page.getByText("internal/execution/manager.go 等 2 个文件"),
  ).toBeVisible();
  await expect(
    page.getByText("未点应用前，项目目录不会有任何改动"),
  ).toBeVisible();
  expect(applied).toHaveLength(0);

  await page.getByRole("button", { name: "应用到项目目录" }).click();
  await expect(
    page.getByText(/将把这些文件写入项目目录/),
  ).toBeVisible();
  expect(applied).toHaveLength(0);

  await page.getByRole("button", { name: "确认应用" }).click();
  await expect(page.getByRole("region", { name: "候选变更" })).toBeHidden();
  expect(applied).toHaveLength(1);
});
