import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { CandidateView } from "../api/client";
import { CandidateBar } from "./CandidateBar";

afterEach(cleanup);

function candidate(overrides: Partial<CandidateView> = {}): CandidateView {
  const execution = {
    id: "execution-one",
    workspace_id: "ws",
    thread_id: "thread",
    kind: "git_worktree",
    path: "/data/worktrees/thread-run",
    canonical_root: "/projects/cocogem",
    base_revision: "abc123",
    tree_digest: "tree-digest",
    owner_member_id: "member",
    lease_owner: "server",
    lease_epoch: 1,
    lease_expires_at: "2026-11-16T10:00:00Z",
    status: "completed",
    observation_completeness: "candidate_tree_only",
    created_at: "2026-11-16T09:00:00Z",
    updated_at: "2026-11-16T09:30:00Z",
  };
  return {
    execution: { ...execution, ...overrides.execution },
    verification: {
      state: "verified",
      changed_paths: ["internal/execution/manager.go"],
      diff_source_ids: ["diff-1"],
      test_source_ids: ["test-1"],
      tree_digest: "tree-digest",
      ...overrides.verification,
    },
  };
}

it("applies only after an explicit confirmation", async () => {
  const onApply = vi.fn().mockResolvedValue(undefined);
  const onDiscard = vi.fn().mockResolvedValue(undefined);
  const user = userEvent.setup();
  render(
    <CandidateBar
      items={[candidate()]}
      onApply={onApply}
      onDiscard={onDiscard}
    />,
  );

  expect(screen.getByText("已验证")).toBeVisible();
  expect(screen.getByText("internal/execution/manager.go")).toBeVisible();
  expect(onApply).not.toHaveBeenCalled();

  await user.click(
    screen.getByRole("button", { name: "应用到项目目录" }),
  );
  expect(
    screen.getByText(/将把这些文件写入项目目录/),
  ).toBeVisible();
  expect(onApply).not.toHaveBeenCalled();

  await user.click(screen.getByRole("button", { name: "确认应用" }));
  expect(onApply).toHaveBeenCalledWith(
    expect.objectContaining({
      execution: expect.objectContaining({ id: "execution-one" }),
    }),
  );
});

it("keeps an unverified mutation explicit and discards only after confirmation", async () => {
  const onApply = vi.fn().mockResolvedValue(undefined);
  const onDiscard = vi.fn().mockResolvedValue(undefined);
  const user = userEvent.setup();
  render(
    <CandidateBar
      items={[
        candidate({
          verification: {
            state: "unverified_mutation",
            changed_paths: ["main.go", "main_test.go"],
            reason: "no passing test captured after the last write against this tree",
          },
        }),
      ]}
      onApply={onApply}
      onDiscard={onDiscard}
    />,
  );

  expect(screen.getByText("未验证变更")).toBeVisible();
  expect(screen.getByText(/no passing test captured/)).toBeVisible();
  // The full changed set is reachable, not silently summarized away.
  expect(screen.getByText("查看 2 个变更文件")).toBeVisible();

  await user.click(screen.getByRole("button", { name: "丢弃变更" }));
  expect(screen.getByText(/项目目录保持不变/)).toBeVisible();
  expect(onDiscard).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "确认丢弃" }));
  expect(onDiscard).toHaveBeenCalledTimes(1);
  expect(onApply).not.toHaveBeenCalled();
});

it("locks both actions while a collaboration is still running", async () => {
  const user = userEvent.setup();
  render(
    <CandidateBar
      items={[candidate()]}
      locked
      onApply={vi.fn()}
      onDiscard={vi.fn()}
    />,
  );

  expect(
    screen.getByRole("button", { name: "应用到项目目录" }),
  ).toBeDisabled();
  expect(screen.getByRole("button", { name: "丢弃变更" })).toBeDisabled();
  expect(screen.getByText(/本轮协作结束后可应用或丢弃/)).toBeVisible();
  await user.click(screen.getByRole("button", { name: "应用到项目目录" }));
  // A locked action never opens the confirmation.
  expect(screen.queryByRole("button", { name: "确认应用" })).toBeNull();
});

it("shows a conflict error and keeps the candidate for a retry", async () => {
  const onApply = vi
    .fn()
    .mockRejectedValue(new Error("项目目录中的相关文件已被外部修改"));
  const user = userEvent.setup();
  render(
    <CandidateBar
      items={[candidate()]}
      onApply={onApply}
      onDiscard={vi.fn()}
    />,
  );

  await user.click(screen.getByRole("button", { name: "应用到项目目录" }));
  await user.click(screen.getByRole("button", { name: "确认应用" }));

  expect(await screen.findByRole("alert")).toHaveTextContent(
    "项目目录中的相关文件已被外部修改",
  );
  // The item stays and the confirmation is still open, so the reader can
  // reconcile the conflict and retry instead of re-arming the whole bar.
  expect(screen.getByText("已验证")).toBeVisible();
  expect(screen.getByRole("button", { name: "确认应用" })).toBeEnabled();
});

it("does not offer Apply for a candidate with no file difference", async () => {
  render(
    <CandidateBar
      items={[
        candidate({
          verification: {
            state: "unchanged",
            changed_paths: [],
            tree_digest: "tree-digest",
          },
        }),
      ]}
      onApply={vi.fn()}
      onDiscard={vi.fn()}
    />,
  );

  expect(screen.getByText("无改动")).toBeVisible();
  expect(screen.getByText("没有文件差异")).toBeVisible();
  const apply = screen.getByRole("button", { name: "应用到项目目录" });
  expect(apply).toBeDisabled();
  expect(apply).toHaveAttribute(
    "title",
    "该候选没有文件差异，无需应用",
  );
  // Discarding an empty candidate is still a valid cleanup.
  expect(screen.getByRole("button", { name: "丢弃变更" })).toBeEnabled();
});

it("renders nothing without an unapplied candidate", () => {
  const { container } = render(
    <CandidateBar items={[]} onApply={vi.fn()} onDiscard={vi.fn()} />,
  );
  expect(container).toBeEmptyDOMElement();
});
